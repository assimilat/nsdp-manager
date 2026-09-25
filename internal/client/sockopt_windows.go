//go:build windows

package client

import "net"

func setBroadcast(conn *net.UDPConn) error { return nil }
