package crypto

import "testing"

func TestSHA3_512HexMatchesKnownVector(t *testing.T) {
	got := SHA3_512Hex("abc")
	want := "b751850b1a57168a5693cd924b6b096e08f621827444f70d884f5d0240d2712e10e116e9192af3c91a7ec57647e3934057340b4cf408d5a56592f8274eec53f0"
	if got != want {
		t.Fatalf("SHA3_512Hex(%q) = %q, want %q", "abc", got, want)
	}
}
