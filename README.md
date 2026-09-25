# prosafe — Linux manager for NETGEAR ProSAFE Plus switches

A native replacement for NETGEAR's Windows-only *ProSAFE Plus Configuration
Utility 2.7.8*. It speaks the same protocol (NSDP) and covers everything the
Windows/Adobe AIR app does, in three front ends over one shared core:

- **`prosafe` — a Go CLI + TUI.** Scriptable commands for every page, plus an
  interactive terminal UI (Bubble Tea).
- **A local JSON API** (`prosafe serve`) used by the desktop app.
- **A Tauri desktop app** with a modern web UI (`desktop/`), backed by the Go
  binary as a sidecar.

Verified against a live **NETGEAR XS708E** (8-port 10-Gigabit, firmware 1.00.12).

## What it does (parity with the Windows utility)

| Windows utility page | Covered |
| --- | --- |
| Discovery / network list / login | ✅ broadcast discovery, add-by-IP, password login |
| Switch Information | ✅ model, name, MAC, serial, firmware, ports |
| Network → IP Setting | ✅ DHCP or static IP/mask/gateway |
| Status → port link/speed | ✅ per-port link, speed, flow control |
| Monitoring → Port Statistics | ✅ RX/TX bytes, CRC errors, clear counters |
| Monitoring → Mirroring | ✅ source ports → destination |
| Monitoring → Cable Tester | ✅ per-port test, fault distance |
| Multicast → IGMP Snooping | ✅ enable, VLAN, IGMPv3 validate, block unknown, router ports |
| VLAN (port-based & 802.1Q, basic/advanced) | ✅ mode, membership, PVID, add/delete |
| QoS (port-based / 802.1p) | ✅ mode, port priority |
| QoS → Rate Limit | ✅ ingress/egress per port |
| QoS → Broadcast Filtering | ✅ storm control on/off + rate |
| Management → Loop Detection | ✅ |
| Management → Power Saving / LED / Loop Prevention | ✅ where the model supports it |
| LAG (membership + admin) | ✅ static LAGs (no LACP — see `docs/LACP.md`) |
| Maintenance → Change Password | ✅ |
| Maintenance → Firmware Upgrade | ✅ NSDP trigger + TFTP push |
| Maintenance → Reboot / Factory Default | ✅ (confirmed) |
| Maintenance → Save / Restore Configuration | ✅ JSON config files |

## Build

Go 1.24+ is the only requirement for the CLI/TUI:

```
cd prosafe-plus
go build -o prosafe ./cmd/prosafe
```

The desktop app additionally needs Node and the Tauri prerequisites
(webkit2gtk, etc.):

```
cd prosafe-plus/desktop
npm install
npm run tauri dev      # or: npm run tauri build
```

The Go binary is bundled as a Tauri sidecar (`desktop/src-tauri/binaries/`);
rebuild it for your host triple before packaging:

```
go build -o desktop/src-tauri/binaries/prosafe-server-$(rustc -Vv | sed -n 's/host: //p') ./cmd/prosafe
```

## CLI usage

```
prosafe discover
prosafe --switch 192.168.3.3 info
prosafe --switch 192.168.3.3 ports
prosafe --switch 192.168.3.3 vlan add 10 1,2,3 3      # 802.1Q VLAN 10, port 3 tagged
prosafe --switch 192.168.3.3 lag set 1 on 1,2         # static LAG 1 over ports 1-2
prosafe --switch 192.168.3.3 qos rate 1 - 8           # port 1 egress = 64 Mbps
prosafe --switch 192.168.3.3 save backup.json
prosafe --switch 192.168.3.3 --json ports             # machine-readable
```

Run `prosafe` with no arguments for the interactive TUI. `--password` (or
`PROSAFE_PASSWORD`) sets the admin password; the factory default is `password`.

## Offline testing

A built-in simulator models a real XS708E, so the tool and the UI can be
exercised without hardware:

```
prosafe sim --listen :63322 &     # fake switch on this host
prosafe discover                  # finds it
```

`go test ./...` runs codec and simulator tests, including the write/auth path
and destructive operations, with no network.

## How it works

`internal/nsdp` implements the protocol (packet codec, every TLV tag with typed
encoders/decoders, and the three password schemes). `internal/client` is the
UDP transport with discovery, retries and TFTP. `internal/api` is the
page-oriented layer every front end shares. `internal/server` exposes that as
JSON/HTTP; `internal/tui` is the terminal UI; `internal/sim` is the simulator.
See `docs/PROTOCOL.md` for the reverse-engineered protocol details and
`docs/LACP.md` for the LACP analysis.

## Releases

Tagged pushes (`vX.Y.Z`) trigger `.github/workflows/release.yml`, which builds
for Linux, macOS and Windows and attaches to the release:

- the standalone `prosafe` CLI/TUI binary (linux/amd64+arm64, darwin/amd64+arm64,
  windows/amd64), and
- the optional Tauri desktop app bundle (`.deb`/`.AppImage`, `.dmg`, `.msi`).

The desktop app is entirely optional — the CLI, TUI and `serve` API work on
their own. The workflow uses GitHub Actions syntax; Forgejo Actions reads the
same `.github/workflows/` path, so it runs there too once a runner is
registered.

## Security note

On this firmware the admin password is sent XOR-obfuscated with a fixed key,
not encrypted — anyone on the LAN can trivially recover it from a captured
packet (CVE-2020-35225 and related). Treat the management network as trusted.
