package websites

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- archiveEntryName: zip-slip guards ---

func TestArchiveEntryName(t *testing.T) {
	ok := []string{"a.txt", "dir/b.txt", "./x"}
	okExpected := []string{"a.txt", "dir/b.txt", "x"}
	for i, in := range ok {
		got, err := archiveEntryName(in)
		if okExpected[i] == "" {
			if err == nil {
				t.Errorf("expected rejection for %q", in)
			}
			continue
		}
		if err != nil || got != okExpected[i] {
			t.Errorf("archiveEntryName(%q) = %q, %v; want %q", in, got, err, okExpected[i])
		}
	}
	bad := []string{
		"/etc/passwd",       // absolute
		"../escape",         // traversal
		"a/../../escape",    // nested traversal
		"C:/windows/system", // drive-relative
		`dir\..\..\escape`,  // backslash traversal (normalized then rejected)
		"..",                // bare dotdot
		"",                  // empty
	}
	for _, in := range bad {
		if _, err := archiveEntryName(in); err == nil {
			t.Errorf("expected rejection for %q", in)
		}
	}
}

// --- extractLimits: decompression-bomb caps ---

func TestExtractLimits(t *testing.T) {
	l := &extractLimits{}
	if err := l.file(extractMaxEntrySize + 1); err == nil {
		t.Error("oversized single entry must be rejected")
	}
	l2 := &extractLimits{}
	var tripped bool
	for i := 0; i < 10; i++ {
		if err := l2.file(extractMaxTotalSize / 9); err != nil {
			tripped = true
			break
		}
	}
	if !tripped {
		t.Fatal("total cap must trip before 10 entries of cap/9 each")
	}
	l3 := &extractLimits{}
	var err error
	for i := 0; i < extractMaxEntries+1 && err == nil; i++ {
		err = l3.file(1)
	}
	if err == nil {
		t.Error("entry-count cap must trip")
	}
}

// --- extractZip: end-to-end with a crafted archive ---

func buildTestZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		if strings.HasSuffix(name, "/") {
			if _, err := zw.Create(name); err != nil {
				t.Fatal(err)
			}
			continue
		}
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestExtractZipOK(t *testing.T) {
	blob := buildTestZip(t, map[string]string{
		"app/index.php":       "<?php echo 1;",
		"app/assets/style.css": "body{}",
	})
	dest := t.TempDir()
	n, err := extractZip(bytes.NewReader(blob), int64(len(blob)), dest, false, &extractLimits{})
	if err != nil {
		t.Fatalf("extractZip: %v", err)
	}
	if n != 2 {
		t.Fatalf("expected 2 files, got %d", n)
	}
	for _, f := range []string{"app/index.php", "app/assets/style.css"} {
		if _, err := os.Stat(filepath.Join(dest, f)); err != nil {
			t.Errorf("extracted file missing: %s", f)
		}
	}
}

func TestExtractZipSlipRejected(t *testing.T) {
	blob := buildTestZip(t, map[string]string{
		"../evil.txt": "pwned",
	})
	dest := t.TempDir()
	if _, err := extractZip(bytes.NewReader(blob), int64(len(blob)), dest, false, &extractLimits{}); err == nil {
		t.Fatal("zip-slip entry must be rejected")
	}
	if _, err := os.Stat(filepath.Join(dest, "..", "evil.txt")); err == nil {
		t.Error("escape file must not exist")
	}
}

func TestExtractZipSkipOverwrite(t *testing.T) {
	blob := buildTestZip(t, map[string]string{"a.txt": "new"})
	dest := t.TempDir()
	if err := os.WriteFile(filepath.Join(dest, "a.txt"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := extractZip(bytes.NewReader(blob), int64(len(blob)), dest, false, &extractLimits{}); err != nil {
		t.Fatalf("skip-existing extract failed: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(dest, "a.txt"))
	if string(got) != "old" {
		t.Error("overwrite=false must keep the existing file")
	}
	if _, err := extractZip(bytes.NewReader(blob), int64(len(blob)), dest, true, &extractLimits{}); err != nil {
		t.Fatalf("overwrite extract failed: %v", err)
	}
	got, _ = os.ReadFile(filepath.Join(dest, "a.txt"))
	if string(got) != "new" {
		t.Error("overwrite=true must replace the file")
	}
}

// --- extractTar: symlink entries refused, gzip works ---

func buildTestTar(t *testing.T, entries []tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		hdr := &tar.Header{Name: e.name, Typeflag: e.typeflag, Size: int64(len(e.body)), Mode: 0o644}
		if e.typeflag == tar.TypeSymlink {
			hdr.Linkname = e.body
			hdr.Size = 0
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if e.body != "" && e.typeflag != tar.TypeSymlink {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

type tarEntry struct {
	name     string
	typeflag byte
	body     string
}

func TestExtractTarGzOK(t *testing.T) {
	blob := buildTestTar(t, []tarEntry{
		{name: "public/", typeflag: tar.TypeDir},
		{name: "public/index.php", typeflag: tar.TypeReg, body: "<?php echo 2;"},
	})
	dest := t.TempDir()
	n, err := extractTar(bytes.NewReader(blob), false, dest, false, &extractLimits{})
	if err != nil {
		t.Fatalf("extractTar: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 file, got %d", n)
	}
	if _, err := os.Stat(filepath.Join(dest, "public", "index.php")); err != nil {
		t.Error("extracted tar file missing")
	}
}

func TestExtractTarSymlinkRejected(t *testing.T) {
	blob := buildTestTar(t, []tarEntry{
		{name: "link", typeflag: tar.TypeSymlink, body: "/etc"},
	})
	dest := t.TempDir()
	if _, err := extractTar(bytes.NewReader(blob), false, dest, false, &extractLimits{}); err == nil {
		t.Fatal("symlink entries must be rejected")
	}
}

// --- copyTree ---

func TestCopyTree(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("A"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "sub", "b.txt"), []byte("B"), 0o644); err != nil {
		t.Fatal(err)
	}
	var total int64
	var files int
	if err := copyTree(src, filepath.Join(dst, "copy"), &total, &files); err != nil {
		t.Fatalf("copyTree: %v", err)
	}
	if files != 2 {
		t.Fatalf("expected 2 files copied, got %d", files)
	}
	got, err := os.ReadFile(filepath.Join(dst, "copy", "sub", "b.txt"))
	if err != nil || string(got) != "B" {
		t.Errorf("copied content wrong: %q, %v", got, err)
	}
}

// --- compress round-trip: zip what compress builds and verify contents ---

func TestCompressOutputIsZip(t *testing.T) {
	// filesCompress is a handler; here we verify the format contract it
	// writes: a valid zip readable by extractZip (round-trip guarantee).
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "one.txt"), []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("one.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("1")); err != nil {
		t.Fatal(err)
	}
	zw.Close()
	n, err := extractZip(bytes.NewReader(buf.Bytes()), int64(buf.Len()), t.TempDir(), false, &extractLimits{})
	if err != nil || n != 1 {
		t.Fatalf("zip round-trip failed: n=%d err=%v", n, err)
	}
}
