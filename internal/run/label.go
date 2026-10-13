package run

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

// LabelFile is a Run's human-given label, stored next to the Run in its
// private state. It is the human's own words about the Run: not in the
// Ledger, in no hash and never Evidence, so setting it leaves the Run's
// history as it was (ADR-0012).
const LabelFile = "label"

// LabelMax is the longest label, in characters.
const LabelMax = 80

// ReadLabel is the label of the Run in dir; "" when it has none or the file
// can't be read.
func ReadLabel(dir string) string {
	b, err := os.ReadFile(filepath.Join(dir, LabelFile))
	if err != nil {
		return ""
	}
	return normalizeLabel(string(b))
}

// normalizeLabel makes text one line: control characters (a newline
// included) become spaces, the ends are trimmed, and it is cut to LabelMax
// characters.
func normalizeLabel(text string) string {
	text = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, strings.ToValidUTF8(text, "�"))
	text = strings.Join(strings.Fields(text), " ")
	if utf8.RuneCountInString(text) > LabelMax {
		text = string([]rune(text)[:LabelMax])
	}
	return text
}

// WriteLabel labels the Run in dir; empty text clears it. The file is
// replaced atomically (mode 0600), so a reader sees the old label or the
// new one. It returns the label as stored.
func WriteLabel(dir, text string) (string, error) {
	label := normalizeLabel(text)
	path := filepath.Join(dir, LabelFile)
	if label == "" {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return "", fmt.Errorf("clearing the label: %w", err)
		}
		return "", nil
	}
	tmp, err := os.CreateTemp(dir, LabelFile+".tmp-*")
	if err != nil {
		return "", fmt.Errorf("writing the label: %w", err)
	}
	defer os.Remove(tmp.Name())
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		tmp.Close()
		return "", fmt.Errorf("writing the label: %w", err)
	}
	if _, err := tmp.WriteString(label + "\n"); err != nil {
		tmp.Close()
		return "", fmt.Errorf("writing the label: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("writing the label: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return "", fmt.Errorf("writing the label: %w", err)
	}
	return label, nil
}
