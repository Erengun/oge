//go:build !darwin && !linux

package oracle

import "errors"

// cloneTreeOS has no copy-on-write clone on this platform.
func cloneTreeOS(src, dst string) error {
	return errors.New("no copy-on-write clone on this platform")
}
