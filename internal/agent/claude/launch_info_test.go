package claude

import (
	"slices"
	"testing"

	"github.com/erengun/oge/internal/agent"
)

func TestLaunchInfoMatchesTheRealProfile(t *testing.T) {
	for _, role := range []string{"implementer", "verifier"} {
		p, err := profileFor(role)
		if err != nil {
			t.Fatal(err)
		}
		i, err := LaunchInfoFor(role, "")
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
	if _, err := LaunchInfoFor("nobody", ""); err == nil {
		t.Error("an unknown role has no profile")
	}
}

func TestEffortReachesArgsAndHash(t *testing.T) {
	spec := agent.LaunchSpec{Role: "implementer", Workspace: t.TempDir()}
	base, _ := profileFor("implementer")
	args, err := base.args(spec, "id", nil)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(args, "--effort") {
		t.Errorf("no effort set, but args = %v", args)
	}
	seen := map[string]bool{base.hash(): true}
	for _, e := range Efforts {
		p := base
		p.effort = e
		args, err := p.args(spec, "id", nil)
		if err != nil {
			t.Fatal(err)
		}
		i := slices.Index(args, "--effort")
		if i < 0 || args[i+1] != e {
			t.Errorf("effort %s: args = %v", e, args)
		}
		if seen[p.hash()] {
			t.Errorf("effort %s does not change the Launch profile hash", e)
		}
		seen[p.hash()] = true
		if info, _ := LaunchInfoFor("implementer", e); info.Hash != p.hash() || info.Effort != e {
			t.Errorf("effort %s: shown %+v, real %s", e, info, p.hash())
		}
	}
	bad := base
	bad.effort = "turbo"
	if _, err := bad.args(spec, "id", nil); err == nil {
		t.Error("an unknown effort was passed to claude")
	}
}
