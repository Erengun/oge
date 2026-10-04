package pipeline

import (
	"strings"
	"testing"
)

func TestCredentialValuesAreRejectedButNeverEchoed(t *testing.T) {
	secrets := []string{
		"sk-ant-api03-AAAAAAAAAAAAAAAAAAAAAAAA",
		"ghp_abcdefghijklmnopqrstuvwxyz0123",
		"AKIAABCDEFGHIJKLMNOP",
	}
	for _, s := range secrets {
		cfg := "schema = 1\n[setup]\nrun = \"curl -H 'x: " + s + "' example.com\"\n"
		_, probs := Load([]byte(cfg))
		if len(probs) == 0 {
			t.Errorf("%s…: no problem reported", s[:4])
		}
		for _, p := range probs {
			if strings.Contains(p.String(), s) || strings.Contains(p.String(), s[4:]) {
				t.Errorf("problem echoes the credential: %q", p)
			}
		}
	}
}

func TestPassEnvTakesNamesOnly(t *testing.T) {
	_, probs := Load([]byte("schema = 1\n[project]\npass_env = [\"GOFLAGS\", \"FOO=bar\"]\n"))
	if len(probs) != 1 || probs[0].Key != "project.pass_env[1]" {
		t.Fatalf("problems = %v", probs)
	}
	if strings.Contains(probs[0].String(), "bar") {
		t.Fatalf("problem echoes the value: %q", probs[0])
	}
}

func TestEveryMandatoryGateIsAHardError(t *testing.T) {
	for key := range mandatoryGates {
		_, probs := Load([]byte("schema = 1\n[pipelines.default.gates]\n" + key + " = true\n"))
		if len(probs) != 1 || !strings.Contains(probs[0].Msg, "mandatory") {
			t.Errorf("%s: problems = %v", key, probs)
		}
	}
}

func TestModesCompileToDistinctGraphs(t *testing.T) {
	seen := map[string]Mode{}
	for _, m := range []Mode{Fast, Standard, Blind} {
		for _, result := range []bool{false, true} {
			h := Compile(m, result).Hash(DefaultLimits)
			if prev, dup := seen[h]; dup {
				t.Fatalf("%s and %s compile to the same hash", prev, m)
			}
			seen[h] = m
			if h != Compile(m, result).Hash(DefaultLimits) {
				t.Fatalf("%s: hash is not deterministic", m)
			}
		}
	}
	l := DefaultLimits
	l.Retries++
	if Compile(Standard, false).Hash(l) == Compile(Standard, false).Hash(DefaultLimits) {
		t.Fatal("limits don't change the hash")
	}
}

func TestOracleGrowthGateOnlyWithAVerifier(t *testing.T) {
	has := func(g Graph) bool {
		for _, n := range g.Nodes {
			if n.ID == "gate.oracle_growth" {
				return true
			}
		}
		return false
	}
	if has(Compile(Fast, false)) || !has(Compile(Standard, false)) || !has(Compile(Blind, false)) {
		t.Fatal("Oracle-growth Gate must exist exactly when a verifier Stage does")
	}
}

func TestEveryEdgeJoinsExistingNodes(t *testing.T) {
	for _, m := range []Mode{Fast, Standard, Blind} {
		g := Compile(m, true)
		ids := map[string]bool{}
		for _, n := range g.Nodes {
			ids[n.ID] = true
		}
		for _, e := range g.Edges {
			if !ids[e.From] || !ids[e.To] {
				t.Errorf("%s: edge %v joins a missing node", m, e)
			}
		}
	}
}
