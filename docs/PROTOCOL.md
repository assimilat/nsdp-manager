# NSDP as implemented by ProSAFE Plus Utility 2.7.8

Reverse-engineered from the utility's own `NsdpManager.exe` (decompiled with
Ghidra), its `UAPI.wsdl`, the embedded Adobe AIR/Flash UI, community projects
(`go-nsdp`, `nsdpc`, `libnsdp`, ProSafeLinux), and confirmed by reading and
writing a live **XS708E** (firmware 1.00.12). Every tag below with a value
example was observed on that unit.

## Transport

- UDP. Protocol v2 uses client port **63321** → switch port **63322**; the
  legacy v1 pair is 63323/63324. Discovery is a broadcast to
  255.255.255.255:63322.
- The reply comes back to the client port. The switch also answers unicast
  reads/writes sent to its own IP.

## Header (32 bytes, big-endian)

| Offset | Size | Field |
| --- | --- | --- |
| 0x00 | 1 | version, always `0x01` |
| 0x01 | 1 | operation: 1 read-req, 2 read-resp, 3 write-req, 4 write-resp |
| 0x02 | 2 | result code (0 = OK) |
| 0x04 | 2 | tag that caused a failure |
| 0x06 | 2 | reserved |
| 0x08 | 6 | manager (host) MAC |
| 0x0e | 6 | switch MAC (all-zero = broadcast/any) |
| 0x14 | 2 | reserved |
| 0x16 | 2 | sequence number (echoed in the reply) |
| 0x18 | 4 | signature `"NSDP"` |
| 0x1c | 4 | reserved |

Body is a chain of TLVs (tag u16, length u16, value); it ends with the marker
tag `0xffff` length 0. Some firmware emits length `0xffff` to mean "empty".

### Result codes observed

| Code | Meaning |
| --- | --- |
| `0x0000` | success |
| `0x0300` | tag not supported by this switch (returned with the failing tag at 0x04) |
| `0x0500` | invalid value |
| `0x0700` | wrong password |

## Passwords (three schemes, selected by tag 0x0014)

The switch advertises its scheme in a 4-byte flags word at tag `0x0014`:

- **bit 0** → XOR obfuscation. The password is XORed byte-for-byte with the
  fixed ASCII key `NtgrSmartSwitchRock` and sent in tag `0x000a`. **The XS708E
  uses this.** It is trivially reversible from a capture.
- **bit 3** → a 4-byte keyed hash of password+MAC+salt in tag `0x000a`.
- **bit 4** → "auth v2": read a 4-byte salt from tag `0x0017`, compute an 8-byte
  hash of the 20-byte-padded password, the switch MAC and the salt, and send it
  in tag `0x001a`. (The hash is a fixed pattern of byte XORs — not
  cryptographic; CVE-2020-35221.)

Changing the password sends the new value in tag `0x0009` next to the current
credential, encoded the same way.

## TLV tags

Values are as they appear on the wire. "[live]" marks tags confirmed present on
the XS708E; ports are numbered from 1 and bitmaps are MSB-first (port 1 = 0x80
of byte 0), `ceil(nports/8)` bytes long.

| Tag | Name | Value | Seen |
| --- | --- | --- | --- |
| 0x0001 | model | string | [live] `XS708E` |
| 0x0003 | name | string | [live] |
| 0x0004 | MAC | 6 bytes | [live] |
| 0x0005 | location | string | |
| 0x0006 | IP | 4 bytes | [live] |
| 0x0007 | netmask | 4 bytes | [live] |
| 0x0008 | gateway | 4 bytes | [live] |
| 0x0009 | new password | encoded per scheme (write) | |
| 0x000a | password | encoded per scheme (write) | |
| 0x000b | DHCP | 1 byte: 0 static, 1 DHCP | [live] |
| 0x000c | active image | 1 byte | [live] |
| 0x000d / 0x000e | firmware slot 1 / 2 | string | [live] |
| 0x000f | next boot image | 1 byte | [live] |
| 0x0010 | firmware upgrade | write 1 byte `0x01`, then TFTP push | |
| 0x0013 | reboot | write 1 byte `0x01` | |
| 0x0014 | password scheme flags | 4 bytes (see above) | [live] `00000001` |
| 0x0017 | password salt | 4 bytes (hashed schemes only) | |
| 0x001a | auth-v2 hash | 8 bytes (write) | |
| 0x0400 | factory reset | write 1 byte `0x01` | |
| 0x0c00 | port link status | `[port][speed][flow]` | [live] |
| 0x1000 | port statistics | `[port][rx u64][tx u64][crc u64][3×u64 reserved]` | [live] |
| 0x1400 | reset statistics | write 1 byte `0x01` | |
| 0x1800 | cable test | write `[port][0x01]` | |
| 0x1c00 | cable result | `[port][status u32][fault-distance u32]` | [live] |
| 0x2000 | VLAN engine mode | 1 byte 0–4 | [live] `00` |
| 0x2400 | port-based VLAN | `[vid u16][member bitmap]` | [live] |
| 0x2800 | 802.1Q VLAN | `[vid u16][member bitmap][tagged bitmap]` | (802.1Q modes only) |
| 0x2c00 | delete VLAN | write `[vid u16]` | |
| 0x3000 | PVID | `[port][vid u16]` | [live] |
| 0x3400 | QoS mode | 1 byte: 1 port-based, 2 802.1p | [live] `02` |
| 0x3800 | port priority | `[port][1 high..4 low]` | [live] |
| 0x4c00 | ingress rate | `[port][0][0][rate-code u16]` | [live] |
| 0x5000 | egress rate | `[port][0][0][rate-code u16]` | [live] |
| 0x5400 | broadcast filter | 1 byte: 0 off, 3 on | [live] |
| 0x5800 | storm-control rate | `[port][0][0][rate-code u16]` | [live] |
| 0x5c00 | mirroring | `[dest port][0][source bitmap]` (dest 0 = off) | [live] |
| 0x6000 | port count | 1 byte | [live] `08` |
| 0x6400 | max 802.1Q VLAN groups | u16 | [live] `0080` = 128 |
| 0x6800 | IGMP snooping | `[enabled u16][vid u16]` | [live] |
| 0x6c00 | block unknown multicast | 1 byte bool | [live] |
| 0x7000 | validate IGMPv3 header | 1 byte bool | [live] |
| 0x7400 | supported-TLV bitmap | 8 bytes | [live] `0000000f7ffcffff` |
| 0x7800 | serial number | `[1 byte][ASCII serial]` | [live] |
| 0x8000 | IGMP static router ports | port bitmap | |
| 0x8800 | LAG | `[id][admin 0/1][member bitmap]` | [live] |
| 0x8c00 | LAG group count | 1 byte | [live] `04` |
| 0x9000 | loop detection | 1 byte bool | [live] |
| 0x9400 | per-port speed setting | `[port][speed][flow]` (not on XS708E) | |
| 0xa000 | port LED | 1 byte bool (model dependent) | |
| 0xa800 / 0xac00 | power saving / flag | 1 byte bool (model dependent) | |
| 0xb000 | port description | string (model dependent) | |
| 0xf000 | loop prevention | 1 byte bool (model dependent) | |
| 0xffff | end-of-message marker | — | [live] |

### Rate codes (tags 0x4c00 / 0x5000 / 0x5800)

`0` none, `1` 512 Kbps, `2` 1M, `3` 2M, `4` 4M, `5` 8M, `6` 16M, `7` 32M,
`8` 64M, `9` 128M, `10` 256M, `11` 512M, then `12` 1G / `13` 2G / `14` 4G on
10-Gigabit models.

### Link speed codes (tag 0x0c00 byte 1)

`0` down, `1` 10M-half, `2` 10M-full, `3` 100M-half, `4` 100M-full, `5` 1000M,
`6` 10G.

## Notes learned from the decompile

- A read request lists tags with length 0; the switch fills in values in the
  response. Multiple records of the same tag (per-port rows) come back in port
  order.
- Writes must carry the credential TLV first, then the config TLVs, in one
  packet. Several settings of the same tag can be batched (e.g. all port
  priorities at once), which is how the utility's "Apply" works.
- Switching the VLAN engine mode (tag 0x2000) resets all membership, so the
  utility warns first; this tool does too.
- Firmware upgrade is: write tag 0x0010 = 1, then the utility runs a TFTP client
  and pushes the `.flash` image to the switch (WRQ, octet mode). The image is a
  64-byte `UMHD` header + body with a 16-bit byte-sum checksum (see
  `FIRMWARE.md`).
