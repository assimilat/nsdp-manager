// Package tui is the interactive terminal interface. It mirrors the pages of
// the ProSAFE Plus Configuration Utility: a device list and login, then a
// tabbed dashboard (Info, Status, Statistics, VLAN, QoS, LAG, Multicast,
// Management, Mirror, Cable Test, Maintenance).
package tui

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"prosafe/internal/api"
	"prosafe/internal/client"
	"prosafe/internal/nsdp"
)

// Options configure the TUI.
type Options struct {
	Client   *client.Client
	Password string
	IP       string
}

// Run starts the interface and blocks until the user quits.
func RunTUI(opts Options) error {
	m := newModel(opts)
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err := p.Run()
	return err
}

// Run is the entry point used by the CLI.
var Run = RunTUI

// ---- styling ---------------------------------------------------------------

var (
	netgearPurple = lipgloss.Color("#5b2d8e")
	accent        = lipgloss.Color("#c77dff")
	good          = lipgloss.Color("#3ecf8e")
	bad           = lipgloss.Color("#ff6b6b")
	dim           = lipgloss.Color("#8a8a8a")
	fg            = lipgloss.Color("#e8e8e8")

	titleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#ffffff")).Background(netgearPurple).Padding(0, 2)
	tabActive   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#ffffff")).Background(netgearPurple).Padding(0, 1)
	tabInactive = lipgloss.NewStyle().Foreground(dim).Padding(0, 1)
	panelStyle  = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(netgearPurple).Padding(0, 1)
	keyStyle    = lipgloss.NewStyle().Foreground(accent).Bold(true)
	hdrStyle    = lipgloss.NewStyle().Foreground(accent).Bold(true)
	footerStyle = lipgloss.NewStyle().Foreground(dim)
	goodStyle   = lipgloss.NewStyle().Foreground(good)
	badStyle    = lipgloss.NewStyle().Foreground(bad)
	selStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#ffffff")).Background(lipgloss.Color("#3a3a5a"))
)

// ---- screens ---------------------------------------------------------------

type screen int

const (
	scrDevices screen = iota
	scrLogin
	scrDash
)

type page int

const (
	pgInfo page = iota
	pgStatus
	pgStats
	pgVLAN
	pgQoS
	pgLAG
	pgMulticast
	pgManagement
	pgMirror
	pgCable
	pgMaint
	pgCount
)

var pageNames = []string{"Info", "Status", "Statistics", "VLAN", "QoS", "LAG", "Multicast", "Management", "Mirror", "Cable", "Maintenance"}

// ---- model -----------------------------------------------------------------

type model struct {
	opts   Options
	screen screen
	w, h   int
	sp     spinner.Model
	busy   bool
	status string
	err    string

	devices []client.Device
	dcursor int

	input       textinput.Model
	inputActive bool
	inputPrompt string
	inputAction func(string) tea.Cmd
	confirm     func() tea.Cmd
	confirmText string

	session *api.Session
	page    page
	rows    []string // rendered content lines for the active page
	pcursor int      // selection within a page (ports, vlans, lags)

	// cached page data
	info   api.Info
	ports  []nsdp.PortStatus
	stats  []nsdp.PortStats
	vlan   api.VLANConfig
	qos    api.QoSConfig
	lags   []nsdp.LAG
	mcast  api.Multicast
	mgmt   api.Management
	mirror nsdp.Mirror
	cable  []nsdp.CableResult
	lagOK  bool
}

func newModel(opts Options) *model {
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(accent)
	ti := textinput.New()
	ti.CharLimit = 64
	return &model{opts: opts, screen: scrDevices, sp: sp, input: ti}
}

func (m *model) Init() tea.Cmd {
	return tea.Batch(m.sp.Tick, m.discover())
}

// ---- messages --------------------------------------------------------------

type devicesMsg struct {
	devs []client.Device
	err  error
}
type loginMsg struct{ err error }
type pageMsg struct {
	p   page
	err error
}
type actionMsg struct {
	note string
	err  error
}

func timeout() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 30*time.Second)
}

func (m *model) discover() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := timeout()
		defer cancel()
		devs, err := m.opts.Client.Discover(ctx, 3*time.Second)
		return devicesMsg{devs, err}
	}
}

func (m *model) doLogin(d client.Device, password string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := timeout()
		defer cancel()
		s := api.NewSession(m.opts.Client, d, password)
		if err := s.Login(ctx); err != nil {
			return loginMsg{err}
		}
		m.session = s
		return loginMsg{nil}
	}
}

// loadPage fetches the data for page p.
func (m *model) loadPage(p page) tea.Cmd {
	s := m.session
	return func() tea.Msg {
		ctx, cancel := timeout()
		defer cancel()
		var err error
		switch p {
		case pgInfo:
			m.info, err = s.Refresh(ctx)
		case pgStatus:
			m.ports, err = s.PortStatuses(ctx)
		case pgStats:
			m.stats, err = s.PortStatistics(ctx)
		case pgVLAN:
			m.vlan, err = s.VLANs(ctx)
		case pgQoS:
			m.qos, err = s.QoS(ctx)
		case pgLAG:
			m.lags, err = s.LAGs(ctx)
			m.lagOK = err == nil
			if err == api.ErrUnsupported {
				err = nil
			}
		case pgMulticast:
			m.mcast, err = s.MulticastConfig(ctx)
		case pgManagement:
			m.mgmt, err = s.ManagementConfig(ctx)
		case pgMirror:
			m.mirror, err = s.MirrorConfig(ctx)
			if err == api.ErrUnsupported {
				err = nil
			}
		case pgCable, pgMaint:
			// no fetch; interactive
		}
		return pageMsg{p, err}
	}
}

// ---- update ----------------------------------------------------------------

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		return m, nil
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.sp, cmd = m.sp.Update(msg)
		return m, cmd
	case devicesMsg:
		m.busy = false
		if msg.err != nil {
			m.err = msg.err.Error()
		} else {
			m.devices = msg.devs
			m.err = ""
			if m.dcursor >= len(m.devices) {
				m.dcursor = 0
			}
		}
		return m, nil
	case loginMsg:
		m.busy = false
		if msg.err != nil {
			m.err = "login failed: " + msg.err.Error()
			m.screen = scrDevices
			return m, nil
		}
		m.err = ""
		m.screen = scrDash
		m.page = pgInfo
		m.busy = true
		return m, m.loadPage(pgInfo)
	case pageMsg:
		m.busy = false
		if msg.err != nil {
			m.err = msg.err.Error()
		} else {
			m.err = ""
		}
		m.pcursor = 0
		return m, nil
	case actionMsg:
		m.busy = false
		if msg.err != nil {
			m.err = msg.err.Error()
		} else {
			m.status = msg.note
			m.err = ""
		}
		if m.screen == scrDash {
			m.busy = true
			return m, m.loadPage(m.page)
		}
		return m, nil
	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m *model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// modal input takes precedence
	if m.inputActive {
		switch msg.Type {
		case tea.KeyEsc:
			m.inputActive = false
			return m, nil
		case tea.KeyEnter:
			m.inputActive = false
			val := m.input.Value()
			if m.inputAction != nil {
				m.busy = true
				return m, m.inputAction(val)
			}
			return m, nil
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}
	// confirm dialog
	if m.confirm != nil {
		switch msg.String() {
		case "y", "Y":
			c := m.confirm
			m.confirm = nil
			m.busy = true
			return m, c()
		default:
			m.confirm = nil
			m.status = "cancelled"
			return m, nil
		}
	}

	switch msg.String() {
	case "ctrl+c", "q":
		if m.screen == scrDash {
			m.screen = scrDevices
			m.session = nil
			return m, nil
		}
		return m, tea.Quit
	}

	switch m.screen {
	case scrDevices:
		return m.keyDevices(msg)
	case scrLogin:
		return m.keyLogin(msg)
	case scrDash:
		return m.keyDash(msg)
	}
	return m, nil
}

func (m *model) keyDevices(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		if m.dcursor > 0 {
			m.dcursor--
		}
	case "down", "j":
		if m.dcursor < len(m.devices)-1 {
			m.dcursor++
		}
	case "r":
		m.busy = true
		return m, m.discover()
	case "a":
		m.promptInput("Add switch by IP address:", func(v string) tea.Cmd {
			return func() tea.Msg {
				ip := net.ParseIP(strings.TrimSpace(v))
				if ip == nil {
					return actionMsg{err: fmt.Errorf("invalid IP %q", v)}
				}
				ctx, cancel := timeout()
				defer cancel()
				s := api.NewSession(m.opts.Client, client.Device{IP: ip.To4(), IPString: ip.String(), MAC: make(net.HardwareAddr, 6)}, m.opts.Password)
				if _, err := s.Refresh(ctx); err != nil {
					return actionMsg{err: err}
				}
				m.devices = append(m.devices, s.Device)
				return actionMsg{note: "added " + s.Device.IPString}
			}
		})
		return m, textinput.Blink
	case "enter":
		if len(m.devices) == 0 {
			return m, nil
		}
		m.screen = scrLogin
		m.input.SetValue(m.opts.Password)
		m.input.EchoMode = textinput.EchoPassword
		m.input.Focus()
		return m, textinput.Blink
	}
	return m, nil
}

func (m *model) keyLogin(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		m.screen = scrDevices
		m.input.EchoMode = textinput.EchoNormal
		return m, nil
	case tea.KeyEnter:
		pw := m.input.Value()
		m.input.EchoMode = textinput.EchoNormal
		m.busy = true
		return m, m.doLogin(m.devices[m.dcursor], pw)
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m *model) keyDash(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "tab", "l", "right":
		m.page = (m.page + 1) % pgCount
		m.busy = true
		return m, m.loadPage(m.page)
	case "shift+tab", "h", "left":
		m.page = (m.page - 1 + pgCount) % pgCount
		m.busy = true
		return m, m.loadPage(m.page)
	case "r":
		m.busy = true
		return m, m.loadPage(m.page)
	case "up", "k":
		if m.pcursor > 0 {
			m.pcursor--
		}
		return m, nil
	case "down", "j":
		m.pcursor++
		return m, nil
	}
	if msg.String() >= "1" && msg.String() <= "9" {
		if n, _ := strconv.Atoi(msg.String()); n >= 1 && n <= int(pgCount) {
			m.page = page(n - 1)
			m.busy = true
			return m, m.loadPage(m.page)
		}
	}
	return m.pageAction(msg)
}

// promptInput opens the modal text input.
func (m *model) promptInput(prompt string, action func(string) tea.Cmd) {
	m.inputActive = true
	m.inputPrompt = prompt
	m.inputAction = action
	m.input.EchoMode = textinput.EchoNormal
	m.input.SetValue("")
	m.input.Focus()
}

func (m *model) askConfirm(text string, action func() tea.Cmd) {
	m.confirmText = text
	m.confirm = action
}

func note(s string) tea.Cmd {
	return func() tea.Msg { return actionMsg{note: s} }
}

// ---- per-page actions ------------------------------------------------------

func (m *model) act(fn func(ctx context.Context) (string, error)) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := timeout()
		defer cancel()
		n, err := fn(ctx)
		return actionMsg{note: n, err: err}
	}
}

func (m *model) pageAction(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := m.session
	if s == nil {
		return m, nil
	}
	key := msg.String()
	switch m.page {
	case pgInfo:
		switch key {
		case "n":
			m.promptInput("Switch name:", func(v string) tea.Cmd {
				return m.act(func(ctx context.Context) (string, error) { return "name set", s.SetName(ctx, v) })
			})
			return m, textinput.Blink
		case "i":
			m.promptInput("Static IP (ip mask [gw], or 'dhcp'):", func(v string) tea.Cmd {
				return m.act(func(ctx context.Context) (string, error) {
					f := strings.Fields(v)
					if len(f) == 1 && f[0] == "dhcp" {
						return "DHCP enabled", s.SetIPSettings(ctx, api.IPSettings{DHCP: true})
					}
					if len(f) < 2 {
						return "", fmt.Errorf("need: ip mask [gateway]")
					}
					in := api.IPSettings{IP: f[0], Netmask: f[1]}
					if len(f) > 2 {
						in.Gateway = f[2]
					}
					return "IP settings applied", s.SetIPSettings(ctx, in)
				})
			})
			return m, textinput.Blink
		}
	case pgStatus:
		if key == "f" && len(m.ports) > 0 {
			idx := clamp(m.pcursor, len(m.ports))
			p := m.ports[idx]
			nf := !p.FlowControl
			return m, m.act(func(ctx context.Context) (string, error) {
				return fmt.Sprintf("port %d flow control %v", p.Port, nf), s.SetPortAdmin(ctx, p.Port, admin(p.Speed), nf)
			})
		}
	case pgStats:
		if key == "c" {
			m.askConfirm("Clear all port counters?", func() tea.Cmd {
				return m.act(func(ctx context.Context) (string, error) { return "counters cleared", s.ClearCounters(ctx) })
			})
		}
	case pgVLAN:
		switch key {
		case "m":
			m.promptInput("VLAN mode 0=off 1=port-basic 2=port-adv 3=802.1Q-basic 4=802.1Q-adv:", func(v string) tea.Cmd {
				return m.act(func(ctx context.Context) (string, error) {
					n, err := strconv.Atoi(strings.TrimSpace(v))
					if err != nil || n < 0 || n > 4 {
						return "", fmt.Errorf("mode must be 0-4")
					}
					return "VLAN mode changed (membership reset)", s.SetVLANMode(ctx, byte(n))
				})
			})
			return m, textinput.Blink
		case "a":
			m.promptInput("Add/set 802.1Q VLAN: <vid> <members> [tagged]:", func(v string) tea.Cmd {
				return m.act(func(ctx context.Context) (string, error) {
					f := strings.Fields(v)
					if len(f) < 2 {
						return "", fmt.Errorf("need: vid members [tagged]")
					}
					vid, _ := strconv.Atoi(f[0])
					mem, err := nsdp.ParsePortList(f[1])
					if err != nil {
						return "", err
					}
					var tag []int
					if len(f) > 2 {
						tag, _ = nsdp.ParsePortList(f[2])
					}
					return fmt.Sprintf("VLAN %d set", vid), s.Set8021QVLAN(ctx, vid, mem, tag)
				})
			})
			return m, textinput.Blink
		case "p":
			m.promptInput("Port-based VLAN: <vid> <ports>:", func(v string) tea.Cmd {
				return m.act(func(ctx context.Context) (string, error) {
					f := strings.Fields(v)
					if len(f) < 2 {
						return "", fmt.Errorf("need: vid ports")
					}
					vid, _ := strconv.Atoi(f[0])
					pl, err := nsdp.ParsePortList(f[1])
					if err != nil {
						return "", err
					}
					return fmt.Sprintf("VLAN %d set", vid), s.SetPortVLAN(ctx, vid, pl)
				})
			})
			return m, textinput.Blink
		case "d":
			m.promptInput("Delete 802.1Q VLAN id:", func(v string) tea.Cmd {
				return m.act(func(ctx context.Context) (string, error) {
					vid, err := strconv.Atoi(strings.TrimSpace(v))
					if err != nil {
						return "", err
					}
					return fmt.Sprintf("VLAN %d deleted", vid), s.Delete8021QVLAN(ctx, vid)
				})
			})
			return m, textinput.Blink
		case "v":
			m.promptInput("Set PVID: <ports> <vid>:", func(v string) tea.Cmd {
				return m.act(func(ctx context.Context) (string, error) {
					f := strings.Fields(v)
					if len(f) < 2 {
						return "", fmt.Errorf("need: ports vid")
					}
					pl, err := nsdp.ParsePortList(f[0])
					if err != nil {
						return "", err
					}
					vid, _ := strconv.Atoi(f[1])
					for _, p := range pl {
						if err := s.SetPVID(ctx, p, vid); err != nil {
							return "", err
						}
					}
					return "PVID set", nil
				})
			})
			return m, textinput.Blink
		}
	case pgQoS:
		switch key {
		case "m":
			m.promptInput("QoS mode 1=port-based 2=802.1p/DSCP:", func(v string) tea.Cmd {
				return m.act(func(ctx context.Context) (string, error) {
					n, _ := strconv.Atoi(strings.TrimSpace(v))
					if n != 1 && n != 2 {
						return "", fmt.Errorf("mode must be 1 or 2")
					}
					return "QoS mode set", s.SetQoSMode(ctx, byte(n))
				})
			})
			return m, textinput.Blink
		case "p":
			m.promptInput("Priority: <ports> <1=high..4=low>:", func(v string) tea.Cmd {
				return m.act(func(ctx context.Context) (string, error) {
					f := strings.Fields(v)
					if len(f) < 2 {
						return "", fmt.Errorf("need: ports priority")
					}
					pl, err := nsdp.ParsePortList(f[0])
					if err != nil {
						return "", err
					}
					pr, _ := strconv.Atoi(f[1])
					return "priority set", s.SetPortPriority(ctx, pl, byte(pr))
				})
			})
			return m, textinput.Blink
		case "l":
			m.promptInput("Rate limit: <ports> <ingressCode|-> <egressCode|->  (codes: 0=none..11=512M):", func(v string) tea.Cmd {
				return m.act(func(ctx context.Context) (string, error) {
					f := strings.Fields(v)
					if len(f) < 3 {
						return "", fmt.Errorf("need: ports ingress egress")
					}
					pl, err := nsdp.ParsePortList(f[0])
					if err != nil {
						return "", err
					}
					in, eg := rateCode(f[1]), rateCode(f[2])
					return "rate limit set", s.SetRateLimit(ctx, pl, in, eg)
				})
			})
			return m, textinput.Blink
		case "b":
			return m, m.act(func(ctx context.Context) (string, error) {
				return "broadcast filter toggled", s.SetBroadcastFilter(ctx, !m.qos.BroadcastFilter)
			})
		}
	case pgLAG:
		if key == "e" && len(m.lags) > 0 {
			m.promptInput("Set LAG: <id> <on|off> <ports>:", func(v string) tea.Cmd {
				return m.act(func(ctx context.Context) (string, error) {
					f := strings.Fields(v)
					if len(f) < 3 {
						return "", fmt.Errorf("need: id on|off ports")
					}
					id, _ := strconv.Atoi(f[0])
					on := f[1] == "on" || f[1] == "enable"
					pl, err := nsdp.ParsePortList(f[2])
					if err != nil {
						return "", err
					}
					return fmt.Sprintf("LAG %d set", id), s.SetLAG(ctx, nsdp.LAG{ID: id, Enabled: on, Ports: pl})
				})
			})
			return m, textinput.Blink
		}
	case pgMulticast:
		switch key {
		case "s":
			return m, m.act(func(ctx context.Context) (string, error) {
				mc := m.mcast
				mc.Snooping = !mc.Snooping
				return "IGMP snooping toggled", s.SetMulticast(ctx, mc)
			})
		case "3":
			return m, m.act(func(ctx context.Context) (string, error) {
				mc := m.mcast
				mc.ValidateIGMPv3 = !mc.ValidateIGMPv3
				return "IGMPv3 validation toggled", s.SetMulticast(ctx, mc)
			})
		case "u":
			return m, m.act(func(ctx context.Context) (string, error) {
				mc := m.mcast
				mc.BlockUnknown = !mc.BlockUnknown
				return "block-unknown toggled", s.SetMulticast(ctx, mc)
			})
		case "v":
			m.promptInput("IGMP snooping VLAN id (1-4094):", func(v string) tea.Cmd {
				return m.act(func(ctx context.Context) (string, error) {
					n, err := strconv.Atoi(strings.TrimSpace(v))
					if err != nil {
						return "", err
					}
					mc := m.mcast
					mc.VID = n
					return "snooping VLAN set", s.SetMulticast(ctx, mc)
				})
			})
			return m, textinput.Blink
		}
	case pgManagement:
		if key == "d" {
			return m, m.act(func(ctx context.Context) (string, error) {
				return "loop detection toggled", s.SetLoopDetection(ctx, !m.mgmt.LoopDetection)
			})
		}
		if key == "p" && m.mgmt.PowerSaving != nil {
			return m, m.act(func(ctx context.Context) (string, error) {
				return "power saving toggled", s.SetPowerSaving(ctx, !*m.mgmt.PowerSaving)
			})
		}
	case pgMirror:
		if key == "s" {
			m.promptInput("Mirror: <dest|0> [sources]  (0 disables):", func(v string) tea.Cmd {
				return m.act(func(ctx context.Context) (string, error) {
					f := strings.Fields(v)
					if len(f) < 1 {
						return "", fmt.Errorf("need: dest [sources]")
					}
					dst, _ := strconv.Atoi(f[0])
					var src []int
					if len(f) > 1 {
						src, _ = nsdp.ParsePortList(f[1])
					}
					return "mirror set", s.SetMirror(ctx, dst, src)
				})
			})
			return m, textinput.Blink
		}
	case pgCable:
		if key == "t" {
			m.promptInput("Test cable on ports (e.g. 1,2,5-8):", func(v string) tea.Cmd {
				return func() tea.Msg {
					ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
					defer cancel()
					pl, err := nsdp.ParsePortList(v)
					if err != nil {
						return actionMsg{err: err}
					}
					res, err := s.CableTest(ctx, pl)
					m.cable = res
					if err != nil {
						return actionMsg{err: err}
					}
					return actionMsg{note: "cable test complete"}
				}
			})
			return m, textinput.Blink
		}
	case pgMaint:
		switch key {
		case "p":
			m.promptInput("New admin password:", func(v string) tea.Cmd {
				return m.act(func(ctx context.Context) (string, error) { return "password changed", s.ChangePassword(ctx, s.Password, v) })
			})
			return m, textinput.Blink
		case "b":
			m.askConfirm("Reboot the switch?", func() tea.Cmd {
				return m.act(func(ctx context.Context) (string, error) { return "reboot requested", s.Reboot(ctx) })
			})
		case "f":
			m.askConfirm("FACTORY RESET - erase all config?", func() tea.Cmd {
				return m.act(func(ctx context.Context) (string, error) { return "factory reset requested", s.FactoryReset(ctx) })
			})
		case "s":
			m.promptInput("Save config to file path:", func(v string) tea.Cmd {
				return m.act(func(ctx context.Context) (string, error) {
					cfg, err := s.SaveConfig(ctx)
					if err != nil {
						return "", err
					}
					return "saved " + v, api.WriteConfigFile(strings.TrimSpace(v), cfg)
				})
			})
			return m, textinput.Blink
		case "u":
			m.promptInput("Firmware image path to flash:", func(v string) tea.Cmd {
				path := strings.TrimSpace(v)
				m.askConfirm("Flash "+path+"? Do not power off.", func() tea.Cmd {
					return m.act(func(ctx context.Context) (string, error) {
						return "firmware uploaded", s.UpgradeFirmware(ctx, path, nil)
					})
				})
				return nil
			})
			return m, textinput.Blink
		}
	}
	return m, nil
}

func clamp(i, n int) int {
	if n == 0 {
		return 0
	}
	if i < 0 {
		return 0
	}
	if i >= n {
		return n - 1
	}
	return i
}

func admin(linkSpeed byte) byte { return nsdp.AdminAuto } // keep speed auto when toggling flow

func rateCode(s string) *uint16 {
	if s == "-" {
		return nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return nil
	}
	v := uint16(n)
	return &v
}

// ---- view ------------------------------------------------------------------

func (m *model) View() string {
	switch m.screen {
	case scrDevices:
		return m.viewDevices()
	case scrLogin:
		return m.viewLogin()
	case scrDash:
		return m.viewDash()
	}
	return ""
}

func (m *model) header(right string) string {
	left := titleStyle.Render(" ProSAFE Plus ")
	gap := m.w - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + lipgloss.NewStyle().Foreground(accent).Render(right)
}

func (m *model) footer(keys string) string {
	status := ""
	switch {
	case m.busy:
		status = m.sp.View() + " working…"
	case m.err != "":
		status = badStyle.Render("✗ " + m.err)
	case m.status != "":
		status = goodStyle.Render("✓ " + m.status)
	}
	return footerStyle.Render(keys) + "\n" + status
}

func (m *model) modalOverlay(body string) string {
	if m.inputActive {
		box := panelStyle.Render(m.inputPrompt + "\n" + m.input.View() + "\n" + footerStyle.Render("enter confirm · esc cancel"))
		return body + "\n\n" + box
	}
	if m.confirm != nil {
		box := panelStyle.BorderForeground(bad).Render(m.confirmText + "\n" + footerStyle.Render("y confirm · any other key cancel"))
		return body + "\n\n" + box
	}
	return body
}

func (m *model) viewDevices() string {
	var b strings.Builder
	b.WriteString(m.header("discovery"))
	b.WriteString("\n\n")
	b.WriteString(hdrStyle.Render(fmt.Sprintf("  %-10s %-16s %-18s %-16s %-9s %s", "MODEL", "NAME", "MAC", "IP", "FIRMWARE", "AUTH")))
	b.WriteString("\n")
	if len(m.devices) == 0 && !m.busy {
		b.WriteString(footerStyle.Render("\n  No switches found. Press r to rescan, a to add by IP.\n  (needs UDP 63321/63322 on the switch's subnet)\n"))
	}
	for i, d := range m.devices {
		row := fmt.Sprintf("  %-10s %-16s %-18s %-16s %-9s %s", trunc(d.Model, 10), trunc(d.Name, 16), d.MACString, d.IPString, d.Firmware, d.PasswordMode)
		if i == m.dcursor {
			row = selStyle.Render(row)
		}
		b.WriteString(row + "\n")
	}
	b.WriteString("\n")
	b.WriteString(m.footer("↑/↓ select · enter login · r rescan · a add IP · q quit"))
	return m.modalOverlay(b.String())
}

func (m *model) viewLogin() string {
	d := m.devices[m.dcursor]
	body := m.header("login") + "\n\n" +
		panelStyle.Render(fmt.Sprintf("Log in to %s at %s\n(%s)\n\nPassword: %s\n\n%s",
			d.Model, d.IPString, d.MACString, m.input.View(), footerStyle.Render("enter · esc back"))) +
		"\n" + footerStyle.Render("default password is 'password'")
	return body
}

func (m *model) viewDash() string {
	var b strings.Builder
	b.WriteString(m.header(m.info.Model + " · " + m.session.Device.IPString))
	b.WriteString("\n")
	// tab bar
	var tabs []string
	for i, name := range pageNames {
		label := fmt.Sprintf("%d %s", i+1, name)
		if page(i) == m.page {
			tabs = append(tabs, tabActive.Render(label))
		} else {
			tabs = append(tabs, tabInactive.Render(label))
		}
	}
	b.WriteString(wrapTabs(tabs, m.w))
	b.WriteString("\n")
	content := m.renderPage()
	b.WriteString(panelStyle.Width(max(m.w-2, 20)).Render(content))
	b.WriteString("\n")
	b.WriteString(m.footer(m.pageKeys() + " · tab/1-9 pages · r refresh · q back"))
	return m.modalOverlay(b.String())
}

func (m *model) pageKeys() string {
	switch m.page {
	case pgInfo:
		return keyStyle.Render("n") + " name  " + keyStyle.Render("i") + " ip"
	case pgStatus:
		return "↑/↓ port  " + keyStyle.Render("f") + " toggle flow control"
	case pgStats:
		return keyStyle.Render("c") + " clear counters"
	case pgVLAN:
		return keyStyle.Render("m") + " mode  " + keyStyle.Render("a") + " add-Q  " + keyStyle.Render("p") + " port-VLAN  " + keyStyle.Render("d") + " del  " + keyStyle.Render("v") + " pvid"
	case pgQoS:
		return keyStyle.Render("m") + " mode  " + keyStyle.Render("p") + " priority  " + keyStyle.Render("l") + " rate  " + keyStyle.Render("b") + " broadcast"
	case pgLAG:
		return keyStyle.Render("e") + " edit LAG"
	case pgMulticast:
		return keyStyle.Render("s") + " snooping  " + keyStyle.Render("v") + " vlan  " + keyStyle.Render("3") + " igmpv3  " + keyStyle.Render("u") + " block-unknown"
	case pgManagement:
		return keyStyle.Render("d") + " loop detect  " + keyStyle.Render("p") + " power saving"
	case pgMirror:
		return keyStyle.Render("s") + " set mirror"
	case pgCable:
		return keyStyle.Render("t") + " test ports"
	case pgMaint:
		return keyStyle.Render("p") + " passwd  " + keyStyle.Render("s") + " save  " + keyStyle.Render("u") + " firmware  " + keyStyle.Render("b") + " reboot  " + keyStyle.Render("f") + " factory-reset"
	}
	return ""
}

func (m *model) renderPage() string {
	switch m.page {
	case pgInfo:
		i := m.info
		return field("Product", i.Model) + field("Name", or(i.Name, "(unset)")) + field("MAC", i.MAC) +
			field("Serial", i.Serial) + field("Firmware", fmt.Sprintf("%s (image %d)", i.Firmware, i.ActiveImage)) +
			field("DHCP", yn(i.DHCP)) + field("IP", i.IP) + field("Netmask", i.Netmask) + field("Gateway", i.Gateway) +
			field("Ports", strconv.Itoa(i.Ports)) + field("Auth mode", i.PasswordMode)
	case pgStatus:
		var b strings.Builder
		b.WriteString(hdrStyle.Render(fmt.Sprintf("%-6s %-8s %-12s %s", "PORT", "LINK", "SPEED", "FLOW")) + "\n")
		for i, p := range m.ports {
			link := badStyle.Render("Down")
			if p.Up() {
				link = goodStyle.Render("Up")
			}
			row := fmt.Sprintf("%-6d %-17s %-12s %s", p.Port, link, p.SpeedName(), yn(p.FlowControl))
			if i == clamp(m.pcursor, len(m.ports)) {
				row = "› " + row
			} else {
				row = "  " + row
			}
			b.WriteString(row + "\n")
		}
		return b.String()
	case pgStats:
		var b strings.Builder
		b.WriteString(hdrStyle.Render(fmt.Sprintf("%-6s %18s %18s %12s", "PORT", "RX BYTES", "TX BYTES", "CRC ERR")) + "\n")
		for _, p := range m.stats {
			b.WriteString(fmt.Sprintf("%-6d %18d %18d %12d\n", p.Port, p.RxBytes, p.TxBytes, p.CRCErrors))
		}
		return b.String()
	case pgVLAN:
		var b strings.Builder
		b.WriteString(field("Mode", m.vlan.ModeName))
		if m.vlan.MaxVLANs > 0 {
			b.WriteString(field("Max 802.1Q", strconv.Itoa(m.vlan.MaxVLANs)))
		}
		b.WriteString("\n")
		for _, v := range m.vlan.Port {
			b.WriteString(fmt.Sprintf("  VLAN %-5d ports %s\n", v.VID, nsdp.PortList(v.Ports)))
		}
		for _, v := range m.vlan.Q {
			b.WriteString(fmt.Sprintf("  VLAN %-5d untagged %-14s tagged %s\n", v.VID, nsdp.PortList(v.Untagged()), nsdp.PortList(v.Tagged)))
		}
		if len(m.vlan.PVIDs) > 0 {
			var ps []string
			for _, p := range m.vlan.PVIDs {
				ps = append(ps, fmt.Sprintf("%d→%d", p.Port, p.VID))
			}
			b.WriteString("\n  PVID: " + strings.Join(ps, "  ") + "\n")
		}
		if len(m.vlan.Port) == 0 && len(m.vlan.Q) == 0 {
			b.WriteString(footerStyle.Render("  VLANs are disabled; press m to pick a mode.\n"))
		}
		return b.String()
	case pgQoS:
		var b strings.Builder
		b.WriteString(field("Mode", m.qos.ModeName) + field("Broadcast filter", yn(m.qos.BroadcastFilter)) + "\n")
		b.WriteString(hdrStyle.Render(fmt.Sprintf("%-6s %-8s %-11s %-11s %s", "PORT", "PRIO", "INGRESS", "EGRESS", "STORM")) + "\n")
		rate := func(rs []nsdp.PortRate, port int) string {
			for _, r := range rs {
				if r.Port == port {
					return nsdp.RateName(r.Rate)
				}
			}
			return "-"
		}
		n := m.session.Ports()
		for i := 1; i <= n; i++ {
			prio := "-"
			for _, p := range m.qos.Priorities {
				if p.Port == i {
					prio = nsdp.PriorityName(p.Priority)
				}
			}
			b.WriteString(fmt.Sprintf("%-6d %-8s %-11s %-11s %s\n", i, prio, rate(m.qos.Ingress, i), rate(m.qos.Egress, i), rate(m.qos.StormRates, i)))
		}
		return b.String()
	case pgLAG:
		if !m.lagOK {
			return footerStyle.Render("This switch does not support LAG.")
		}
		var b strings.Builder
		b.WriteString(hdrStyle.Render(fmt.Sprintf("%-5s %-9s %s", "LAG", "ADMIN", "PORTS")) + "\n")
		for _, l := range m.lags {
			adm := "Disable"
			if l.Enabled {
				adm = goodStyle.Render("Enable ")
			}
			b.WriteString(fmt.Sprintf("%-5d %-9s %s\n", l.ID, adm, nsdp.PortList(l.Ports)))
		}
		b.WriteString(footerStyle.Render("\nStatic aggregation only (no LACP on this model).\n"))
		return b.String()
	case pgMulticast:
		mc := m.mcast
		s := field("IGMP snooping", yn(mc.Snooping)) + field("Snooping VLAN", strconv.Itoa(mc.VID)) +
			field("Validate IGMPv3", yn(mc.ValidateIGMPv3)) + field("Block unknown mcast", yn(mc.BlockUnknown))
		if mc.RouterPortsSupp {
			s += field("Static router ports", nsdp.PortList(mc.RouterPorts))
		}
		return s
	case pgManagement:
		s := field("Loop detection", yn(m.mgmt.LoopDetection))
		if m.mgmt.PowerSaving != nil {
			s += field("Power saving", yn(*m.mgmt.PowerSaving))
		}
		if m.mgmt.PortLED != nil {
			s += field("Port LEDs", yn(*m.mgmt.PortLED))
		}
		if m.mgmt.LoopPrevention != nil {
			s += field("Loop prevention", yn(*m.mgmt.LoopPrevention))
		}
		return s
	case pgMirror:
		if m.mirror.Dest == 0 {
			return field("Mirroring", "disabled")
		}
		return field("Destination port", strconv.Itoa(m.mirror.Dest)) + field("Source ports", nsdp.PortList(m.mirror.Sources))
	case pgCable:
		var b strings.Builder
		b.WriteString("Press t to test ports.\n\n")
		if len(m.cable) > 0 {
			b.WriteString(hdrStyle.Render(fmt.Sprintf("%-6s %-16s %s", "PORT", "RESULT", "FAULT (m)")) + "\n")
			for _, c := range m.cable {
				b.WriteString(fmt.Sprintf("%-6d %-16s %d\n", c.Port, c.StatusName(), c.Distance))
			}
		}
		return b.String()
	case pgMaint:
		i := m.info
		return field("Firmware", i.Firmware) + field("Serial", i.Serial) + "\n" +
			footerStyle.Render("p change password · s save config · u upgrade firmware\nb reboot · f factory reset")
	}
	return ""
}

// ---- small helpers ---------------------------------------------------------

func field(k, v string) string {
	return lipgloss.NewStyle().Foreground(dim).Render(fmt.Sprintf("%-20s", k+":")) + lipgloss.NewStyle().Foreground(fg).Render(v) + "\n"
}
func yn(b bool) string {
	if b {
		return goodStyle.Render("Enabled")
	}
	return "Disabled"
}
func or(s, alt string) string {
	if s == "" {
		return alt
	}
	return s
}
func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
func wrapTabs(tabs []string, width int) string {
	var lines []string
	cur := ""
	for _, t := range tabs {
		if lipgloss.Width(cur)+lipgloss.Width(t)+1 > width && cur != "" {
			lines = append(lines, cur)
			cur = ""
		}
		cur += t + " "
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return strings.Join(lines, "\n")
}
