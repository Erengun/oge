package delivery

import (
	"bytes"
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
// except tab and newline goes (a CRLF file's CR included), and invalid
// UTF-8 becomes U+FFFD.
func Clean(patch []byte) string {
	return strings.Map(func(r rune) rune {
		if r == '\t' || r == '\n' {
			return r
		}
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
			return -1
		}
		return r
	}, strings.ToValidUTF8(string(patch), "�"))
}
