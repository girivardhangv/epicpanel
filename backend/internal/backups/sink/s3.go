// SigV4 client for S3-compatible object storage. Dependency-free (net/http
// only, per the wave contract): AWS Signature Version 4 over a minimal
// surface — Put/Get/Delete/Stat/List — enough for backup artifacts.
// Testable against httptest with known AWS test vectors.
package sink

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	s3Algorithm    = "AWS4-HMAC-SHA256"
	s3TimeFormat   = "20060102T150405Z"
	s3EmptyPayload = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	// UNSIGNED-PAYLOAD is the S3-specific marker accepted by header-auth
	// requests (AWS S3 + MinIO + compatible stores): the body hash is not
	// verified server-side, which lets us stream without buffering.
	s3UnsignedPayload = "UNSIGNED-PAYLOAD"
)

// S3Client speaks minimal S3 with SigV4 request signing.
type S3Client struct {
	Endpoint    string // http(s)://host[:port]
	Region      string
	Bucket      string
	Prefix      string
	AccessKeyID string
	SecretKey   string
	PathStyle   bool // path-style (bucket in URL path) vs virtual-host style
	// HTTPClient is injectable (tests); default a bounded client.
	HTTPClient *http.Client
}

// key derives the SigV4 signing key (AWS documented chain).
func sigKey(secret, date, region, service string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secret), date)
	kRegion := hmacSHA256(kDate, region)
	kService := hmacSHA256(kRegion, service)
	return hmacSHA256(kService, "aws4_request")
}

func hmacSHA256(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}

func sha256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// canonicalURI escapes the object key per S3 rules (each path segment,
// preserving slashes).
func canonicalURI(prefix, key string) string {
	raw := "/" + strings.Trim(prefix+"/"+key, "/")
	segs := strings.Split(raw, "/")
	for i, seg := range segs {
		segs[i] = s3Escape(seg)
	}
	return strings.Join(segs, "/")
}

// s3Escape percent-encodes per RFC 3986 (unreserved = A-Z a-z 0-9 - _ . ~),
// which is what AWS expects in the canonical URI.
func s3Escape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9',
			c == '-', c == '_', c == '.', c == '~':
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// canonicalRequest builds the SigV4 canonical request for one signed request
// (exported for the known-vector test).
func canonicalRequest(method, canonicalURI, rawQuery, canonicalHeaders, signedHeaders, payloadHash string) string {
	return strings.Join([]string{method, canonicalURI, rawQuery, canonicalHeaders, signedHeaders, payloadHash}, "\n")
}

// SignString computes the SigV4 signature hex for a canonical request.
func SignString(secret, date, region, service, stringToSign string) string {
	return hex.EncodeToString(hmacSHA256(sigKey(secret, date, region, service), stringToSign))
}

// Sign computes the SigV4 Authorization header for one request. amzDate is
// in s3TimeFormat. The signed header set is fixed
// (host;x-amz-content-sha256;x-amz-date) — the client always sends exactly
// those three headers.
func (c *S3Client) Sign(req *http.Request, payloadHash, amzDate string) string {
	date := amzDate[:8]
	_, host := c.hostFor(req.URL)

	canonicalHeaders := "host:" + host + "\n" +
		"x-amz-content-sha256:" + payloadHash + "\n" +
		"x-amz-date:" + amzDate + "\n"
	signedHeaders := "host;x-amz-content-sha256;x-amz-date"

	canonicalReq := canonicalRequest(req.Method, canonicalURI(c.Prefix, c.objectKey(req)),
		req.URL.RawQuery, canonicalHeaders, signedHeaders, payloadHash)

	scope := strings.Join([]string{date, c.Region, "s3", "aws4_request"}, "/")
	stringToSign := strings.Join([]string{
		s3Algorithm, amzDate, scope, sha256Hex([]byte(canonicalReq)),
	}, "\n")

	signature := SignString(c.SecretKey, date, c.Region, "s3", stringToSign)
	return s3Algorithm + " Credential=" + c.AccessKeyID + "/" + scope +
		", SignedHeaders=" + signedHeaders + ", Signature=" + signature
}

// hostFor returns (bucket, host) per path/virtual-host style.
func (c *S3Client) hostFor(u *url.URL) (string, string) {
	host := u.Host
	if c.PathStyle {
		return c.Bucket, host
	}
	return c.Bucket, c.Bucket + "." + host
}

// objectKey extracts the object key from the request: the URL path without
// the bucket segment (path style) minus the configured prefix.
func (c *S3Client) objectKey(req *http.Request) string {
	p := strings.TrimPrefix(req.URL.Path, "/")
	if c.PathStyle {
		p = strings.TrimPrefix(p, c.Bucket+"/")
	}
	return strings.TrimPrefix(p, strings.Trim(c.Prefix, "/")+"/")
}

func (c *S3Client) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return &http.Client{Timeout: 30 * time.Minute}
}

func (c *S3Client) validate() error {
	if c.Endpoint == "" || c.Bucket == "" || c.Region == "" || c.AccessKeyID == "" || c.SecretKey == "" {
		return fmt.Errorf("%w: object storage target is incomplete (endpoint/region/bucket/keys)", ErrInvalid)
	}
	return nil
}

// doRaw signs and executes one request, returning the response (caller
// closes the body). Payload hash: UNSIGNED-PAYLOAD for bodies, empty-payload
// hash otherwise.
func (c *S3Client) doRaw(ctx context.Context, method, key string, body io.Reader, length int64, query url.Values) (*http.Response, error) {
	if err := c.validate(); err != nil {
		return nil, err
	}
	payloadHash := s3EmptyPayload
	if body != nil {
		payloadHash = s3UnsignedPayload
	}
	amzDate := time.Now().UTC().Format(s3TimeFormat)

	httpReq, err := http.NewRequestWithContext(ctx, method, c.endpointURL(key, query), body)
	if err != nil {
		return nil, err
	}
	if length > 0 {
		httpReq.ContentLength = length
	}
	httpReq.Header.Set("x-amz-content-sha256", payloadHash)
	httpReq.Header.Set("x-amz-date", amzDate)
	httpReq.Header.Set("Authorization", c.Sign(httpReq, payloadHash, amzDate))

	resp, err := c.httpClient().Do(httpReq)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		data, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, s3Error(resp.StatusCode, data)
	}
	return resp, nil
}

func (c *S3Client) endpointURL(key string, query url.Values) string {
	base := strings.TrimSuffix(c.Endpoint, "/")
	objPath := strings.Trim(c.Prefix+"/"+key, "/")
	if c.PathStyle {
		u := base + "/" + c.Bucket
		if objPath != "" {
			u += "/" + objPath
		}
		if len(query) > 0 {
			u += "?" + query.Encode()
		}
		return u
	}
	host := c.Bucket + "." + strings.TrimPrefix(strings.TrimPrefix(base, "https://"), "http://")
	scheme := "https"
	if strings.HasPrefix(base, "http://") {
		scheme = "http"
	}
	u := scheme + "://" + host
	if objPath != "" {
		u += "/" + objPath
	}
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	return u
}

type s3ErrBody struct {
	Code    string `xml:"Code"`
	Message string `xml:"Message"`
}

func s3Error(status int, body []byte) error {
	if status == http.StatusNotFound {
		return ErrNotFound
	}
	var eb s3ErrBody
	_ = xml.Unmarshal(body, &eb)
	if eb.Code != "" {
		return fmt.Errorf("s3 %d %s: %s", status, eb.Code, eb.Message)
	}
	return fmt.Errorf("s3 status %d: %s", status, truncateForLog(string(body), 200))
}

func truncateForLog(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// Put uploads a local file to key.
func (c *S3Client) Put(ctx context.Context, localPath, key string) (int64, error) {
	f, err := os.Open(localPath)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return 0, err
	}
	resp, err := c.doRaw(ctx, http.MethodPut, key, f, fi.Size(), nil)
	if err != nil {
		return 0, err
	}
	resp.Body.Close()
	return fi.Size(), nil
}

// Get downloads key into destPath.
func (c *S3Client) Get(ctx context.Context, key, destPath string) error {
	resp, err := c.doRaw(ctx, http.MethodGet, key, nil, 0, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := os.MkdirAll(dirOf(destPath), 0o750); err != nil {
		return err
	}
	out, err := os.OpenFile(destPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o640)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, resp.Body); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// Stat reports one object.
func (c *S3Client) Stat(ctx context.Context, key string) (ObjectInfo, error) {
	resp, err := c.doRaw(ctx, http.MethodHead, key, nil, 0, nil)
	if err != nil {
		return ObjectInfo{}, err
	}
	resp.Body.Close()
	size, _ := strconv.ParseInt(resp.Header.Get("Content-Length"), 10, 64)
	mod, _ := http.ParseTime(resp.Header.Get("Last-Modified"))
	return ObjectInfo{SizeBytes: size, ModTime: mod}, nil
}

// Delete removes one object (S3 DELETE is idempotent: 204 even when absent).
func (c *S3Client) Delete(ctx context.Context, key string) error {
	resp, err := c.doRaw(ctx, http.MethodDelete, key, nil, 0, nil)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

type listBucketResult struct {
	Contents []struct {
		Key          string `xml:"Key"`
		Size         int64  `xml:"Size"`
		LastModified string `xml:"LastModified"`
	} `xml:"Contents"`
	IsTruncated bool `xml:"IsTruncated"`
}

// listKeys lists object keys under a prefix.
func (c *S3Client) listKeys(ctx context.Context, prefix string) ([]string, error) {
	q := url.Values{}
	q.Set("list-type", "2")
	full := strings.Trim(strings.Trim(c.Prefix, "/")+"/"+prefix, "/")
	if full != "" {
		q.Set("prefix", full)
	}
	resp, err := c.doRaw(ctx, http.MethodGet, "", nil, 0, q)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var res listBucketResult
	if err := xml.Unmarshal(data, &res); err != nil {
		return nil, err
	}
	var out []string
	for _, o := range res.Contents {
		key := o.Key
		if p := strings.Trim(c.Prefix, "/"); p != "" && strings.HasPrefix(key, p+"/") {
			key = strings.TrimPrefix(key, p+"/")
		}
		out = append(out, key)
	}
	sort.Strings(out)
	return out, nil
}

// S3Sink adapts S3Client to the Sink interface.
type S3Sink struct {
	Client *S3Client
	Prefix string
}

// Kind implements Sink.
func (s *S3Sink) Kind() string { return KindObject }

// Put implements Sink.
func (s *S3Sink) Put(ctx context.Context, localPath, name string) (Ref, error) {
	if !safeName(name) {
		return Ref{}, fmt.Errorf("%w: invalid artifact name", ErrInvalid)
	}
	if _, err := s.Client.Put(ctx, localPath, name); err != nil {
		return Ref{}, err
	}
	return Ref{Kind: KindObject, Path: name}, nil
}

// Get implements Sink.
func (s *S3Sink) Get(ctx context.Context, ref Ref, destPath string) error {
	return s.Client.Get(ctx, ref.Path, destPath)
}

// Stat implements Sink.
func (s *S3Sink) Stat(ctx context.Context, ref Ref) (ObjectInfo, error) {
	return s.Client.Stat(ctx, ref.Path)
}

// Delete implements Sink.
func (s *S3Sink) Delete(ctx context.Context, ref Ref) error {
	return s.Client.Delete(ctx, ref.Path)
}

func dirOf(p string) string {
	if i := strings.LastIndexByte(p, '/'); i > 0 {
		return p[:i]
	}
	return "."
}
