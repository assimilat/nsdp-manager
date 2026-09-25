// Package nsdp implements the Netgear Switch Discovery Protocol (NSDP) as
// spoken by the ProSAFE Plus Configuration Utility 2.7.8. Tag numbers and value
// layouts were confirmed against a live XS708E (firmware 1.00.12) and the
// decompiled NsdpManager.exe that ships inside the utility.
package nsdp

// Tag is a 16-bit TLV type identifier.
type Tag uint16

// TLV tags used by the utility. Comments give the value layout on the wire.
const (
	TagStartOfMark Tag = 0x0000 // marker, empty
	TagModel       Tag = 0x0001 // string
	TagUnknown0002 Tag = 0x0002 // 2 bytes, always 0 on XS708E
	TagName        Tag = 0x0003 // string (switch name)
	TagMAC         Tag = 0x0004 // 6 bytes
	TagLocation    Tag = 0x0005 // string
	TagIP          Tag = 0x0006 // 4 bytes
	TagNetmask     Tag = 0x0007 // 4 bytes
	TagGateway     Tag = 0x0008 // 4 bytes
	TagNewPassword Tag = 0x0009 // write only; obfuscated per PasswordMode
	TagPassword    Tag = 0x000a // write only; obfuscated per PasswordMode
	TagDHCP        Tag = 0x000b // 1 byte: 0 static, 1 DHCP (2 = renew on some models)
	TagActiveImage Tag = 0x000c // 1 byte: active firmware slot
	TagFirmware1   Tag = 0x000d // string
	TagFirmware2   Tag = 0x000e // string
	TagNextImage   Tag = 0x000f // 1 byte: slot to boot next
	TagFirmwareUpg Tag = 0x0010 // write: 1 byte 0x01, then TFTP push of the image
	TagUpgradeStat Tag = 0x0011 // read: firmware upgrade status (model dependent)
	TagUpgradeStat2 Tag = 0x0012
	TagReboot      Tag = 0x0013 // write: 1 byte 0x01
	TagPasswordMode Tag = 0x0014 // 4 bytes BE flags: bit0 XOR, bit3 4-byte hash, bit4 salted 8-byte hash
	TagPasswordSalt Tag = 0x0017 // 4 bytes, read before hashed auth
	TagAuthV2Hash   Tag = 0x001a // write: 8 bytes (salted hash mode)
	TagFactoryReset Tag = 0x0400 // write: 1 byte 0x01
	TagPortStatus   Tag = 0x0c00 // [port][speed][flowctl], one per port
	TagPortStats    Tag = 0x1000 // [port][rx u64][tx u64][crc u64][3 reserved u64]
	TagResetStats   Tag = 0x1400 // write: 1 byte 0x01
	TagCableTest    Tag = 0x1800 // write: [port][0x01]
	TagCableResult  Tag = 0x1c00 // [port][status u32][fault distance u32]
	TagVLANMode     Tag = 0x2000 // 1 byte, see VLANMode
	TagPortVLAN     Tag = 0x2400 // [vid u16][member bitmap]
	TagVLAN8021Q    Tag = 0x2800 // [vid u16][member bitmap][tagged bitmap]
	TagDeleteVLAN   Tag = 0x2c00 // write: [vid u16]
	TagPVID         Tag = 0x3000 // [port][vid u16]
	TagQoSMode      Tag = 0x3400 // 1 byte: 1 port based, 2 802.1p
	TagPortPriority Tag = 0x3800 // [port][prio 1 high..4 low]
	TagIngressRate  Tag = 0x4c00 // [port][0][0][rate code u16]
	TagEgressRate   Tag = 0x5000 // [port][0][0][rate code u16]
	TagBroadcastFilter Tag = 0x5400 // 1 byte: 0 off, 3 on
	TagStormRate    Tag = 0x5800 // [port][0][0][rate code u16]
	TagMirror       Tag = 0x5c00 // [dst port][0][src bitmap]
	TagPortCount    Tag = 0x6000 // 1 byte
	TagMaxVLANs     Tag = 0x6400 // u16: max 802.1Q VLAN groups
	TagIGMPSnooping Tag = 0x6800 // [enabled u16][vid u16]
	TagBlockUnknownMcast Tag = 0x6c00 // 1 byte bool
	TagIGMPv3Validate Tag = 0x7000 // 1 byte bool
	TagSupportedTLVs Tag = 0x7400 // 8 byte bitmap of supported tags
	TagSerial       Tag = 0x7800 // [1 byte][serial string]
	TagUnknown7c00  Tag = 0x7c00 // 1 byte (1 on XS708E)
	TagIGMPRouterPorts Tag = 0x8000 // port bitmap of static IGMP router ports
	TagUnknown8400  Tag = 0x8400 // 1 byte (1 on XS708E)
	TagLAG          Tag = 0x8800 // [lag id][admin 0/1][member bitmap]
	TagLAGCount     Tag = 0x8c00 // 1 byte: number of LAG groups
	TagLoopDetection Tag = 0x9000 // 1 byte bool
	TagPortAdminStatus Tag = 0x9400 // [port][speed setting][flowctl] (not on XS708E)
	TagPortLED      Tag = 0xa000 // 1 byte bool (not on XS708E)
	TagPowerSaving  Tag = 0xa800 // 1 byte bool (not on XS708E)
	TagPowerSavingFlag Tag = 0xac00 // 1 byte bool (not on XS708E)
	TagPortDescription Tag = 0xb000 // port description (not on XS708E)
	TagLoopPrevention Tag = 0xf000 // 1 byte bool (not on XS708E)
	TagEndOfMark    Tag = 0xffff // terminator
)

var tagNames = map[Tag]string{
	TagModel: "model", TagName: "name", TagMAC: "mac", TagLocation: "location",
	TagIP: "ip", TagNetmask: "netmask", TagGateway: "gateway", TagNewPassword: "new_password",
	TagPassword: "password", TagDHCP: "dhcp", TagActiveImage: "active_image",
	TagFirmware1: "firmware1", TagFirmware2: "firmware2", TagNextImage: "next_image",
	TagFirmwareUpg: "firmware_upgrade", TagUpgradeStat: "upgrade_status", TagUpgradeStat2: "upgrade_status2",
	TagReboot: "reboot", TagPasswordMode: "password_mode", TagPasswordSalt: "password_salt",
	TagAuthV2Hash: "auth_v2_hash", TagFactoryReset: "factory_reset", TagPortStatus: "port_status",
	TagPortStats: "port_stats", TagResetStats: "reset_stats", TagCableTest: "cable_test",
	TagCableResult: "cable_result", TagVLANMode: "vlan_mode", TagPortVLAN: "port_vlan",
	TagVLAN8021Q: "vlan_8021q", TagDeleteVLAN: "delete_vlan", TagPVID: "pvid", TagQoSMode: "qos_mode",
	TagPortPriority: "port_priority", TagIngressRate: "ingress_rate", TagEgressRate: "egress_rate",
	TagBroadcastFilter: "broadcast_filter", TagStormRate: "storm_rate", TagMirror: "mirror",
	TagPortCount: "port_count", TagMaxVLANs: "max_vlans", TagIGMPSnooping: "igmp_snooping",
	TagBlockUnknownMcast: "block_unknown_multicast", TagIGMPv3Validate: "igmpv3_validate",
	TagSupportedTLVs: "supported_tlvs", TagSerial: "serial", TagUnknown7c00: "unknown_7c00",
	TagIGMPRouterPorts: "igmp_router_ports", TagUnknown8400: "unknown_8400", TagLAG: "lag",
	TagLAGCount: "lag_count", TagLoopDetection: "loop_detection", TagPortAdminStatus: "port_admin_status",
	TagPortLED: "port_led", TagPowerSaving: "power_saving", TagPowerSavingFlag: "power_saving_flag",
	TagPortDescription: "port_description", TagLoopPrevention: "loop_prevention", TagEndOfMark: "end",
}

// String returns a short symbolic name for the tag.
func (t Tag) String() string {
	if n, ok := tagNames[t]; ok {
		return n
	}
	return "tag_" + hex4(uint16(t))
}

func hex4(v uint16) string {
	const d = "0123456789abcdef"
	return string([]byte{d[v>>12&0xf], d[v>>8&0xf], d[v>>4&0xf], d[v&0xf]})
}
