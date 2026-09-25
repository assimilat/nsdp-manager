// Package server exposes the api package over a small local JSON HTTP API.
// It is what the Tauri desktop app talks to (the Go binary runs as a sidecar).
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"prosafe/internal/api"
	"prosafe/internal/client"
	"prosafe/internal/nsdp"
)

// Server holds the client and per-switch sessions.
type Server struct {
	cli      *client.Client
	mu       sync.Mutex
	devices  map[string]client.Device // by MAC
	sessions map[string]*api.Session  // by MAC
	token    string
}

// New creates a server. token, when non-empty, must be sent as X-Auth-Token.
func New(cli *client.Client, token string) *Server {
	return &Server{cli: cli, devices: map[string]client.Device{}, sessions: map[string]*api.Session{}, token: token}
}

// Handler returns the HTTP routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.wrap(func(r *http.Request) (any, error) { return map[string]any{"ok": true}, nil }))
	mux.HandleFunc("POST /api/discover", s.wrap(s.discover))
	mux.HandleFunc("GET /api/devices", s.wrap(func(r *http.Request) (any, error) { return s.deviceList(), nil }))
	mux.HandleFunc("POST /api/devices/add", s.wrap(s.addDevice))
	mux.HandleFunc("POST /api/switch/{mac}/login", s.wrap(s.login))
	mux.HandleFunc("GET /api/switch/{mac}/info", s.session(func(ctx context.Context, ss *api.Session, r *http.Request) (any, error) { return ss.Refresh(ctx) }))
	mux.HandleFunc("POST /api/switch/{mac}/name", s.session(func(ctx context.Context, ss *api.Session, r *http.Request) (any, error) {
		var in struct{ Name string }
		if err := dec(r, &in); err != nil {
			return nil, err
		}
		return ok(ss.SetName(ctx, in.Name))
	}))
	mux.HandleFunc("POST /api/switch/{mac}/ip", s.session(func(ctx context.Context, ss *api.Session, r *http.Request) (any, error) {
		var in api.IPSettings
		if err := dec(r, &in); err != nil {
			return nil, err
		}
		return ok(ss.SetIPSettings(ctx, in))
	}))
	mux.HandleFunc("GET /api/switch/{mac}/ports", s.session(func(ctx context.Context, ss *api.Session, r *http.Request) (any, error) {
		st, err := ss.PortStatuses(ctx)
		if err != nil {
			return nil, err
		}
		type row struct {
			nsdp.PortStatus
			SpeedName string `json:"speed_name"`
			Up        bool   `json:"up"`
		}
		var rows []row
		for _, p := range st {
			rows = append(rows, row{p, p.SpeedName(), p.Up()})
		}
		admin, aerr := ss.PortAdminSettings(ctx)
		return map[string]any{"ports": rows, "admin": admin, "admin_supported": aerr == nil, "admin_speeds": nsdp.AdminSpeedNames()}, nil
	}))
	mux.HandleFunc("POST /api/switch/{mac}/ports/admin", s.session(func(ctx context.Context, ss *api.Session, r *http.Request) (any, error) {
		var in struct {
			Port        int
			Speed       byte
			FlowControl bool `json:"flow_control"`
		}
		if err := dec(r, &in); err != nil {
			return nil, err
		}
		return ok(ss.SetPortAdmin(ctx, in.Port, in.Speed, in.FlowControl))
	}))
	mux.HandleFunc("GET /api/switch/{mac}/stats", s.session(func(ctx context.Context, ss *api.Session, r *http.Request) (any, error) { return ss.PortStatistics(ctx) }))
	mux.HandleFunc("POST /api/switch/{mac}/stats/clear", s.session(func(ctx context.Context, ss *api.Session, r *http.Request) (any, error) { return ok(ss.ClearCounters(ctx)) }))
	mux.HandleFunc("POST /api/switch/{mac}/cabletest", s.session(func(ctx context.Context, ss *api.Session, r *http.Request) (any, error) {
		var in struct{ Ports []int }
		if err := dec(r, &in); err != nil {
			return nil, err
		}
		res, err := ss.CableTest(ctx, in.Ports)
		type row struct {
			nsdp.CableResult
			StatusName string `json:"status_name"`
		}
		var rows []row
		for _, c := range res {
			rows = append(rows, row{c, c.StatusName()})
		}
		if err != nil {
			return map[string]any{"results": rows, "error": err.Error()}, nil
		}
		return map[string]any{"results": rows}, nil
	}))
	mux.HandleFunc("GET /api/switch/{mac}/mirror", s.session(func(ctx context.Context, ss *api.Session, r *http.Request) (any, error) { return ss.MirrorConfig(ctx) }))
	mux.HandleFunc("POST /api/switch/{mac}/mirror", s.session(func(ctx context.Context, ss *api.Session, r *http.Request) (any, error) {
		var in nsdp.Mirror
		if err := dec(r, &in); err != nil {
			return nil, err
		}
		return ok(ss.SetMirror(ctx, in.Dest, in.Sources))
	}))
	mux.HandleFunc("GET /api/switch/{mac}/multicast", s.session(func(ctx context.Context, ss *api.Session, r *http.Request) (any, error) { return ss.MulticastConfig(ctx) }))
	mux.HandleFunc("POST /api/switch/{mac}/multicast", s.session(func(ctx context.Context, ss *api.Session, r *http.Request) (any, error) {
		var in api.Multicast
		if err := dec(r, &in); err != nil {
			return nil, err
		}
		return ok(ss.SetMulticast(ctx, in))
	}))
	mux.HandleFunc("GET /api/switch/{mac}/lags", s.session(func(ctx context.Context, ss *api.Session, r *http.Request) (any, error) {
		l, err := ss.LAGs(ctx)
		if errors.Is(err, api.ErrUnsupported) {
			return map[string]any{"supported": false, "lags": []nsdp.LAG{}}, nil
		}
		return map[string]any{"supported": err == nil, "lags": l}, err
	}))
	mux.HandleFunc("POST /api/switch/{mac}/lags", s.session(func(ctx context.Context, ss *api.Session, r *http.Request) (any, error) {
		var in nsdp.LAG
		if err := dec(r, &in); err != nil {
			return nil, err
		}
		return ok(ss.SetLAG(ctx, in))
	}))
	mux.HandleFunc("GET /api/switch/{mac}/management", s.session(func(ctx context.Context, ss *api.Session, r *http.Request) (any, error) { return ss.ManagementConfig(ctx) }))
	mux.HandleFunc("POST /api/switch/{mac}/management", s.session(func(ctx context.Context, ss *api.Session, r *http.Request) (any, error) {
		var in api.Management
		if err := dec(r, &in); err != nil {
			return nil, err
		}
		if err := ss.SetLoopDetection(ctx, in.LoopDetection); err != nil {
			return nil, err
		}
		if in.PowerSaving != nil {
			if err := ss.SetPowerSaving(ctx, *in.PowerSaving); err != nil {
				return nil, err
			}
		}
		if in.PortLED != nil {
			if err := ss.SetPortLED(ctx, *in.PortLED); err != nil {
				return nil, err
			}
		}
		if in.LoopPrevention != nil {
			if err := ss.SetLoopPrevention(ctx, *in.LoopPrevention); err != nil {
				return nil, err
			}
		}
		return ok(nil)
	}))
	mux.HandleFunc("GET /api/switch/{mac}/vlan", s.session(func(ctx context.Context, ss *api.Session, r *http.Request) (any, error) {
		cfg, err := ss.VLANs(ctx)
		if err != nil {
			return nil, err
		}
		return map[string]any{"config": cfg, "modes": nsdp.VLANModeNames()}, nil
	}))
	mux.HandleFunc("POST /api/switch/{mac}/vlan/mode", s.session(func(ctx context.Context, ss *api.Session, r *http.Request) (any, error) {
		var in struct{ Mode byte }
		if err := dec(r, &in); err != nil {
			return nil, err
		}
		return ok(ss.SetVLANMode(ctx, in.Mode))
	}))
	mux.HandleFunc("POST /api/switch/{mac}/vlan/port", s.session(func(ctx context.Context, ss *api.Session, r *http.Request) (any, error) {
		var in nsdp.PortVLAN
		if err := dec(r, &in); err != nil {
			return nil, err
		}
		return ok(ss.SetPortVLAN(ctx, in.VID, in.Ports))
	}))
	mux.HandleFunc("POST /api/switch/{mac}/vlan/8021q", s.session(func(ctx context.Context, ss *api.Session, r *http.Request) (any, error) {
		var in nsdp.VLAN8021Q
		if err := dec(r, &in); err != nil {
			return nil, err
		}
		return ok(ss.Set8021QVLAN(ctx, in.VID, in.Members, in.Tagged))
	}))
	mux.HandleFunc("POST /api/switch/{mac}/vlan/8021q/delete", s.session(func(ctx context.Context, ss *api.Session, r *http.Request) (any, error) {
		var in struct{ VID int }
		if err := dec(r, &in); err != nil {
			return nil, err
		}
		return ok(ss.Delete8021QVLAN(ctx, in.VID))
	}))
	mux.HandleFunc("POST /api/switch/{mac}/vlan/pvid", s.session(func(ctx context.Context, ss *api.Session, r *http.Request) (any, error) {
		var in struct {
			Ports []int
			VID   int
		}
		if err := dec(r, &in); err != nil {
			return nil, err
		}
		for _, p := range in.Ports {
			if err := ss.SetPVID(ctx, p, in.VID); err != nil {
				return nil, err
			}
		}
		return ok(nil)
	}))
	mux.HandleFunc("GET /api/switch/{mac}/qos", s.session(func(ctx context.Context, ss *api.Session, r *http.Request) (any, error) {
		cfg, err := ss.QoS(ctx)
		if err != nil {
			return nil, err
		}
		return map[string]any{"config": cfg, "rates": nsdp.RateNames(), "priorities": nsdp.PriorityNames()}, nil
	}))
	mux.HandleFunc("POST /api/switch/{mac}/qos/mode", s.session(func(ctx context.Context, ss *api.Session, r *http.Request) (any, error) {
		var in struct{ Mode byte }
		if err := dec(r, &in); err != nil {
			return nil, err
		}
		return ok(ss.SetQoSMode(ctx, in.Mode))
	}))
	mux.HandleFunc("POST /api/switch/{mac}/qos/priority", s.session(func(ctx context.Context, ss *api.Session, r *http.Request) (any, error) {
		var in struct {
			Ports    []int
			Priority byte
		}
		if err := dec(r, &in); err != nil {
			return nil, err
		}
		return ok(ss.SetPortPriority(ctx, in.Ports, in.Priority))
	}))
	mux.HandleFunc("POST /api/switch/{mac}/qos/rate", s.session(func(ctx context.Context, ss *api.Session, r *http.Request) (any, error) {
		var in struct {
			Ports   []int
			Ingress *uint16
			Egress  *uint16
		}
		if err := dec(r, &in); err != nil {
			return nil, err
		}
		return ok(ss.SetRateLimit(ctx, in.Ports, in.Ingress, in.Egress))
	}))
	mux.HandleFunc("POST /api/switch/{mac}/qos/broadcast", s.session(func(ctx context.Context, ss *api.Session, r *http.Request) (any, error) {
		var in struct {
			Enabled bool
			Ports   []int
			Rate    *uint16
		}
		if err := dec(r, &in); err != nil {
			return nil, err
		}
		if err := ss.SetBroadcastFilter(ctx, in.Enabled); err != nil {
			return nil, err
		}
		if in.Rate != nil && len(in.Ports) > 0 {
			return ok(ss.SetStormRate(ctx, in.Ports, *in.Rate))
		}
		return ok(nil)
	}))
	mux.HandleFunc("POST /api/switch/{mac}/password", s.session(func(ctx context.Context, ss *api.Session, r *http.Request) (any, error) {
		var in struct{ Old, New string }
		if err := dec(r, &in); err != nil {
			return nil, err
		}
		return ok(ss.ChangePassword(ctx, in.Old, in.New))
	}))
	mux.HandleFunc("POST /api/switch/{mac}/reboot", s.session(func(ctx context.Context, ss *api.Session, r *http.Request) (any, error) { return ok(ss.Reboot(ctx)) }))
	mux.HandleFunc("POST /api/switch/{mac}/factory-reset", s.session(func(ctx context.Context, ss *api.Session, r *http.Request) (any, error) { return ok(ss.FactoryReset(ctx)) }))
	mux.HandleFunc("POST /api/switch/{mac}/firmware", s.session(func(ctx context.Context, ss *api.Session, r *http.Request) (any, error) {
		var in struct{ Path string }
		if err := dec(r, &in); err != nil {
			return nil, err
		}
		var last int
		err := ss.UpgradeFirmware(ctx, in.Path, func(sent, total int) { last = sent })
		return map[string]any{"ok": err == nil, "sent": last, "error": errString(err)}, nil
	}))
	mux.HandleFunc("GET /api/switch/{mac}/config", s.session(func(ctx context.Context, ss *api.Session, r *http.Request) (any, error) { return ss.SaveConfig(ctx) }))
	mux.HandleFunc("POST /api/switch/{mac}/config/save", s.session(func(ctx context.Context, ss *api.Session, r *http.Request) (any, error) {
		var in struct{ Path string }
		if err := dec(r, &in); err != nil {
			return nil, err
		}
		cfg, err := ss.SaveConfig(ctx)
		if err != nil {
			return nil, err
		}
		return ok(api.WriteConfigFile(in.Path, cfg))
	}))
	mux.HandleFunc("POST /api/switch/{mac}/config/restore", s.session(func(ctx context.Context, ss *api.Session, r *http.Request) (any, error) {
		var in struct {
			Path   string
			Config *api.SavedConfig
		}
		if err := dec(r, &in); err != nil {
			return nil, err
		}
		cfg := in.Config
		if cfg == nil {
			var err error
			if cfg, err = api.ReadConfigFile(in.Path); err != nil {
				return nil, err
			}
		}
		var log []string
		err := ss.RestoreConfig(ctx, cfg, func(m string) { log = append(log, m) })
		return map[string]any{"ok": err == nil, "log": log, "error": errString(err)}, nil
	}))
	mux.HandleFunc("GET /api/switch/{mac}/raw", s.session(func(ctx context.Context, ss *api.Session, r *http.Request) (any, error) {
		// debugging aid: read arbitrary tags, ?tags=0c00,1000
		var tags []nsdp.Tag
		for _, t := range strings.Split(r.URL.Query().Get("tags"), ",") {
			var v uint16
			if _, err := fmt.Sscanf(strings.TrimSpace(t), "%x", &v); err == nil {
				tags = append(tags, nsdp.Tag(v))
			}
		}
		p, err := ss.Client.Read(ctx, client.Target{IP: ss.Device.IP, MAC: ss.Device.MAC}, tags...)
		if err != nil {
			return nil, err
		}
		type row struct {
			Tag   string `json:"tag"`
			Name  string `json:"name"`
			Hex   string `json:"hex"`
			Ascii string `json:"ascii"`
		}
		var rows []row
		for _, t := range p.TLVs {
			rows = append(rows, row{fmt.Sprintf("%04x", uint16(t.Tag)), t.Tag.String(), fmt.Sprintf("%x", t.Value), printable(t.Value)})
		}
		return map[string]any{"result": p.Result, "tlvs": rows}, nil
	}))
	s.registerDocs(mux)
	return cors(mux)
}

func printable(b []byte) string {
	out := make([]byte, len(b))
	for i, c := range b {
		if c >= 32 && c < 127 {
			out[i] = c
		} else {
			out[i] = '.'
		}
	}
	return string(out)
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func ok(err error) (any, error) {
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true}, nil
}

func dec(r *http.Request, v any) error {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		return fmt.Errorf("bad request body: %w", err)
	}
	return nil
}

type handler func(r *http.Request) (any, error)

func (s *Server) wrap(h handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.token != "" && r.Header.Get("X-Auth-Token") != s.token && r.URL.Query().Get("token") != s.token {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		v, err := h(r)
		if err != nil {
			status := http.StatusBadGateway
			var nerr *nsdp.Error
			if errors.As(err, &nerr) && nerr.Code == nsdp.ResultBadPassword {
				status = http.StatusUnauthorized
			} else if errors.Is(err, api.ErrUnsupported) || nsdp.IsUnsupported(err) {
				status = http.StatusNotImplemented
			} else if strings.HasPrefix(err.Error(), "bad request") || errors.Is(err, errNoSession) {
				status = http.StatusBadRequest
			}
			w.WriteHeader(status)
			json.NewEncoder(w).Encode(map[string]any{"error": err.Error()})
			return
		}
		json.NewEncoder(w).Encode(v)
	}
}

var errNoSession = errors.New("not logged in to that switch")

func (s *Server) session(h func(ctx context.Context, ss *api.Session, r *http.Request) (any, error)) http.HandlerFunc {
	return s.wrap(func(r *http.Request) (any, error) {
		mac := strings.ToLower(r.PathValue("mac"))
		s.mu.Lock()
		ss := s.sessions[mac]
		s.mu.Unlock()
		if ss == nil {
			return nil, errNoSession
		}
		ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
		defer cancel()
		return h(ctx, ss, r)
	})
}

func (s *Server) discover(r *http.Request) (any, error) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	devs, err := s.cli.Discover(ctx, 3*time.Second)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	for _, d := range devs {
		s.devices[strings.ToLower(d.MACString)] = d
		if ss, ok := s.sessions[strings.ToLower(d.MACString)]; ok {
			ss.Device = d
		}
	}
	s.mu.Unlock()
	return s.deviceList(), nil
}

func (s *Server) deviceList() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []map[string]any
	for mac, d := range s.devices {
		_, logged := s.sessions[mac]
		out = append(out, map[string]any{"device": d, "password_mode": d.PasswordMode.String(), "logged_in": logged})
	}
	return out
}

// addDevice registers a switch by IP without discovery (e.g. across subnets).
func (s *Server) addDevice(r *http.Request) (any, error) {
	var in struct{ IP string }
	if err := dec(r, &in); err != nil {
		return nil, err
	}
	ip := net.ParseIP(in.IP)
	if ip == nil {
		return nil, errors.New("bad request: invalid ip")
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	ss := api.NewSession(s.cli, client.Device{IP: ip.To4(), IPString: ip.String(), MAC: make(net.HardwareAddr, 6)}, "")
	info, err := ss.Refresh(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.devices[strings.ToLower(info.MAC)] = ss.Device
	s.mu.Unlock()
	return s.deviceList(), nil
}

func (s *Server) login(r *http.Request) (any, error) {
	var in struct{ Password string }
	if err := dec(r, &in); err != nil {
		return nil, err
	}
	mac := strings.ToLower(r.PathValue("mac"))
	s.mu.Lock()
	d, ok := s.devices[mac]
	s.mu.Unlock()
	if !ok {
		return nil, errors.New("bad request: unknown switch, discover first")
	}
	ss := api.NewSession(s.cli, d, in.Password)
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if err := ss.Login(ctx); err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.sessions[mac] = ss
	s.mu.Unlock()
	return map[string]any{"ok": true}, nil
}

func cors(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Auth-Token")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		h.ServeHTTP(w, r)
	})
}
