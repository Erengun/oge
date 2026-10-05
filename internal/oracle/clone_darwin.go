package oracle

import "golang.org/x/sys/unix"

// cloneTree clones src to a new dst with clonefile(2), which clones a
// directory tree copy-on-write in one call. It fails where the filesystem
// can't clone (not APFS); it never falls back to copying.
func cloneTree(src, dst string) error {
	return unix.Clonefile(src, dst, unix.CLONE_NOFOLLOW)
}
