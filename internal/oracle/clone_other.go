//go:build !darwin && !linux

package oracle

import "errors"

// cloneTree has no copy-on-write clone on this platform.
func cloneTree(src, dst string) error {
	return errors.New("no copy-on-write clone on this platform")
}
