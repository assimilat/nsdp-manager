# Can the XS708E do LACP, and could the firmware add it?

**Short answer:** No LACP today, and enabling it is not a realistic firmware
patch. The switch supports **static** link aggregation only. LACP (IEEE
802.3ad / 802.1AX dynamic aggregation) is a control protocol that this firmware
simply does not contain, and it is not a hidden flag you can flip.

## What the switch has now

The XS708E exposes **static LAGs** over NSDP tag `0x8800`: up to 4 groups, each
a set of member ports plus an admin-enable bit. That is exactly the "LAG" tab in
the ProSAFE Plus utility, and it is what this project's `lag set` command
writes. Static means both ends are hard-configured into a bundle and hash
traffic across the members; there is no negotiation, no LACPDUs, no detection of
a mis-cabled or half-configured bundle.

## Why it is not "just a software toggle"

LACP is a live protocol, not a setting. To do it, the switch's management CPU
must, per bundle:

1. Emit LACPDUs every 1s/30s and parse the partner's LACPDUs.
2. Run the mux/selection state machines from 802.3ad (actor/partner churn,
   collecting/distributing, marker protocol).
3. Program the hardware trunk block only for links the state machine has
   brought up, and tear members out when the neighbor stops responding.

None of that code is in the image:

- The firmware is a **440 KiB flat, uncompressed** bare-metal image for a
  Broadcom **BCM53823** switch SoC (management runs as small "tasks", not a
  general OS with room to drop in a daemon).
- A full-image search for `lacp`, `802.3ad`, `LACPDU`, `marker`, `partner`,
  `actor`, `aggregat*` finds **nothing** — the only `actor` matches are the
  letters inside "factory". There is no LACP state machine to enable, no
  disabled build flag, no stubbed handler.

So "enabling LACP" would mean **writing an 802.3ad implementation from scratch**
in Broadcom SDK calls, cross-compiling it for the BCM53823, fitting it into a
440 KiB image with no source, and re-sealing the container. That is a firmware
*development* project against an undocumented bare-metal target, not a patch.
The Broadcom silicon can hash across a hardware trunk (that is how static LAGs
already work), but it does not run the LACP control plane by itself — the CPU
firmware has to, and here it doesn't.

## The container is patchable (for other things)

If you do want to modify this firmware, the wrapper is trivial to reseal — see
`FIRMWARE.md`:

- 64-byte `UMHD` header, body at `0x40`.
- Length at `0x04` (ASCII hex) and a checksum at `0x10` (ASCII hex) that is just
  `sum(body_bytes) & 0xFFFF`.
- Recompute that one 16-bit sum after any edit and the image passes the
  utility's `Flash image ... chksum %04X` validation.

That gets a modified image *accepted*; it does nothing to conjure an LACP stack
that was never written. Loading a hand-modified image also risks bricking the
unit, and TFTP recovery on these is not guaranteed.

## If you actually need LACP on 10GbE

Practical routes, roughly in order of effort:

1. **Use static LAG on both ends.** If the neighbor is a Linux host or a managed
   switch, configure a *static* bond there (`bonding mode balance-xor`, or the
   peer switch's "static"/"on" trunk mode — not "active/passive LACP"). Static
   both sides gets you the aggregated bandwidth without any LACP.
2. **Get a switch whose firmware includes LACP.** NETGEAR's *Smart Managed Pro*
   line (e.g. XS708T / XS716T) runs a different, much larger firmware that does
   implement 802.3ad. The "Plus" (XS708E) firmware never has.
3. **Replace the firmware with an OS that has an LACP stack** (e.g. an OpenWrt
   or SONiC port for the board). None exists for the XS708E today; porting one
   is a large project and, again, risks bricking.

Bottom line: the hardware can bond ports, but LACP negotiation lives in firmware
that this "Plus" model was never given. Static aggregation is the supported way
to bundle these ports, and this tool can configure it.
