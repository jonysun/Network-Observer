package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	networkobserverv1 "github.com/network-observer/network-observer/gen/networkobserver/v1"
	"github.com/network-observer/network-observer/internal/attribution"
	"github.com/network-observer/network-observer/internal/config"
	"github.com/network-observer/network-observer/internal/store"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const (
	sessionCookieName     = "observer_session"
	sessionLifetime       = 12 * time.Hour
	websocketWriteTimeout = 5 * time.Second
)

var now = time.Now

type session struct {
	username string
	expires  time.Time
}

type Server struct {
	networkobserverv1.UnimplementedAgentServiceServer
	config    config.Config
	store     *store.Store
	logger    *slog.Logger
	hub       *hub
	manager   *attribution.Manager
	registry  *prometheus.Registry
	metrics   metrics
	sessions  map[string]session
	sessionMu sync.RWMutex
}

type metrics struct {
	ingested     prometheus.Counter
	errors       prometheus.Counter
	connected    prometheus.Gauge
	active       prometheus.Gauge
	storageUp    prometheus.Gauge
	securityMode prometheus.Gauge
}

func New(cfg config.Config, repository *store.Store, logger *slog.Logger) (*Server, error) {
	passwordHash, err := hashPassword(cfg.AdminPassword)
	if err != nil {
		return nil, fmt.Errorf("hash bootstrap admin password: %w", err)
	}
	if err := repository.BootstrapAdmin(context.Background(), cfg.AdminUsername, passwordHash); err != nil {
		return nil, fmt.Errorf("bootstrap admin account: %w", err)
	}
	rules, err := repository.ListRules(context.Background())
	if err != nil {
		return nil, fmt.Errorf("load attribution rules: %w", err)
	}
	decodedRules := make([]attribution.Rule, 0, len(rules))
	for _, record := range rules {
		var rule attribution.Rule
		rule.ID, rule.Name, rule.Priority, rule.Enabled = record.ID, record.Name, record.Priority, record.Enabled
		rule.EffectiveFrom, rule.CreatedAt = record.EffectiveFrom, record.CreatedAt
		if err := json.Unmarshal(record.Matcher, &rule.Matcher); err != nil {
			return nil, fmt.Errorf("decode attribution rule %s matcher: %w", record.ID, err)
		}
		if err := json.Unmarshal(record.Attribution, &rule.Attribution); err != nil {
			return nil, fmt.Errorf("decode attribution rule %s action: %w", record.ID, err)
		}
		decodedRules = append(decodedRules, rule)
	}
	rawOverrides, err := repository.ActiveOverrides(context.Background())
	if err != nil {
		return nil, fmt.Errorf("load attribution overrides: %w", err)
	}
	overrides := make(map[string]attribution.Value, len(rawOverrides))
	for key, raw := range rawOverrides {
		var value attribution.Value
		if err := json.Unmarshal(raw, &value); err != nil {
			return nil, fmt.Errorf("decode attribution override: %w", err)
		}
		overrides[key] = value
	}

	registry := prometheus.NewRegistry()
	metrics := metrics{
		ingested:     prometheus.NewCounter(prometheus.CounterOpts{Name: "network_observer_ingested_observations_total", Help: "Flow observations durably accepted from agents."}),
		errors:       prometheus.NewCounter(prometheus.CounterOpts{Name: "network_observer_ingest_errors_total", Help: "Agent ingest failures."}),
		connected:    prometheus.NewGauge(prometheus.GaugeOpts{Name: "network_observer_agent_connections", Help: "Number of connected agent streams."}),
		active:       prometheus.NewGauge(prometheus.GaugeOpts{Name: "network_observer_active_connections", Help: "Current active connection count."}),
		storageUp:    prometheus.NewGauge(prometheus.GaugeOpts{Name: "network_observer_storage_ready", Help: "Whether both configured storage systems are available."}),
		securityMode: prometheus.NewGauge(prometheus.GaugeOpts{Name: "network_observer_agent_security_mode", Help: "Whether TLS is enabled for agent connections."}),
	}
	if cfg.AgentSecurityMode {
		metrics.securityMode.Set(1)
	}
	registry.MustRegister(metrics.ingested, metrics.errors, metrics.connected, metrics.active, metrics.storageUp, metrics.securityMode)
	if logger == nil {
		logger = slog.New(slog.NewJSONHandler(os.Stdout, nil))
	}
	return &Server{
		config: cfg, store: repository, logger: logger, hub: newHub(), manager: attribution.New(decodedRules, overrides),
		registry: registry, metrics: metrics, sessions: make(map[string]session),
	}, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.router().ServeHTTP(w, r)
}

func (s *Server) router() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /readyz", s.handleReady)
	mux.Handle("GET /metrics", promhttp.HandlerFor(s.registry, promhttp.HandlerOpts{}))
	mux.HandleFunc("POST /api/v1/auth/login", s.handleLogin)
	mux.Handle("GET /api/v1/auth/session", s.requireAdmin(http.HandlerFunc(s.handleSession)))
	mux.Handle("POST /api/v1/auth/logout", s.requireAdmin(http.HandlerFunc(s.handleLogout)))
	mux.Handle("GET /api/v1/devices", s.requireAdmin(http.HandlerFunc(s.handleDevices)))
	mux.Handle("DELETE /api/v1/devices/{id}/credential", s.requireAdmin(http.HandlerFunc(s.handleRevokeDevice)))
	mux.Handle("GET /api/v1/connections", s.requireAdmin(http.HandlerFunc(s.handleConnections)))
	mux.Handle("GET /api/v1/traffic/summary", s.requireAdmin(http.HandlerFunc(s.handleTrafficSummary)))
	mux.Handle("POST /api/v1/attribution/overrides", s.requireAdmin(http.HandlerFunc(s.handleCreateOverride)))
	mux.Handle("DELETE /api/v1/attribution/overrides/{id}", s.requireAdmin(http.HandlerFunc(s.handleDeleteOverride)))
	mux.Handle("GET /api/v1/attribution/rules", s.requireAdmin(http.HandlerFunc(s.handleListRules)))
	mux.Handle("POST /api/v1/attribution/rules", s.requireAdmin(http.HandlerFunc(s.handleCreateRule)))
	mux.Handle("PATCH /api/v1/attribution/rules/{id}", s.requireAdmin(http.HandlerFunc(s.handleUpdateRule)))
	mux.Handle("DELETE /api/v1/attribution/rules/{id}", s.requireAdmin(http.HandlerFunc(s.handleDeleteRule)))
	mux.Handle("GET /ws/v1/connections", s.requireAdmin(http.HandlerFunc(s.handleConnectionsSocket)))
	mux.HandleFunc("/", s.handleWeb)
	return requestIDMiddleware(mux)
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.store.Ready(ctx); err != nil {
		s.metrics.storageUp.Set(0)
		writeError(w, r, http.StatusServiceUnavailable, "storage_unavailable", err.Error())
		return
	}
	s.metrics.storageUp.Set(1)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (s *Server) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessionCookieName)
		if err != nil {
			writeError(w, r, http.StatusUnauthorized, "unauthorized", "login required")
			return
		}
		s.sessionMu.RLock()
		saved, ok := s.sessions[cookie.Value]
		s.sessionMu.RUnlock()
		if !ok || !saved.expires.After(now()) {
			writeError(w, r, http.StatusUnauthorized, "unauthorized", "session expired")
			return
		}
		ctx := context.WithValue(r.Context(), actorContextKey{}, saved.username)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

type actorContextKey struct{}

func actorFromRequest(r *http.Request) string {
	if value, ok := r.Context().Value(actorContextKey{}).(string); ok {
		return value
	}
	return "admin"
}

func requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if id == "" || len(id) > 128 {
			var b [16]byte
			if _, err := rand.Read(b[:]); err == nil {
				id = base64.RawURLEncoding.EncodeToString(b[:])
			} else {
				id = fmt.Sprintf("%d", now().UnixNano())
			}
		}
		w.Header().Set("X-Request-ID", id)
		ctx := context.WithValue(r.Context(), requestIDContextKey{}, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

type requestIDContextKey struct{}

func requestIDFromRequest(r *http.Request) string {
	value, _ := r.Context().Value(requestIDContextKey{}).(string)
	return value
}

func randomToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

func (s *Server) generateSession(username string) (string, error) {
	token, err := randomToken()
	if err != nil {
		return "", err
	}
	s.sessionMu.Lock()
	s.sessions[token] = session{username: username, expires: now().Add(sessionLifetime)}
	s.sessionMu.Unlock()
	return token, nil
}

func (s *Server) deleteSession(token string) {
	s.sessionMu.Lock()
	delete(s.sessions, token)
	s.sessionMu.Unlock()
}

func (s *Server) validateEnrollmentToken(candidate string) bool {
	return subtle.ConstantTimeCompare([]byte(candidate), []byte(s.config.EnrollmentToken)) == 1
}

func (s *Server) checkStorage(ctx context.Context) error {
	return s.store.Ready(ctx)
}

var errNotFound = errors.New("not found")
