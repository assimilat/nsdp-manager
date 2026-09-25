# XS708E firmware dump — `XS708E-V1.00.12.flash`

This directory is a working dump of the stock NETGEAR XS708E firmware image so
it can be inspected without special tools. Nothing here is compressed; the
image is a flat, plaintext code+data blob, so `strings.txt` and a disassembler
are enough to read it.

## Files

| File | What it is |
| --- | --- |
| `XS708E-V1.00.12.bin` | byte-for-byte copy of the `.flash` image |
| `header.txt` | parsed 64-byte container header |
| `strings.txt` | every ASCII string ≥ 5 chars, with file offsets (`strings -t x`) |
| `symbols.txt` | unique identifier-like tokens (function/symbol names) |

## Container format (UMHD)

The first 64 bytes (`0x00`–`0x3F`) are a NETGEAR "uPlus" header; the switch
firmware body follows at `0x40`.

| Offset | Size | Field | Value in this image |
| --- | --- | --- | --- |
| `0x00` | 4 | magic | `UMHD` |
| `0x04` | 8 | body length, ASCII hex | `0006E358` = 451,416 bytes |
| `0x10` | 4 | checksum, ASCII hex | `1ED0` |
| `0x14` | ~10 | board name, NUL-padded | `XS708E` |
| `0x34` | 4 | version, ASCII | `100C` → firmware **1.00.12** |
| `0x40` | … | firmware body | 451,416 bytes |

**Checksum**: the `1ED0` field is the low 16 bits of the **8-bit sum of every
body byte** (`sum(body) & 0xFFFF`). Verified: recomputing over `0x40..EOF`
yields `0x1ED0`. The header length field plus the 64-byte header equals the file
size exactly. So a patched image is re-sealed by recomputing this one 16-bit
sum and writing it back as ASCII hex at `0x10`. The utility's own check string
confirms the format: `Flash image is %d bytes, chksum %04X, version %c.%c.%c
for board %s`.

## Hardware identified from the image

- **Switch SoC**: Broadcom **BCM53823** (StrataConnect / "RoboSwitch" managed
  switch with an on-die management CPU). Init strings: `bcm53823_soc_init`,
  `bcm53823_misc_init`, `_bcm53823_loop_detect_init`.
- **10G PHYs**: Broadcom **BCM84727** and **BCM84833** (`MDIO Firmware download
  completed for BCM84727`, `firmware version for BCM84833 = ...`).
- **I²C / GPIO**: `bcm53003_i2c_bus`, `ksgpio_*` helpers.
- The forwarding tables are the Broadcom ARL/VLAN "Robo" register set
  (`invalid_vlan_group`, `VLAN`, `Robo`). Management runs as bare-metal
  "tasks", not a general-purpose OS (no eCos/Linux/VxWorks strings).

## Link aggregation / LACP finding

There is **no LACP in this firmware**. A full-image search for `lacp`,
`802.3ad`, `LACPDU`, `marker`, `partner`, `actor`, and `aggregat*` returns
nothing (the only `actor` hits are the substring inside "f**actor**y"). The
switch does static trunking only, exposed through NSDP tag `0x8800` (up to 4
groups on this model), which is what the "LAG" tab in the utility writes. See
`LACP.md` for the full analysis and what
adding LACP would actually take.
