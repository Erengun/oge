package oracle

import (
	"strings"
	"testing"
)

const passing = `{"Action":"start","Package":"fx"}
{"Action":"run","Package":"fx","Test":"TestAdd"}
{"Action":"output","Package":"fx","Test":"TestAdd","Output":"=== RUN   TestAdd\n"}
{"Action":"pass","Package":"fx","Test":"TestAdd","Elapsed":0}
{"Action":"run","Package":"fx","Test":"TestSub"}
{"Action":"skip","Package":"fx","Test":"TestSub","Elapsed":0}
{"Action":"pass","Package":"fx","Elapsed":0.2}
`

func TestParseGoTestJSON(t *testing.T) {
	rep := ParseGoTestJSON([]byte(passing))
	if rep.Error != "" || rep.Ran != 1 || rep.Failed != 0 || rep.Skipped != 1 {
		t.Fatalf("passing: %+v", rep)
	}

	failing := strings.Replace(passing, `"pass","Package":"fx","Test":"TestAdd"`, `"fail","Package":"fx","Test":"TestAdd"`, 1)
	rep = ParseGoTestJSON([]byte(failing))
	if rep.Failed != 1 || rep.Ran != 1 || len(rep.FailedTests) != 1 || rep.FailedTests[0] != "TestAdd" {
		t.Fatalf("failing: %+v", rep)
	}

	for name, in := range map[string]string{
		"empty":    "",
		"not json": "ok  \tfx\t0.2s\n",
		"mixed":    passing + "FAIL\n",
	} {
		if rep := ParseGoTestJSON([]byte(in)); rep.Error == "" {
			t.Errorf("%s: parsed as %+v, want a report error", name, rep)
		}
	}
}
