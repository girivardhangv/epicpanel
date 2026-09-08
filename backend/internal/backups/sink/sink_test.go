package sink

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// SigV4 known vector — the AWS S3 documentation "example GET object" from
// "Authenticating Requests: Using the Authorization Header". Expected values
// are the documented constants; a signature mismatch fails the test.
// ---------------------------------------------------------------------------

func TestSigV4KnownVector(t *testing.T) {
	c := &S3Client{
		Endpoint:    "https://examplebucket.s3.amazonaws.com",
		Region:      "us-east-1",
		Bucket:      "examplebucket",
		AccessKeyID: "AKIAIOSFODNN7EXAMPLE",
		SecretKey:   "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
	}

	// Reconstruct the documented canonical request for:
	//   GET /test.txt, Range: bytes=0-9, 20130524T000000Z
	amzDate := "20130524T000000Z"
	payloadHash := s3EmptyPayload
	canonicalHeaders := "host:examplebucket.s3.amazonaws.com\nrange:bytes=0-9\nx-amz-content-sha256:" +
		payloadHash + "\nx-amz-date:" + amzDate + "\n"
	signedHeaders := "host;range;x-amz-content-sha256;x-amz-date"
	canonicalReq := canonicalRequest("GET", "/test.txt", "", canonicalHeaders, signedHeaders, payloadHash)

	scope := "20130524/us-east-1/s3/aws4_request"
	stringToSign := strings.Join([]string{
		s3Algorithm, amzDate, scope, sha256Hex([]byte(canonicalReq)),
	}, "\n")
	got := SignString(c.SecretKey, "20130524", "us-east-1", "s3", stringToSign)

	// AWS-documented signature for this example.
	want := "f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41"
	if got != want {
		t.Fatalf("SigV4 signature mismatch:\n got %s\nwant %s", got, want)
	}

	// Determinism: same inputs, same signature.
	if again := SignString(c.SecretKey, "20130524", "us-east-1", "s3", stringToSign); again != got {
		t.Fatalf("signature not deterministic")
	}
	// A different secret must produce a different signature.
	if other := SignString("wrongs3cretkey", "20130524", "us-east-1", "s3", stringToSign); other == got {
		t.Fatalf("wrong secret produced the same signature")
	}
}

// ---------------------------------------------------------------------------
// S3 fake + round-trips (put/get/head/delete/list with signature checks).
// ---------------------------------------------------------------------------

type fakeS3 struct {
	objects map[string][]byte
	badAuth int
}

func (f *fakeS3) key(r *http.Request) string {
	return strings.TrimPrefix(r.URL.Path, "/bkt/")
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Verify the signed headers set + credential scope (honest fake).
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential=testkey/20") ||
		!strings.Contains(auth, "/us-east-1/s3/aws4_request, SignedHeaders=host;x-amz-content-sha256;x-amz-date") ||
		!strings.Contains(auth, "Signature=") {
		f.badAuth++
		w.WriteHeader(http.StatusForbidden)
		return
	}
	if r.Header.Get("x-amz-date") == "" {
		f.badAuth++
		w.WriteHeader(http.StatusForbidden)
		return
	}
	switch r.Method {
	case http.MethodPut:
		b, _ := io.ReadAll(r.Body)
		f.objects[f.key(r)] = b
		w.WriteHeader(http.StatusOK)
	case http.MethodGet:
		if r.URL.Query().Get("list-type") == "2" {
			type item struct {
				Key string `xml:"Key"`
			}
			type res struct {
				Items []item `xml:"Contents"`
			}
			prefix := r.URL.Query().Get("prefix")
			out := res{}
			for k := range f.objects {
				if strings.HasPrefix(k, prefix) {
					out.Items = append(out.Items, item{Key: k})
				}
			}
			w.WriteHeader(http.StatusOK)
			enc := xml.NewEncoder(w)
			_ = enc.Encode(out)
			_ = enc.Flush()
			return
		}
		b, ok := f.objects[f.key(r)]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(b)
	case http.MethodHead:
		b, ok := f.objects[f.key(r)]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(b)))
		w.WriteHeader(http.StatusOK)
	case http.MethodDelete:
		delete(f.objects, f.key(r))
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func newFakeS3(t *testing.T) (*fakeS3, *S3Client) {
	t.Helper()
	f := &fakeS3{objects: map[string][]byte{}}
	ts := httptest.NewServer(f)
	t.Cleanup(ts.Close)
	client := &S3Client{
		Endpoint: ts.URL, Region: "us-east-1", Bucket: "bkt", Prefix: "backups",
		AccessKeyID: "testkey", SecretKey: "testsecret", PathStyle: true,
		HTTPClient: ts.Client(),
	}
	return f, client
}

func TestS3RoundTrip(t *testing.T) {
	f, client := newFakeS3(t)
	ctx := context.Background()

	src := filepath.Join(t.TempDir(), "artifact.tar.gz")
	payload := []byte("EPICPANEL-ARTIFACT-CONTENT-0123456789")
	if err := os.WriteFile(src, payload, 0o640); err != nil {
		t.Fatal(err)
	}

	s := &S3Sink{Client: client, Prefix: "backups"}
	ref, err := s.Put(ctx, src, "abc-123.tar.gz")
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if ref.Kind != KindObject || ref.Path != "abc-123.tar.gz" {
		t.Fatalf("ref: %+v", ref)
	}

	info, err := s.Stat(ctx, ref)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.SizeBytes != int64(len(payload)) {
		t.Fatalf("stat size %d want %d", info.SizeBytes, len(payload))
	}

	dst := filepath.Join(t.TempDir(), "out.tar.gz")
	if err := s.Get(ctx, ref, dst); err != nil {
		t.Fatalf("get: %v", err)
	}
	got, _ := os.ReadFile(dst)
	if !bytes.Equal(got, payload) {
		t.Fatalf("round trip content mismatch")
	}

	names, err := ListNames(ctx, s, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(names) != 1 || names[0] != "abc-123.tar.gz" {
		t.Fatalf("list names: %v", names)
	}

	if err := s.Delete(ctx, ref); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.Stat(ctx, ref); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stat after delete: %v want ErrNotFound", err)
	}
	if f.badAuth != 0 {
		t.Fatalf("fake rejected %d requests as badly signed", f.badAuth)
	}
}

func TestS3IncompleteConfigFails(t *testing.T) {
	client := &S3Client{Endpoint: "http://x", Region: "us-east-1", Bucket: "bkt"}
	if _, err := client.Put(context.Background(), "/dev/null", "k"); err == nil {
		t.Fatalf("expected error for incomplete config")
	}
}

// ---------------------------------------------------------------------------
// Chunked AES-GCM round-trips + corruption detection + key wrap.
// ---------------------------------------------------------------------------

func TestCryptoRoundTrip(t *testing.T) {
	dir := t.TempDir()
	key := make([]byte, DataKeySize)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	sizes := []int{0, 1, 100, DefaultChunkSize - 1, DefaultChunkSize, DefaultChunkSize + 1, 3*DefaultChunkSize + 77}
	for _, size := range sizes {
		name := fmt.Sprintf("in-%d", size)
		src := filepath.Join(dir, name+".src")
		dst := filepath.Join(dir, name+".enc")
		out := filepath.Join(dir, name+".out")
		body := make([]byte, size)
		if _, err := rand.Read(body); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(src, body, 0o640); err != nil {
			t.Fatal(err)
		}
		n, err := EncryptFile(key, src, dst)
		if err != nil {
			t.Fatalf("encrypt %d: %v", size, err)
		}
		if n != int64(size) {
			t.Fatalf("encrypt %d: wrote %d", size, n)
		}
		if _, err := DecryptFile(key, dst, out); err != nil {
			t.Fatalf("decrypt %d: %v", size, err)
		}
		got, _ := os.ReadFile(out)
		if !bytes.Equal(got, body) {
			t.Fatalf("round trip %d mismatch", size)
		}
		// Wrong key must fail.
		bad := append([]byte(nil), key...)
		bad[0] ^= 0xff
		if _, err := DecryptFile(bad, dst, filepath.Join(dir, name+".bad")); err == nil {
			t.Fatalf("decrypt with wrong key succeeded (%d)", size)
		}
	}
}

func TestCryptoCorruptionDetected(t *testing.T) {
	dir := t.TempDir()
	key := make([]byte, DataKeySize)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(dir, "src")
	enc := filepath.Join(dir, "enc")
	out := filepath.Join(dir, "out")
	if err := os.WriteFile(src, []byte("hello epicpanel backup"), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := EncryptFile(key, src, enc); err != nil {
		t.Fatal(err)
	}
	// Flip one byte inside a chunk (past the 10-byte header).
	blob, _ := os.ReadFile(enc)
	blob[32] ^= 0x01
	if err := os.WriteFile(enc, blob, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := DecryptFile(key, enc, out); err == nil {
		t.Fatalf("corrupted artifact decrypted without error")
	}
	// Truncated header must be refused.
	if err := os.WriteFile(enc, magic[:4], 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := DecryptFile(key, enc, out); err == nil {
		t.Fatalf("truncated header accepted")
	}
}

func TestKeyWrapRoundTrip(t *testing.T) {
	key, err := NewDataKey()
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := WrapKey(key)
	if err != nil {
		t.Fatal(err)
	}
	// Sealed blob must not contain the raw key bytes (ciphertext != plaintext).
	if strings.Contains(sealed, string(key)) {
		t.Fatalf("sealed key leaks plaintext")
	}
	back, err := UnwrapKey(sealed)
	if err != nil {
		t.Fatalf("unwrap: %v", err)
	}
	if !bytes.Equal(back, key) {
		t.Fatalf("wrapped key round trip mismatch")
	}
}

func TestCredsSealedNotPlaintext(t *testing.T) {
	enc, err := EncodeCreds(Credentials{AccessKeyID: "AKIAEXAMPLE", SecretKey: "super-secret-value"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(enc, "super-secret-value") || strings.Contains(enc, "AKIAEXAMPLE") {
		t.Fatalf("sealed credentials contain plaintext")
	}
	var c Credentials
	if err := DecodeCreds(enc, &c); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if c.SecretKey != "super-secret-value" || c.AccessKeyID != "AKIAEXAMPLE" {
		t.Fatalf("creds round trip mismatch: %+v", c)
	}
}

// ---------------------------------------------------------------------------
// Local sink round-trips + base confinement.
// ---------------------------------------------------------------------------

func TestLocalSinkRoundTripAndConfinement(t *testing.T) {
	root := t.TempDir()
	s := &LocalSink{Root: root}
	ctx := context.Background()

	src := filepath.Join(t.TempDir(), "a.tar.gz")
	if err := os.WriteFile(src, []byte("local-content"), 0o640); err != nil {
		t.Fatal(err)
	}
	ref, err := s.Put(ctx, src, "bk-1/a.tar.gz")
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	info, err := s.Stat(ctx, ref)
	if err != nil || info.SizeBytes != int64(len("local-content")) {
		t.Fatalf("stat: %v %+v", err, info)
	}
	dst := filepath.Join(t.TempDir(), "copy.tar.gz")
	if err := s.Get(ctx, ref, dst); err != nil {
		t.Fatalf("get: %v", err)
	}
	if b, _ := os.ReadFile(dst); string(b) != "local-content" {
		t.Fatalf("copy mismatch")
	}

	// Path escape attempts must be refused.
	for _, evil := range []string{"../evil", "/etc/passwd", "a/../../b"} {
		if err := s.Delete(ctx, Ref{Kind: KindLocal, Path: evil}); err == nil {
			t.Fatalf("delete accepted escape ref %q", evil)
		}
	}
	// Deleting an escape ref must not have touched anything outside root.
	if _, err := os.Stat("/etc/passwd"); err != nil {
		t.Fatalf("/etc/passwd vanished?!")
	}

	// Delete inside root works.
	if err := s.Delete(ctx, ref); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.Stat(ctx, ref); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stat after delete: %v", err)
	}
}

func TestSafeName(t *testing.T) {
	for _, ok := range []string{"a.tar.gz", "bk_1/sub/x", "123", "A-b_c.tar"} {
		if !safeName(ok) {
			t.Errorf("safeName(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{"", "../x", "a b", "a;b", "$x", strings.Repeat("a", 201)} {
		if safeName(bad) {
			t.Errorf("safeName(%q) = true, want false", bad)
		}
	}
}

// ---------------------------------------------------------------------------
// Remote sink: config validation + fixed-arg transport seam.
// ---------------------------------------------------------------------------

type fakeTransport struct {
	puts, gets, dels []string
}

func (f *fakeTransport) Put(_ context.Context, src, dest string) error {
	f.puts = append(f.puts, src+" -> "+dest)
	return nil
}
func (f *fakeTransport) Get(_ context.Context, src, dest string) error {
	f.gets = append(f.gets, src+" -> "+dest)
	return nil
}
func (f *fakeTransport) Delete(_ context.Context, dest string) error {
	f.dels = append(f.dels, dest)
	return nil
}
func (f *fakeTransport) Stat(_ context.Context, dest string) (ObjectInfo, error) {
	return ObjectInfo{SizeBytes: 5}, nil
}

func TestRemoteSinkConfigAndSeam(t *testing.T) {
	if err := ValidateRemoteConfig(RemoteConfig{Host: "bad host", User: "u", Path: "/b"}); err == nil {
		t.Fatalf("host with space accepted")
	}
	if err := ValidateRemoteConfig(RemoteConfig{Host: "h", User: "u", Path: "relative"}); err == nil {
		t.Fatalf("relative path accepted")
	}
	if err := ValidateRemoteConfig(RemoteConfig{Host: "h", User: "u", Path: "/ok", Transport: "shell"}); err == nil {
		t.Fatalf("unknown transport accepted")
	}
	if err := ValidateRemoteConfig(RemoteConfig{Host: "backup.example.test", User: "bk", Path: "/srv/bk", Port: 22}); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}

	ft := &fakeTransport{}
	s := &RemoteSink{
		Config:    RemoteConfig{Host: "backup.example.test", User: "bk", Path: "/srv/bk", Port: 22},
		Transport: ft,
	}
	ctx := context.Background()
	ref, err := s.Put(ctx, "/tmp/local.tar.gz", "job-1/artifact.tar.gz")
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if ref.Kind != KindRemote {
		t.Fatalf("ref kind: %s", ref.Kind)
	}
	if len(ft.puts) != 1 || !strings.Contains(ft.puts[0], "bk@backup.example.test:/srv/bk/job-1/artifact.tar.gz") {
		t.Fatalf("transport put spec: %v", ft.puts)
	}
	// Malicious name is refused before any transport call.
	if _, err := s.Put(ctx, "/tmp/x", "../..//evil"); err == nil {
		t.Fatalf("malicious artifact name accepted")
	}
}
