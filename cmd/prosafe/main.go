// Command prosafe is a Linux replacement for the NETGEAR ProSAFE Plus
// Configuration Utility: a TUI, a scripting-friendly CLI, a local JSON API
// used by the desktop app, and a switch simulator.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"

	"prosafe/internal/api"
	"prosafe/internal/client"
	"prosafe/internal/nsdp"
	"prosafe/internal/server"
	"prosafe/internal/sim"
	"prosafe/internal/tui"
)

const version = "0.1.0"

func usage() {
	fmt.Fprint(os.Stderr, `prosafe - NETGEAR ProSAFE Plus switch manager (NSDP)

Usage: prosafe [global flags] <command> [args]

Commands:
  tui                         interactive terminal UI (default)
  discover                    list switches on the LAN
  info                        switch information
  ports                       port link status
  stats [--clear]             port statistics
  cabletest <ports>           run the cable tester, e.g. 1,2,5-8
  vlan                        show VLAN configuration
  vlan mode <0-4>             set VLAN engine mode
  vlan port <vid> <ports>     set port-based VLAN membership
  vlan add <vid> <members> [tagged]   create/update 802.1Q VLAN
  vlan del <vid>              delete 802.1Q VLAN
  vlan pvid <ports> <vid>     set PVID
  qos                         show QoS, rate limits, broadcast filtering
  qos mode <1|2>              1 = port based, 2 = 802.1p/DSCP
  qos priority <ports> <1-4>  1 high, 2 medium, 3 normal, 4 low
  qos rate <ports> <in> <out> rate codes (see 'qos rates'), '-' keeps
  qos broadcast <on|off> [ports rate]
  qos rates                   list rate codes
  multicast                   show IGMP snooping settings
  multicast set <on|off> <vid> [v3validate on|off] [block on|off]
  lag                         show LAGs
  lag set <id> <on|off> <ports>
  mirror                      show mirroring
  mirror set <dst|0> [sources]
  loop <on|off>               loop detection
  management                  show management settings
  name <name>                 set switch name
  ip dhcp | ip <ip> <mask> [gw]  IP settings
  password <new>              change admin password
  reboot | factory-reset      maintenance (asks for confirmation)
  firmware <file>             upgrade firmware (asks for confirmation)
  save <file.json>            save configuration
  restore <file.json>         restore configuration
  raw <tag,tag...>            read raw TLVs (hex tags)
  serve [--listen addr]       local JSON API for the desktop app
  sim [--listen addr]         run a simulated XS708E

Global flags:
  --switch <ip>      target switch by IP (discovers if omitted)
  --mac <mac>        target switch by MAC
  --password <pw>    admin password (or PROSAFE_PASSWORD env), default "password"
  --iface <name>     network interface to use
  --json             machine readable output
  --debug            log packets
  --yes              do not ask for confirmation
`)
}

type globals struct {
	sw, mac, password, iface string
	jsonOut, debug, yes      bool
}

func main() {
	g := globals{password: os.Getenv("PROSAFE_PASSWORD")}
	fs := flag.NewFlagSet("prosafe", flag.ExitOnError)
	fs.Usage = usage
	fs.StringVar(&g.sw, "switch", "", "")
	fs.StringVar(&g.mac, "mac", "", "")
	fs.StringVar(&g.password, "password", g.password, "")
	fs.StringVar(&g.iface, "iface", "", "")
	fs.BoolVar(&g.jsonOut, "json", false, "")
	fs.BoolVar(&g.debug, "debug", false, "")
	fs.BoolVar(&g.yes, "yes", false, "")
	showVersion := fs.Bool("version", false, "")
	// Split argv into global flags (before the command) and the command with
	// its own args. Global flags only precede the command; anything after the
	// command name (including that subcommand's own flags) is left untouched.
	boolFlag := map[string]bool{"json": true, "debug": true, "yes": true, "version": true}
	var globalArgs, rest []string
	args0 := os.Args[1:]
	for i := 0; i < len(args0); i++ {
		a := args0[i]
		if !strings.HasPrefix(a, "-") {
			rest = args0[i:] // this is the command; stop consuming global flags
			break
		}
		globalArgs = append(globalArgs, a)
		name := strings.TrimLeft(a, "-")
		if eq := strings.IndexByte(name, '='); eq >= 0 {
			continue // --flag=value form carries its own value
		}
		if !boolFlag[name] && i+1 < len(args0) {
			i++
			globalArgs = append(globalArgs, args0[i])
		}
	}
	fs.Parse(globalArgs)
	if *showVersion {
		fmt.Println("prosafe", version)
		return
	}
	cmdArgs := rest
	if g.password == "" {
		g.password = "password"
	}
	cmd := "tui"
	if len(cmdArgs) > 0 {
		cmd, cmdArgs = cmdArgs[0], cmdArgs[1:]
	}
	if err := run(g, cmd, cmdArgs); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}


func run(g globals, cmd string, args []string) error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	if cmd == "sim" {
		return runSim(ctx, args)
	}
	var debug func(string, ...any)
	if g.debug {
		debug = func(f string, a ...any) { fmt.Fprintf(os.Stderr, f+"\n", a...) }
	}
	cli, err := client.New(client.Options{Interface: g.iface, Debug: debug})
	if err != nil {
		return err
	}
	defer cli.Close()

	switch cmd {
	case "tui":
		return tui.Run(tui.Options{Client: cli, Password: g.password, IP: g.sw})
	case "serve":
		return runServe(ctx, cli, args)
	case "discover":
		devs, err := cli.Discover(ctx, 3*time.Second)
		if err != nil {
			return err
		}
		if g.jsonOut {
			return printJSON(devs)
		}
		if len(devs) == 0 {
			fmt.Println("no switches found (is the switch on the same subnet? UDP 63321/63322 open?)")
			return nil
		}
		fmt.Printf("%-10s %-16s %-18s %-16s %-10s %s\n", "MODEL", "NAME", "MAC", "IP", "FIRMWARE", "AUTH")
		for _, d := range devs {
			fmt.Printf("%-10s %-16s %-18s %-16s %-10s %s\n", d.Model, d.Name, d.MACString, d.IPString, d.Firmware, d.PasswordMode)
		}
		return nil
	}

	s, err := connect(ctx, cli, g)
	if err != nil {
		return err
	}
	return runSwitchCmd(ctx, g, s, cmd, args)
}

// connect finds the target switch (by --switch/--mac or the only device on the LAN).
func connect(ctx context.Context, cli *client.Client, g globals) (*api.Session, error) {
	var dev client.Device
	if g.sw != "" && g.mac == "" {
		ip := net.ParseIP(g.sw)
		if ip == nil {
			return nil, fmt.Errorf("invalid --switch address %q", g.sw)
		}
		s := api.NewSession(cli, client.Device{IP: ip.To4(), IPString: ip.String(), MAC: make(net.HardwareAddr, 6)}, g.password)
		if _, err := s.Refresh(ctx); err != nil {
			return nil, fmt.Errorf("switch %s did not answer: %w", g.sw, err)
		}
		return s, nil
	}
	devs, err := cli.Discover(ctx, 2*time.Second)
	if err != nil {
		return nil, err
	}
	for _, d := range devs {
		if g.mac != "" && !strings.EqualFold(d.MACString, g.mac) {
			continue
		}
		if g.sw != "" && d.IPString != g.sw {
			continue
		}
		if dev.MAC != nil {
			return nil, errors.New("several switches found; pick one with --switch or --mac")
		}
		dev = d
	}
	if dev.MAC == nil {
		return nil, errors.New("no switch found")
	}
	return api.NewSession(cli, dev, g.password), nil
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func confirm(g globals, what string) bool {
	if g.yes {
		return true
	}
	fmt.Printf("%s Type 'yes' to continue: ", what)
	var s string
	fmt.Scanln(&s)
	return s == "yes"
}

func ports(s string) ([]int, error) { return nsdp.ParsePortList(s) }

func onOff(s string) (bool, error) {
	switch strings.ToLower(s) {
	case "on", "enable", "enabled", "1", "true", "yes":
		return true, nil
	case "off", "disable", "disabled", "0", "false", "no":
		return false, nil
	}
	return false, fmt.Errorf("expected on/off, got %q", s)
}

func atoi(s string) (int, error) { return strconv.Atoi(s) }

func runSwitchCmd(ctx context.Context, g globals, s *api.Session, cmd string, args []string) error {
	out := func(v any, text func()) error {
		if g.jsonOut {
			return printJSON(v)
		}
		text()
		return nil
	}
	sub := ""
	if len(args) > 0 {
		sub = args[0]
	}
	switch cmd {
	case "info":
		info, err := s.Refresh(ctx)
		if err != nil {
			return err
		}
		return out(info, func() {
			fmt.Printf("Product:   %s\nName:      %s\nMAC:       %s\nSerial:    %s\nFirmware:  %s (image %d active, %d next)\nDHCP:      %v\nIP:        %s\nNetmask:   %s\nGateway:   %s\nPorts:     %d\nAuth mode: %s\n",
				info.Model, info.Name, info.MAC, info.Serial, info.Firmware, info.ActiveImage, info.NextImage, info.DHCP, info.IP, info.Netmask, info.Gateway, info.Ports, info.PasswordMode)
		})
	case "ports":
		st, err := s.PortStatuses(ctx)
		if err != nil {
			return err
		}
		return out(st, func() {
			fmt.Printf("%-5s %-6s %-10s %s\n", "PORT", "LINK", "SPEED", "FLOWCTL")
			for _, p := range st {
				link := "Down"
				if p.Up() {
					link = "Up"
				}
				fmt.Printf("%-5d %-6s %-10s %v\n", p.Port, link, p.SpeedName(), p.FlowControl)
			}
		})
	case "stats":
		if sub == "--clear" || sub == "clear" {
			return s.ClearCounters(ctx)
		}
		st, err := s.PortStatistics(ctx)
		if err != nil {
			return err
		}
		return out(st, func() {
			fmt.Printf("%-5s %16s %16s %12s\n", "PORT", "RX BYTES", "TX BYTES", "CRC ERRORS")
			for _, p := range st {
				fmt.Printf("%-5d %16d %16d %12d\n", p.Port, p.RxBytes, p.TxBytes, p.CRCErrors)
			}
		})
	case "cabletest":
		if sub == "" {
			return errors.New("usage: cabletest <ports>")
		}
		pl, err := ports(sub)
		if err != nil {
			return err
		}
		res, err := s.CableTest(ctx, pl)
		if err != nil && len(res) == 0 {
			return err
		}
		return out(res, func() {
			fmt.Printf("%-5s %-14s %s\n", "PORT", "RESULT", "FAULT DISTANCE (m)")
			for _, r := range res {
				fmt.Printf("%-5d %-14s %d\n", r.Port, r.StatusName(), r.Distance)
			}
			if err != nil {
				fmt.Println("warning:", err)
			}
		})
	case "vlan":
		switch sub {
		case "":
			cfg, err := s.VLANs(ctx)
			if err != nil {
				return err
			}
			return out(cfg, func() {
				fmt.Printf("Mode: %s (max 802.1Q VLANs: %d)\n", cfg.ModeName, cfg.MaxVLANs)
				for _, v := range cfg.Port {
					fmt.Printf("  VLAN %-4d ports %s\n", v.VID, nsdp.PortList(v.Ports))
				}
				for _, v := range cfg.Q {
					fmt.Printf("  VLAN %-4d untagged %-12s tagged %s\n", v.VID, nsdp.PortList(v.Untagged()), nsdp.PortList(v.Tagged))
				}
				if len(cfg.PVIDs) > 0 {
					fmt.Print("  PVID:")
					for _, p := range cfg.PVIDs {
						fmt.Printf(" %d=%d", p.Port, p.VID)
					}
					fmt.Println()
				}
			})
		case "mode":
			if len(args) < 2 {
				return errors.New("usage: vlan mode <0-4>")
			}
			m, err := atoi(args[1])
			if err != nil || m < 0 || m > 4 {
				return errors.New("mode must be 0 (off), 1/2 (port basic/advanced), 3/4 (802.1Q basic/advanced)")
			}
			if !confirm(g, "Changing the VLAN mode resets all VLAN membership.") {
				return nil
			}
			return s.SetVLANMode(ctx, byte(m))
		case "port":
			if len(args) < 3 {
				return errors.New("usage: vlan port <vid> <ports>")
			}
			vid, err := atoi(args[1])
			if err != nil {
				return err
			}
			pl, err := ports(args[2])
			if err != nil {
				return err
			}
			return s.SetPortVLAN(ctx, vid, pl)
		case "add":
			if len(args) < 3 {
				return errors.New("usage: vlan add <vid> <members> [tagged]")
			}
			vid, err := atoi(args[1])
			if err != nil {
				return err
			}
			members, err := ports(args[2])
			if err != nil {
				return err
			}
			var tagged []int
			if len(args) > 3 {
				if tagged, err = ports(args[3]); err != nil {
					return err
				}
			}
			return s.Set8021QVLAN(ctx, vid, members, tagged)
		case "del":
			if len(args) < 2 {
				return errors.New("usage: vlan del <vid>")
			}
			vid, err := atoi(args[1])
			if err != nil {
				return err
			}
			return s.Delete8021QVLAN(ctx, vid)
		case "pvid":
			if len(args) < 3 {
				return errors.New("usage: vlan pvid <ports> <vid>")
			}
			pl, err := ports(args[1])
			if err != nil {
				return err
			}
			vid, err := atoi(args[2])
			if err != nil {
				return err
			}
			for _, p := range pl {
				if err := s.SetPVID(ctx, p, vid); err != nil {
					return err
				}
			}
			return nil
		}
		return fmt.Errorf("unknown vlan subcommand %q", sub)
	case "qos":
		switch sub {
		case "":
			cfg, err := s.QoS(ctx)
			if err != nil {
				return err
			}
			return out(cfg, func() {
				fmt.Printf("Mode: %s   Broadcast filtering: %v\n", cfg.ModeName, cfg.BroadcastFilter)
				fmt.Printf("%-5s %-8s %-10s %-10s %s\n", "PORT", "PRIO", "INGRESS", "EGRESS", "STORM")
				rate := func(rs []nsdp.PortRate, port int) string {
					for _, r := range rs {
						if r.Port == port {
							return nsdp.RateName(r.Rate)
						}
					}
					return "-"
				}
				for i := 1; i <= s.Ports(); i++ {
					prio := "-"
					for _, p := range cfg.Priorities {
						if p.Port == i {
							prio = nsdp.PriorityName(p.Priority)
						}
					}
					fmt.Printf("%-5d %-8s %-10s %-10s %s\n", i, prio, rate(cfg.Ingress, i), rate(cfg.Egress, i), rate(cfg.StormRates, i))
				}
			})
		case "rates":
			for i, n := range nsdp.RateNames() {
				fmt.Printf("%2d  %s\n", i, n)
			}
			return nil
		case "mode":
			if len(args) < 2 {
				return errors.New("usage: qos mode <1|2>")
			}
			m, err := atoi(args[1])
			if err != nil || (m != 1 && m != 2) {
				return errors.New("mode must be 1 (port based) or 2 (802.1p/DSCP)")
			}
			return s.SetQoSMode(ctx, byte(m))
		case "priority":
			if len(args) < 3 {
				return errors.New("usage: qos priority <ports> <1-4>")
			}
			pl, err := ports(args[1])
			if err != nil {
				return err
			}
			p, err := atoi(args[2])
			if err != nil || p < 1 || p > 4 {
				return errors.New("priority must be 1 (high) to 4 (low)")
			}
			return s.SetPortPriority(ctx, pl, byte(p))
		case "rate":
			if len(args) < 4 {
				return errors.New("usage: qos rate <ports> <ingress|-> <egress|->")
			}
			pl, err := ports(args[1])
			if err != nil {
				return err
			}
			var in, eg *uint16
			if args[2] != "-" {
				v, err := atoi(args[2])
				if err != nil {
					return err
				}
				x := uint16(v)
				in = &x
			}
			if args[3] != "-" {
				v, err := atoi(args[3])
				if err != nil {
					return err
				}
				x := uint16(v)
				eg = &x
			}
			return s.SetRateLimit(ctx, pl, in, eg)
		case "broadcast":
			if len(args) < 2 {
				return errors.New("usage: qos broadcast <on|off> [ports rate]")
			}
			on, err := onOff(args[1])
			if err != nil {
				return err
			}
			if err := s.SetBroadcastFilter(ctx, on); err != nil {
				return err
			}
			if len(args) >= 4 {
				pl, err := ports(args[2])
				if err != nil {
					return err
				}
				r, err := atoi(args[3])
				if err != nil {
					return err
				}
				return s.SetStormRate(ctx, pl, uint16(r))
			}
			return nil
		}
		return fmt.Errorf("unknown qos subcommand %q", sub)
	case "multicast":
		if sub == "set" {
			if len(args) < 3 {
				return errors.New("usage: multicast set <on|off> <vid> [v3validate on|off] [block on|off]")
			}
			m, err := s.MulticastConfig(ctx)
			if err != nil {
				return err
			}
			if m.Snooping, err = onOff(args[1]); err != nil {
				return err
			}
			if m.VID, err = atoi(args[2]); err != nil {
				return err
			}
			for i := 3; i+1 < len(args); i += 2 {
				v, err := onOff(args[i+1])
				if err != nil {
					return err
				}
				switch args[i] {
				case "v3validate":
					m.ValidateIGMPv3 = v
				case "block":
					m.BlockUnknown = v
				}
			}
			return s.SetMulticast(ctx, m)
		}
		m, err := s.MulticastConfig(ctx)
		if err != nil {
			return err
		}
		return out(m, func() {
			fmt.Printf("IGMP snooping: %v (VLAN %d)\nValidate IGMPv3 header: %v\nBlock unknown multicast: %v\n", m.Snooping, m.VID, m.ValidateIGMPv3, m.BlockUnknown)
			if m.RouterPortsSupp {
				fmt.Printf("Static router ports: %s\n", nsdp.PortList(m.RouterPorts))
			}
		})
	case "lag":
		if sub == "set" {
			if len(args) < 4 {
				return errors.New("usage: lag set <id> <on|off> <ports>")
			}
			id, err := atoi(args[1])
			if err != nil {
				return err
			}
			on, err := onOff(args[2])
			if err != nil {
				return err
			}
			pl, err := ports(args[3])
			if err != nil {
				return err
			}
			return s.SetLAG(ctx, nsdp.LAG{ID: id, Enabled: on, Ports: pl})
		}
		lags, err := s.LAGs(ctx)
		if err != nil {
			return err
		}
		return out(lags, func() {
			fmt.Printf("%-4s %-8s %s\n", "LAG", "ADMIN", "PORTS")
			for _, l := range lags {
				adm := "Disable"
				if l.Enabled {
					adm = "Enable"
				}
				fmt.Printf("%-4d %-8s %s\n", l.ID, adm, nsdp.PortList(l.Ports))
			}
		})
	case "mirror":
		if sub == "set" {
			if len(args) < 2 {
				return errors.New("usage: mirror set <dst|0> [sources]")
			}
			dst, err := atoi(args[1])
			if err != nil {
				return err
			}
			var src []int
			if len(args) > 2 {
				if src, err = ports(args[2]); err != nil {
					return err
				}
			}
			return s.SetMirror(ctx, dst, src)
		}
		m, err := s.MirrorConfig(ctx)
		if err != nil {
			return err
		}
		return out(m, func() {
			if m.Dest == 0 {
				fmt.Println("Mirroring: disabled")
			} else {
				fmt.Printf("Mirroring: ports %s -> port %d\n", nsdp.PortList(m.Sources), m.Dest)
			}
		})
	case "loop":
		if sub == "" {
			return errors.New("usage: loop <on|off>")
		}
		on, err := onOff(sub)
		if err != nil {
			return err
		}
		return s.SetLoopDetection(ctx, on)
	case "management":
		m, err := s.ManagementConfig(ctx)
		if err != nil {
			return err
		}
		return out(m, func() {
			fmt.Printf("Loop detection: %v\n", m.LoopDetection)
			if m.PowerSaving != nil {
				fmt.Printf("Power saving: %v\n", *m.PowerSaving)
			}
			if m.PortLED != nil {
				fmt.Printf("Port LEDs: %v\n", *m.PortLED)
			}
			if m.LoopPrevention != nil {
				fmt.Printf("Loop prevention: %v\n", *m.LoopPrevention)
			}
		})
	case "name":
		return s.SetName(ctx, strings.Join(args, " "))
	case "ip":
		if sub == "dhcp" {
			return s.SetIPSettings(ctx, api.IPSettings{DHCP: true})
		}
		if len(args) < 2 {
			return errors.New("usage: ip dhcp | ip <ip> <mask> [gateway]")
		}
		in := api.IPSettings{IP: args[0], Netmask: args[1]}
		if len(args) > 2 {
			in.Gateway = args[2]
		}
		return s.SetIPSettings(ctx, in)
	case "password":
		if sub == "" {
			return errors.New("usage: password <new>")
		}
		return s.ChangePassword(ctx, g.password, sub)
	case "reboot":
		if !confirm(g, "Reboot the switch?") {
			return nil
		}
		return s.Reboot(ctx)
	case "factory-reset":
		if !confirm(g, "Restore FACTORY DEFAULTS? All configuration will be lost.") {
			return nil
		}
		return s.FactoryReset(ctx)
	case "firmware":
		if sub == "" {
			return errors.New("usage: firmware <file>")
		}
		if !confirm(g, "Flash "+sub+" to the switch? Do not power off during the upgrade.") {
			return nil
		}
		return s.UpgradeFirmware(ctx, sub, func(sent, total int) { fmt.Printf("\r%d / %d bytes", sent, total) })
	case "save":
		if sub == "" {
			return errors.New("usage: save <file.json>")
		}
		cfg, err := s.SaveConfig(ctx)
		if err != nil {
			return err
		}
		if err := api.WriteConfigFile(sub, cfg); err != nil {
			return err
		}
		fmt.Println("saved", sub)
		return nil
	case "restore":
		if sub == "" {
			return errors.New("usage: restore <file.json>")
		}
		cfg, err := api.ReadConfigFile(sub)
		if err != nil {
			return err
		}
		if !confirm(g, fmt.Sprintf("Apply configuration from %s (saved %s for %s)?", sub, cfg.SavedAt.Format(time.RFC822), cfg.Model)) {
			return nil
		}
		return s.RestoreConfig(ctx, cfg, func(m string) { fmt.Println(" ", m) })
	case "raw":
		if sub == "" {
			return errors.New("usage: raw <hextag,hextag,...>")
		}
		var tags []nsdp.Tag
		for _, t := range strings.Split(sub, ",") {
			v, err := strconv.ParseUint(strings.TrimPrefix(t, "0x"), 16, 16)
			if err != nil {
				return err
			}
			tags = append(tags, nsdp.Tag(v))
		}
		p, err := s.Client.Read(ctx, client.Target{IP: s.Device.IP, MAC: s.Device.MAC}, tags...)
		if err != nil {
			return err
		}
		fmt.Printf("result=0x%04x\n", p.Result)
		for _, t := range p.TLVs {
			fmt.Printf("%04x %-24s len=%-3d %x\n", uint16(t.Tag), t.Tag, len(t.Value), t.Value)
		}
		return nil
	}
	usage()
	return fmt.Errorf("unknown command %q", cmd)
}

func runServe(ctx context.Context, cli *client.Client, args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	listen := fs.String("listen", "127.0.0.1:0", "address to listen on")
	token := fs.String("token", os.Getenv("PROSAFE_TOKEN"), "require this X-Auth-Token header")
	fs.Parse(args)
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: server.New(cli, *token).Handler()}
	// The desktop app reads this line to find the port.
	fmt.Printf("PROSAFE_LISTEN=http://%s\n", ln.Addr())
	os.Stdout.Sync()
	go func() { <-ctx.Done(); srv.Close() }()
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func runSim(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("sim", flag.ExitOnError)
	listen := fs.String("listen", ":63322", "UDP address to listen on")
	fs.Parse(args)
	sw := sim.NewXS708E()
	sw.Log = func(f string, a ...any) { fmt.Fprintf(os.Stderr, "[sim] "+f+"\n", a...) }
	fmt.Fprintf(os.Stderr, "simulating %s on %s\n", sw, *listen)
	go sw.RunTicker(ctx, time.Second)
	return sw.Serve(ctx, *listen)
}
