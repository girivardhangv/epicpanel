package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// Regression test for v0.4.1: downloadReleaseAsset used to remove the temp
// file via defer os.Remove before installStaged could consume it, so the
// updater failed with "chmod /tmp/...: no such file or directory".
func TestInstallStagedConsumesTempFile(t *testing.T) {
	tmp, err := os.CreateTemp(t.TempDir(), "epicpanel-update-*")
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("#!/bin/sh\necho ok\n")
	if _, err := tmp.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tmp.Close(); err != nil {
		t.Fatal(err)
	}
	src := tmp.Name()

	dest := filepath.Join(t.TempDir(), "epicpanel-agentd")
	if err := installStaged(src, dest); err != nil {
		t.Fatalf("installStaged: %v", err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("temp file still exists after successful install (want removed): %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read installed binary: %v", err)
	}
	if string(got) != string(content) {
		t.Fatalf("installed content mismatch")
	}
	// Windows cannot overwrite a running exe; the temp file survives there.
	if runtime.GOOS != "windows" {
		if fi, err := os.Stat(dest); err != nil {
			t.Fatalf("stat installed binary: %v", err)
		} else if fi.Mode().Perm() != 0o755 {
			t.Fatalf("installed binary perm = %o, want 755", fi.Mode().Perm())
		}
	}
}
