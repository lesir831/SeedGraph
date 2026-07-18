package store

import (
	"context"
	"strings"
	"testing"
)

func TestTrackerIdentityRedactsSecrets(t *testing.T) {
	raw := "https://user:password@Tracker.Example.com:443/announce/" + strings.Repeat("a", 32) + "?passkey=secret#fragment"
	host, path, err := TrackerIdentity(raw)
	if err != nil {
		t.Fatal(err)
	}
	if host != "tracker.example.com" || strings.Contains(path, strings.Repeat("a", 32)) || strings.Contains(path, "secret") {
		t.Fatalf("unsafe tracker identity: host=%q path=%q", host, path)
	}
}

func TestTrackerIdentityRedactsArbitraryShortPathSecrets(t *testing.T) {
	host, path, err := TrackerIdentity("udp://tracker.example.com/announce/x7Kp2")
	if err != nil {
		t.Fatal(err)
	}
	if host != "tracker.example.com" || path != "/announce/*" || strings.Contains(path, "x7Kp2") {
		t.Fatalf("unsafe short tracker identity: host=%q path=%q", host, path)
	}
}

func TestCreateCustomTrackerRule(t *testing.T) {
	store := openTestStore(t)
	rule, err := store.CreateCustomTrackerRule(context.Background(), CreateTrackerRuleParams{
		HostPattern: "tracker.example.com",
		SiteName:    "example",
		DisplayName: "Example PT",
	})
	if err != nil {
		t.Fatal(err)
	}
	if rule.Source != "custom" || rule.Priority != 1000 {
		t.Fatalf("unexpected rule: %+v", rule)
	}
}
