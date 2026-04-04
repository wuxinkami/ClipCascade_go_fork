package config

import (
	"path/filepath"
	"testing"
)

func TestLogPathsLiveUnderConfigDir(t *testing.T) {
	wantDir := filepath.Join(ConfigDir(), "logs")
	if got := LogDir(); got != wantDir {
		t.Fatalf("LogDir() = %q, want %q", got, wantDir)
	}

	wantFile := filepath.Join(wantDir, "desktop.log")
	if got := LogFilePath(); got != wantFile {
		t.Fatalf("LogFilePath() = %q, want %q", got, wantFile)
	}
}
