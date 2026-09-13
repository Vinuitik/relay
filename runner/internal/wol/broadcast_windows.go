//go:build windows

package wol

import (
	"net"
	"syscall"
)

// setBroadcast enables SO_BROADCAST on conn so sends to the limited
// broadcast address (255.255.255.255) are permitted.
func setBroadcast(conn *net.UDPConn) error {
	rawConn, err := conn.SyscallConn()
	if err != nil {
		return err
	}

	var sockErr error
	if err := rawConn.Control(func(fd uintptr) {
		sockErr = syscall.SetsockoptInt(syscall.Handle(fd), syscall.SOL_SOCKET, syscall.SO_BROADCAST, 1)
	}); err != nil {
		return err
	}
	return sockErr
}
