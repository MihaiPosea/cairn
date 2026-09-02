package scan

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A typo'd path must fail loudly. Reporting "0 files, 0 imports, 0%
// unresolved" with exit 0 is indistinguishable from a clean scan.
func TestMissingRootIsAnError(t *testing.T) {
	if _, err := Run(filepath.Join(t.TempDir(), "does-not-exist")); err == nil {
		t.Fatal("BUG: scanning a nonexistent directory must be an error")
	}
}

func TestFileAsRootIsAnError(t *testing.T) {
	f := filepath.Join(t.TempDir(), "a.ts")
	os.WriteFile(f, []byte("export const x = 1;"), 0o644)
	_, err := Run(f)
	if err == nil {
		t.Fatal("BUG: scanning a file must be an error")
	}
	if !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("error should say it is a file, got: %v", err)
	}
}

// An empty but valid directory is not an error — a repo can have no JS.
func TestEmptyDirectoryScansCleanly(t *testing.T) {
	res, err := Run(t.TempDir())
	if err != nil {
		t.Fatalf("an empty directory is valid, got: %v", err)
	}
	if res.FilesScanned != 0 {
		t.Errorf("expected 0 files, got %d", res.FilesScanned)
	}
}
