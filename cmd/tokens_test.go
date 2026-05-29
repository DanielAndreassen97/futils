package cmd

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/DanielAndreassen97/futils/internal/config"
	"github.com/zalando/go-keyring"
)

// keyringService mirrors the unexported fabric.keyringService constant — the
// namespace under which tokens are stored. If the app's namespace changes,
// this test should be updated alongside it.
const testKeyringService = "futils"

// TestLogout_ReportsKeyringFailure: when the OS keyring refuses deletes,
// logout must surface an error rather than printing a false "cleared" and
// returning nil — otherwise a user on a shared machine is told a long-lived
// refresh token is gone when it may still be present.
func TestLogout_ReportsKeyringFailure(t *testing.T) {
	keyring.MockInitWithError(errors.New("keyring locked"))
	t.Cleanup(keyring.MockInit)

	path := filepath.Join(t.TempDir(), "config.json")
	if err := config.AddCustomer(path, "acme", config.Customer{}); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	if err := Logout(path); err == nil {
		t.Fatal("expected Logout to return an error when keyring deletes fail, got nil")
	}
}

// TestLogout_SucceedsWhenKeyringWorks: the happy path (nothing to delete, or
// deletes succeed) returns nil.
func TestLogout_SucceedsWhenKeyringWorks(t *testing.T) {
	keyring.MockInit()

	path := filepath.Join(t.TempDir(), "config.json")
	if err := config.AddCustomer(path, "acme", config.Customer{}); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	if err := Logout(path); err != nil {
		t.Errorf("expected nil on successful clear, got %v", err)
	}
}

// TestRemoveCustomerAndTokens_WipesTokens: removing a customer must also wipe
// its cached OAuth tokens from the keyring, not just the config entry —
// otherwise a long-lived refresh token is orphaned and unreachable by logout.
func TestRemoveCustomerAndTokens_WipesTokens(t *testing.T) {
	keyring.MockInit()

	path := filepath.Join(t.TempDir(), "config.json")
	if err := config.AddCustomer(path, "acme", config.Customer{}); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	// Seed tokens the way the app stores them: service "futils", key "acme:<kind>".
	for _, kind := range []string{"access_token", "refresh_token", "token_expiry"} {
		if err := keyring.Set(testKeyringService, "acme:"+kind, "seeded"); err != nil {
			t.Fatalf("seed token %s: %v", kind, err)
		}
	}

	if err := removeCustomerAndTokens(path, "acme"); err != nil {
		t.Fatalf("removeCustomerAndTokens: %v", err)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if _, ok := cfg.Customers["acme"]; ok {
		t.Error("customer still present in config after remove")
	}
	if _, err := keyring.Get(testKeyringService, "acme:access_token"); !errors.Is(err, keyring.ErrNotFound) {
		t.Errorf("access_token was not wiped (err=%v)", err)
	}
	if _, err := keyring.Get(testKeyringService, "acme:refresh_token"); !errors.Is(err, keyring.ErrNotFound) {
		t.Errorf("refresh_token was not wiped (err=%v)", err)
	}
}
