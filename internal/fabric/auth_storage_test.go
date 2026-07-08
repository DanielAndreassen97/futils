// internal/fabric/auth_storage_test.go
package fabric

import (
	"net/url"
	"testing"
)

func TestGetStorageTokenNoRefreshToken(t *testing.T) {
	// A profile with no stored refresh token must fail clearly, not panic.
	_, err := GetStorageToken("nonexistent-profile-xyz")
	if err == nil {
		t.Fatal("expected an error when no refresh token is stored")
	}
}

func TestStorageTokenGrantUsesStorageScope(t *testing.T) {
	// The grant must request the Storage audience, not the Fabric one.
	var captured url.Values
	orig := tokenPost
	tokenPost = func(_ string, data url.Values) (tokenResponse, error) {
		captured = data
		return tokenResponse{AccessToken: "stub-token"}, nil
	}
	defer func() { tokenPost = orig }()

	tok, err := storageTokenGrant("fake-refresh-token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok != "stub-token" {
		t.Fatalf("got token %q, want stub-token", tok)
	}
	if captured.Get("scope") != storageScope {
		t.Errorf("scope = %q, want %q", captured.Get("scope"), storageScope)
	}
	if captured.Get("grant_type") != "refresh_token" {
		t.Errorf("grant_type = %q, want refresh_token", captured.Get("grant_type"))
	}
}
