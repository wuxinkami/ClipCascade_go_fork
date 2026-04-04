//go:build linux

package app

import (
	"regexp"
	"strings"
	"testing"
)

func TestPortalTokenUsesDBusObjectPathSafeCharacters(t *testing.T) {
	token := portalToken("clipcascade")
	if token != "clipcascade" {
		t.Fatalf("token = %q, want %q", token, "clipcascade")
	}
	if strings.Contains(token, "-") {
		t.Fatalf("token = %q, should not contain '-'", token)
	}
	matched, err := regexp.MatchString(`^[A-Za-z0-9_]+$`, token)
	if err != nil {
		t.Fatalf("MatchString() error = %v", err)
	}
	if !matched {
		t.Fatalf("token = %q, want only letters/digits/underscore", token)
	}
}

func TestPortalTokenIsStableForSamePrefix(t *testing.T) {
	first := portalToken("clipcascade_session")
	second := portalToken("clipcascade_session")
	if first != second {
		t.Fatalf("portalToken() changed between calls: %q vs %q", first, second)
	}
}
