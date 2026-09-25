package client

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"time"
)

// TFTPPut uploads data to host:69 using a TFTP write request (octet mode),
// reporting progress as blocks are acknowledged. The ProSAFE utility pushes
// firmware to the switch this way right after writing tag 0x0010.
func TFTPPut(ctx context.Context, host net.IP, filename string, data []byte, progress func(sent, total int)) error {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{})
	if err != nil {
		return err
	}
	defer conn.Close()
	server := &net.UDPAddr{IP: host.To4(), Port: 69}

	wrq := []byte{0, 2}
	wrq = append(wrq, filename...)
	wrq = append(wrq, 0)
	wrq = append(wrq, "octet"...)
	wrq = append(wrq, 0)

	buf := make([]byte, 1500)
	var peer *net.UDPAddr
	// send WRQ, wait for ACK 0
	if peer, err = tftpSendAwaitAck(ctx, conn, server, wrq, 0, buf); err != nil {
		return fmt.Errorf("tftp write request: %w", err)
	}
	total := len(data)
	block := uint16(1)
	for off := 0; ; off += 512 {
		end := off + 512
		if end > total {
			end = total
		}
		pkt := []byte{0, 3, byte(block >> 8), byte(block)}
		pkt = append(pkt, data[off:end]...)
		if _, err := tftpSendAwaitAck(ctx, conn, peer, pkt, block, buf); err != nil {
			return fmt.Errorf("tftp block %d: %w", block, err)
		}
		if progress != nil {
			progress(end, total)
		}
		block++
		if end-off < 512 {
			break
		}
	}
	return nil
}

func tftpSendAwaitAck(ctx context.Context, conn *net.UDPConn, to *net.UDPAddr, pkt []byte, want uint16, buf []byte) (*net.UDPAddr, error) {
	for attempt := 0; attempt < 5; attempt++ {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if _, err := conn.WriteToUDP(pkt, to); err != nil {
			return nil, err
		}
		conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		n, from, err := conn.ReadFromUDP(buf)
		if err != nil {
			continue
		}
		if n < 4 {
			continue
		}
		op := binary.BigEndian.Uint16(buf[0:2])
		switch op {
		case 4: // ACK
			if binary.BigEndian.Uint16(buf[2:4]) == want {
				return from, nil
			}
		case 5: // ERROR
			msg := string(buf[4:n])
			return nil, fmt.Errorf("tftp error %d: %s", binary.BigEndian.Uint16(buf[2:4]), msg)
		}
	}
	return nil, errors.New("no acknowledgement")
}
