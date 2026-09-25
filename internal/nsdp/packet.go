package nsdp

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
)

// Operation codes.
const (
	OpReadRequest   = 0x01
	OpReadResponse  = 0x02
	OpWriteRequest  = 0x03
	OpWriteResponse = 0x04
)

// Result codes seen in the header.
const (
	ResultOK             = 0x0000
	ResultUnsupportedTLV = 0x0300 // switch does not implement the tag
	ResultBadPassword    = 0x0700
	ResultInvalidValue   = 0x0500
)

// UDP ports. The manager binds the client port and talks to the switch port.
const (
	ClientPort = 63321
	SwitchPort = 63322
	// Older (v1) devices use 63323/63324.
	ClientPortV1 = 63323
	SwitchPortV1 = 63324
)

// HeaderLen is the fixed header size in bytes.
const HeaderLen = 32

var signature = [4]byte{'N', 'S', 'D', 'P'}

// TLV is one type-length-value record.
type TLV struct {
	Tag   Tag
	Value []byte
}

// Packet is a decoded NSDP message.
type Packet struct {
	Op        uint8
	Result    uint16
	FailTag   Tag // tag that caused a failure (from the failure TLV field)
	HostMAC   net.HardwareAddr
	DeviceMAC net.HardwareAddr
	Seq       uint16
	TLVs      []TLV
}

// Marshal encodes the packet, appending the end-of-mark terminator.
func (p *Packet) Marshal() []byte {
	n := HeaderLen + 4
	for _, t := range p.TLVs {
		n += 4 + len(t.Value)
	}
	b := make([]byte, 0, n)
	b = append(b, 1, p.Op)
	b = binary.BigEndian.AppendUint16(b, p.Result)
	b = binary.BigEndian.AppendUint16(b, uint16(p.FailTag))
	b = append(b, 0, 0)
	b = append(b, mac6(p.HostMAC)...)
	b = append(b, mac6(p.DeviceMAC)...)
	b = append(b, 0, 0)
	b = binary.BigEndian.AppendUint16(b, p.Seq)
	b = append(b, signature[:]...)
	b = append(b, 0, 0, 0, 0)
	for _, t := range p.TLVs {
		if t.Tag == TagEndOfMark {
			continue
		}
		b = binary.BigEndian.AppendUint16(b, uint16(t.Tag))
		b = binary.BigEndian.AppendUint16(b, uint16(len(t.Value)))
		b = append(b, t.Value...)
	}
	b = append(b, 0xff, 0xff, 0, 0)
	return b
}

func mac6(m net.HardwareAddr) []byte {
	out := make([]byte, 6)
	copy(out, m)
	return out
}

// Unmarshal decodes a packet. Records with the special 0xffff length that
// some switches emit for empty values are decoded with an empty value.
func Unmarshal(b []byte) (*Packet, error) {
	if len(b) < HeaderLen {
		return nil, errors.New("nsdp: packet shorter than header")
	}
	if [4]byte(b[24:28]) != signature {
		return nil, errors.New("nsdp: bad signature")
	}
	p := &Packet{
		Op:        b[1],
		Result:    binary.BigEndian.Uint16(b[2:4]),
		FailTag:   Tag(binary.BigEndian.Uint16(b[4:6])),
		HostMAC:   net.HardwareAddr(append([]byte{}, b[8:14]...)),
		DeviceMAC: net.HardwareAddr(append([]byte{}, b[14:20]...)),
		Seq:       binary.BigEndian.Uint16(b[22:24]),
	}
	i := HeaderLen
	for i+4 <= len(b) {
		tag := Tag(binary.BigEndian.Uint16(b[i : i+2]))
		l := int(binary.BigEndian.Uint16(b[i+2 : i+4]))
		i += 4
		if tag == TagEndOfMark {
			break
		}
		if l == 0xffff { // "no data" marker used by some firmware
			l = 0
		}
		if i+l > len(b) {
			return p, fmt.Errorf("nsdp: tag %s length %d exceeds packet", tag, l)
		}
		p.TLVs = append(p.TLVs, TLV{Tag: tag, Value: append([]byte{}, b[i:i+l]...)})
		i += l
	}
	return p, nil
}

// Get returns the first value for tag, or nil.
func (p *Packet) Get(tag Tag) []byte {
	for _, t := range p.TLVs {
		if t.Tag == tag {
			return t.Value
		}
	}
	return nil
}

// All returns every value carried for tag, in order.
func (p *Packet) All(tag Tag) [][]byte {
	var out [][]byte
	for _, t := range p.TLVs {
		if t.Tag == tag {
			out = append(out, t.Value)
		}
	}
	return out
}

// ResultError converts a non-zero result code into an error.
func (p *Packet) ResultError() error {
	if p.Result == ResultOK {
		return nil
	}
	return &Error{Code: p.Result, Tag: p.FailTag}
}

// Error is a switch-reported failure.
type Error struct {
	Code uint16
	Tag  Tag
}

func (e *Error) Error() string {
	var what string
	switch e.Code {
	case ResultBadPassword:
		what = "invalid password"
	case ResultUnsupportedTLV:
		what = "operation not supported by this switch"
	case ResultInvalidValue:
		what = "invalid value"
	default:
		what = fmt.Sprintf("switch error 0x%04x", e.Code)
	}
	if e.Tag != 0 {
		return fmt.Sprintf("%s (tag %s)", what, e.Tag)
	}
	return what
}

// IsUnsupported reports whether err is the switch saying a tag is unknown.
func IsUnsupported(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Code == ResultUnsupportedTLV
}
