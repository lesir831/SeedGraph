package cryptox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
)

// Cipher encrypts downloader credentials and signs session values.
type Cipher struct {
	aead    cipher.AEAD
	signKey [sha256.Size]byte
}

func New(secret []byte) (*Cipher, error) {
	if len(secret) < 32 {
		return nil, errors.New("secret must contain at least 32 bytes")
	}
	key := sha256.Sum256(append([]byte("seedgraph/encryption/v1\x00"), secret...))
	signKey := sha256.Sum256(append([]byte("seedgraph/signing/v1\x00"), secret...))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, fmt.Errorf("create AES cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create AES-GCM: %w", err)
	}
	return &Cipher{aead: aead, signKey: signKey}, nil
}

func (c *Cipher) Encrypt(plaintext []byte) (string, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}
	sealed := c.aead.Seal(nonce, nonce, plaintext, []byte("seedgraph/credentials/v1"))
	return base64.RawURLEncoding.EncodeToString(sealed), nil
}

func (c *Cipher) Decrypt(encoded string) ([]byte, error) {
	sealed, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("decode ciphertext: %w", err)
	}
	if len(sealed) < c.aead.NonceSize() {
		return nil, errors.New("ciphertext is truncated")
	}
	nonce, ciphertext := sealed[:c.aead.NonceSize()], sealed[c.aead.NonceSize():]
	plaintext, err := c.aead.Open(nil, nonce, ciphertext, []byte("seedgraph/credentials/v1"))
	if err != nil {
		return nil, errors.New("decrypt ciphertext: authentication failed")
	}
	return plaintext, nil
}

func (c *Cipher) Sign(value []byte) string {
	mac := hmac.New(sha256.New, c.signKey[:])
	_, _ = mac.Write(value)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (c *Cipher) Verify(value []byte, signature string) bool {
	provided, err := base64.RawURLEncoding.DecodeString(signature)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, c.signKey[:])
	_, _ = mac.Write(value)
	return hmac.Equal(mac.Sum(nil), provided)
}
