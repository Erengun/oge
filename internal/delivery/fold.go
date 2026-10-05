package delivery

import (
	"strings"

	"golang.org/x/text/unicode/norm"
)

// fold is a path as a case-insensitive, normalisation-insensitive
// filesystem (macOS, Windows) sees it: two paths with the same fold may be
// one file.
func fold(s string) string { return strings.ToLower(norm.NFC.String(s)) }
