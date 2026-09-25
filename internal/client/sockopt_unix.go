//go:build !windows

package client

import (
	"net"

	"golang.org/x/sys/unix"
)

func setBroadcast(conn *net.UDPConn) error {
	rc, err := conn.SyscallConn()
	if err != nil {
		return err
	}
	var serr error
	err = rc.Control(func(fd uintptr) {
		serr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_BROADCAST, 1)
	})
	if err != nil {
		return err
	}
	return serr
}
