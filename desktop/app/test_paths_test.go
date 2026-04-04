package app

import (
	"os"
	"path/filepath"
	"testing"
)

func testReplayPath(t *testing.T, elems ...string) string {
	t.Helper()

	baseDir := filepath.Join(replayTempRootDir(), "tests", sanitizeFileName(t.Name()))
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", baseDir, err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(baseDir) })

	if len(elems) == 0 {
		return baseDir
	}
	return filepath.Join(append([]string{baseDir}, elems...)...)
}
