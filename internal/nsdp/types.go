package nsdp

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
)

// ---- port bitmaps ---------------------------------------------------------

// BitmapLen is the number of bytes a port bitmap occupies for n ports.
func BitmapLen(n int) int { return (n + 7) / 8 }

// PortsToBitmap encodes 1-based port numbers MSB-first (port 1 = 0x80 of byte 0).
func PortsToBitmap(ports []int, n int) []byte {
	b := make([]byte, BitmapLen(n))
	for _, p := range ports {
		if p < 1 || p > len(b)*8 {
			continue
		}
		b[(p-1)/8] |= 0x80 >> uint((p-1)%8)
	}
	return b
}

// BitmapToPorts decodes an MSB-first port bitmap into sorted 1-based ports.
func BitmapToPorts(b []byte) []int {
	var out []int
	for i, x := range b {
		for bit := 0; bit < 8; bit++ {
			if x&(0x80>>uint(bit)) != 0 {
				out = append(out, i*8+bit+1)
			}
		}
	}
	return out
}

// PortList formats ports as "1,2,5-8".
func PortList(ports []int) string {
	if len(ports) == 0 {
		return "-"
	}
	s := append([]int{}, ports...)
	sort.Ints(s)
	var parts []string
	for i := 0; i < len(s); {
		j := i
		for j+1 < len(s) && s[j+1] == s[j]+1 {
			j++
		}
		if j-i >= 2 {
			parts = append(parts, fmt.Sprintf("%d-%d", s[i], s[j]))
		} else {
			for k := i; k <= j; k++ {
				parts = append(parts, fmt.Sprint(s[k]))
			}
		}
		i = j + 1
	}
	return strings.Join(parts, ",")
}

// ParsePortList parses "1,2,5-8" into 1-based ports.
func ParsePortList(s string) ([]int, error) {
	s = strings.TrimSpace(s)
	if s == "" || s == "-" {
		return nil, nil
	}
	var out []int
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		var a, b int
		if n, _ := fmt.Sscanf(part, "%d-%d", &a, &b); n == 2 {
			if b < a {
				return nil, fmt.Errorf("bad range %q", part)
			}
			for p := a; p <= b; p++ {
				out = append(out, p)
			}
			continue
		}
		if _, err := fmt.Sscanf(part, "%d", &a); err != nil {
			return nil, fmt.Errorf("bad port %q", part)
		}
		out = append(out, a)
	}
	sort.Ints(out)
	return out, nil
}

// ---- simple scalar helpers -------------------------------------------------

func Bool(v []byte) bool { return len(v) > 0 && v[len(v)-1] == 1 }

func BoolTLV(tag Tag, on bool) TLV {
	if on {
		return TLV{Tag: tag, Value: []byte{1}}
	}
	return TLV{Tag: tag, Value: []byte{0}}
}

func ByteTLV(tag Tag, v byte) TLV { return TLV{Tag: tag, Value: []byte{v}} }

func StringTLV(tag Tag, s string) TLV { return TLV{Tag: tag, Value: []byte(s)} }

func IPTLV(tag Tag, ip net.IP) TLV { return TLV{Tag: tag, Value: append([]byte{}, ip.To4()...)} }

// Str trims NUL padding from a string value.
func Str(v []byte) string { return strings.TrimRight(string(v), "\x00") }

// Serial decodes tag 0x7800, which carries one leading byte before the text.
func Serial(v []byte) string {
	if len(v) > 1 && v[0] < 0x20 {
		v = v[1:]
	}
	return strings.TrimRight(strings.TrimSpace(string(v)), "\x00")
}

// ---- port status -----------------------------------------------------------

// Link speed codes in tag 0x0c00 byte 1.
const (
	SpeedDown    = 0
	Speed10Half  = 1
	Speed10Full  = 2
	Speed100Half = 3
	Speed100Full = 4
	Speed1000    = 5
	Speed10G     = 6
)

var speedNames = map[byte]string{
	SpeedDown: "No Speed", Speed10Half: "10M Half", Speed10Full: "10M Full",
	Speed100Half: "100M Half", Speed100Full: "100M Full", Speed1000: "1000M", Speed10G: "10G",
}

// SpeedName renders a link speed code.
func SpeedName(code byte) string {
	if s, ok := speedNames[code]; ok {
		return s
	}
	return fmt.Sprintf("code %d", code)
}

// PortStatus is one tag 0x0c00 record.
type PortStatus struct {
	Port        int  `json:"port"`
	Speed       byte `json:"speed"`
	FlowControl bool `json:"flow_control"`
}

func (p PortStatus) Up() bool          { return p.Speed != SpeedDown }
func (p PortStatus) SpeedName() string { return SpeedName(p.Speed) }

func DecodePortStatus(v []byte) (PortStatus, error) {
	if len(v) < 2 {
		return PortStatus{}, errors.New("port status too short")
	}
	ps := PortStatus{Port: int(v[0]), Speed: v[1]}
	if len(v) > 2 {
		ps.FlowControl = v[2] == 1
	}
	return ps, nil
}

// Admin speed settings written to tag 0x0c00 / read from 0x9400 on models that
// support per-port speed configuration. Codes follow the utility's option list.
const (
	AdminAuto     = 0
	AdminDisable  = 1
	Admin10Half   = 2
	Admin10Full   = 3
	Admin100Half  = 4
	Admin100Full  = 5
	Admin1000Full = 6
)

var adminSpeedNames = []string{"Auto", "Disable", "10M Half", "10M Full", "100M Half", "100M Full", "1000M Full"}

func AdminSpeedName(code byte) string {
	if int(code) < len(adminSpeedNames) {
		return adminSpeedNames[code]
	}
	return fmt.Sprintf("code %d", code)
}

// AdminSpeedNames lists the selectable settings in code order.
func AdminSpeedNames() []string { return append([]string{}, adminSpeedNames...) }

// PortAdminTLV encodes a per-port speed/flow-control setting.
func PortAdminTLV(port int, speed byte, flow bool) TLV {
	f := byte(0)
	if flow {
		f = 1
	}
	return TLV{Tag: TagPortStatus, Value: []byte{byte(port), speed, f}}
}

// ---- statistics ------------------------------------------------------------

// PortStats is one tag 0x1000 record.
type PortStats struct {
	Port      int    `json:"port"`
	RxBytes   uint64 `json:"rx_bytes"`
	TxBytes   uint64 `json:"tx_bytes"`
	CRCErrors uint64 `json:"crc_errors"`
}

func DecodePortStats(v []byte) (PortStats, error) {
	if len(v) < 25 {
		return PortStats{}, errors.New("port stats too short")
	}
	return PortStats{
		Port:      int(v[0]),
		RxBytes:   binary.BigEndian.Uint64(v[1:9]),
		TxBytes:   binary.BigEndian.Uint64(v[9:17]),
		CRCErrors: binary.BigEndian.Uint64(v[17:25]),
	}, nil
}

// ---- cable test ------------------------------------------------------------

// CableResult is one tag 0x1c00 record.
type CableResult struct {
	Port     int    `json:"port"`
	Status   uint32 `json:"status"`
	Distance uint32 `json:"distance_m"`
}

var cableStatus = map[uint32]string{
	0: "OK", 1: "No Cable", 2: "Open Cable", 3: "Short Circuit", 4: "Fiber Cable", 5: "Shorted Cable",
	6: "Unknown", 7: "Crosstalk",
}

func (c CableResult) StatusName() string {
	if s, ok := cableStatus[c.Status]; ok {
		return s
	}
	return fmt.Sprintf("status %d", c.Status)
}

func DecodeCableResult(v []byte) (CableResult, error) {
	if len(v) < 9 {
		return CableResult{}, errors.New("cable result too short")
	}
	return CableResult{Port: int(v[0]), Status: binary.BigEndian.Uint32(v[1:5]), Distance: binary.BigEndian.Uint32(v[5:9])}, nil
}

func CableTestTLV(port int) TLV { return TLV{Tag: TagCableTest, Value: []byte{byte(port), 1}} }

// ---- VLAN ------------------------------------------------------------------

// VLAN engine modes (tag 0x2000).
const (
	VLANDisabled     = 0
	VLANPortBasic    = 1
	VLANPortAdvanced = 2
	VLAN8021QBasic   = 3
	VLAN8021QAdv     = 4
)

var vlanModeNames = []string{"Disabled", "Port Based (Basic)", "Port Based (Advanced)", "802.1Q (Basic)", "802.1Q (Advanced)"}

func VLANModeName(m byte) string {
	if int(m) < len(vlanModeNames) {
		return vlanModeNames[m]
	}
	return fmt.Sprintf("mode %d", m)
}

func VLANModeNames() []string { return append([]string{}, vlanModeNames...) }

// PortVLAN is a tag 0x2400 record.
type PortVLAN struct {
	VID   int   `json:"vid"`
	Ports []int `json:"ports"`
}

func DecodePortVLAN(v []byte) (PortVLAN, error) {
	if len(v) < 3 {
		return PortVLAN{}, errors.New("port vlan too short")
	}
	return PortVLAN{VID: int(binary.BigEndian.Uint16(v[0:2])), Ports: BitmapToPorts(v[2:])}, nil
}

func PortVLANTLV(vid int, ports []int, nports int) TLV {
	v := binary.BigEndian.AppendUint16(nil, uint16(vid))
	v = append(v, PortsToBitmap(ports, nports)...)
	return TLV{Tag: TagPortVLAN, Value: v}
}

// VLAN8021Q is a tag 0x2800 record. Members includes tagged ports.
type VLAN8021Q struct {
	VID     int   `json:"vid"`
	Members []int `json:"members"`
	Tagged  []int `json:"tagged"`
}

// Untagged returns the members that are not tagged.
func (v VLAN8021Q) Untagged() []int {
	t := map[int]bool{}
	for _, p := range v.Tagged {
		t[p] = true
	}
	var out []int
	for _, p := range v.Members {
		if !t[p] {
			out = append(out, p)
		}
	}
	return out
}

func DecodeVLAN8021Q(v []byte) (VLAN8021Q, error) {
	if len(v) < 4 || (len(v)-2)%2 != 0 {
		return VLAN8021Q{}, errors.New("802.1q vlan record has unexpected length")
	}
	n := (len(v) - 2) / 2
	return VLAN8021Q{
		VID:     int(binary.BigEndian.Uint16(v[0:2])),
		Members: BitmapToPorts(v[2 : 2+n]),
		Tagged:  BitmapToPorts(v[2+n:]),
	}, nil
}

func VLAN8021QTLV(vid int, members, tagged []int, nports int) TLV {
	v := binary.BigEndian.AppendUint16(nil, uint16(vid))
	v = append(v, PortsToBitmap(members, nports)...)
	v = append(v, PortsToBitmap(tagged, nports)...)
	return TLV{Tag: TagVLAN8021Q, Value: v}
}

func DeleteVLANTLV(vid int) TLV {
	return TLV{Tag: TagDeleteVLAN, Value: binary.BigEndian.AppendUint16(nil, uint16(vid))}
}

// PVID is a tag 0x3000 record.
type PVID struct {
	Port int `json:"port"`
	VID  int `json:"vid"`
}

func DecodePVID(v []byte) (PVID, error) {
	if len(v) < 3 {
		return PVID{}, errors.New("pvid too short")
	}
	return PVID{Port: int(v[0]), VID: int(binary.BigEndian.Uint16(v[1:3]))}, nil
}

func PVIDTLV(port, vid int) TLV {
	return TLV{Tag: TagPVID, Value: []byte{byte(port), byte(vid >> 8), byte(vid)}}
}

// ---- QoS ---------------------------------------------------------------------

const (
	QoSPortBased = 1
	QoS8021p     = 2
)

func QoSModeName(m byte) string {
	switch m {
	case QoSPortBased:
		return "Port Based"
	case QoS8021p:
		return "802.1p/DSCP Based"
	}
	return fmt.Sprintf("mode %d", m)
}

// Port priorities (tag 0x3800 byte 1).
const (
	PriorityHigh   = 1
	PriorityMedium = 2
	PriorityNormal = 3
	PriorityLow    = 4
)

var priorityNames = map[byte]string{1: "High", 2: "Medium", 3: "Normal", 4: "Low"}

func PriorityName(p byte) string {
	if s, ok := priorityNames[p]; ok {
		return s
	}
	return fmt.Sprintf("priority %d", p)
}

// PriorityNames lists selectable priorities with their codes.
func PriorityNames() []string { return []string{"High", "Medium", "Normal", "Low"} }

// PortPriority is a tag 0x3800 record.
type PortPriority struct {
	Port     int  `json:"port"`
	Priority byte `json:"priority"`
}

func DecodePortPriority(v []byte) (PortPriority, error) {
	if len(v) < 2 {
		return PortPriority{}, errors.New("port priority too short")
	}
	return PortPriority{Port: int(v[0]), Priority: v[1]}, nil
}

func PortPriorityTLV(port int, prio byte) TLV {
	return TLV{Tag: TagPortPriority, Value: []byte{byte(port), prio}}
}

// ---- rate limits -------------------------------------------------------------

// Rate codes shared by ingress, egress and storm-control rates.
var rateNames = []string{
	"No Limit", "512 Kbps", "1 Mbps", "2 Mbps", "4 Mbps", "8 Mbps", "16 Mbps", "32 Mbps",
	"64 Mbps", "128 Mbps", "256 Mbps", "512 Mbps", "1 Gbps", "2 Gbps", "4 Gbps",
}

// RateName renders a rate code.
func RateName(code uint16) string {
	if int(code) < len(rateNames) {
		return rateNames[code]
	}
	return fmt.Sprintf("code %d", code)
}

// RateNames returns the selectable rates in code order. Codes above 11 are
// only accepted by 10 Gigabit models.
func RateNames() []string { return append([]string{}, rateNames...) }

// PortRate is a tag 0x4c00/0x5000/0x5800 record.
type PortRate struct {
	Port int    `json:"port"`
	Rate uint16 `json:"rate"`
}

func DecodePortRate(v []byte) (PortRate, error) {
	if len(v) < 5 {
		return PortRate{}, errors.New("port rate too short")
	}
	return PortRate{Port: int(v[0]), Rate: binary.BigEndian.Uint16(v[3:5])}, nil
}

func PortRateTLV(tag Tag, port int, rate uint16) TLV {
	return TLV{Tag: tag, Value: []byte{byte(port), 0, 0, byte(rate >> 8), byte(rate)}}
}

// BroadcastFilterTLV encodes tag 0x5400; the utility writes 3 for enabled.
func BroadcastFilterTLV(on bool) TLV {
	if on {
		return ByteTLV(TagBroadcastFilter, 3)
	}
	return ByteTLV(TagBroadcastFilter, 0)
}

// ---- mirroring ---------------------------------------------------------------

// Mirror is the tag 0x5c00 record. Dest 0 means mirroring is off.
type Mirror struct {
	Dest    int   `json:"dest"`
	Sources []int `json:"sources"`
}

func DecodeMirror(v []byte) (Mirror, error) {
	if len(v) < 3 {
		return Mirror{}, errors.New("mirror record too short")
	}
	return Mirror{Dest: int(v[0]), Sources: BitmapToPorts(v[2:])}, nil
}

func MirrorTLV(dest int, sources []int, nports int) TLV {
	v := []byte{byte(dest), 0}
	if dest == 0 {
		sources = nil
	}
	v = append(v, PortsToBitmap(sources, nports)...)
	return TLV{Tag: TagMirror, Value: v}
}

// ---- multicast ----------------------------------------------------------------

// IGMPSnooping is the tag 0x6800 record.
type IGMPSnooping struct {
	Enabled bool `json:"enabled"`
	VID     int  `json:"vid"`
}

func DecodeIGMPSnooping(v []byte) (IGMPSnooping, error) {
	if len(v) < 4 {
		return IGMPSnooping{}, errors.New("igmp record too short")
	}
	return IGMPSnooping{Enabled: binary.BigEndian.Uint16(v[0:2]) == 1, VID: int(binary.BigEndian.Uint16(v[2:4]))}, nil
}

func IGMPSnoopingTLV(on bool, vid int) TLV {
	e := uint16(0)
	if on {
		e = 1
	}
	v := binary.BigEndian.AppendUint16(nil, e)
	v = binary.BigEndian.AppendUint16(v, uint16(vid))
	return TLV{Tag: TagIGMPSnooping, Value: v}
}

func IGMPRouterPortsTLV(ports []int, nports int) TLV {
	return TLV{Tag: TagIGMPRouterPorts, Value: PortsToBitmap(ports, nports)}
}

// ---- LAG ------------------------------------------------------------------------

// LAG is a tag 0x8800 record.
type LAG struct {
	ID      int   `json:"id"`
	Enabled bool  `json:"enabled"`
	Ports   []int `json:"ports"`
}

func DecodeLAG(v []byte) (LAG, error) {
	if len(v) < 3 {
		return LAG{}, errors.New("lag record too short")
	}
	return LAG{ID: int(v[0]), Enabled: v[1] == 1, Ports: BitmapToPorts(v[2:])}, nil
}

func LAGTLV(id int, enabled bool, ports []int, nports int) TLV {
	e := byte(0)
	if enabled {
		e = 1
	}
	v := []byte{byte(id), e}
	v = append(v, PortsToBitmap(ports, nports)...)
	return TLV{Tag: TagLAG, Value: v}
}

// ---- supported TLV bitmap --------------------------------------------------------

// SupportedTags decodes tag 0x7400: bit i (MSB-first across the 8 bytes,
// counted from the least significant end) marks tag i*0x400 as supported.
// Only the presence of a tag in a response is authoritative; this helper is
// informational.
func SupportedTags(v []byte) []Tag {
	if len(v) != 8 {
		return nil
	}
	word := binary.BigEndian.Uint64(v)
	var out []Tag
	for i := 0; i < 64; i++ {
		if word&(1<<uint(i)) != 0 {
			out = append(out, Tag(i*0x400))
		}
	}
	return out
}
