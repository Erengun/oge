package run

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestLabelWriteRead(t *testing.T) {
	dir := t.TempDir()
	if got := ReadLabel(dir); got != "" {
		t.Fatalf("no label yet, got %q", got)
	}
	got, err := WriteLabel(dir, "  the retry\nfix \x1b[31m ")
	if err != nil || got != "the retry fix [31m" {
		t.Fatalf("WriteLabel = %q, %v", got, err)
	}
	if ReadLabel(dir) != got {
		t.Errorf("ReadLabel = %q", ReadLabel(dir))
	}
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(filepath.Join(dir, LabelFile))
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("mode %v", fi.Mode().Perm())
		}
	}
	// Replaced atomically: no temp file is left behind.
	if es, _ := os.ReadDir(dir); len(es) != 1 {
		t.Errorf("leftovers: %v", es)
	}
	if _, err := WriteLabel(dir, "  "); err != nil || ReadLabel(dir) != "" {
		t.Errorf("empty text didn't clear the label")
	}
	if _, err := WriteLabel(dir, ""); err != nil {
		t.Errorf("clearing a missing label: %v", err)
	}
}

func TestLabelLengthCap(t *testing.T) {
	dir := t.TempDir()
	got, err := WriteLabel(dir, strings.Repeat("é", 200))
	if err != nil || utf8.RuneCountInString(got) != LabelMax || !utf8.ValidString(got) {
		t.Fatalf("got %d runes, %v", utf8.RuneCountInString(got), err)
	}
	if ReadLabel(dir) != got {
		t.Error("read back differs")
	}
}
