//go:build unix

package workspace

import "syscall"

// oNoFollow makes opening a link fail instead of following it.
const oNoFollow = syscall.O_NOFOLLOW
