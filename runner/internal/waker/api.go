package waker

import (
	"context"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"relay/runner/internal/wol"
)

// Prober reports whether a machine is reachable right now. It's an
// interface so tests can decide up/down without touching the network,
// mirroring how wol.PacketSender makes the packet send fakeable.
type Prober interface {
	Probe(ctx context.Context, m Machine) bool
}

// TCPProber is the real Prober: a short TCP dial to the machine's
// probeHost:probePort. A sleeping host doesn't answer (connection times
// out or is refused by nothing at all), an awake runner accepts instantly.
type TCPProber struct {
	Timeout time.Duration
}

// Probe implements Prober.
func (p TCPProber) Probe(ctx context.Context, m Machine) bool {
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = DefaultProbeTimeout
	}
	d := net.Dialer{Timeout: timeout}
	addr := net.JoinHostPort(m.Host(), strconv.Itoa(m.Port()))
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// Server holds the dependencies needed to serve wakerd's HTTP API.
type Server struct {
	Key      string
	Machines []Machine
	Sender   wol.PacketSender
	Prober   Prober
	// WakeTimeout bounds how long POST .../wake waits for the target.
	// Zero means DefaultWakeTimeout.
	WakeTimeout time.Duration
	// PollInterval is the gap between probes while waiting. Zero means
	// DefaultPollInterval.
	PollInterval time.Duration
	// Logf defaults to log.Printf when nil.
	Logf func(format string, args ...any)
}

// NewServer builds a Server from a loaded Config using the real sender and
// prober.
func NewServer(cfg *Config) *Server {
	return &Server{
		Key:      cfg.Key,
		Machines: cfg.Machines,
		Sender:   wol.DirectedSender,
		Prober:   TCPProber{Timeout: DefaultProbeTimeout},
	}
}

func (s *Server) logf(format string, args ...any) {
	if s.Logf != nil {
		s.Logf(format, args...)
		return
	}
	log.Printf(format, args...)
}

func (s *Server) wakeTimeout() time.Duration {
	if s.WakeTimeout > 0 {
		return s.WakeTimeout
	}
	return DefaultWakeTimeout
}

func (s *Server) pollInterval() time.Duration {
	if s.PollInterval > 0 {
		return s.PollInterval
	}
	return DefaultPollInterval
}

func (s *Server) machine(id string) (Machine, bool) {
	for _, m := range s.Machines {
		if m.ID == id {
			return m, true
		}
	}
	return Machine{}, false
}

// Routes builds the HTTP handler, using Go 1.22's method-and-pattern aware
// http.ServeMux (stdlib only, no router dependency).
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /v1/health", s.handleHealth)
	mux.HandleFunc("GET /v1/machines", s.auth(s.handleListMachines))
	mux.HandleFunc("GET /v1/machines/{id}/status", s.auth(s.handleStatus))
	mux.HandleFunc("POST /v1/machines/{id}/wake", s.auth(s.handleWake))

	return mux
}

// auth wraps a handler with the X-Relay-Key check. /v1/health is
// deliberately not wrapped - same header name and middleware shape as the
// runner's internal/api, so the phone app has one auth scheme, not two.
func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Relay-Key") != s.Key {
			writeError(w, http.StatusUnauthorized, "invalid or missing X-Relay-Key")
			return
		}
		next(w, r)
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// machineView is the JSON shape of GET /v1/machines entries.
type machineView struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	MAC  string `json:"mac"`
	Up   bool   `json:"up"`
}

func (s *Server) handleListMachines(w http.ResponseWriter, r *http.Request) {
	out := make([]machineView, len(s.Machines))

	// Probed concurrently: probes are mostly dead time waiting on a dial
	// timeout, and a sleeping machine costs the full DefaultProbeTimeout.
	var wg sync.WaitGroup
	for i, m := range s.Machines {
		out[i] = machineView{ID: m.ID, Name: m.Name, MAC: m.MAC}
		wg.Add(1)
		go func(i int, m Machine) {
			defer wg.Done()
			out[i].Up = s.Prober.Probe(r.Context(), m)
		}(i, m)
	}
	wg.Wait()

	writeJSON(w, http.StatusOK, out)
}

type statusResponse struct {
	ID string `json:"id"`
	Up bool   `json:"up"`
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	m, ok := s.machine(id)
	if !ok {
		writeError(w, http.StatusNotFound, "unknown machine "+id)
		return
	}
	writeJSON(w, http.StatusOK, statusResponse{ID: m.ID, Up: s.Prober.Probe(r.Context(), m)})
}

type wakeResponse struct {
	ID            string `json:"id"`
	Woken         bool   `json:"woken"`
	WaitedSeconds int    `json:"waitedSeconds"`
}

// handleWake sends the magic packet, then polls until the host answers or
// the timeout expires. It reports woken=false on timeout rather than an
// error status: the request itself succeeded, the machine just didn't come
// back (NIC not armed for WoL, packet didn't reach the segment, host
// unplugged).
func (s *Server) handleWake(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	m, ok := s.machine(id)
	if !ok {
		writeError(w, http.StatusNotFound, "unknown machine "+id)
		return
	}

	// Validate the MAC before sending anything: a typo in the hand-edited
	// config should be a 400 the caller can act on, not a silent no-op.
	packet, err := wol.BuildMagicPacket(m.MAC)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Already up: nothing to wake, answer immediately.
	if s.Prober.Probe(r.Context(), m) {
		writeJSON(w, http.StatusOK, wakeResponse{ID: m.ID, Woken: true, WaitedSeconds: 0})
		return
	}

	if err := s.Sender.SendBroadcast(packet); err != nil {
		writeError(w, http.StatusInternalServerError, "send magic packet: "+err.Error())
		return
	}
	s.logf("waker: magic packet sent for %s (%s), waiting up to %s", m.ID, m.MAC, s.wakeTimeout())

	start := time.Now()
	deadline := start.Add(s.wakeTimeout())
	ticker := time.NewTicker(s.pollInterval())
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			if s.Prober.Probe(r.Context(), m) {
				waited := int(time.Since(start).Round(time.Second) / time.Second)
				s.logf("waker: %s answered after %ds", m.ID, waited)
				writeJSON(w, http.StatusOK, wakeResponse{ID: m.ID, Woken: true, WaitedSeconds: waited})
				return
			}
			if time.Now().After(deadline) {
				waited := int(time.Since(start).Round(time.Second) / time.Second)
				s.logf("waker: %s did not answer within %ds", m.ID, waited)
				writeJSON(w, http.StatusOK, wakeResponse{ID: m.ID, Woken: false, WaitedSeconds: waited})
				return
			}
		}
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
