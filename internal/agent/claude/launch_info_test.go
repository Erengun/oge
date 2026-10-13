package claude

import "testing"

func TestLaunchInfoMatchesTheRealProfile(t *testing.T) {
	for _, role := range []string{"implementer", "verifier"} {
		p, err := profileFor(role)
		if err != nil {
			t.Fatal(err)
		}
		i, err := LaunchInfoFor(role)
		if err != nil {
			t.Fatal(err)
		}
		if i.Hash != p.hash() || i.Version != ProfileVersion {
			t.Errorf("%s: shown %d/%s, real %d/%s", role, i.Version, i.Hash, ProfileVersion, p.hash())
		}
		if len(i.BuiltinPlugins) != len(builtinPlugins) || i.HomeSecrets != len(homeSecrets) {
			t.Errorf("%s: lists differ from the profile's", role)
		}
	}
	if _, err := LaunchInfoFor("nobody"); err == nil {
		t.Error("an unknown role has no profile")
	}
}
