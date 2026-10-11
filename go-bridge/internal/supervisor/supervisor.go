package supervisor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const (
	StatusSchema         = "watersheep.supervisor.status/v1"
	SupervisorOwner      = "go-bridge"
	SupervisorModule     = "github.com/es-3581100/WaterSheep/go-bridge"
	ObservationAuthority = "observation-only"
	DebugApprovalPhrase  = "I-APPROVE-READONLY-DIRECT-OUTPUT"
	DebugApprovalHeader  = "X-WaterSheep-Debug-Approval"
)

type Config struct {
	ListenAddr     string
	UpstreamBase   string
	ChildCommand   []string
	DevMode        bool
	StartupTimeout time.Duration
	LogBytes       int
}

type Status struct {
	Schema                      string `json:"schema"`
	Owner                       string `json:"owner"`
	GoModule                    string `json:"go_module"`
	Authority                   string `json:"authority"`
	State                       string `json:"state"`
	SupervisorURL               string `json:"supervisor_url,omitempty"`
	UpstreamEndpoint            string `json:"upstream_endpoint"`
	ChildPID                    int    `json:"child_pid,omitempty"`
	StartedAt                   string `json:"started_at,omitempty"`
	ReadyAt                     string `json:"ready_at,omitempty"`
	DevMode                     bool   `json:"dev_mode"`
	DebugDirectOutputAvailable  bool   `json:"debug_direct_output_available"`
	DebugDirectRequiresApproval bool   `json:"debug_direct_requires_approval"`
	LastError                   string `json:"last_error,omitempty"`
}

type Supervisor struct {
	cfg      Config
	upstream *url.URL
	client   *http.Client
	logs     *ringBuffer

	mu     sync.RWMutex
	status Status
	server *http.Server
	cmd    *exec.Cmd
	exitCh chan error
}

func New(cfg Config) (*Supervisor, error) {
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = "127.0.0.1:8777"
	}
	if cfg.UpstreamBase == "" {
		cfg.UpstreamBase = "http://127.0.0.1:8766"
	}
	if cfg.StartupTimeout <= 0 {
		cfg.StartupTimeout = 45 * time.Second
	}
	if cfg.LogBytes <= 0 {
		cfg.LogBytes = 256 * 1024
	}
	if len(cfg.ChildCommand) == 0 {
		return nil, errors.New("child command is required")
	}
	upstream, err := url.Parse(strings.TrimRight(cfg.UpstreamBase, "/"))
	if err != nil || upstream.Scheme == "" || upstream.Host == "" {
		return nil, fmt.Errorf("invalid upstream base URL %q", cfg.UpstreamBase)
	}

	s := &Supervisor{
		cfg:      cfg,
		upstream: upstream,
		client:   &http.Client{Timeout: 120 * time.Second},
		logs:     newRingBuffer(cfg.LogBytes),
		exitCh:   make(chan error, 1),
	}
	s.status = Status{
		Schema:                      StatusSchema,
		Owner:                       SupervisorOwner,
		GoModule:                    SupervisorModule,
		Authority:                   ObservationAuthority,
		State:                       "created",
		UpstreamEndpoint:            s.systemOneURL(),
		DevMode:                     cfg.DevMode,
		DebugDirectOutputAvailable:  cfg.DevMode,
		DebugDirectRequiresApproval: true,
	}
	return s, nil
}

func (s *Supervisor) Run(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.cfg.ListenAddr)
	if err != nil {
		return err
	}
	defer ln.Close()

	s.server = &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second}
	s.setStatus(func(st *Status) {
		st.State = "starting"
		st.SupervisorURL = "http://" + ln.Addr().String()
		st.StartedAt = time.Now().UTC().Format(time.RFC3339Nano)
	})

	serverErr := make(chan error, 1)
	go func() {
		err := s.server.Serve(ln)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	if err := s.startChild(ctx); err != nil {
		s.fail(err)
		_ = s.server.Shutdown(context.Background())
		return err
	}

	if err := s.waitReady(ctx); err != nil {
		s.fail(err)
		_ = s.server.Shutdown(context.Background())
		return err
	}
	s.setStatus(func(st *Status) {
		st.State = "ready"
		st.ReadyAt = time.Now().UTC().Format(time.RFC3339Nano)
	})

	select {
	case <-ctx.Done():
		s.setStatus(func(st *Status) { st.State = "stopping" })
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = s.server.Shutdown(shutdownCtx)
		return nil
	case err := <-s.exitCh:
		if ctx.Err() != nil {
			return nil
		}
		if err == nil {
			err = errors.New("WaterSheep child exited unexpectedly")
		}
		s.fail(err)
		_ = s.server.Shutdown(context.Background())
		return err
	case err := <-serverErr:
		return err
	}
}

func (s *Supervisor) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/status", s.handleStatus)
	mux.HandleFunc("POST /v1/systemone", s.handleSystemOne)
	mux.HandleFunc("POST /v1/debug/direct", s.handleDebugDirect)
	mux.HandleFunc("GET /v1/debug/logs", s.handleDebugLogs)
	return mux
}

func (s *Supervisor) Snapshot() Status {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.status
}

func (s *Supervisor) startChild(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, s.cfg.ChildCommand[0], s.cfg.ChildCommand[1:]...)
	cmd.Stdout = s.logs
	cmd.Stderr = s.logs
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start WaterSheep child: %w", err)
	}
	s.cmd = cmd
	s.setStatus(func(st *Status) { st.ChildPID = cmd.Process.Pid })
	go func() { s.exitCh <- cmd.Wait() }()
	return nil
}

func (s *Supervisor) waitReady(ctx context.Context) error {
	deadline := time.NewTimer(s.cfg.StartupTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	host := s.upstream.Host
	if !strings.Contains(host, ":") {
		if s.upstream.Scheme == "https" {
			host += ":443"
		} else {
			host += ":80"
		}
	}

	for {
		conn, err := net.DialTimeout("tcp", host, 200*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-s.exitCh:
			if err == nil {
				err = errors.New("WaterSheep child exited during startup")
			}
			return err
		case <-deadline.C:
			return fmt.Errorf("WaterSheep did not become ready within %s", s.cfg.StartupTimeout)
		case <-ticker.C:
		}
	}
}

func (s *Supervisor) handleStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.Snapshot())
}

func (s *Supervisor) handleSystemOne(w http.ResponseWriter, r *http.Request) {
	if s.Snapshot().State != "ready" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "WaterSheep is not ready"})
		return
	}
	s.forwardSystemOne(w, r)
}

func (s *Supervisor) handleDebugDirect(w http.ResponseWriter, r *http.Request) {
	if !s.debugApproved(r) {
		writeJSON(w, http.StatusForbidden, map[string]any{
			"error":     "readonly direct output requires dev mode and explicit user approval",
			"authority": ObservationAuthority,
		})
		return
	}
	if s.Snapshot().State != "ready" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "WaterSheep is not ready"})
		return
	}
	w.Header().Set("X-WaterSheep-Debug-Authority", ObservationAuthority)
	w.Header().Set("X-WaterSheep-Debug-Mode", "readonly-direct-output")
	s.forwardSystemOne(w, r)
}

func (s *Supervisor) handleDebugLogs(w http.ResponseWriter, r *http.Request) {
	if !s.debugApproved(r) {
		writeJSON(w, http.StatusForbidden, map[string]any{
			"error":     "readonly logs require dev mode and explicit user approval",
			"authority": ObservationAuthority,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"schema":    "watersheep.supervisor.debug-output/v1",
		"authority": ObservationAuthority,
		"readonly":  true,
		"output":    s.logs.String(),
	})
}

func (s *Supervisor) debugApproved(r *http.Request) bool {
	return s.cfg.DevMode && r.Header.Get(DebugApprovalHeader) == DebugApprovalPhrase
}

func (s *Supervisor) forwardSystemOne(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, s.systemOneURL(), bytes.NewReader(body))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.Header().Set("X-WaterSheep-Status-Owner", SupervisorOwner)
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, io.LimitReader(resp.Body, 8<<20))
}

func (s *Supervisor) systemOneURL() string {
	return strings.TrimRight(s.upstream.String(), "/") + "/v1/systemone"
}

func (s *Supervisor) setStatus(fn func(*Status)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(&s.status)
}

func (s *Supervisor) fail(err error) {
	s.setStatus(func(st *Status) {
		st.State = "failed"
		st.LastError = err.Error()
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

type ringBuffer struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func newRingBuffer(max int) *ringBuffer { return &ringBuffer{max: max} }

func (r *ringBuffer) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf = append(r.buf, p...)
	if len(r.buf) > r.max {
		r.buf = append([]byte(nil), r.buf[len(r.buf)-r.max:]...)
	}
	return len(p), nil
}

func (r *ringBuffer) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return string(append([]byte(nil), r.buf...))
}
