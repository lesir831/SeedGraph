package auth

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lesir831/SeedGraph/internal/cryptox"
)

const sessionVersion = 1

type Session struct {
	Version   int       `json:"version"`
	Subject   string    `json:"subject"`
	CSRFToken string    `json:"csrf_token"`
	ExpiresAt time.Time `json:"expires_at"`
}

type SessionManager struct {
	cipher *cryptox.Cipher
	now    func() time.Time
	ttl    time.Duration
}

func NewSessionManager(cipher *cryptox.Cipher, ttl time.Duration) *SessionManager {
	return &SessionManager{cipher: cipher, now: time.Now, ttl: ttl}
}

func (m *SessionManager) Create(subject string) (string, Session, error) {
	csrfBytes := make([]byte, 32)
	if _, err := rand.Read(csrfBytes); err != nil {
		return "", Session{}, fmt.Errorf("generate CSRF token: %w", err)
	}
	session := Session{
		Version:   sessionVersion,
		Subject:   subject,
		CSRFToken: base64.RawURLEncoding.EncodeToString(csrfBytes),
		ExpiresAt: m.now().Add(m.ttl).UTC(),
	}
	payload, err := json.Marshal(session)
	if err != nil {
		return "", Session{}, fmt.Errorf("encode session: %w", err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	return encoded + "." + m.cipher.Sign([]byte(encoded)), session, nil
}

func (m *SessionManager) Parse(cookie string) (Session, error) {
	encoded, signature, ok := strings.Cut(cookie, ".")
	if !ok || !m.cipher.Verify([]byte(encoded), signature) {
		return Session{}, errors.New("invalid session signature")
	}
	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return Session{}, errors.New("invalid session encoding")
	}
	var session Session
	if err := json.Unmarshal(payload, &session); err != nil {
		return Session{}, errors.New("invalid session payload")
	}
	if session.Version != sessionVersion || session.Subject == "" || session.CSRFToken == "" {
		return Session{}, errors.New("invalid session fields")
	}
	if !session.ExpiresAt.After(m.now()) {
		return Session{}, errors.New("session expired")
	}
	return session, nil
}
