package secret

import "testing"

func TestCipherBindsTokenToOwner(t *testing.T) {
	cipher, err := New(make([]byte, 32))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	encrypted, err := cipher.Encrypt([]byte("refresh-token"), []byte("user-a"))
	if err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}
	got, err := cipher.Decrypt(encrypted, []byte("user-a"))
	if err != nil || string(got) != "refresh-token" {
		t.Fatalf("Decrypt() = %q, %v", got, err)
	}
	if _, err := cipher.Decrypt(encrypted, []byte("user-b")); err == nil {
		t.Fatal("Decrypt() accepted a different owner")
	}
}
