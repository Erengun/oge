package oracle

import "testing"

func TestMatch(t *testing.T) {
	for _, c := range []struct {
		pat, name string
		want      bool
	}{
		{"**/*_test.go", "add_test.go", true},
		{"**/*_test.go", "internal/auth/login_test.go", true},
		{"**/*_test.go", "internal/auth/login.go", false},
		{"*_test.go", "internal/a_test.go", false},
		{"testdata/**", "testdata/x/y.json", true},
		{"testdata/**", "src/testdata/y.json", false},
		{"internal/**/*.go", "internal/a.go", true},
		{"[", "[", false},
	} {
		if got := Match(c.pat, c.name); got != c.want {
			t.Errorf("Match(%q, %q) = %v, want %v", c.pat, c.name, got, c.want)
		}
	}
}
