package cryptox

import (
	"bytes"
	"strings"
	"testing"
)

func TestCipherRoundTrip(t *testing.T) {
	cipher, err := New([]byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatal(err)
	}
	want := []byte(`{"username":"seed","password":"secret"}`)
	encoded, err := cipher.Encrypt(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := cipher.Decrypt(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("Decrypt() = %q, want %q", got, want)
	}
	if strings.Contains(encoded, "secret") {
		t.Fatal("ciphertext leaks plaintext")
	}
}

func TestCipherRejectsTampering(t *testing.T) {
	cipher, _ := New([]byte(strings.Repeat("k", 32)))
	encoded, _ := cipher.Encrypt([]byte("secret"))
	position := len(encoded) / 2
	replacement := byte('A')
	if encoded[position] == replacement {
		replacement = 'B'
	}
	tampered := encoded[:position] + string(replacement) + encoded[position+1:]
	if _, err := cipher.Decrypt(tampered); err == nil {
		t.Fatal("Decrypt() accepted a modified ciphertext")
	}
	if !cipher.Verify([]byte("payload"), cipher.Sign([]byte("payload"))) {
		t.Fatal("Verify() rejected a valid signature")
	}
	if cipher.Verify([]byte("changed"), cipher.Sign([]byte("payload"))) {
		t.Fatal("Verify() accepted a modified value")
	}
}
