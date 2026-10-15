package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestHelpSeparatesAdvancedControls(t *testing.T) {
	for _, prefix := range []string{"help", "--help"} {
		for _, advanced := range []bool{false, true} {
			var out bytes.Buffer
			args := []string{prefix}
			if advanced {
				args = append(args, "advanced")
			}
			if code := Main(Env{Stdout: &out}, args); code != ExitOK {
				t.Fatalf("%v: exit %d", args, code)
			}
			got := out.String()
			if !strings.Contains(got, "oge diff") {
				t.Errorf("%v lacks review command", args)
			}
			if strings.Contains(got, "--agent <stage>") != advanced {
				t.Errorf("%v: stage controls exposed at wrong level", args)
			}
			if strings.Contains(got, "--blind") {
				t.Errorf("%v advertises unimplemented mode", args)
			}
		}
	}
}
