package agent

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/go-acme/lego/v4/certcrypto"
	"github.com/go-acme/lego/v4/certificate"
	"github.com/go-acme/lego/v4/lego"
	"github.com/go-acme/lego/v4/providers/http/webroot"
	"github.com/go-acme/lego/v4/registration"
)

const sslBaseDir = "/etc/epicpanel/ssl"
const acmeWebroot = "/var/www/_acme-challenge"

// CertOutcome is returned in the issue_certificate job result.
type CertOutcome struct {
	Domain     string    `json:"domain"`
	NotAfter   time.Time `json:"not_after"`
	Issuer     string    `json:"issuer"`
	CertPath   string    `json:"cert_path"`
	KeyPath    string    `json:"key_path"`
	SelfSigned bool      `json:"self_signed"`
}

func certDir(domain string) string {
	return filepath.Join(sslBaseDir, domain)
}

// EnsureCertificate obtains or renews a certificate for the domain.
// selfsigned: openssl-free, generated in-process (dev). letsencrypt: real ACME
// HTTP-01 via lego with a webroot served by our nginx vhosts.
func (e *Executor) EnsureCertificate(ctx context.Context, domain, mode, acmeEmail, acmeDirectory string) (*CertOutcome, error) {
	switch mode {
	case "selfsigned":
		return e.ensureSelfSigned(ctx, domain)
	case "letsencrypt":
		return e.ensureLetsEncrypt(ctx, domain, acmeEmail, acmeDirectory)
	default:
		return nil, fmt.Errorf("unsupported ssl mode %q", mode)
	}
}

// ensureSelfSigned creates a 1-year self-signed cert if missing or near expiry.
func (e *Executor) ensureSelfSigned(ctx context.Context, domain string) (*CertOutcome, error) {
	dir := certDir(domain)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	certPath := filepath.Join(dir, "fullchain.pem")
	keyPath := filepath.Join(dir, "privkey.pem")

	if _, err := os.Stat(certPath); err == nil {
		if expires, err := certExpiry(certPath); err == nil && time.Until(expires) > 30*24*time.Hour {
			return &CertOutcome{Domain: domain, NotAfter: expires, Issuer: "self-signed", CertPath: certPath, KeyPath: keyPath, SelfSigned: true}, nil
		}
	}

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	tmpl := x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: domain, Organization: []string{"EpicPanel"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{domain},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	if err := writePem(certPath, "CERTIFICATE", der); err != nil {
		return nil, err
	}
	if err := writePem(keyPath, "RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(key)); err != nil {
		return nil, err
	}
	slog.Info("self-signed certificate issued", "domain", domain)
	return &CertOutcome{Domain: domain, NotAfter: tmpl.NotAfter, Issuer: "self-signed", CertPath: certPath, KeyPath: keyPath, SelfSigned: true}, nil
}

// atomicWrite writes data to a temp file in the same directory, fsyncs and
// renames it into place so a crash mid-write can never leave a truncated
// cert or key on disk (nginx would fail to load the pair).
func atomicWrite(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".ep-tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// writePem encodes raw DER to a PEM file atomically (tmp + rename).
func writePem(path, blockType string, der []byte) error {
	return atomicWrite(path, pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der}), 0o600)
}

func certExpiry(certPath string) (time.Time, error) {
	b, err := os.ReadFile(certPath)
	if err != nil {
		return time.Time{}, err
	}
	block, _ := pem.Decode(b)
	if block == nil {
		return time.Time{}, fmt.Errorf("no pem block")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return time.Time{}, err
	}
	return cert.NotAfter, nil
}

// acmeUser is the lego registration account (key persisted per agent).
type acmeUser struct {
	email        string
	registration *registration.Resource
	key          crypto.PrivateKey
}

func (u *acmeUser) GetEmail() string                        { return u.email }
func (u *acmeUser) GetRegistration() *registration.Resource { return u.registration }
func (u *acmeUser) GetPrivateKey() crypto.PrivateKey        { return u.key }

// ensureLetsEncrypt obtains/renews a LE certificate. Requires the domain to
// resolve to this server and port 80 to be reachable from the internet.
func (e *Executor) ensureLetsEncrypt(ctx context.Context, domain, email, directory string) (*CertOutcome, error) {
	if directory == "" {
		directory = lego.LEDirectoryProduction
	}

	// Pre-flight: fail fast with a clear message when the domain cannot
	// resolve at all (ACME would only time out later). A resolved-but-not-
	// ours mismatch only warns: CDN-proxied domains can still pass HTTP-01.
	ips, err := net.LookupHost(domain)
	if err != nil || len(ips) == 0 {
		return nil, fmt.Errorf("domain %s does not resolve — point its DNS records at this server first", domain)
	}
	if local, lerr := localIPs(); lerr == nil {
		matches := false
		for _, rip := range ips {
			for _, sip := range local {
				if rip == sip {
					matches = true
					break
				}
			}
		}
		if !matches {
			slog.Warn("acme dns mismatch (continuing; cdn-fronted domains may still pass http-01)", "domain", domain, "resolved", ips, "local", local)
		}
	}

	dir := certDir(domain)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(acmeWebroot, 0o755); err != nil {
		return nil, err
	}

	accountKeyPath := filepath.Join(sslBaseDir, "acme-account.key")
	key, err := loadOrCreateAccountKey(accountKeyPath)
	if err != nil {
		return nil, fmt.Errorf("acme account key: %w", err)
	}
	user := &acmeUser{email: email, key: key}

	cfg := lego.NewConfig(user)
	cfg.CADirURL = directory
	cfg.Certificate.KeyType = certcrypto.RSA2048

	client, err := lego.NewClient(cfg)
	if err != nil {
		return nil, err
	}
	reg, err := client.Registration.ResolveAccountByKey()
	if err != nil {
		reg, err = client.Registration.Register(registration.RegisterOptions{TermsOfServiceAgreed: true})
		if err != nil {
			return nil, fmt.Errorf("acme register: %w", err)
		}
	}
	user.registration = reg

	// lego's webroot solver writes challenges under
	// <webroot>/.well-known/acme-challenge/<token>, which is exactly what
	// the nginx vhosts serve via `root /var/www/_acme-challenge`.
	wp, err := webroot.NewHTTPProvider(acmeWebroot)
	if err != nil {
		return nil, err
	}
	if err := client.Challenge.SetHTTP01Provider(wp); err != nil {
		return nil, err
	}

	names := []string{domain}
	req := certificate.ObtainRequest{Domains: names, Bundle: true}
	certs, err := client.Certificate.Obtain(req)
	if err != nil {
		return nil, fmt.Errorf("acme obtain: %w", err)
	}

	certPath := filepath.Join(dir, "fullchain.pem")
	keyPath := filepath.Join(dir, "privkey.pem")
	// lego returns both fields already PEM-encoded; write them atomically
	// (cert world-readable, key private) via tmp + rename.
	if err := atomicWrite(certPath, certs.Certificate, 0o644); err != nil {
		return nil, err
	}
	if err := atomicWrite(keyPath, certs.PrivateKey, 0o600); err != nil {
		return nil, err
	}
	_ = os.Chmod(dir, 0o750)

	x509Certs, err := certcrypto.ParsePEMBundle(certs.Certificate)
	if err != nil {
		return nil, err
	}
	notAfter := x509Certs[0].NotAfter
	slog.Info("letsencrypt certificate issued", "domain", domain, "not_after", notAfter)
	return &CertOutcome{Domain: domain, NotAfter: notAfter, Issuer: "letsencrypt", CertPath: certPath, KeyPath: keyPath}, nil
}

func loadOrCreateAccountKey(path string) (crypto.PrivateKey, error) {
	if b, err := os.ReadFile(path); err == nil {
		return certcrypto.ParsePEMPrivateKey(b)
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	der := x509.MarshalPKCS1PrivateKey(key)
	if err := writePem(path, "RSA PRIVATE KEY", der); err != nil {
		return nil, err
	}
	return key, nil
}

// RemoveCertificate deletes stored certificates for a domain (idempotent).
func (e *Executor) RemoveCertificate(ctx context.Context, domain string) error {
	return os.RemoveAll(certDir(domain))
}
