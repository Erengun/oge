package delivery

import (
	"bytes"
	"fmt"
	"strings"
)

// Diff is the Candidate's change since the Snapshot as a unified patch,
// byte for byte: text files as text, whatever their attributes in the
// Snapshot, and binary files as git's one-line stanza. Viewing it is not a
// Delivery and records nothing (ADR-0014).
func Diff(r *Run) ([]byte, error) {
	if r.Candidate == "" {
		return nil, refuse("Run %s has no Candidate yet", r.ID)
	}
	repo := r.repo()
	changes, err := repo.Changes(r.Snapshot, r.Candidate)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	for _, c := range changes {
		if gitlink(c) {
			// Shown, and marked: apply and branch refuse it.
			fmt.Fprintf(&out, "# Öge: %s is a submodule (gitlink); oge apply and oge branch refuse it\n", c.Path)
			patch, err := repo.PathDiff(r.Snapshot, r.Candidate, c.Path, true)
			if err != nil {
				return nil, err
			}
			out.Write(patch)
			continue
		}
		text := true
		for _, oid := range []string{c.OldOID, c.NewOID} {
			if oid == "" {
				continue
			}
			b, err := repo.Blob(oid)
			if err != nil {
				return nil, err
			}
			text = text && !binary(b)
		}
		// The Run repository marks every path -diff, so git calls each
		// one binary unless told otherwise.
		patch, err := repo.PathDiff(r.Snapshot, r.Candidate, c.Path, text)
		if err != nil {
			return nil, err
		}
		out.Write(patch)
	}
	return out.Bytes(), nil
}

// Clean makes a patch safe to show on a terminal: every control character
// except tab and newline goes (a CRLF file's CR included), so do the
// bidirectional overrides that can make code read other than it runs,
// and invalid UTF-8 becomes U+FFFD.
func Clean(patch []byte) string {
	return strings.Map(func(r rune) rune {
		if r == '\t' || r == '\n' {
			return r
		}
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) || bidi(r) || invisible(r) {
			return -1
		}
		return r
	}, strings.ToValidUTF8(string(patch), "�"))
}

// bidi reports the bidirectional formatting characters: embeddings and
// overrides (U+202A–U+202E), isolates (U+2066–U+2069) and the marks.
func bidi(r rune) bool {
	return (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069) || r == 0x200e || r == 0x200f || r == 0x061c
}

// invisible reports the zero-width characters and line separators that
// hide text or break lines unseen: U+200B–U+200D, U+2028, U+2029, U+FEFF.
func invisible(r rune) bool {
	return (r >= 0x200b && r <= 0x200d) || r == 0x2028 || r == 0x2029 || r == 0xfeff
}
