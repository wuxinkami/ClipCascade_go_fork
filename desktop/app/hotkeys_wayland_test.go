//go:build linux

package app

import (
	"regexp"
	"strings"
	"testing"
)

func TestPortalTokenUsesDBusObjectPathSafeCharacters(t *testing.T) {
	token := portalToken("clipcascade")
	if !strings.HasPrefix(token, "clipcascade_") {
		t.Fatalf("token = %q, want clipcascade_ prefix", token)
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
