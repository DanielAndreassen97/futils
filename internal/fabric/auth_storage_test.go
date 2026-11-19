// internal/fabric/auth_storage_test.go
package fabric

import (
	"net/url"
	"testing"

	"github.com/zalando/go-keyring"
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

	tr, err := tokenGrant("", storageScope, "fake-refresh-token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tr.AccessToken != "stub-token" {
		t.Fatalf("got token %q, want stub-token", tr.AccessToken)
	}
	if captured.Get("scope") != storageScope {
		t.Errorf("scope = %q, want %q", captured.Get("scope"), storageScope)
	}
	if captured.Get("grant_type") != "refresh_token" {
		t.Errorf("grant_type = %q, want refresh_token", captured.Get("grant_type"))
	}
}

// Schema compare reads OneLake with this token. Left on /common/, a bound
// customer would get a Fabric token for its tenant and a OneLake token for
// another, and only the OneLake features would fail.
func TestGetStorageTokenUsesCustomerTenant(t *testing.T) {
	keyring.MockInit()
	if err := keyring.Set(keyringService, keyFor("Contoso", "refresh_token"), "rt-1"); err != nil {
		t.Fatal(err)
	}
	fabricToken := "fabric-token-stays"
	_ = keyring.Set(keyringService, keyFor("Contoso", "access_token"), fabricToken)
	resolveTo(t, Tenant{ID: tenantContoso}, nil)
	endpoints := stubTokenPost(t, func(_ string, data url.Values) (tokenResponse, error) {
		if data.Get("scope") != storageScope {
			t.Errorf("scope = %q, want the storage scope", data.Get("scope"))
		}
		return tokenResponse{AccessToken: "storage-token", RefreshToken: "rt-2"}, nil
	})

	tok, err := GetStorageToken("Contoso")
	if err != nil || tok != "storage-token" {
		t.Fatalf("GetStorageToken = %q, %v", tok, err)
	}
	want := "https://login.microsoftonline.com/" + tenantContoso + "/oauth2/v2.0/token"
	if len(*endpoints) != 1 || (*endpoints)[0] != want {
		t.Errorf("storage grant went to %v, want %s", *endpoints, want)
	}
	// The grant must not touch the cached Fabric token.
	if got, _ := keyring.Get(keyringService, keyFor("Contoso", "access_token")); got != fabricToken {
		t.Errorf("storage grant overwrote the cached Fabric token: %q", got)
	}
}
