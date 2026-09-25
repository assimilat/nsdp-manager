// Package api is the typed, page-oriented view of a switch used by the TUI,
// the CLI, the JSON server and the desktop app. Every method maps onto one of
// the ProSAFE Plus Configuration Utility's screens.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"time"

	"prosafe/internal/client"
	"prosafe/internal/nsdp"
)

// Session is an authenticated view of one switch.
type Session struct {
	Client   *client.Client
	Device   client.Device
	Password string
	target   client.Target
}

// NewSession binds a discovered device and password. No traffic is sent.
func NewSession(c *client.Client, d client.Device, password string) *Session {
	return &Session{Client: c, Device: d, Password: password, target: client.Target{IP: d.IP, MAC: d.MAC}}
}

// Ports is the port count, defaulting to 8 if the switch did not report it.
func (s *Session) Ports() int {
	if s.Device.Ports > 0 {
		return s.Device.Ports
	}
	return 8
}

func (s *Session) read(ctx context.Context, tags ...nsdp.Tag) (*nsdp.Packet, error) {
	return s.Client.Read(ctx, s.target, tags...)
}

func (s *Session) write(ctx context.Context, tlvs ...nsdp.TLV) error {
	_, err := s.Client.Write(ctx, s.target, s.Device.PasswordMode, s.Password, tlvs...)
	return err
}

// Login verifies the password with a harmless write: it re-applies the
// current broadcast-filter state, which the utility also does on login.
func (s *Session) Login(ctx context.Context) error {
	p, err := s.read(ctx, nsdp.TagBroadcastFilter)
	if err != nil {
		return err
	}
	v := p.Get(nsdp.TagBroadcastFilter)
	if len(v) != 1 {
		// fall back to rewriting the switch name, which is always supported
		return s.write(ctx, nsdp.StringTLV(nsdp.TagName, s.Device.Name))
	}
	return s.write(ctx, nsdp.TLV{Tag: nsdp.TagBroadcastFilter, Value: v})
}

// ---- Switch information / IP settings ---------------------------------------

// Info is the Switch Information page.
type Info struct {
	Model        string `json:"model"`
	Name         string `json:"name"`
	Location     string `json:"location,omitempty"`
	MAC          string `json:"mac"`
	Serial       string `json:"serial"`
	Firmware     string `json:"firmware"`
	Firmware2    string `json:"firmware2,omitempty"`
	ActiveImage  int    `json:"active_image"`
	NextImage    int    `json:"next_image"`
	DHCP         bool   `json:"dhcp"`
	IP           string `json:"ip"`
	Netmask      string `json:"netmask"`
	Gateway      string `json:"gateway"`
	Ports        int    `json:"ports"`
	PasswordMode string `json:"password_mode"`
}

// Refresh re-reads the device record and returns the info page.
func (s *Session) Refresh(ctx context.Context) (Info, error) {
	p, err := s.read(ctx, nsdp.TagModel, nsdp.TagName, nsdp.TagLocation, nsdp.TagMAC, nsdp.TagIP, nsdp.TagNetmask,
		nsdp.TagGateway, nsdp.TagDHCP, nsdp.TagActiveImage, nsdp.TagFirmware1, nsdp.TagFirmware2, nsdp.TagNextImage,
		nsdp.TagPasswordMode, nsdp.TagSerial, nsdp.TagPortCount)
	if err != nil {
		return Info{}, err
	}
	d := deviceFrom(p, s.Device)
	s.Device = d
	s.target = client.Target{IP: d.IP, MAC: d.MAC}
	return Info{
		Model: d.Model, Name: d.Name, Location: nsdp.Str(p.Get(nsdp.TagLocation)), MAC: d.MACString, Serial: d.Serial,
		Firmware: d.Firmware, Firmware2: d.Firmware2, ActiveImage: d.ActiveImage, NextImage: d.NextImage,
		DHCP: d.DHCP, IP: d.IPString, Netmask: d.Netmask, Gateway: d.Gateway, Ports: s.Ports(),
		PasswordMode: d.PasswordMode.String(),
	}, nil
}

func deviceFrom(p *nsdp.Packet, old client.Device) client.Device {
	d := old
	if v := p.Get(nsdp.TagModel); v != nil {
		d.Model = nsdp.Str(v)
	}
	if v := p.Get(nsdp.TagName); v != nil {
		d.Name = nsdp.Str(v)
	}
	if v := p.Get(nsdp.TagMAC); len(v) == 6 {
		d.MAC = net.HardwareAddr(v)
		d.MACString = d.MAC.String()
	}
	if v := p.Get(nsdp.TagIP); len(v) == 4 {
		d.IP = net.IP(v)
		d.IPString = d.IP.String()
	}
	if v := p.Get(nsdp.TagNetmask); len(v) == 4 {
		d.Netmask = net.IP(v).String()
	}
	if v := p.Get(nsdp.TagGateway); len(v) == 4 {
		d.Gateway = net.IP(v).String()
	}
	if v := p.Get(nsdp.TagDHCP); v != nil {
		d.DHCP = nsdp.Bool(v)
	}
	if v := p.Get(nsdp.TagFirmware1); v != nil {
		d.Firmware = nsdp.Str(v)
	}
	if v := p.Get(nsdp.TagFirmware2); v != nil {
		d.Firmware2 = nsdp.Str(v)
	}
	if v := p.Get(nsdp.TagActiveImage); len(v) == 1 {
		d.ActiveImage = int(v[0])
	}
	if v := p.Get(nsdp.TagNextImage); len(v) == 1 {
		d.NextImage = int(v[0])
	}
	if v := p.Get(nsdp.TagSerial); v != nil {
		d.Serial = nsdp.Serial(v)
	}
	if v := p.Get(nsdp.TagPortCount); len(v) == 1 {
		d.Ports = int(v[0])
	}
	if v := p.Get(nsdp.TagPasswordMode); len(v) == 4 {
		d.PasswordFlags = uint32(v[0])<<24 | uint32(v[1])<<16 | uint32(v[2])<<8 | uint32(v[3])
		d.PasswordMode = nsdp.ModeFromFlags(d.PasswordFlags)
	}
	d.LastSeen = time.Now()
	return d
}

// IPSettings is the Network > IP Setting form.
type IPSettings struct {
	DHCP    bool   `json:"dhcp"`
	IP      string `json:"ip"`
	Netmask string `json:"netmask"`
	Gateway string `json:"gateway"`
}

// SetIPSettings applies DHCP mode or a static address. With DHCP on, the
// address fields are ignored. The switch may move to a new IP afterwards.
func (s *Session) SetIPSettings(ctx context.Context, in IPSettings) error {
	if in.DHCP {
		return s.write(ctx, nsdp.BoolTLV(nsdp.TagDHCP, true))
	}
	ip, mask, gw := net.ParseIP(in.IP), net.ParseIP(in.Netmask), net.ParseIP(in.Gateway)
	if ip.To4() == nil || mask.To4() == nil {
		return errors.New("IP address and subnet mask must be valid IPv4 addresses")
	}
	if gw.To4() == nil {
		gw = net.IPv4zero
	}
	err := s.write(ctx, nsdp.BoolTLV(nsdp.TagDHCP, false), nsdp.IPTLV(nsdp.TagIP, ip), nsdp.IPTLV(nsdp.TagNetmask, mask), nsdp.IPTLV(nsdp.TagGateway, gw))
	if err == nil {
		s.Device.IP = ip.To4()
		s.Device.IPString = ip.String()
		s.target.IP = ip.To4()
	}
	return err
}

// SetName changes the switch name (tag 0x0003) and location (0x0005).
func (s *Session) SetName(ctx context.Context, name string) error {
	if err := s.write(ctx, nsdp.StringTLV(nsdp.TagName, name)); err != nil {
		return err
	}
	s.Device.Name = name
	return nil
}

// ---- Status --------------------------------------------------------------------

// PortStatuses is the System > Status page.
func (s *Session) PortStatuses(ctx context.Context) ([]nsdp.PortStatus, error) {
	p, err := s.read(ctx, nsdp.TagPortStatus)
	if err != nil {
		return nil, err
	}
	var out []nsdp.PortStatus
	for _, v := range p.All(nsdp.TagPortStatus) {
		if ps, err := nsdp.DecodePortStatus(v); err == nil {
			out = append(out, ps)
		}
	}
	return fillPorts(out, s.Ports(), func(i int) nsdp.PortStatus { return nsdp.PortStatus{Port: i} }, func(x nsdp.PortStatus) int { return x.Port }), nil
}

// PortAdmin is one row of the per-port speed / flow-control settings.
type PortAdmin struct {
	Port        int  `json:"port"`
	Speed       byte `json:"speed"`
	FlowControl bool `json:"flow_control"`
}

// ErrUnsupported marks features a model does not implement.
var ErrUnsupported = errors.New("not supported by this switch model")

// PortAdminSettings reads the configured (not linked) speed per port. Models
// without tag 0x9400, such as the XS708E, return ErrUnsupported.
func (s *Session) PortAdminSettings(ctx context.Context) ([]PortAdmin, error) {
	p, err := s.read(ctx, nsdp.TagPortAdminStatus)
	if err != nil {
		return nil, err
	}
	if p.Result == nsdp.ResultUnsupportedTLV || len(p.All(nsdp.TagPortAdminStatus)) == 0 {
		return nil, ErrUnsupported
	}
	var out []PortAdmin
	for _, v := range p.All(nsdp.TagPortAdminStatus) {
		if len(v) >= 3 {
			out = append(out, PortAdmin{Port: int(v[0]), Speed: v[1], FlowControl: v[2] == 1})
		}
	}
	return out, nil
}

// SetPortAdmin writes speed and flow control for one port.
func (s *Session) SetPortAdmin(ctx context.Context, port int, speed byte, flow bool) error {
	return s.write(ctx, nsdp.PortAdminTLV(port, speed, flow))
}

// ---- Monitoring ------------------------------------------------------------------

// PortStatistics is Monitoring > Port Statistics.
func (s *Session) PortStatistics(ctx context.Context) ([]nsdp.PortStats, error) {
	p, err := s.read(ctx, nsdp.TagPortStats)
	if err != nil {
		return nil, err
	}
	var out []nsdp.PortStats
	for _, v := range p.All(nsdp.TagPortStats) {
		if st, err := nsdp.DecodePortStats(v); err == nil {
			out = append(out, st)
		}
	}
	return fillPorts(out, s.Ports(), func(i int) nsdp.PortStats { return nsdp.PortStats{Port: i} }, func(x nsdp.PortStats) int { return x.Port }), nil
}

// ClearCounters resets all port statistics.
func (s *Session) ClearCounters(ctx context.Context) error {
	return s.write(ctx, nsdp.ByteTLV(nsdp.TagResetStats, 1))
}

// CableTest runs the cable tester on the given ports, one port per request,
// and returns the results in port order.
func (s *Session) CableTest(ctx context.Context, ports []int) ([]nsdp.CableResult, error) {
	var out []nsdp.CableResult
	for _, port := range ports {
		if err := s.write(ctx, nsdp.CableTestTLV(port)); err != nil {
			return out, fmt.Errorf("port %d: %w", port, err)
		}
		// The switch needs a moment before the result is readable.
		var res nsdp.CableResult
		var got bool
		for attempt := 0; attempt < 6 && !got; attempt++ {
			time.Sleep(500 * time.Millisecond)
			p, err := s.read(ctx, nsdp.TagCableResult)
			if err != nil {
				continue
			}
			for _, v := range p.All(nsdp.TagCableResult) {
				r, err := nsdp.DecodeCableResult(v)
				if err == nil && (r.Port == port || r.Port == 0) {
					res, got = r, true
					res.Port = port
				}
			}
		}
		if !got {
			return out, fmt.Errorf("port %d: no cable test result", port)
		}
		out = append(out, res)
	}
	return out, nil
}

// MirrorConfig is Monitoring > Mirroring.
func (s *Session) MirrorConfig(ctx context.Context) (nsdp.Mirror, error) {
	p, err := s.read(ctx, nsdp.TagMirror)
	if err != nil {
		return nsdp.Mirror{}, err
	}
	v := p.Get(nsdp.TagMirror)
	if v == nil {
		return nsdp.Mirror{}, ErrUnsupported
	}
	return nsdp.DecodeMirror(v)
}

// SetMirror configures mirroring; dest 0 disables it.
func (s *Session) SetMirror(ctx context.Context, dest int, sources []int) error {
	for _, sp := range sources {
		if sp == dest {
			return errors.New("destination port cannot also be a source port")
		}
	}
	return s.write(ctx, nsdp.MirrorTLV(dest, sources, s.Ports()))
}

// ---- Multicast ---------------------------------------------------------------------

// Multicast is System > Multicast.
type Multicast struct {
	Snooping        bool  `json:"snooping"`
	VID             int   `json:"vid"`
	ValidateIGMPv3  bool  `json:"validate_igmpv3"`
	BlockUnknown    bool  `json:"block_unknown"`
	RouterPorts     []int `json:"router_ports"`
	RouterPortsSupp bool  `json:"router_ports_supported"`
}

func (s *Session) MulticastConfig(ctx context.Context) (Multicast, error) {
	p, err := s.read(ctx, nsdp.TagIGMPSnooping, nsdp.TagIGMPv3Validate, nsdp.TagBlockUnknownMcast, nsdp.TagIGMPRouterPorts)
	if err != nil {
		return Multicast{}, err
	}
	var m Multicast
	if ig, err := nsdp.DecodeIGMPSnooping(p.Get(nsdp.TagIGMPSnooping)); err == nil {
		m.Snooping, m.VID = ig.Enabled, ig.VID
	}
	m.ValidateIGMPv3 = nsdp.Bool(p.Get(nsdp.TagIGMPv3Validate))
	m.BlockUnknown = nsdp.Bool(p.Get(nsdp.TagBlockUnknownMcast))
	if v := p.Get(nsdp.TagIGMPRouterPorts); len(v) > 0 {
		m.RouterPortsSupp = true
		m.RouterPorts = nsdp.BitmapToPorts(v)
	}
	return m, nil
}

func (s *Session) SetMulticast(ctx context.Context, m Multicast) error {
	if m.VID < 1 || m.VID > 4094 {
		m.VID = 1
	}
	tlvs := []nsdp.TLV{
		nsdp.IGMPSnoopingTLV(m.Snooping, m.VID),
		nsdp.BoolTLV(nsdp.TagIGMPv3Validate, m.ValidateIGMPv3),
		nsdp.BoolTLV(nsdp.TagBlockUnknownMcast, m.BlockUnknown),
	}
	if err := s.write(ctx, tlvs...); err != nil {
		return err
	}
	if m.RouterPortsSupp {
		return s.write(ctx, nsdp.IGMPRouterPortsTLV(m.RouterPorts, s.Ports()))
	}
	return nil
}

// ---- LAG ------------------------------------------------------------------------------

// LAGs is System > LAG. Returns nil, ErrUnsupported when the model has none.
func (s *Session) LAGs(ctx context.Context) ([]nsdp.LAG, error) {
	p, err := s.read(ctx, nsdp.TagLAG, nsdp.TagLAGCount)
	if err != nil {
		return nil, err
	}
	vals := p.All(nsdp.TagLAG)
	if len(vals) == 0 {
		return nil, ErrUnsupported
	}
	var out []nsdp.LAG
	for _, v := range vals {
		if l, err := nsdp.DecodeLAG(v); err == nil {
			out = append(out, l)
		}
	}
	return out, nil
}

// SetLAG writes membership and admin mode for one LAG. Ports may belong to
// at most one LAG; the switch enforces this and rejects overlaps.
func (s *Session) SetLAG(ctx context.Context, l nsdp.LAG) error {
	return s.write(ctx, nsdp.LAGTLV(l.ID, l.Enabled, l.Ports, s.Ports()))
}

// ---- Management -------------------------------------------------------------------------

// Management is System > Management (loop detection, power saving, LED, loop prevention).
type Management struct {
	LoopDetection  bool `json:"loop_detection"`
	PowerSaving    *bool `json:"power_saving,omitempty"`
	PortLED        *bool `json:"port_led,omitempty"`
	LoopPrevention *bool `json:"loop_prevention,omitempty"`
}

func (s *Session) ManagementConfig(ctx context.Context) (Management, error) {
	var m Management
	p, err := s.read(ctx, nsdp.TagLoopDetection)
	if err != nil {
		return m, err
	}
	m.LoopDetection = nsdp.Bool(p.Get(nsdp.TagLoopDetection))
	// optional features: read individually so an unsupported tag does not hide the rest
	for _, opt := range []struct {
		tag nsdp.Tag
		dst **bool
	}{{nsdp.TagPowerSaving, &m.PowerSaving}, {nsdp.TagPortLED, &m.PortLED}, {nsdp.TagLoopPrevention, &m.LoopPrevention}} {
		p, err := s.read(ctx, opt.tag)
		if err != nil || p.Result != nsdp.ResultOK {
			continue
		}
		if v := p.Get(opt.tag); v != nil {
			b := nsdp.Bool(v)
			*opt.dst = &b
		}
	}
	return m, nil
}

func (s *Session) SetLoopDetection(ctx context.Context, on bool) error {
	return s.write(ctx, nsdp.BoolTLV(nsdp.TagLoopDetection, on))
}

func (s *Session) SetPowerSaving(ctx context.Context, on bool) error {
	return s.write(ctx, nsdp.BoolTLV(nsdp.TagPowerSaving, on))
}

func (s *Session) SetPortLED(ctx context.Context, on bool) error {
	return s.write(ctx, nsdp.BoolTLV(nsdp.TagPortLED, on))
}

func (s *Session) SetLoopPrevention(ctx context.Context, on bool) error {
	return s.write(ctx, nsdp.BoolTLV(nsdp.TagLoopPrevention, on))
}

// ---- VLAN ---------------------------------------------------------------------------------

// VLANConfig is the whole VLAN tab.
type VLANConfig struct {
	Mode     byte             `json:"mode"`
	ModeName string           `json:"mode_name"`
	MaxVLANs int              `json:"max_vlans"`
	Port     []nsdp.PortVLAN  `json:"port_vlans"`
	Q        []nsdp.VLAN8021Q `json:"vlans_8021q"`
	PVIDs    []nsdp.PVID      `json:"pvids"`
}

func (s *Session) VLANMode(ctx context.Context) (byte, error) {
	p, err := s.read(ctx, nsdp.TagVLANMode)
	if err != nil {
		return 0, err
	}
	v := p.Get(nsdp.TagVLANMode)
	if len(v) != 1 {
		return 0, ErrUnsupported
	}
	return v[0], nil
}

// VLANs reads everything the current mode exposes. Switches do not answer
// 802.1Q queries while in port-based mode, so those reads are best effort.
func (s *Session) VLANs(ctx context.Context) (VLANConfig, error) {
	var cfg VLANConfig
	mode, err := s.VLANMode(ctx)
	if err != nil {
		return cfg, err
	}
	cfg.Mode, cfg.ModeName = mode, nsdp.VLANModeName(mode)
	if p, err := s.read(ctx, nsdp.TagMaxVLANs); err == nil {
		if v := p.Get(nsdp.TagMaxVLANs); len(v) == 2 {
			cfg.MaxVLANs = int(v[0])<<8 | int(v[1])
		}
	}
	switch mode {
	case nsdp.VLANPortBasic, nsdp.VLANPortAdvanced:
		p, err := s.read(ctx, nsdp.TagPortVLAN)
		if err != nil {
			return cfg, err
		}
		for _, v := range p.All(nsdp.TagPortVLAN) {
			if pv, err := nsdp.DecodePortVLAN(v); err == nil {
				cfg.Port = append(cfg.Port, pv)
			}
		}
	case nsdp.VLAN8021QBasic, nsdp.VLAN8021QAdv:
		p, err := s.read(ctx, nsdp.TagVLAN8021Q)
		if err != nil {
			return cfg, err
		}
		for _, v := range p.All(nsdp.TagVLAN8021Q) {
			if q, err := nsdp.DecodeVLAN8021Q(v); err == nil {
				cfg.Q = append(cfg.Q, q)
			}
		}
		sort.Slice(cfg.Q, func(i, j int) bool { return cfg.Q[i].VID < cfg.Q[j].VID })
		if p, err := s.read(ctx, nsdp.TagPVID); err == nil {
			for _, v := range p.All(nsdp.TagPVID) {
				if pv, err := nsdp.DecodePVID(v); err == nil {
					cfg.PVIDs = append(cfg.PVIDs, pv)
				}
			}
		}
	}
	return cfg, nil
}

// SetVLANMode switches the VLAN engine. Changing mode resets VLAN membership
// on the switch, exactly as the utility warns.
func (s *Session) SetVLANMode(ctx context.Context, mode byte) error {
	return s.write(ctx, nsdp.ByteTLV(nsdp.TagVLANMode, mode))
}

// SetPortVLAN writes a port-based VLAN's membership.
func (s *Session) SetPortVLAN(ctx context.Context, vid int, ports []int) error {
	return s.write(ctx, nsdp.PortVLANTLV(vid, ports, s.Ports()))
}

// Set8021QVLAN creates or updates an 802.1Q VLAN. Tagged must be a subset of members.
func (s *Session) Set8021QVLAN(ctx context.Context, vid int, members, tagged []int) error {
	if vid < 1 || vid > 4094 {
		return errors.New("VLAN ID must be 1-4094")
	}
	return s.write(ctx, nsdp.VLAN8021QTLV(vid, members, tagged, s.Ports()))
}

// Delete8021QVLAN removes a VLAN.
func (s *Session) Delete8021QVLAN(ctx context.Context, vid int) error {
	return s.write(ctx, nsdp.DeleteVLANTLV(vid))
}

// SetPVID sets the port's default VLAN.
func (s *Session) SetPVID(ctx context.Context, port, vid int) error {
	return s.write(ctx, nsdp.PVIDTLV(port, vid))
}

// ---- QoS -----------------------------------------------------------------------------------

// QoSConfig is the QoS tab.
type QoSConfig struct {
	Mode            byte                `json:"mode"`
	ModeName        string              `json:"mode_name"`
	Priorities      []nsdp.PortPriority `json:"priorities"`
	Ingress         []nsdp.PortRate     `json:"ingress"`
	Egress          []nsdp.PortRate     `json:"egress"`
	BroadcastFilter bool                `json:"broadcast_filter"`
	StormRates      []nsdp.PortRate     `json:"storm_rates"`
}

func (s *Session) QoS(ctx context.Context) (QoSConfig, error) {
	var cfg QoSConfig
	p, err := s.read(ctx, nsdp.TagQoSMode, nsdp.TagPortPriority, nsdp.TagIngressRate, nsdp.TagEgressRate, nsdp.TagBroadcastFilter, nsdp.TagStormRate)
	if err != nil {
		return cfg, err
	}
	if v := p.Get(nsdp.TagQoSMode); len(v) == 1 {
		cfg.Mode = v[0]
	}
	cfg.ModeName = nsdp.QoSModeName(cfg.Mode)
	for _, v := range p.All(nsdp.TagPortPriority) {
		if pp, err := nsdp.DecodePortPriority(v); err == nil {
			cfg.Priorities = append(cfg.Priorities, pp)
		}
	}
	dec := func(tag nsdp.Tag) []nsdp.PortRate {
		var out []nsdp.PortRate
		for _, v := range p.All(tag) {
			if r, err := nsdp.DecodePortRate(v); err == nil {
				out = append(out, r)
			}
		}
		return out
	}
	cfg.Ingress, cfg.Egress, cfg.StormRates = dec(nsdp.TagIngressRate), dec(nsdp.TagEgressRate), dec(nsdp.TagStormRate)
	if v := p.Get(nsdp.TagBroadcastFilter); len(v) == 1 {
		cfg.BroadcastFilter = v[0] != 0
	}
	return cfg, nil
}

func (s *Session) SetQoSMode(ctx context.Context, mode byte) error {
	return s.write(ctx, nsdp.ByteTLV(nsdp.TagQoSMode, mode))
}

func (s *Session) SetPortPriority(ctx context.Context, ports []int, prio byte) error {
	var tlvs []nsdp.TLV
	for _, p := range ports {
		tlvs = append(tlvs, nsdp.PortPriorityTLV(p, prio))
	}
	return s.write(ctx, tlvs...)
}

// SetRateLimit sets ingress and/or egress limits for ports. A nil pointer leaves that direction unchanged.
func (s *Session) SetRateLimit(ctx context.Context, ports []int, ingress, egress *uint16) error {
	var tlvs []nsdp.TLV
	for _, p := range ports {
		if ingress != nil {
			tlvs = append(tlvs, nsdp.PortRateTLV(nsdp.TagIngressRate, p, *ingress))
		}
		if egress != nil {
			tlvs = append(tlvs, nsdp.PortRateTLV(nsdp.TagEgressRate, p, *egress))
		}
	}
	if len(tlvs) == 0 {
		return nil
	}
	return s.write(ctx, tlvs...)
}

func (s *Session) SetBroadcastFilter(ctx context.Context, on bool) error {
	return s.write(ctx, nsdp.BroadcastFilterTLV(on))
}

func (s *Session) SetStormRate(ctx context.Context, ports []int, rate uint16) error {
	var tlvs []nsdp.TLV
	for _, p := range ports {
		tlvs = append(tlvs, nsdp.PortRateTLV(nsdp.TagStormRate, p, rate))
	}
	return s.write(ctx, tlvs...)
}

// ---- Maintenance -------------------------------------------------------------------------------

// ChangePassword sets a new admin password.
func (s *Session) ChangePassword(ctx context.Context, oldPw, newPw string) error {
	if len(newPw) == 0 || len(newPw) > 20 {
		return errors.New("password must be 1-20 characters")
	}
	if _, err := s.Client.WriteWithNewPassword(ctx, s.target, s.Device.PasswordMode, oldPw, newPw); err != nil {
		return err
	}
	s.Password = newPw
	return nil
}

// Reboot restarts the switch.
func (s *Session) Reboot(ctx context.Context) error {
	return s.write(ctx, nsdp.ByteTLV(nsdp.TagReboot, 1))
}

// FactoryReset restores factory defaults (password "password", DHCP on).
func (s *Session) FactoryReset(ctx context.Context) error {
	return s.write(ctx, nsdp.ByteTLV(nsdp.TagFactoryReset, 1))
}

// UpgradeFirmware follows the utility's sequence: write tag 0x0010, then push
// the image to the switch over TFTP. The switch reboots when flashing ends.
// Progress receives bytes sent and total.
func (s *Session) UpgradeFirmware(ctx context.Context, path string, progress func(sent, total int)) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(data) < 1024 {
		return errors.New("firmware file is implausibly small")
	}
	if err := s.write(ctx, nsdp.ByteTLV(nsdp.TagFirmwareUpg, 1)); err != nil {
		return fmt.Errorf("switch refused upgrade mode: %w", err)
	}
	time.Sleep(2 * time.Second)
	if err := client.TFTPPut(ctx, s.Device.IP, filepath.Base(path), data, progress); err != nil {
		return fmt.Errorf("tftp upload: %w", err)
	}
	return nil
}

// ---- Save / restore configuration --------------------------------------------------------------

// SavedConfig is the on-disk configuration file format (JSON).
type SavedConfig struct {
	Version    int         `json:"version"`
	SavedAt    time.Time   `json:"saved_at"`
	Model      string      `json:"model"`
	MAC        string      `json:"mac"`
	Name       string      `json:"name"`
	IP         IPSettings  `json:"ip"`
	Multicast  Multicast   `json:"multicast"`
	LAGs       []nsdp.LAG  `json:"lags,omitempty"`
	Management Management  `json:"management"`
	VLAN       VLANConfig  `json:"vlan"`
	QoS        QoSConfig   `json:"qos"`
	Mirror     nsdp.Mirror `json:"mirror"`
}

// SaveConfig reads every configurable page into one document.
func (s *Session) SaveConfig(ctx context.Context) (*SavedConfig, error) {
	info, err := s.Refresh(ctx)
	if err != nil {
		return nil, err
	}
	cfg := &SavedConfig{Version: 1, SavedAt: time.Now(), Model: info.Model, MAC: info.MAC, Name: info.Name,
		IP: IPSettings{DHCP: info.DHCP, IP: info.IP, Netmask: info.Netmask, Gateway: info.Gateway}}
	if cfg.Multicast, err = s.MulticastConfig(ctx); err != nil {
		return nil, fmt.Errorf("multicast: %w", err)
	}
	if lags, err := s.LAGs(ctx); err == nil {
		cfg.LAGs = lags
	}
	if cfg.Management, err = s.ManagementConfig(ctx); err != nil {
		return nil, fmt.Errorf("management: %w", err)
	}
	if cfg.VLAN, err = s.VLANs(ctx); err != nil {
		return nil, fmt.Errorf("vlan: %w", err)
	}
	if cfg.QoS, err = s.QoS(ctx); err != nil {
		return nil, fmt.Errorf("qos: %w", err)
	}
	if m, err := s.MirrorConfig(ctx); err == nil {
		cfg.Mirror = m
	}
	return cfg, nil
}

// WriteConfigFile saves cfg as JSON.
func WriteConfigFile(path string, cfg *SavedConfig) error {
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

// ReadConfigFile loads a saved configuration.
func ReadConfigFile(path string) (*SavedConfig, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg SavedConfig
	if err := json.Unmarshal(b, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// RestoreConfig applies a saved configuration page by page. The IP settings
// are applied last because they can move the switch. Progress messages are
// reported through log.
func (s *Session) RestoreConfig(ctx context.Context, cfg *SavedConfig, log func(string)) error {
	step := func(name string, err error) error {
		if err != nil {
			if errors.Is(err, ErrUnsupported) || nsdp.IsUnsupported(err) {
				log(name + ": skipped (not supported)")
				return nil
			}
			return fmt.Errorf("%s: %w", name, err)
		}
		log(name + ": ok")
		return nil
	}
	if cfg.Name != s.Device.Name {
		if err := step("name", s.SetName(ctx, cfg.Name)); err != nil {
			return err
		}
	}
	if err := step("multicast", s.SetMulticast(ctx, cfg.Multicast)); err != nil {
		return err
	}
	if err := step("loop detection", s.SetLoopDetection(ctx, cfg.Management.LoopDetection)); err != nil {
		return err
	}
	if cfg.Management.PowerSaving != nil {
		if err := step("power saving", s.SetPowerSaving(ctx, *cfg.Management.PowerSaving)); err != nil {
			return err
		}
	}
	// VLAN: set mode, then membership and PVIDs
	if err := step("vlan mode", s.SetVLANMode(ctx, cfg.VLAN.Mode)); err != nil {
		return err
	}
	for _, pv := range cfg.VLAN.Port {
		if err := step(fmt.Sprintf("port vlan %d", pv.VID), s.SetPortVLAN(ctx, pv.VID, pv.Ports)); err != nil {
			return err
		}
	}
	for _, q := range cfg.VLAN.Q {
		if err := step(fmt.Sprintf("802.1q vlan %d", q.VID), s.Set8021QVLAN(ctx, q.VID, q.Members, q.Tagged)); err != nil {
			return err
		}
	}
	for _, pv := range cfg.VLAN.PVIDs {
		if err := step(fmt.Sprintf("pvid port %d", pv.Port), s.SetPVID(ctx, pv.Port, pv.VID)); err != nil {
			return err
		}
	}
	// QoS
	if cfg.QoS.Mode != 0 {
		if err := step("qos mode", s.SetQoSMode(ctx, cfg.QoS.Mode)); err != nil {
			return err
		}
	}
	for _, pp := range cfg.QoS.Priorities {
		if err := step(fmt.Sprintf("priority port %d", pp.Port), s.SetPortPriority(ctx, []int{pp.Port}, pp.Priority)); err != nil {
			return err
		}
	}
	for i := range cfg.QoS.Ingress {
		in := cfg.QoS.Ingress[i]
		var eg *uint16
		for _, e := range cfg.QoS.Egress {
			if e.Port == in.Port {
				r := e.Rate
				eg = &r
			}
		}
		r := in.Rate
		if err := step(fmt.Sprintf("rate limit port %d", in.Port), s.SetRateLimit(ctx, []int{in.Port}, &r, eg)); err != nil {
			return err
		}
	}
	if err := step("broadcast filter", s.SetBroadcastFilter(ctx, cfg.QoS.BroadcastFilter)); err != nil {
		return err
	}
	for _, sr := range cfg.QoS.StormRates {
		if err := step(fmt.Sprintf("storm rate port %d", sr.Port), s.SetStormRate(ctx, []int{sr.Port}, sr.Rate)); err != nil {
			return err
		}
	}
	for _, l := range cfg.LAGs {
		if err := step(fmt.Sprintf("lag %d", l.ID), s.SetLAG(ctx, l)); err != nil {
			return err
		}
	}
	if err := step("mirror", s.SetMirror(ctx, cfg.Mirror.Dest, cfg.Mirror.Sources)); err != nil {
		return err
	}
	return step("ip settings", s.SetIPSettings(ctx, cfg.IP))
}

// fillPorts makes sure every port 1..n has a row, in order.
func fillPorts[T any](rows []T, n int, blank func(int) T, portOf func(T) int) []T {
	byPort := map[int]T{}
	for _, r := range rows {
		byPort[portOf(r)] = r
	}
	out := make([]T, 0, n)
	for i := 1; i <= n; i++ {
		if r, ok := byPort[i]; ok {
			out = append(out, r)
		} else {
			out = append(out, blank(i))
		}
	}
	return out
}
