package githubauth

import (
	"context"
	"testing"
)

func TestStaticTokenAuthenticator_IgnoresInstallationID(t *testing.T) {
	a := StaticTokenAuthenticator{Token: "ghp_fixed"}

	for _, id := range []int64{0, 1, 12345} {
		tok, err := a.InstallationToken(context.Background(), id)
		if err != nil {
			t.Fatalf("InstallationToken(%d): unexpected error: %v", id, err)
		}
		if tok != "ghp_fixed" {
			t.Errorf("InstallationToken(%d) = %q, want the fixed token regardless of installation", id, tok)
		}
	}
}
