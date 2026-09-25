package nsdp

// PasswordMode describes how the switch expects the admin password on the
// wire. It is derived from the flags in tag 0x0014 (TagPasswordMode).
type PasswordMode int

const (
	// PasswordPlain sends the password as-is in tag 0x000a (very old firmware).
	PasswordPlain PasswordMode = iota
	// PasswordXOR XORs the password with a fixed key (flag bit 0). XS708E uses this.
	PasswordXOR
	// PasswordHash4 sends a 4-byte hash of password, MAC and salt in tag 0x000a (flag bit 3).
	PasswordHash4
	// PasswordHash8 sends an 8-byte salted hash in tag 0x001a (flag bit 4, "auth v2").
	PasswordHash8
)

func (m PasswordMode) String() string {
	switch m {
	case PasswordXOR:
		return "xor"
	case PasswordHash4:
		return "hash4"
	case PasswordHash8:
		return "hash8"
	}
	return "plain"
}

// ModeFromFlags maps the tag 0x0014 flags word to a PasswordMode.
func ModeFromFlags(flags uint32) PasswordMode {
	switch {
	case flags&0x10 != 0:
		return PasswordHash8
	case flags&0x08 != 0:
		return PasswordHash4
	case flags&0x01 != 0:
		return PasswordXOR
	}
	return PasswordPlain
}

// NeedsSalt reports whether the mode requires reading tag 0x0017 first.
func (m PasswordMode) NeedsSalt() bool { return m == PasswordHash4 || m == PasswordHash8 }

// xorKey is the fixed key the utility uses for PasswordXOR (NsdpManager.exe).
const xorKey = "NtgrSmartSwitchRock"

// XORPassword obfuscates pw with the fixed key; the output has the same length.
func XORPassword(pw []byte) []byte {
	out := make([]byte, len(pw))
	for i, c := range pw {
		out[i] = c ^ xorKey[i%len(xorKey)]
	}
	return out
}

// Hash8Password is the "auth v2" hash: password (padded to 20 bytes), device
// MAC and the 4-byte salt from tag 0x0017 combined into 8 bytes.
func Hash8Password(pw []byte, mac []byte, salt []byte) []byte {
	var k [20]byte
	copy(k[:], pw)
	if len(mac) < 6 || len(salt) < 4 {
		return nil
	}
	return []byte{
		salt[3] ^ salt[2] ^ mac[1] ^ mac[5] ^ k[0] ^ k[1] ^ k[2],
		salt[3] ^ salt[1] ^ mac[4] ^ mac[0] ^ k[3] ^ k[4] ^ k[5],
		salt[0] ^ salt[2] ^ mac[3] ^ mac[2] ^ k[6] ^ k[7] ^ k[8],
		salt[0] ^ salt[1] ^ mac[4] ^ mac[5] ^ k[9] ^ k[10] ^ k[11],
		salt[3] ^ salt[2] ^ mac[1] ^ mac[5] ^ k[12] ^ k[13] ^ k[14],
		salt[3] ^ salt[1] ^ mac[4] ^ mac[0] ^ k[15] ^ k[16] ^ k[17],
		salt[0] ^ salt[2] ^ mac[3] ^ mac[2] ^ k[18] ^ k[19] ^ k[0],
		salt[0] ^ salt[1] ^ mac[4] ^ mac[5] ^ k[1] ^ k[3] ^ k[5],
	}
}

// Hash4Password is the older 4-byte hash (flag bit 3), transcribed from
// nsdp_manager_set_interface in NsdpManager.exe. Untested against hardware.
func Hash4Password(pw []byte, mac []byte, salt []byte) []byte {
	var k [20]byte
	copy(k[:], pw)
	if len(mac) < 6 || len(salt) < 4 {
		return nil
	}
	return []byte{
		mac[1] ^ salt[3] ^ salt[2] ^ k[13] ^ k[9] ^ mac[5] ^ k[7] ^ k[1],
		salt[3] ^ salt[1] ^ mac[4] ^ k[14] ^ k[10] ^ k[6] ^ k[2] ^ mac[0],
		mac[3] ^ mac[2] ^ salt[0] ^ salt[2] ^ k[12] ^ k[8] ^ k[5] ^ k[0],
		salt[0] ^ salt[1] ^ mac[4] ^ k[15] ^ k[11] ^ k[4] ^ k[3] ^ mac[5],
	}
}

// AuthTLV builds the credential TLV for a write request.
func AuthTLV(mode PasswordMode, pw string, mac, salt []byte) TLV {
	return credentialTLV(TagPassword, mode, pw, mac, salt)
}

// NewPasswordTLV builds the tag 0x0009 record used when changing the password.
func NewPasswordTLV(mode PasswordMode, pw string, mac, salt []byte) TLV {
	return credentialTLV(TagNewPassword, mode, pw, mac, salt)
}

func credentialTLV(tag Tag, mode PasswordMode, pw string, mac, salt []byte) TLV {
	switch mode {
	case PasswordXOR:
		return TLV{Tag: tag, Value: XORPassword([]byte(pw))}
	case PasswordHash4:
		return TLV{Tag: tag, Value: Hash4Password([]byte(pw), mac, salt)}
	case PasswordHash8:
		if tag == TagPassword {
			tag = TagAuthV2Hash
		}
		return TLV{Tag: tag, Value: Hash8Password([]byte(pw), mac, salt)}
	}
	return TLV{Tag: tag, Value: []byte(pw)}
}
