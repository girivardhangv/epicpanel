// Package sink abstracts where backup artifacts are stored. Phase 11
// drivers: Local (node filesystem — the pre-Phase-11 behavior), Remote
// (ssh/rsync-style target — config only, never arbitrary shell) and Object
// Storage (S3-compatible, dependency-free SigV4 over net/http).
//
// The package is stdlib-only (plus internal/secretbox for the wrapped
// credentials) so the node agent can import it without dragging the
// control-plane HTTP stack into the agent binary.
package sink

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Sink kinds (verbatim from the master doc: Local | Remote | Object Storage).
const (
	KindLocal  = "local"
	KindRemote = "remote"
	KindObject = "object"
)

// Ref locates one stored artifact inside a sink.
type Ref struct {
	// Kind is the sink kind this ref belongs to.
	Kind string `json:"kind"`
	// Path is the sink-relative location: a filesystem path for local
	// (directory containing the archive), a key for object storage, or a
	// remote path for remote targets.
	Path string `json:"path"`
}

// String renders the ref for logs (no credentials involved).
func (r Ref) String() string { return r.Kind + ":" + r.Path }

// ObjectInfo is the Stat result of one stored artifact.
type ObjectInfo struct {
	SizeBytes int64
	ModTime   time.Time
}

// Sink stores and retrieves backup artifacts.
type Sink interface {
	Kind() string
	// Put uploads the local file/dir to name; returns the stored ref.
	Put(ctx context.Context, localPath, name string) (Ref, error)
	// Get downloads the artifact into destPath (file, created if missing).
	Get(ctx context.Context, ref Ref, destPath string) error
	// Stat reports one stored artifact.
	Stat(ctx context.Context, ref Ref) (ObjectInfo, error)
	// Delete removes one stored artifact. Must refuse paths outside the
	// sink's scope (prune jobs are the only caller and their input is
	// control-plane generated, but the sink defends anyway).
	Delete(ctx context.Context, ref Ref) error
}

// Config is the wire form of a target (public fields only). Credentials ride
// separately as a secretbox-sealed blob (CredsEnc) — plaintext secrets never
// appear in job payloads, rows or logs.
type Config struct {
	Kind string `json:"kind"`
	// LocalDir is the archive root for local sinks (empty = agent default).
	LocalDir string `json:"local_dir,omitempty"`
	// S3 is the object-storage configuration.
	S3 *S3Config `json:"s3,omitempty"`
	// Remote is the ssh/rsync-style target configuration.
	Remote *RemoteConfig `json:"remote,omitempty"`
	// CredsEnc is the sealed credentials blob (base64 AES-GCM of the
	// JSON-encoded Credentials struct). Empty for local sinks.
	CredsEnc string `json:"creds_enc,omitempty"`
}

// S3Config is one S3-compatible object storage target.
type S3Config struct {
	Endpoint  string `json:"endpoint"` // https://s3.example.test or http://host:9000
	Region    string `json:"region"`
	Bucket    string `json:"bucket"`
	Prefix    string `json:"prefix,omitempty"`
	PathStyle bool   `json:"path_style,omitempty"` // path-style addressing (MinIO etc.)
}

// RemoteConfig is one ssh/rsync-style target. Config only: the agent
// assembles fixed-argument transfer commands from these validated fields —
// the API can never express a shell command.
type RemoteConfig struct {
	Host    string `json:"host"`
	Port    int    `json:"port,omitempty"` // default 22
	User    string `json:"user"`
	Path    string `json:"path"` // absolute base dir on the remote host
	// Transport is reserved for future pluggable transports ("rsync").
	Transport string `json:"transport,omitempty"`
}

// Credentials is the decrypted shape of the sealed blob.
type Credentials struct {
	// S3: static access keys.
	AccessKeyID string `json:"access_key_id,omitempty"`
	SecretKey   string `json:"secret_key,omitempty"`
	// Remote: password or key material (transport uses env/stdin, never argv).
	Password string `json:"password,omitempty"`
	PrivateKey string `json:"private_key,omitempty"`
}

// ErrNotFound is returned by Get/Stat/Delete when the artifact is absent.
var ErrNotFound = errors.New("artifact not found in sink")

// ErrInvalid is returned for invalid sink configuration.
var ErrInvalid = errors.New("invalid sink configuration")

// Open builds a Sink from a wire config. Sealed credentials are decrypted
// here (the caller holds the panel key on both control plane and agent).
func Open(cfg Config) (Sink, error) {
	// Vocabulary normalization: the DB CHECK + UI say "s3"; the driver kind
	// is "object" (master doc: "Object Storage"). "remote" is verbatim.
	if cfg.Kind == "s3" {
		cfg.Kind = KindObject
	}
	switch cfg.Kind {
	case KindLocal:
		return &LocalSink{Root: cfg.LocalDir}, nil
	case KindObject:
		if cfg.S3 == nil {
			return nil, fmt.Errorf("%w: object sink requires s3 config", ErrInvalid)
		}
		var creds Credentials
		if cfg.CredsEnc != "" {
			if err := DecodeCreds(cfg.CredsEnc, &creds); err != nil {
				return nil, err
			}
		}
		return &S3Sink{
			Client: &S3Client{
				Endpoint: cfg.S3.Endpoint, Region: cfg.S3.Region,
				Bucket: cfg.S3.Bucket, Prefix: cfg.S3.Prefix,
				AccessKeyID: creds.AccessKeyID, SecretKey: creds.SecretKey,
				PathStyle: cfg.S3.PathStyle,
			},
			Prefix: cfg.S3.Prefix,
		}, nil
	case KindRemote:
		if cfg.Remote == nil {
			return nil, fmt.Errorf("%w: remote sink requires remote config", ErrInvalid)
		}
		var creds Credentials
		if cfg.CredsEnc != "" {
			if err := DecodeCreds(cfg.CredsEnc, &creds); err != nil {
				return nil, err
			}
		}
		return NewRemoteSink(*cfg.Remote, creds)
	default:
		return nil, fmt.Errorf("%w: unknown sink kind %q", ErrInvalid, cfg.Kind)
	}
}

// ---------------------------------------------------------------------------
// Local — the node filesystem (existing behavior).
// ---------------------------------------------------------------------------

// LocalSink stores artifacts under a root directory on the node.
type LocalSink struct {
	// Root is the archive base. Empty = the agent default (backupsBase).
	Root string
}

// Kind implements Sink.
func (s *LocalSink) Kind() string { return KindLocal }

func (s *LocalSink) base() string {
	if s.Root == "" {
		return defaultLocalRoot
	}
	return s.Root
}

// defaultLocalRoot matches the agent's archive base (set by the agent package
// init; keeps this package free of agent layout imports).
var defaultLocalRoot = "/srv/epicpanel/backups"

// SetDefaultLocalRoot overrides the agent default (tests).
func SetDefaultLocalRoot(dir string) { defaultLocalRoot = dir }

// Put moves nothing: local artifacts are already node-local, so Put records
// the destination path and copies the source into the archive root when it
// lives elsewhere. The artifact dir must exist; the ref is its path.
func (s *LocalSink) Put(_ context.Context, localPath, name string) (Ref, error) {
	if !safeName(name) {
		return Ref{}, fmt.Errorf("%w: invalid artifact name", ErrInvalid)
	}
	dst := filepath.Join(s.base(), name)
	if localPath != "" && localPath != dst {
		if err := copyPath(localPath, dst); err != nil {
			return Ref{}, err
		}
	}
	return Ref{Kind: KindLocal, Path: dst}, nil
}

// Get copies a local artifact to destPath.
func (s *LocalSink) Get(_ context.Context, ref Ref, destPath string) error {
	p := s.resolve(ref.Path)
	if p == "" {
		return fmt.Errorf("%w: local ref path outside sink root", ErrInvalid)
	}
	if _, err := os.Stat(p); err != nil {
		return fmt.Errorf("%w: %s", ErrNotFound, ref.Path)
	}
	return copyPath(p, destPath)
}

// Stat reports a local artifact.
func (s *LocalSink) Stat(_ context.Context, ref Ref) (ObjectInfo, error) {
	p := s.resolve(ref.Path)
	if p == "" {
		return ObjectInfo{}, fmt.Errorf("%w: local ref path outside sink root", ErrInvalid)
	}
	fi, err := os.Stat(p)
	if err != nil {
		if os.IsNotExist(err) {
			return ObjectInfo{}, fmt.Errorf("%w: %s", ErrNotFound, ref.Path)
		}
		return ObjectInfo{}, err
	}
	return ObjectInfo{SizeBytes: fi.Size(), ModTime: fi.ModTime()}, nil
}

// Delete removes a local artifact. The ref must resolve inside the root.
func (s *LocalSink) Delete(_ context.Context, ref Ref) error {
	p := s.resolve(ref.Path)
	if p == "" {
		return fmt.Errorf("%w: local ref path outside sink root", ErrInvalid)
	}
	if _, err := os.Stat(p); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%w: %s", ErrNotFound, ref.Path)
		}
		return err
	}
	return os.RemoveAll(p)
}

// resolve enforces that a stored path stays inside the sink root (the prune
// job deletes whatever the control plane sends; the sink defends the base).
func (s *LocalSink) resolve(p string) string {
	if p == "" {
		return ""
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(s.base(), p)
	}
	clean := filepath.Clean(p) + string(os.PathSeparator)
	base := filepath.Clean(s.base()) + string(os.PathSeparator)
	if !strings.HasPrefix(clean, base) {
		return ""
	}
	return filepath.Clean(p)
}

// safeName bounds artifact names to plain file components.
func safeName(name string) bool {
	if name == "" || len(name) > 200 || strings.Contains(name, "..") {
		return false
	}
	for _, c := range name {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '-', c == '_', c == '.', c == '/':
		default:
			return false
		}
	}
	return true
}

func copyPath(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	fi, err := in.Stat()
	if err != nil {
		return err
	}
	if fi.IsDir() {
		return copyDir(src, dst)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o640)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := out.ReadFrom(in); err != nil {
		return err
	}
	return out.Close()
}

func copyDir(src, dst string) error {
	return filepath.Walk(src, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if fi.IsDir() {
			return os.MkdirAll(target, 0o750)
		}
		if !fi.Mode().IsRegular() {
			return nil // skip symlinks/special files when copying artifacts
		}
		return copyPath(p, target)
	})
}

// ---------------------------------------------------------------------------
// Remote — ssh/rsync-style target (config only, no arbitrary shell).
// ---------------------------------------------------------------------------

// RemoteTransport performs one transfer pair. The default transport shells
// out to rsync over ssh with FIXED argument templates built from validated
// config fields; tests inject a fake. Because the interface is narrow
// (source path, destination spec), payloads can never express a command.
type RemoteTransport interface {
	Put(ctx context.Context, srcPath, destSpec string) error
	Get(ctx context.Context, srcSpec, destPath string) error
	Delete(ctx context.Context, destSpec string) error
	Stat(ctx context.Context, destSpec string) (ObjectInfo, error)
}

// RemoteSink stores artifacts on a remote host over the transport.
type RemoteSink struct {
	Config RemoteConfig
	Creds  Credentials
	// Transport is created lazily via TransportFactory (injectable).
	Transport RemoteTransport
}

// TransportFactory builds the transport for one sink (overridden in tests).
var TransportFactory func(cfg RemoteConfig, creds Credentials) (RemoteTransport, error)

// NewRemoteSink validates the config and wires the default transport.
func NewRemoteSink(cfg RemoteConfig, creds Credentials) (*RemoteSink, error) {
	if err := ValidateRemoteConfig(cfg); err != nil {
		return nil, err
	}
	s := &RemoteSink{Config: cfg, Creds: creds}
	if TransportFactory != nil {
		t, err := TransportFactory(cfg, creds)
		if err != nil {
			return nil, err
		}
		s.Transport = t
	}
	return s, nil
}

// ValidateRemoteConfig enforces the shape that keeps the generated transfer
// commands fixed-argument: no whitespace/metacharacters in host/user, an
// absolute base path, sane port.
func ValidateRemoteConfig(cfg RemoteConfig) error {
	if cfg.Host == "" || cfg.User == "" || cfg.Path == "" {
		return fmt.Errorf("%w: remote target needs host, user and path", ErrInvalid)
	}
	if !isSafeHost(cfg.Host) || !isSafeUser(cfg.User) {
		return fmt.Errorf("%w: remote host/user contain forbidden characters", ErrInvalid)
	}
	if !strings.HasPrefix(cfg.Path, "/") || strings.Contains(cfg.Path, "..") {
		return fmt.Errorf("%w: remote path must be absolute", ErrInvalid)
	}
	if cfg.Port < 0 || cfg.Port > 65535 {
		return fmt.Errorf("%w: invalid remote port", ErrInvalid)
	}
	if t := cfg.Transport; t != "" && t != "rsync" {
		return fmt.Errorf("%w: unsupported remote transport %q", ErrInvalid, t)
	}
	return nil
}

func isSafeHost(h string) bool {
	if len(h) == 0 || len(h) > 253 {
		return false
	}
	for _, c := range h {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '.', c == '-', c == ':':
		default:
			return false
		}
	}
	return true
}

func isSafeUser(u string) bool {
	if len(u) == 0 || len(u) > 32 {
		return false
	}
	for _, c := range u {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '-', c == '_', c == '.':
		default:
			return false
		}
	}
	return true
}

// Kind implements Sink.
func (s *RemoteSink) Kind() string { return KindRemote }

func (s *RemoteSink) transport() (RemoteTransport, error) {
	if s.Transport != nil {
		return s.Transport, nil
	}
	if TransportFactory == nil {
		return nil, fmt.Errorf("%w: remote transport unavailable (no factory)", ErrInvalid)
	}
	t, err := TransportFactory(s.Config, s.Creds)
	if err != nil {
		return nil, err
	}
	s.Transport = t
	return t, nil
}

func (s *RemoteSink) spec(name string) (string, error) {
	if !safeName(name) {
		return "", fmt.Errorf("%w: invalid artifact name", ErrInvalid)
	}
	return s.Config.User + "@" + s.Config.Host + ":" + strings.TrimSuffix(s.Config.Path, "/") + "/" + name, nil
}

// Put transfers a local artifact to the remote target.
func (s *RemoteSink) Put(ctx context.Context, localPath, name string) (Ref, error) {
	t, err := s.transport()
	if err != nil {
		return Ref{}, err
	}
	spec, err := s.spec(name)
	if err != nil {
		return Ref{}, err
	}
	if err := t.Put(ctx, localPath, spec); err != nil {
		return Ref{}, err
	}
	return Ref{Kind: KindRemote, Path: name}, nil
}

// Get pulls a remote artifact to a local file.
func (s *RemoteSink) Get(ctx context.Context, ref Ref, destPath string) error {
	t, err := s.transport()
	if err != nil {
		return err
	}
	spec, err := s.spec(ref.Path)
	if err != nil {
		return err
	}
	return t.Get(ctx, spec, destPath)
}

// Stat reports a remote artifact via the transport.
func (s *RemoteSink) Stat(ctx context.Context, ref Ref) (ObjectInfo, error) {
	t, err := s.transport()
	if err != nil {
		return ObjectInfo{}, err
	}
	spec, err := s.spec(ref.Path)
	if err != nil {
		return ObjectInfo{}, err
	}
	return t.Stat(ctx, spec)
}

// Delete removes a remote artifact via the transport.
func (s *RemoteSink) Delete(ctx context.Context, ref Ref) error {
	t, err := s.transport()
	if err != nil {
		return err
	}
	spec, err := s.spec(ref.Path)
	if err != nil {
		return err
	}
	return t.Delete(ctx, spec)
}

// ListNames lists artifact names under the sink root (best effort; used by
// the verify sweeper only where the sink supports listing — object storage).
func ListNames(ctx context.Context, s Sink, prefix string) ([]string, error) {
	switch v := s.(type) {
	case *S3Sink:
		return v.Client.listKeys(ctx, prefix)
	case *LocalSink:
		base := v.base()
		entries, err := os.ReadDir(base)
		if err != nil {
			if os.IsNotExist(err) {
				return nil, nil
			}
			return nil, err
		}
		var out []string
		for _, e := range entries {
			name := e.Name()
			if prefix != "" && !strings.HasPrefix(name, prefix) {
				continue
			}
			out = append(out, name)
		}
		sort.Strings(out)
		return out, nil
	default:
		return nil, fmt.Errorf("%w: listing unsupported for sink kind %s", ErrInvalid, s.Kind())
	}
}
