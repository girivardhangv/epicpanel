package software

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ulikunitz/xz"
)

func extractZip(src, prefix, dst string) error {
	r, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer r.Close()
	for _, f := range r.File {
		rel := stripPrefix(f.Name, prefix)
		if rel == "" {
			continue
		}
		target, err := safeJoin(dst, rel)
		if err != nil {
			return err
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		mode := f.Mode()
		if mode == 0 {
			mode = 0o644
		}
		out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
		if err != nil {
			rc.Close()
			return err
		}
		_, err = io.Copy(out, rc)
		rc.Close()
		if err != nil {
			out.Close()
			return err
		}
		out.Close()
	}
	return nil
}

// openTarReader opens a tar stream from a gzip or plain file, or an xz stream
// when the format requests it.
func openTarReader(path, format string) (io.ReadCloser, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	switch format {
	case "tar.gz", "tgz":
		gz, err := gzip.NewReader(f)
		if err != nil {
			f.Close()
			return nil, err
		}
		return &wrapReadCloser{r: gz, closers: []io.Closer{gz, f}}, nil
	case "tar.xz":
		xr, err := xz.NewReader(f)
		if err != nil {
			f.Close()
			return nil, err
		}
		return &wrapReadCloser{r: xr, closers: []io.Closer{f}}, nil
	default:
		return f, nil
	}
}

type wrapReadCloser struct {
	r       io.Reader
	closers []io.Closer
}

func (w *wrapReadCloser) Read(p []byte) (int, error) { return w.r.Read(p) }
func (w *wrapReadCloser) Close() error {
	var first error
	for i := len(w.closers) - 1; i >= 0; i-- {
		if err := w.closers[i].Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func extractTarGz(src, prefix, dst string) error { return extractTarFrom(src, "tar.gz", prefix, dst) }
func extractTarXz(src, prefix, dst string) error { return extractTarFrom(src, "tar.xz", prefix, dst) }
func extractTar(src, prefix, dst string) error   { return extractTarFrom(src, "tar", prefix, dst) }

func extractTarFrom(src, format, prefix, dst string) error {
	rc, err := openTarReader(src, format)
	if err != nil {
		return err
	}
	defer rc.Close()

	tr := tar.NewReader(rc)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		rel := stripPrefix(hdr.Name, prefix)
		if rel == "" {
			continue
		}
		target, err := safeJoin(dst, rel)
		if err != nil {
			return err
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			mode := os.FileMode(hdr.Mode) & os.ModePerm
			if mode == 0 {
				mode = 0o644
			}
			out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return err
			}
			out.Close()
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			_ = os.Remove(target)
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				// symlinks may fail on unprivileged Windows; not fatal
				if !strings.Contains(strings.ToLower(err.Error()), "a required privilege") {
					return err
				}
			}
		default:
			// devices, fifos, hardlinks: skip silently
		}
	}
	return nil
}
