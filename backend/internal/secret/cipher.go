// Package secret encrypts server-only credentials before database persistence.
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"io"
)

type Cipher struct {
	aead cipher.AEAD
}

func New(key []byte) (*Cipher, error) {
	if len(key) != 32 {
		return nil, errors.New("token encryption key must be 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Cipher{aead: aead}, nil
}

// Encrypt prefixes a randomly generated nonce. associatedData should bind ciphertext to its owner.
func (c *Cipher) Encrypt(plaintext, associatedData []byte) ([]byte, error) {
	if c == nil || c.aead == nil {
		return nil, errors.New("cipher is not configured")
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return c.aead.Seal(nonce, nonce, plaintext, associatedData), nil
}

func (c *Cipher) Decrypt(ciphertext, associatedData []byte) ([]byte, error) {
	if c == nil || c.aead == nil {
		return nil, errors.New("cipher is not configured")
	}
	nonceSize := c.aead.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, errors.New("ciphertext is malformed")
	}
	return c.aead.Open(nil, ciphertext[:nonceSize], ciphertext[nonceSize:], associatedData)
}
