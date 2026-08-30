// Development-only reference implementation of the EpicPanel licensing
// contract. It exists so integrators can exercise the real HTTP flow locally;
// it is NOT part of the product and must never ship attached to a panel.
//
// Contract served here mirrors internal/licensing.RemoteClient:
//   POST /v1/activate    {license_key, fingerprint} -> response
//   POST /v1/validate    {fingerprint}              -> response
//   POST /v1/deactivate  {license_id, fingerprint}  -> {"ok":true}
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"io"
	"log"
	"net/http"
	"sync"
	"time"
)

type licenseRecord struct {
	LicenseID   string     `json:"license_id"`
	Plan        string     `json:"plan"`
	Status      string     `json:"status"` // active | expired | suspended | invalid
	Message     string     `json:"message,omitempty"`
	Fingerprint string     `json:"-"`
	IssuedTo    string     `json:"issued_to_name,omitempty"`
	Features    []string   `json:"features"`
	ExpiresAt   *time.Time `json:"expires_at"`
}

type store struct {
	mu       sync.Mutex
	keyIndex map[string]*licenseRecord // key -> record
	fpIndex  map[string]*licenseRecord // fingerprint -> record
}

var licenses = &store{
	keyIndex: map[string]*licenseRecord{},
	fpIndex:  map[string]*licenseRecord{},
}

func main() {
	listen := flag.String("listen", ":9911", "HTTP listen address")
	flag.Parse()

	http.HandleFunc("/v1/activate", logged(handleActivate))
	http.HandleFunc("/v1/validate", logged(handleValidate))
	http.HandleFunc("/v1/deactivate", logged(handleDeactivate))
	http.HandleFunc("/debug", handleDebug)

	log.Printf("[DEV-ONLY] licensing stub listening on %s", *listen)
	log.Fatal(http.ListenAndServe(*listen, nil))
}

// logged echoes the request body so dev operators can see exactly what the
// panel sends (never enable this in a real licensing service).
func logged(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, io.LimitReader(r.Body, 1<<16))
		log.Printf("%s %s body=%q", r.Method, r.URL.Path, buf.String())
		r.Body = io.NopCloser(&buf)
		next(w, r)
	}
}

func handleDebug(w http.ResponseWriter, r *http.Request) {
	licenses.mu.Lock()
	defer licenses.mu.Unlock()
	out := map[string]any{"by_key": licenses.keyIndex, "by_fp": licenses.fpIndex}
	writeJSON(w, http.StatusOK, out)
}

func handleActivate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LicenseKey  string `json:"license_key"`
		Fingerprint string `json:"fingerprint"`
	}
	if !decode(w, r, &req) {
		return
	}

	if len(req.LicenseKey) < 8 || req.Fingerprint == "" {
		writeJSON(w, http.StatusPaymentRequired, licenseRecord{Status: "invalid", Message: "key or fingerprint missing"})
		return
	}

	licenses.mu.Lock()
	defer licenses.mu.Unlock()

	if rec, ok := licenses.keyIndex[req.LicenseKey]; ok && rec.Fingerprint != "" && rec.Fingerprint != req.Fingerprint {
		writeJSON(w, http.StatusPaymentRequired, licenseRecord{Status: "invalid", Message: "already bound to another installation"})
		return
	}
	if rec, ok := licenses.fpIndex[req.Fingerprint]; ok {
		rec.Status = "active"
		writeJSON(w, http.StatusOK, redact(rec))
		return
	}

	sum := sha256.Sum256([]byte(req.LicenseKey))
	expiry := time.Now().UTC().AddDate(0, 12, 0)
	rec := &licenseRecord{
		LicenseID:   hex.EncodeToString(sum[:6]),
		Plan:        planFor(req.LicenseKey),
		Status:      "active",
		Fingerprint: req.Fingerprint,
		IssuedTo:    "Development Customer",
		Features:    []string{"dashboard", "servers"},
		ExpiresAt:   &expiry,
	}
	licenses.keyIndex[req.LicenseKey] = rec
	licenses.fpIndex[req.Fingerprint] = rec

	writeJSON(w, http.StatusOK, redact(rec))
}

func handleValidate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Fingerprint string `json:"fingerprint"`
	}
	if !decode(w, r, &req) {
		return
	}
	licenses.mu.Lock()
	defer licenses.mu.Unlock()

	rec, ok := licenses.fpIndex[req.Fingerprint]
	if !ok || rec.Status != "active" {
		writeJSON(w, http.StatusPaymentRequired, licenseRecord{Status: "invalid", Message: "no active binding"})
		return
	}
	if rec.ExpiresAt != nil && time.Now().After(*rec.ExpiresAt) {
		rec.Status = "expired"
		writeJSON(w, http.StatusPaymentRequired, redact(rec))
		return
	}
	writeJSON(w, http.StatusOK, redact(rec))
}

func handleDeactivate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LicenseID   string `json:"license_id"`
		Fingerprint string `json:"fingerprint"`
	}
	if !decode(w, r, &req) {
		return
	}
	licenses.mu.Lock()
	defer licenses.mu.Unlock()
	for key, rec := range licenses.fpIndex {
		if rec.Fingerprint == req.Fingerprint {
			delete(licenses.fpIndex, key)
			for k, byKey := range licenses.keyIndex {
				if byKey == rec {
					delete(licenses.keyIndex, k)
					break
				}
			}
			break
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	err := json.NewDecoder(r.Body).Decode(dst)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func redact(r *licenseRecord) licenseRecord {
	c := *r
	c.Fingerprint = ""
	return c
}

func planFor(key string) string {
	if len(key) > 4 && key[:5] == "pro-" {
		return "EpicPanel Pro"
	}
	return "EpicPanel Standard"
}
