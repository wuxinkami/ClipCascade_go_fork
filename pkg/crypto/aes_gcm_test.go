package crypto

import (
	"bytes"
	"strings"
	"testing"
)

func TestEncryptWithAAD(t *testing.T) {
	key := bytes.Repeat([]byte("k"), 32)
	plaintext := []byte("hello with aad")
	aad := []byte("transfer-1|0")

	encrypted, err := EncryptWithAAD(key, plaintext, aad)
	if err != nil {
		t.Fatalf("EncryptWithAAD() error = %v", err)
	}
	if len(encrypted.Nonce) == 0 {
		t.Fatal("Nonce should not be empty")
	}
	if len(encrypted.Ciphertext) == 0 {
		t.Fatal("Ciphertext should not be empty")
	}
	if len(encrypted.Tag) == 0 {
		t.Fatal("Tag should not be empty")
	}
}

func TestDecryptWithAAD(t *testing.T) {
	key := bytes.Repeat([]byte("k"), 32)
	plaintext := []byte("hello with aad")
	aad := []byte("transfer-1|0")

	encrypted, err := EncryptWithAAD(key, plaintext, aad)
	if err != nil {
		t.Fatalf("EncryptWithAAD() error = %v", err)
	}

	decrypted, err := DecryptWithAAD(key, encrypted, aad)
	if err != nil {
		t.Fatalf("DecryptWithAAD() error = %v", err)
	}
	if !bytes.Equal(decrypted, plaintext) {
		t.Fatalf("decrypted = %q, want %q", string(decrypted), string(plaintext))
	}

	_, err = DecryptWithAAD(key, encrypted, []byte("transfer-1|1"))
	if err == nil {
		t.Fatal("DecryptWithAAD() error = nil, want failure")
	}
	if !strings.Contains(err.Error(), "decryption/verification failed") {
		t.Fatalf("error = %v, want verification failure", err)
	}
}
