// Package ssl obtains TLS certificates on behalf of the panel: real ACME
// (Let's Encrypt via golang.org/x/crypto/acme, HTTP-01 challenge through the
// site webroot) or a mock self-signed mode for development environments that
// lack public DNS. The ACME account key persists under the agent data dir.
package ssl

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/acme"
)

// ACME directory URLs.
const (
	DirProduction = "https://acme-v02.api.letsencrypt.org/directory"
	DirStaging    = "https://acme-staging-v02.api.letsencrypt.org/directory"
)

// OrderRequest describes what to issue.
type OrderRequest struct {
	SiteSlug string
	Domains  []string // SAN list (validated upstream)
	WebRoot  string   // document root used for the HTTP-01 challenge
	Mode     string   // production | staging | mock
	Email    string   // optional ACME account contact
}

// Result is the outcome of issuance.
type Result struct {
	CertPath  string
	KeyPath   string
	Domains   []string
	ExpiresAt string // RFC3339
	Provider  string // acme | mock
}

// Options holds directories the agent owns.
type Options struct {
	CertsDir    string // where <siteSlug>/fullchain.pem + privkey.pem live
	AccountDir  string // where the ACME account key persists
	HTTPClient  *http.Client
}

// Order issues a certificate for the given domains.
func Order(ctx context.Context, opts Options, req OrderRequest) (*Result, error) {
	if req.Mode == "mock" {
		return mockIssue(opts, req)
	}
	return acmeIssue(ctx, opts, req)
}

// ---------------------------------------------------------------------------
// Mock (development): self-signed certificate, honest and offline.
// ---------------------------------------------------------------------------

func mockIssue(opts Options, req OrderRequest) (*Result, error) {
	if len(req.Domains) == 0 {
		return nil, errors.New("no domains for certificate")
	}
	dir := filepath.Join(opts.CertsDir, req.SiteSlug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: req.Domains[0]},
		DNSNames:              req.Domains,
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              now.Add(90 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	certPath := filepath.Join(dir, "fullchain.pem")
	keyPath := filepath.Join(dir, "privkey.pem")

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	if err := atomicWrite(certPath, certPEM, 0o644); err != nil {
		return nil, err
	}
	if err := atomicWrite(keyPath, keyPEM, 0o600); err != nil {
		return nil, err
	}
	return &Result{
		CertPath:  certPath,
		KeyPath:   keyPath,
		Domains:   req.Domains,
		ExpiresAt: tmpl.NotAfter.UTC().Format(time.RFC3339),
		Provider:  "mock",
	}, nil
}

// ---------------------------------------------------------------------------
// ACME (Let's Encrypt)
// ---------------------------------------------------------------------------

func acmeIssue(ctx context.Context, opts Options, req OrderRequest) (*Result, error) {
	if len(req.Domains) == 0 {
		return nil, errors.New("no domains for certificate")
	}
	dirURL := DirProduction
	switch req.Mode {
	case "staging":
		dirURL = DirStaging
	case "production", "":
		dirURL = DirProduction
	default:
		return nil, fmt.Errorf("unknown acme mode %q", req.Mode)
	}

	acctKey, err := getOrCreateAccountKey(filepath.Join(opts.AccountDir, "epicpanel-acme-account.key"))
	if err != nil {
		return nil, fmt.Errorf("acme account key: %w", err)
	}
	client := &acme.Client{Key: acctKey, DirectoryURL: dirURL}
	if opts.HTTPClient != nil {
		client.HTTPClient = opts.HTTPClient
	}
	acct := &acme.Account{}
	if req.Email != "" {
		acct.Contact = []string{"mailto:" + req.Email}
	}
	if _, err := client.Register(ctx, acct, func(tosURL string) bool { return true }); err != nil {
		if !errors.Is(err, acme.ErrAccountAlreadyExists) {
			return nil, fmt.Errorf("acme register: %w", err)
		}
	}

	order, err := client.AuthorizeOrder(ctx, acme.DomainIDs(req.Domains...))
	if err != nil {
		return nil, fmt.Errorf("acme authorize order: %w", err)
	}

	if err := solveHTTP01V2(ctx, client, order, req.WebRoot); err != nil {
		return nil, fmt.Errorf("acme challenge: %w", err)
	}

	// Wait for the order to be ready or valid; then create the certificate.
	readyOrValid := func(s string) bool { return s == acme.StatusReady || s == acme.StatusValid }
	order, err = client.WaitOrder(ctx, order.URI)
	if err != nil {
		return nil, fmt.Errorf("acme wait order: %w", err)
	}
	if !readyOrValid(order.Status) {
		return nil, fmt.Errorf("acme order status %s (expected ready/valid)", order.Status)
	}

	csr, key, err := makeCSR(req.Domains)
	if err != nil {
		return nil, err
	}
	certChain, _, err := client.CreateOrderCert(ctx, order.URI, csr, true)
	if err != nil {
		return nil, fmt.Errorf("acme create cert: %w", err)
	}

	dir := filepath.Join(opts.CertsDir, req.SiteSlug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	certPath := filepath.Join(dir, "fullchain.pem")
	keyPath := filepath.Join(dir, "privkey.pem")
	if err := atomicWrite(certPath, bytes.Join(certChain, nil), 0o644); err != nil {
		return nil, err
	}
	if err := writeSiteKey(keyPath, key); err != nil {
		return nil, err
	}

	expires, err := certExpiry(certChain)
	if err != nil {
		return nil, err
	}
	return &Result{
		CertPath:  certPath,
		KeyPath:   keyPath,
		Domains:   req.Domains,
		ExpiresAt: expires.UTC().Format(time.RFC3339),
		Provider:  "acme",
	}, nil
}

// solveHTTP01V2 completes every HTTP-01 authorization by placing the
// challenge response into the site webroot; nginx serves it via try_files.
func solveHTTP01V2(ctx context.Context, client *acme.Client, order *acme.Order, webroot string) error {
	if webroot == "" {
		return errors.New("webroot is required for HTTP-01")
	}
	wellKnown := filepath.Join(webroot, ".well-known", "acme-challenge")
	if err := os.MkdirAll(wellKnown, 0o755); err != nil {
		return err
	}
	placed := []string{}
	defer func() {
		for _, p := range placed {
			_ = os.Remove(p)
		}
	}()

	for _, authzURL := range order.AuthzURLs {
		authz, err := client.GetAuthorization(ctx, authzURL)
		if err != nil {
			return err
		}
		http01 := findHTTP01Challenge(authz.Challenges)
		if http01 == nil {
			return fmt.Errorf("authorization %s has no http-01 challenge", authzURL)
		}
		keyAuth, err := client.HTTP01ChallengeResponse(http01.Token)
		if err != nil {
			return err
		}
		challengePath := filepath.Join(wellKnown, http01.Token)
		if err := atomicWrite(challengePath, []byte(keyAuth), 0o644); err != nil {
			return err
		}
		placed = append(placed, challengePath)

		if _, err := client.Accept(ctx, http01); err != nil {
			return err
		}
		if _, err := client.WaitAuthorization(ctx, authz.URI); err != nil {
			return err
		}
	}
	return nil
}

func findHTTP01Challenge(challenges []*acme.Challenge) *acme.Challenge {
	for _, ch := range challenges {
		if ch.Type == "http-01" {
			return ch
		}
	}
	return nil
}

func makeCSR(domains []string) ([]byte, *rsa.PrivateKey, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.CertificateRequest{DNSNames: domains}
	der, err := x509.CreateCertificateRequest(rand.Reader, tmpl, key)
	if err != nil {
		return nil, nil, err
	}
	return der, key, nil
}

func writeSiteKey(path string, key *rsa.PrivateKey) error {
	return atomicWrite(path, pem.EncodeToMemory(&pem.Block{
		Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key),
	}), 0o600)
}

func certExpiry(chain [][]byte) (time.Time, error) {
	for _, der := range chain {
		c, err := x509.ParseCertificate(der)
		if err == nil {
			return c.NotAfter, nil
		}
	}
	return time.Time{}, errors.New("no parseable certificate in chain")
}

// ---------------------------------------------------------------------------
// Shared helpers
// ---------------------------------------------------------------------------

func getOrCreateAccountKey(path string) (*rsa.PrivateKey, error) {
	if raw, err := os.ReadFile(path); err == nil {
		if block, _ := pem.Decode(raw); block != nil {
			if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
				return k, nil
			}
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	raw := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)})
	if err := atomicWrite(path, raw, 0o600); err != nil {
		return nil, err
	}
	return k, nil
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Remove deletes a site's certificate directory (best effort).
func Remove(certsDir, siteSlug string) error {
	dir := filepath.Join(certsDir, siteSlug)
	if strings.TrimSpace(siteSlug) == "" {
		return errors.New("invalid site slug")
	}
	return os.RemoveAll(dir)
}
