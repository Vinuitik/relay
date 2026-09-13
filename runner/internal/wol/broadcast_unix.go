//go:build !windows

package wol

import (
	"net"
	"syscall"
)

// setBroadcast enables SO_BROADCAST on conn so sends to the limited
// broadcast address (255.255.255.255) are permitted - without it, Linux
// (and other unix-likes) reject broadcast sends with EACCES.
func setBroadcast(conn *net.UDPConn) error {
	rawConn, err := conn.SyscallConn()
	if err != nil {
		return err
	}

	var sockErr error
	if err := rawConn.Control(func(fd uintptr) {
		sockErr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_BROADCAST, 1)
	}); err != nil {
		return err
	}
	return sockErr
}
