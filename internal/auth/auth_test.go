package auth

import (
	"strings"
	"testing"
	"time"

	"github.com/lesir831/SeedGraph/internal/cryptox"
)

func TestPasswordHashAndVerify(t *testing.T) {
	hash, err := HashPassword("correct-horse-battery-staple")
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword(hash, "correct-horse-battery-staple") {
		t.Fatal("VerifyPassword() rejected the password")
	}
	if VerifyPassword(hash, "wrong-password") {
		t.Fatal("VerifyPassword() accepted the wrong password")
	}
}

func TestSessionRoundTripAndExpiry(t *testing.T) {
	cipher, _ := cryptox.New([]byte(strings.Repeat("s", 32)))
	manager := NewSessionManager(cipher, time.Hour)
	base := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	manager.now = func() time.Time { return base }

	cookie, created, err := manager.Create("admin")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := manager.Parse(cookie)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Subject != "admin" || parsed.CSRFToken != created.CSRFToken {
		t.Fatalf("unexpected parsed session: %+v", parsed)
	}

	manager.now = func() time.Time { return base.Add(2 * time.Hour) }
	if _, err := manager.Parse(cookie); err == nil {
		t.Fatal("Parse() accepted an expired session")
	}
}

func TestSessionRejectsTampering(t *testing.T) {
	cipher, _ := cryptox.New([]byte(strings.Repeat("s", 32)))
	manager := NewSessionManager(cipher, time.Hour)
	cookie, _, _ := manager.Create("admin")
	if _, err := manager.Parse(cookie + "x"); err == nil {
		t.Fatal("Parse() accepted a modified session")
	}
}
