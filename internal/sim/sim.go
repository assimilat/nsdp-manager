// Package sim is a fake NSDP switch modelled on a live XS708E capture. It lets
// the front ends and the codec be exercised without hardware, and lets
// destructive operations (reboot, factory reset, firmware) be tested safely.
package sim

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"sync"
	"time"

	"prosafe/internal/nsdp"
)

// Switch holds simulated state.
type Switch struct {
	mu       sync.Mutex
	Model    string
	Name     string
	MAC      net.HardwareAddr
	IP       net.IP
	Netmask  net.IP
	Gateway  net.IP
	DHCP     bool
	Firmware string
	Serial   string
	Ports    int
	Password string
	Mode     nsdp.PasswordMode

	link      []byte // link speed per port
	stats     [][3]uint64
	vlanMode  byte
	portVLAN  map[int][]int
	qVLAN     map[int][2][]int // vid -> members, tagged
	pvid      []int
	qosMode   byte
	prio      []byte
	ingress   []uint16
	egress    []uint16
	bcast     byte
	storm     []uint16
	mirrorDst int
	mirrorSrc []int
	igmp      nsdp.IGMPSnooping
	igmpv3    bool
	blockMc   bool
	routerPts []int
	lags      []nsdp.LAG
	loopDet   bool
	cable     map[int]nsdp.CableResult
	rebooted  int
	reset     int
	upgrades  int
	Log       func(string, ...any)
}

// NewXS708E returns a simulator with the defaults captured from a real unit.
func NewXS708E() *Switch {
	s := &Switch{
		Model: "XS708E", Name: "", MAC: net.HardwareAddr{0x08, 0xbd, 0x43, 0x71, 0x42, 0x88},
		IP: net.IPv4(192, 168, 3, 3).To4(), Netmask: net.IPv4(255, 255, 255, 0).To4(), Gateway: net.IPv4(192, 168, 3, 1).To4(),
		DHCP: true, Firmware: "1.00.12", Serial: "3GW5577R80220", Ports: 8, Password: "password", Mode: nsdp.PasswordXOR,
		vlanMode: nsdp.VLANDisabled, qosMode: nsdp.QoS8021p, igmp: nsdp.IGMPSnooping{Enabled: true, VID: 1},
	}
	s.link = []byte{nsdp.Speed10G, nsdp.Speed1000, nsdp.Speed10G, nsdp.Speed10G, 0, 0, 0, nsdp.Speed1000}
	s.stats = make([][3]uint64, 8)
	s.stats[0] = [3]uint64{125599, 3958, 0}
	s.portVLAN = map[int][]int{1: {1, 2, 3, 4, 5, 6, 7, 8}}
	s.qVLAN = map[int][2][]int{1: {{1, 2, 3, 4, 5, 6, 7, 8}, nil}}
	s.pvid = []int{1, 1, 1, 1, 1, 1, 1, 1}
	s.prio = []byte{4, 4, 4, 4, 4, 4, 4, 4}
	s.ingress, s.egress, s.storm = make([]uint16, 8), make([]uint16, 8), make([]uint16, 8)
	s.lags = []nsdp.LAG{{ID: 1, Ports: []int{1, 2}}, {ID: 2, Ports: []int{3, 4}}, {ID: 3}, {ID: 4}}
	s.cable = map[int]nsdp.CableResult{}
	return s
}

// resetToDefaults restores factory state in place, preserving the mutex and
// the Log hook (mutating *s would clobber the lock Handle is holding).
func (s *Switch) resetToDefaults() {
	def := NewXS708E()
	def.mu = sync.Mutex{}
	log, reset := s.Log, s.reset
	def.Log = log
	def.reset = reset
	// copy every field except the mutex
	s.Model, s.Name, s.MAC, s.IP, s.Netmask, s.Gateway = def.Model, def.Name, def.MAC, def.IP, def.Netmask, def.Gateway
	s.DHCP, s.Firmware, s.Serial, s.Ports, s.Password, s.Mode = def.DHCP, def.Firmware, def.Serial, def.Ports, def.Password, def.Mode
	s.link, s.stats, s.vlanMode, s.portVLAN, s.qVLAN, s.pvid = def.link, def.stats, def.vlanMode, def.portVLAN, def.qVLAN, def.pvid
	s.qosMode, s.prio, s.ingress, s.egress, s.bcast, s.storm = def.qosMode, def.prio, def.ingress, def.egress, def.bcast, def.storm
	s.mirrorDst, s.mirrorSrc, s.igmp, s.igmpv3, s.blockMc, s.routerPts = def.mirrorDst, def.mirrorSrc, def.igmp, def.igmpv3, def.blockMc, def.routerPts
	s.lags, s.loopDet, s.cable = def.lags, def.loopDet, def.cable
}

// Serve answers NSDP requests on addr (e.g. ":63322") until ctx ends.
func (s *Switch) Serve(ctx context.Context, addr string) error {
	ua, err := net.ResolveUDPAddr("udp4", addr)
	if err != nil {
		return err
	}
	conn, err := net.ListenUDP("udp4", ua)
	if err != nil {
		return err
	}
	defer conn.Close()
	go func() { <-ctx.Done(); conn.Close() }()
	buf := make([]byte, 65535)
	for {
		n, from, err := conn.ReadFromUDP(buf)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		req, err := nsdp.Unmarshal(buf[:n])
		if err != nil {
			continue
		}
		resp := s.Handle(req)
		if resp == nil {
			continue
		}
		// The real switch replies to the client port from its own port.
		conn.WriteToUDP(resp.Marshal(), &net.UDPAddr{IP: from.IP, Port: nsdp.ClientPort})
		if from.Port != nsdp.ClientPort {
			conn.WriteToUDP(resp.Marshal(), from)
		}
	}
}

func (s *Switch) logf(f string, a ...any) {
	if s.Log != nil {
		s.Log(f, a...)
	}
}

// Handle processes one request packet and returns the reply (nil = silence).
func (s *Switch) Handle(req *nsdp.Packet) *nsdp.Packet {
	s.mu.Lock()
	defer s.mu.Unlock()
	zero := true
	for _, b := range req.DeviceMAC {
		if b != 0 {
			zero = false
		}
	}
	if !zero && req.DeviceMAC.String() != s.MAC.String() {
		return nil
	}
	resp := &nsdp.Packet{Op: req.Op + 1, HostMAC: req.HostMAC, DeviceMAC: s.MAC, Seq: req.Seq}
	switch req.Op {
	case nsdp.OpReadRequest:
		for _, t := range req.TLVs {
			vals, ok := s.readTag(t.Tag)
			if !ok {
				resp.Result, resp.FailTag = nsdp.ResultUnsupportedTLV, t.Tag
				resp.TLVs = nil
				return resp
			}
			if vals == nil && t.Tag == nsdp.TagVLAN8021Q {
				return nil // real switch stays silent outside 802.1Q mode
			}
			for _, v := range vals {
				resp.TLVs = append(resp.TLVs, nsdp.TLV{Tag: t.Tag, Value: v})
			}
		}
	case nsdp.OpWriteRequest:
		if !s.authOK(req) {
			resp.Result, resp.FailTag = nsdp.ResultBadPassword, nsdp.TagPassword
			return resp
		}
		for _, t := range req.TLVs {
			if t.Tag == nsdp.TagPassword || t.Tag == nsdp.TagAuthV2Hash {
				continue
			}
			if code := s.writeTag(t); code != 0 {
				resp.Result, resp.FailTag = code, t.Tag
				return resp
			}
		}
	default:
		return nil
	}
	return resp
}

func (s *Switch) authOK(req *nsdp.Packet) bool {
	var want nsdp.TLV
	switch s.Mode {
	case nsdp.PasswordXOR:
		want = nsdp.TLV{Tag: nsdp.TagPassword, Value: nsdp.XORPassword([]byte(s.Password))}
	case nsdp.PasswordHash8:
		want = nsdp.TLV{Tag: nsdp.TagAuthV2Hash, Value: nsdp.Hash8Password([]byte(s.Password), s.MAC, []byte{1, 2, 3, 4})}
	default:
		want = nsdp.TLV{Tag: nsdp.TagPassword, Value: []byte(s.Password)}
	}
	for _, t := range req.TLVs {
		if t.Tag == want.Tag && string(t.Value) == string(want.Value) {
			return true
		}
	}
	return false
}

func u16(v uint16) []byte { return binary.BigEndian.AppendUint16(nil, v) }

func (s *Switch) readTag(tag nsdp.Tag) ([][]byte, bool) {
	one := func(b []byte) ([][]byte, bool) { return [][]byte{b}, true }
	switch tag {
	case nsdp.TagStartOfMark:
		return one(nil)
	case nsdp.TagModel:
		return one([]byte(s.Model))
	case nsdp.TagUnknown0002:
		return one([]byte{0, 0})
	case nsdp.TagName:
		return one([]byte(s.Name))
	case nsdp.TagMAC:
		return one(s.MAC)
	case nsdp.TagLocation:
		return one(nil)
	case nsdp.TagIP:
		return one(s.IP.To4())
	case nsdp.TagNetmask:
		return one(s.Netmask.To4())
	case nsdp.TagGateway:
		return one(s.Gateway.To4())
	case nsdp.TagDHCP:
		return one(boolByte(s.DHCP))
	case nsdp.TagActiveImage, nsdp.TagNextImage:
		return one([]byte{1})
	case nsdp.TagFirmware1:
		return one([]byte(s.Firmware))
	case nsdp.TagFirmware2:
		return one(nil)
	case nsdp.TagUpgradeStat, nsdp.TagUpgradeStat2:
		return one(nil)
	case nsdp.TagPasswordMode:
		flags := uint32(0)
		switch s.Mode {
		case nsdp.PasswordXOR:
			flags = 1
		case nsdp.PasswordHash4:
			flags = 8
		case nsdp.PasswordHash8:
			flags = 0x10
		}
		return one(binary.BigEndian.AppendUint32(nil, flags))
	case nsdp.TagPasswordSalt:
		if s.Mode.NeedsSalt() {
			return one([]byte{1, 2, 3, 4})
		}
		return nil, false
	case nsdp.TagPortStatus:
		var out [][]byte
		for i := 0; i < s.Ports; i++ {
			out = append(out, []byte{byte(i + 1), s.link[i], 1})
		}
		return out, true
	case nsdp.TagPortStats:
		var out [][]byte
		for i := 0; i < s.Ports; i++ {
			v := []byte{byte(i + 1)}
			for _, c := range s.stats[i] {
				v = binary.BigEndian.AppendUint64(v, c)
			}
			v = append(v, make([]byte, 24)...)
			out = append(out, v)
		}
		return out, true
	case nsdp.TagCableResult:
		var out [][]byte
		for p := 1; p <= s.Ports; p++ {
			if r, ok := s.cable[p]; ok {
				v := []byte{byte(p)}
				v = binary.BigEndian.AppendUint32(v, r.Status)
				v = binary.BigEndian.AppendUint32(v, r.Distance)
				out = append(out, v)
			}
		}
		if out == nil {
			out = [][]byte{make([]byte, 9)}
		}
		return out, true
	case nsdp.TagVLANMode:
		return one([]byte{s.vlanMode})
	case nsdp.TagPortVLAN:
		var out [][]byte
		for vid := 1; vid <= s.Ports+1; vid++ {
			v := u16(uint16(vid))
			v = append(v, nsdp.PortsToBitmap(s.portVLAN[vid], s.Ports)...)
			out = append(out, v)
		}
		return out, true
	case nsdp.TagVLAN8021Q:
		if s.vlanMode != nsdp.VLAN8021QBasic && s.vlanMode != nsdp.VLAN8021QAdv {
			return nil, true
		}
		var out [][]byte
		for vid := 1; vid <= 4094; vid++ {
			q, ok := s.qVLAN[vid]
			if !ok {
				continue
			}
			v := u16(uint16(vid))
			v = append(v, nsdp.PortsToBitmap(q[0], s.Ports)...)
			v = append(v, nsdp.PortsToBitmap(q[1], s.Ports)...)
			out = append(out, v)
		}
		return out, true
	case nsdp.TagPVID:
		var out [][]byte
		for i := 0; i < s.Ports; i++ {
			out = append(out, []byte{byte(i + 1), byte(s.pvid[i] >> 8), byte(s.pvid[i])})
		}
		return out, true
	case nsdp.TagQoSMode:
		return one([]byte{s.qosMode})
	case nsdp.TagPortPriority:
		var out [][]byte
		for i := 0; i < s.Ports; i++ {
			out = append(out, []byte{byte(i + 1), s.prio[i]})
		}
		return out, true
	case nsdp.TagIngressRate, nsdp.TagEgressRate, nsdp.TagStormRate:
		src := s.ingress
		if tag == nsdp.TagEgressRate {
			src = s.egress
		} else if tag == nsdp.TagStormRate {
			src = s.storm
		}
		var out [][]byte
		for i := 0; i < s.Ports; i++ {
			out = append(out, []byte{byte(i + 1), 0, 0, byte(src[i] >> 8), byte(src[i])})
		}
		return out, true
	case nsdp.TagBroadcastFilter:
		return one([]byte{s.bcast})
	case nsdp.TagMirror:
		v := []byte{byte(s.mirrorDst), 0}
		v = append(v, nsdp.PortsToBitmap(s.mirrorSrc, s.Ports)...)
		return one(v)
	case nsdp.TagPortCount:
		return one([]byte{byte(s.Ports)})
	case nsdp.TagMaxVLANs:
		return one([]byte{0, 0x80})
	case nsdp.TagIGMPSnooping:
		return one(nsdp.IGMPSnoopingTLV(s.igmp.Enabled, s.igmp.VID).Value)
	case nsdp.TagBlockUnknownMcast:
		return one(boolByte(s.blockMc))
	case nsdp.TagIGMPv3Validate:
		return one(boolByte(s.igmpv3))
	case nsdp.TagSupportedTLVs:
		return one([]byte{0, 0, 0, 0x0f, 0x7f, 0xfc, 0xff, 0xff})
	case nsdp.TagSerial:
		return one(append([]byte{1}, []byte(s.Serial+"\x00\x00\x00\x00\x00\x00\x00")...))
	case nsdp.TagUnknown7c00, nsdp.TagUnknown8400:
		return one([]byte{1})
	case nsdp.TagIGMPRouterPorts:
		return one(nil)
	case nsdp.TagLAG:
		var out [][]byte
		for _, l := range s.lags {
			out = append(out, nsdp.LAGTLV(l.ID, l.Enabled, l.Ports, s.Ports).Value)
		}
		return out, true
	case nsdp.TagLAGCount:
		return one([]byte{byte(len(s.lags))})
	case nsdp.TagLoopDetection:
		return one(boolByte(s.loopDet))
	}
	return nil, false
}

func boolByte(b bool) []byte {
	if b {
		return []byte{1}
	}
	return []byte{0}
}

func (s *Switch) writeTag(t nsdp.TLV) uint16 {
	v := t.Value
	port := func() int {
		if len(v) > 0 && int(v[0]) >= 1 && int(v[0]) <= s.Ports {
			return int(v[0])
		}
		return 0
	}
	switch t.Tag {
	case nsdp.TagName:
		s.Name = string(v)
	case nsdp.TagLocation:
	case nsdp.TagIP:
		if len(v) == 4 {
			s.IP = net.IP(append([]byte{}, v...))
		}
	case nsdp.TagNetmask:
		if len(v) == 4 {
			s.Netmask = net.IP(append([]byte{}, v...))
		}
	case nsdp.TagGateway:
		if len(v) == 4 {
			s.Gateway = net.IP(append([]byte{}, v...))
		}
	case nsdp.TagDHCP:
		s.DHCP = nsdp.Bool(v)
	case nsdp.TagNewPassword:
		switch s.Mode {
		case nsdp.PasswordXOR:
			s.Password = string(nsdp.XORPassword(v))
		case nsdp.PasswordPlain:
			s.Password = string(v)
		default:
			s.Password = "" // hashed modes: cannot recover; simulator accepts any next
		}
		s.logf("password changed")
	case nsdp.TagReboot:
		s.rebooted++
		s.logf("reboot requested")
	case nsdp.TagFactoryReset:
		s.reset++
		s.logf("factory reset requested")
		s.resetToDefaults()
	case nsdp.TagFirmwareUpg:
		s.upgrades++
		s.logf("firmware upgrade mode entered")
	case nsdp.TagResetStats:
		for i := range s.stats {
			s.stats[i] = [3]uint64{}
		}
	case nsdp.TagCableTest:
		p := port()
		if p == 0 {
			return nsdp.ResultInvalidValue
		}
		if s.link[p-1] == 0 {
			s.cable[p] = nsdp.CableResult{Port: p, Status: 1}
		} else {
			s.cable[p] = nsdp.CableResult{Port: p, Status: 0, Distance: uint32(3 + p)}
		}
	case nsdp.TagPortStatus:
		if port() == 0 || len(v) < 3 {
			return nsdp.ResultInvalidValue
		}
	case nsdp.TagVLANMode:
		if len(v) != 1 || v[0] > nsdp.VLAN8021QAdv {
			return nsdp.ResultInvalidValue
		}
		s.vlanMode = v[0]
		all := []int{1, 2, 3, 4, 5, 6, 7, 8}
		s.portVLAN = map[int][]int{1: all}
		s.qVLAN = map[int][2][]int{1: {all, nil}}
		for i := range s.pvid {
			s.pvid[i] = 1
		}
	case nsdp.TagPortVLAN:
		if len(v) < 3 {
			return nsdp.ResultInvalidValue
		}
		s.portVLAN[int(binary.BigEndian.Uint16(v))] = nsdp.BitmapToPorts(v[2:])
	case nsdp.TagVLAN8021Q:
		q, err := nsdp.DecodeVLAN8021Q(v)
		if err != nil || q.VID < 1 || q.VID > 4094 {
			return nsdp.ResultInvalidValue
		}
		if _, ok := s.qVLAN[q.VID]; !ok && len(s.qVLAN) >= 128 {
			return nsdp.ResultInvalidValue
		}
		s.qVLAN[q.VID] = [2][]int{q.Members, q.Tagged}
	case nsdp.TagDeleteVLAN:
		if len(v) != 2 {
			return nsdp.ResultInvalidValue
		}
		vid := int(binary.BigEndian.Uint16(v))
		if vid == 1 {
			return nsdp.ResultInvalidValue
		}
		delete(s.qVLAN, vid)
	case nsdp.TagPVID:
		pv, err := nsdp.DecodePVID(v)
		if err != nil || pv.Port < 1 || pv.Port > s.Ports {
			return nsdp.ResultInvalidValue
		}
		s.pvid[pv.Port-1] = pv.VID
	case nsdp.TagQoSMode:
		if len(v) != 1 || (v[0] != 1 && v[0] != 2) {
			return nsdp.ResultInvalidValue
		}
		s.qosMode = v[0]
	case nsdp.TagPortPriority:
		p := port()
		if p == 0 || len(v) < 2 || v[1] < 1 || v[1] > 4 {
			return nsdp.ResultInvalidValue
		}
		s.prio[p-1] = v[1]
	case nsdp.TagIngressRate, nsdp.TagEgressRate, nsdp.TagStormRate:
		r, err := nsdp.DecodePortRate(v)
		if err != nil || r.Port < 1 || r.Port > s.Ports || r.Rate > 14 {
			return nsdp.ResultInvalidValue
		}
		switch t.Tag {
		case nsdp.TagIngressRate:
			s.ingress[r.Port-1] = r.Rate
		case nsdp.TagEgressRate:
			s.egress[r.Port-1] = r.Rate
		default:
			s.storm[r.Port-1] = r.Rate
		}
	case nsdp.TagBroadcastFilter:
		if len(v) != 1 {
			return nsdp.ResultInvalidValue
		}
		s.bcast = v[0]
	case nsdp.TagMirror:
		m, err := nsdp.DecodeMirror(v)
		if err != nil || m.Dest > s.Ports {
			return nsdp.ResultInvalidValue
		}
		s.mirrorDst, s.mirrorSrc = m.Dest, m.Sources
	case nsdp.TagIGMPSnooping:
		ig, err := nsdp.DecodeIGMPSnooping(v)
		if err != nil {
			return nsdp.ResultInvalidValue
		}
		s.igmp = ig
	case nsdp.TagBlockUnknownMcast:
		s.blockMc = nsdp.Bool(v)
	case nsdp.TagIGMPv3Validate:
		s.igmpv3 = nsdp.Bool(v)
	case nsdp.TagIGMPRouterPorts:
		s.routerPts = nsdp.BitmapToPorts(v)
	case nsdp.TagLAG:
		l, err := nsdp.DecodeLAG(v)
		if err != nil || l.ID < 1 || l.ID > len(s.lags) {
			return nsdp.ResultInvalidValue
		}
		for _, o := range s.lags {
			if o.ID == l.ID {
				continue
			}
			for _, p := range o.Ports {
				for _, np := range l.Ports {
					if p == np {
						return nsdp.ResultInvalidValue
					}
				}
			}
		}
		s.lags[l.ID-1] = l
	case nsdp.TagLoopDetection:
		s.loopDet = nsdp.Bool(v)
	case nsdp.TagPowerSaving, nsdp.TagPortLED, nsdp.TagLoopPrevention, nsdp.TagPortAdminStatus:
		return nsdp.ResultUnsupportedTLV
	default:
		return nsdp.ResultUnsupportedTLV
	}
	return 0
}

// Counters exposes destructive-operation counts for tests.
func (s *Switch) Counters() (reboots, resets, upgrades int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rebooted, s.reset, s.upgrades
}

// Tick advances traffic counters so statistics pages look alive.
func (s *Switch) Tick() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.stats {
		if s.link[i] != 0 {
			s.stats[i][0] += uint64(1500 * (i + 1))
			s.stats[i][1] += uint64(900 * (i + 1))
		}
	}
}

// RunTicker calls Tick every interval until ctx ends.
func (s *Switch) RunTicker(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.Tick()
		}
	}
}

// String summarises the simulator.
func (s *Switch) String() string {
	return fmt.Sprintf("%s %s at %s (%d ports, password mode %s)", s.Model, s.MAC, s.IP, s.Ports, s.Mode)
}
