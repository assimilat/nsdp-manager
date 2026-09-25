// Package client sends NSDP requests over UDP and handles discovery,
// authentication and retries.
package client

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"sync"
	"time"

	"prosafe/internal/nsdp"
)

// Options tune the client.
type Options struct {
	Interface string        // bind to this interface's address (optional)
	Timeout   time.Duration // per-attempt wait for a reply
	Retries   int           // attempts per request
	Debug     func(format string, args ...any)
}

// Client owns one UDP socket. It is safe for concurrent use.
type Client struct {
	opts    Options
	conn    *net.UDPConn
	hostMAC net.HardwareAddr
	seq     uint16
	mu      sync.Mutex
	v1      bool // use the 63323/63324 port pair
}

// Device is a discovered switch.
type Device struct {
	Model        string           `json:"model"`
	Name         string           `json:"name"`
	MAC          net.HardwareAddr `json:"-"`
	MACString    string           `json:"mac"`
	IP           net.IP           `json:"-"`
	IPString     string           `json:"ip"`
	Netmask      string           `json:"netmask"`
	Gateway      string           `json:"gateway"`
	DHCP         bool             `json:"dhcp"`
	Firmware     string           `json:"firmware"`
	Firmware2    string           `json:"firmware2,omitempty"`
	ActiveImage  int              `json:"active_image"`
	NextImage    int              `json:"next_image"`
	Serial       string           `json:"serial"`
	Ports        int              `json:"ports"`
	PasswordMode nsdp.PasswordMode `json:"-"`
	PasswordFlags uint32          `json:"password_flags"`
	LastSeen     time.Time        `json:"last_seen"`
}

// New opens the client socket. It prefers the utility's client port so that
// switches which only answer to 63321 still reply, and falls back to an
// ephemeral port.
func New(opts Options) (*Client, error) {
	if opts.Timeout == 0 {
		opts.Timeout = 1200 * time.Millisecond
	}
	if opts.Retries == 0 {
		opts.Retries = 3
	}
	var bindIP net.IP
	var mac net.HardwareAddr
	if opts.Interface != "" {
		ifi, err := net.InterfaceByName(opts.Interface)
		if err != nil {
			return nil, err
		}
		mac = ifi.HardwareAddr
		if addrs, err := ifi.Addrs(); err == nil {
			for _, a := range addrs {
				if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil {
					bindIP = ipn.IP.To4()
					break
				}
			}
		}
	}
	if mac == nil {
		mac = defaultMAC()
	}
	var conn *net.UDPConn
	var err error
	for _, port := range []int{nsdp.ClientPort, 0} {
		conn, err = net.ListenUDP("udp4", &net.UDPAddr{IP: bindIP, Port: port})
		if err == nil {
			break
		}
	}
	if err != nil {
		return nil, fmt.Errorf("bind udp: %w", err)
	}
	if err := setBroadcast(conn); err != nil {
		conn.Close()
		return nil, err
	}
	c := &Client{opts: opts, conn: conn, hostMAC: mac, seq: uint16(rand.Intn(0xfff0))}
	return c, nil
}

// Close releases the socket.
func (c *Client) Close() error { return c.conn.Close() }

// HostMAC is the manager MAC placed in every request header.
func (c *Client) HostMAC() net.HardwareAddr { return c.hostMAC }

func (c *Client) debugf(format string, args ...any) {
	if c.opts.Debug != nil {
		c.opts.Debug(format, args...)
	}
}

func (c *Client) nextSeq() uint16 {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seq++
	if c.seq == 0 {
		c.seq = 1
	}
	return c.seq
}

func (c *Client) switchPort() int {
	if c.v1 {
		return nsdp.SwitchPortV1
	}
	return nsdp.SwitchPort
}

// Discover broadcasts a read request and collects every switch that answers
// within the timeout.
func (c *Client) Discover(ctx context.Context, timeout time.Duration) ([]Device, error) {
	req := &nsdp.Packet{Op: nsdp.OpReadRequest, HostMAC: c.hostMAC, DeviceMAC: make(net.HardwareAddr, 6), Seq: c.nextSeq()}
	for _, t := range discoveryTags {
		req.TLVs = append(req.TLVs, nsdp.TLV{Tag: t})
	}
	raw := req.Marshal()
	dst := &net.UDPAddr{IP: net.IPv4bcast, Port: c.switchPort()}
	c.mu.Lock()
	_, err := c.conn.WriteToUDP(raw, dst)
	c.mu.Unlock()
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(timeout)
	seen := map[string]int{}
	var devs []Device
	buf := make([]byte, 4096)
	for {
		if ctx.Err() != nil {
			return devs, nil
		}
		c.conn.SetReadDeadline(deadline)
		n, from, err := c.conn.ReadFromUDP(buf)
		if err != nil {
			break
		}
		p, err := nsdp.Unmarshal(buf[:n])
		if err != nil || p.Op != nsdp.OpReadResponse || p.Seq != req.Seq {
			continue
		}
		d := deviceFromPacket(p, from.IP)
		if i, ok := seen[d.MACString]; ok {
			devs[i] = d
		} else {
			seen[d.MACString] = len(devs)
			devs = append(devs, d)
		}
	}
	return devs, nil
}

var discoveryTags = []nsdp.Tag{
	nsdp.TagModel, nsdp.TagName, nsdp.TagMAC, nsdp.TagIP, nsdp.TagNetmask, nsdp.TagGateway,
	nsdp.TagDHCP, nsdp.TagActiveImage, nsdp.TagFirmware1, nsdp.TagFirmware2, nsdp.TagNextImage,
	nsdp.TagPasswordMode, nsdp.TagSerial, nsdp.TagPortCount,
}

func deviceFromPacket(p *nsdp.Packet, from net.IP) Device {
	d := Device{LastSeen: time.Now()}
	d.Model = nsdp.Str(p.Get(nsdp.TagModel))
	d.Name = nsdp.Str(p.Get(nsdp.TagName))
	if m := p.Get(nsdp.TagMAC); len(m) == 6 {
		d.MAC = net.HardwareAddr(m)
	} else {
		d.MAC = p.DeviceMAC
	}
	d.MACString = d.MAC.String()
	if ip := p.Get(nsdp.TagIP); len(ip) == 4 {
		d.IP = net.IP(ip)
	} else {
		d.IP = from
	}
	d.IPString = d.IP.String()
	if v := p.Get(nsdp.TagNetmask); len(v) == 4 {
		d.Netmask = net.IP(v).String()
	}
	if v := p.Get(nsdp.TagGateway); len(v) == 4 {
		d.Gateway = net.IP(v).String()
	}
	d.DHCP = nsdp.Bool(p.Get(nsdp.TagDHCP))
	d.Firmware = nsdp.Str(p.Get(nsdp.TagFirmware1))
	d.Firmware2 = nsdp.Str(p.Get(nsdp.TagFirmware2))
	if v := p.Get(nsdp.TagActiveImage); len(v) == 1 {
		d.ActiveImage = int(v[0])
	}
	if v := p.Get(nsdp.TagNextImage); len(v) == 1 {
		d.NextImage = int(v[0])
	}
	d.Serial = nsdp.Serial(p.Get(nsdp.TagSerial))
	if v := p.Get(nsdp.TagPortCount); len(v) == 1 {
		d.Ports = int(v[0])
	}
	if v := p.Get(nsdp.TagPasswordMode); len(v) == 4 {
		d.PasswordFlags = uint32(v[0])<<24 | uint32(v[1])<<16 | uint32(v[2])<<8 | uint32(v[3])
	}
	d.PasswordMode = nsdp.ModeFromFlags(d.PasswordFlags)
	return d
}

// Target identifies the switch a request goes to.
type Target struct {
	IP  net.IP
	MAC net.HardwareAddr
}

// Read sends a read request for the given tags and returns the reply.
// Some switches stay silent for tags that are not applicable in the current
// mode (e.g. 802.1Q membership while VLANs are disabled); that surfaces as
// ErrTimeout.
func (c *Client) Read(ctx context.Context, t Target, tags ...nsdp.Tag) (*nsdp.Packet, error) {
	req := &nsdp.Packet{Op: nsdp.OpReadRequest, HostMAC: c.hostMAC, DeviceMAC: t.MAC}
	for _, tag := range tags {
		req.TLVs = append(req.TLVs, nsdp.TLV{Tag: tag})
	}
	return c.exchange(ctx, t, req)
}

// ErrTimeout is returned when the switch does not answer.
var ErrTimeout = errors.New("no reply from switch")

// Write sends an authenticated write request. The password is encoded per
// the device's password mode; hashed modes fetch the salt first.
func (c *Client) Write(ctx context.Context, t Target, mode nsdp.PasswordMode, password string, tlvs ...nsdp.TLV) (*nsdp.Packet, error) {
	var salt []byte
	if mode.NeedsSalt() {
		p, err := c.Read(ctx, t, nsdp.TagPasswordSalt)
		if err != nil {
			return nil, fmt.Errorf("read password salt: %w", err)
		}
		salt = p.Get(nsdp.TagPasswordSalt)
		if len(salt) != 4 {
			return nil, errors.New("switch returned no password salt")
		}
	}
	req := &nsdp.Packet{Op: nsdp.OpWriteRequest, HostMAC: c.hostMAC, DeviceMAC: t.MAC}
	req.TLVs = append(req.TLVs, nsdp.AuthTLV(mode, password, t.MAC, salt))
	req.TLVs = append(req.TLVs, tlvs...)
	p, err := c.exchange(ctx, t, req)
	if err != nil {
		return nil, err
	}
	return p, p.ResultError()
}

// WriteWithNewPassword changes the admin password (tag 0x0009 alongside the
// credential). Hashed modes derive both from the same salt.
func (c *Client) WriteWithNewPassword(ctx context.Context, t Target, mode nsdp.PasswordMode, oldPw, newPw string) (*nsdp.Packet, error) {
	var salt []byte
	if mode.NeedsSalt() {
		p, err := c.Read(ctx, t, nsdp.TagPasswordSalt)
		if err != nil {
			return nil, fmt.Errorf("read password salt: %w", err)
		}
		salt = p.Get(nsdp.TagPasswordSalt)
	}
	req := &nsdp.Packet{Op: nsdp.OpWriteRequest, HostMAC: c.hostMAC, DeviceMAC: t.MAC}
	req.TLVs = append(req.TLVs, nsdp.AuthTLV(mode, oldPw, t.MAC, salt), nsdp.NewPasswordTLV(mode, newPw, t.MAC, salt))
	p, err := c.exchange(ctx, t, req)
	if err != nil {
		return nil, err
	}
	return p, p.ResultError()
}

// exchange sends req (unicast to the target IP) and waits for the matching
// reply, retrying on silence.
func (c *Client) exchange(ctx context.Context, t Target, req *nsdp.Packet) (*nsdp.Packet, error) {
	req.Seq = c.nextSeq()
	raw := req.Marshal()
	dst := &net.UDPAddr{IP: t.IP.To4(), Port: c.switchPort()}
	if dst.IP == nil {
		dst.IP = net.IPv4bcast
	}
	buf := make([]byte, 65535)
	c.mu.Lock()
	defer c.mu.Unlock()
	for attempt := 0; attempt < c.opts.Retries; attempt++ {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		c.debugf("-> %s seq=%d op=%d tags=%v", dst, req.Seq, req.Op, tagList(req))
		if _, err := c.conn.WriteToUDP(raw, dst); err != nil {
			return nil, err
		}
		deadline := time.Now().Add(c.opts.Timeout)
		for {
			c.conn.SetReadDeadline(deadline)
			n, _, err := c.conn.ReadFromUDP(buf)
			if err != nil {
				break
			}
			p, err := nsdp.Unmarshal(buf[:n])
			if err != nil {
				c.debugf("<- undecodable packet: %v", err)
				continue
			}
			if p.Seq != req.Seq || p.Op != req.Op+1 {
				continue
			}
			c.debugf("<- result=0x%04x fail=%s tlvs=%d", p.Result, p.FailTag, len(p.TLVs))
			return p, nil
		}
	}
	return nil, ErrTimeout
}

func tagList(p *nsdp.Packet) []string {
	var out []string
	for _, t := range p.TLVs {
		out = append(out, t.Tag.String())
	}
	return out
}

func defaultMAC() net.HardwareAddr {
	ifs, _ := net.Interfaces()
	for _, ifi := range ifs {
		if ifi.Flags&net.FlagLoopback != 0 || ifi.Flags&net.FlagUp == 0 || len(ifi.HardwareAddr) != 6 {
			continue
		}
		return ifi.HardwareAddr
	}
	return net.HardwareAddr{0x02, 0x00, 0x00, 0x00, 0x00, 0x01}
}
