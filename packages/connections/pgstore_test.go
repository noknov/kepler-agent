package connections

import "testing"

func TestScopesForStorageUsesEmptyArrayForAbsentScopes(t *testing.T) {
	scopes := scopesForStorage(nil)
	if scopes == nil || len(scopes) != 0 {
		t.Fatalf("scopes = %#v, want non-nil empty slice", scopes)
	}
}
