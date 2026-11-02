//go:build !linux && !darwin

package run

import (
	"errors"
	"net"
)

// peerUID can't be read here, so the Desk's socket refuses every peer.
func peerUID(net.Conn) (uint32, error) {
	return 0, errors.New("peer uid is not readable on this platform")
}
