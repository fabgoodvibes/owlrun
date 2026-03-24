//go:build windows

package dashboard

import "syscall"

func setReuseAddr(network, address string, c syscall.RawConn) error {
	return nil // Windows enables SO_REUSEADDR by default
}
