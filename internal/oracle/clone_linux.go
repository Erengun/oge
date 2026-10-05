package oracle

import (
	"fmt"
	"os"
	"os/exec"
)

// cloneTree clones src to a new dst with reflinks. --reflink=always fails
// where the filesystem can't reflink, instead of quietly copying, so a
// clone is never mislabelled.
func cloneTree(src, dst string) error {
	cp := "/bin/cp"
	if _, err := os.Stat(cp); err != nil {
		cp = "/usr/bin/cp"
	}
	if out, err := exec.Command(cp, "-R", "--reflink=always", "--", src, dst).CombinedOutput(); err != nil {
		return fmt.Errorf("cp --reflink=always: %v: %s", err, out)
	}
	return nil
}
