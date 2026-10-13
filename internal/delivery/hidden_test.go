package delivery

import "testing"

// The invisible characters #109 names are hidden; their neighbours and
// ordinary text are not.
func TestHiddenCoversSoftHyphenJoinersAndTags(t *testing.T) {
	for _, r := range []rune{0x00ad, 0x2060, 0x2062, 0x2064, 0xe0000, 0xe0041, 0xe007f, 0x200b, 0xfeff} {
		if in := "a" + string(r) + "b"; Shown(in) == in {
			t.Errorf("U+%04X is shown as is", r)
		}
	}
	for _, s := range []string{"Öge", "İstanbul", "日本", "naïve café", "a" + string(rune(0x2065)) + "b", "a" + string(rune(0xe0080)) + "b", "a\u00acb"} {
		if Shown(s) != s {
			t.Errorf("%q changed to %q", s, Shown(s))
		}
	}
}
