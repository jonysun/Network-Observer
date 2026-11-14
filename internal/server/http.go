package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/network-observer/network-observer/internal/attribution"
	"github.com/network-observer/network-observer/internal/store"
)

type errorEnvelope struct {
	Error struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		RequestID string `json:"request_id"`
	} `json:"error"`
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r.Body, &request); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}
	if request.Username != s.config.AdminUsername || request.Password == "" {
		writeError(w, r, http.StatusUnauthorized, "invalid_credentials", "invalid username or password")
		return
	}
	hash, err := s.store.AdminPasswordHash(r.Context(), request.Username)
	if err != nil || !verifyPassword(hash, request.Password) {
		writeError(w, r, http.StatusUnauthorized, "invalid_credentials", "invalid username or password")
		return
	}
	token, err := s.generateSession(request.Username)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "session_error", "could not create a session")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookieName, Value: token, Path: "/", HttpOnly: true, Secure: true,
		SameSite: http.SameSiteStrictMode, Expires: now().Add(sessionLifetime), MaxAge: int(sessionLifetime.Seconds()),
	})
	writeJSON(w, http.StatusOK, map[string]any{"username": request.Username, "expires_at": now().Add(sessionLifetime)})
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"username": actorFromRequest(r)})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		s.deleteSession(cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: "", Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDevices(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListDevices(r.Context())
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "query_failed", "could not load devices")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleRevokeDevice(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.store.RevokeDevice(r.Context(), id, actorFromRequest(r), requestIDFromRequest(r)); err != nil {
		status := http.StatusInternalServerError
		code := "update_failed"
		if strings.Contains(err.Error(), "not found") {
			status, code = http.StatusNotFound, "not_found"
		}
		writeError(w, r, status, code, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleConnections(w http.ResponseWriter, r *http.Request) {
	limit := queryUint(r, "limit", 100, 10_000)
	offset := queryUint(r, "offset", 0, 1_000_000)
	protocol := strings.ToLower(r.URL.Query().Get("protocol"))
	if protocol != "" && protocol != "tcp" && protocol != "udp" {
		writeError(w, r, http.StatusBadRequest, "invalid_protocol", "protocol must be tcp or udp")
		return
	}
	items, total, err := s.store.ListConnections(r.Context(), r.URL.Query().Get("device_id"), protocol, r.URL.Query().Get("q"), limit, offset)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "query_failed", "could not load active connections")
		return
	}
	s.metrics.active.Set(float64(total))
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": total, "limit": limit, "offset": offset})
}

func (s *Server) handleTrafficSummary(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	dimension := query.Get("dimension")
	if dimension == "" {
		dimension = "device"
	}
	from, to, err := s.parseTrafficRange(r)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_range", err.Error())
		return
	}
	limit := queryUint(r, "limit", 5000, 20000)
	filter, err := trafficFilterFromRequest(r, dimension, from, to, limit)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_filter", err.Error())
		return
	}
	items, err := s.store.TrafficSummaryFiltered(r.Context(), filter)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "query_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "from": from, "to": to, "dimension": dimension})
}

func trafficFilterFromRequest(r *http.Request, dimension string, from, to time.Time, limit uint32) (store.TrafficFilter, error) {
	filter := store.TrafficFilter{Dimension: dimension, Value: r.URL.Query().Get("value"), DeviceID: r.URL.Query().Get("device_id"), Interface: r.URL.Query().Get("interface"), Protocol: strings.ToLower(r.URL.Query().Get("protocol")), SourceIP: r.URL.Query().Get("source_ip"), DestinationIP: r.URL.Query().Get("destination_ip"), Domain: r.URL.Query().Get("domain"), Organization: r.URL.Query().Get("organization"), Country: r.URL.Query().Get("country"), Region: r.URL.Query().Get("region"), City: r.URL.Query().Get("city"), ProcessName: r.URL.Query().Get("process"), ContainerName: r.URL.Query().Get("container"), Direction: strings.ToLower(r.URL.Query().Get("direction")), From: from, To: to, Limit: limit}
	for key, target := range map[string]*uint16{"local_port": &filter.LocalPort, "remote_port": &filter.RemotePort} {
		if value := r.URL.Query().Get(key); value != "" {
			parsed, err := strconv.ParseUint(value, 10, 16)
			if err != nil || parsed == 0 {
				return filter, fmt.Errorf("%s must be between 1 and 65535", key)
			}
			*target = uint16(parsed)
		}
	}
	if value := r.URL.Query().Get("asn"); value != "" {
		parsed, err := strconv.ParseUint(value, 10, 32)
		if err != nil {
			return filter, errors.New("asn must be numeric")
		}
		filter.ASN = uint32(parsed)
	}
	if filter.Protocol != "" && filter.Protocol != "tcp" && filter.Protocol != "udp" {
		return filter, errors.New("protocol must be tcp or udp")
	}
	if filter.Direction != "" && filter.Direction != "upload" && filter.Direction != "download" {
		return filter, errors.New("direction must be upload or download")
	}
	for key, value := range map[string]string{"source_ip": filter.SourceIP, "destination_ip": filter.DestinationIP} {
		if value != "" && net.ParseIP(value) == nil {
			return filter, fmt.Errorf("%s must be a valid IP address", key)
		}
	}
	if filter.Country != "" && (len(filter.Country) != 2 || strings.ToUpper(filter.Country) != filter.Country) {
		return filter, errors.New("country must be a two-letter uppercase code")
	}
	return filter, nil
}

func (s *Server) parseTrafficRange(r *http.Request) (time.Time, time.Time, error) {
	to := now().UTC()
	from := to.Add(-24 * time.Hour)
	var err error
	if value := r.URL.Query().Get("to"); value != "" {
		to, err = time.Parse(time.RFC3339, value)
		if err != nil {
			return time.Time{}, time.Time{}, errors.New("to must be RFC3339")
		}
	}
	if value := r.URL.Query().Get("from"); value != "" {
		from, err = time.Parse(time.RFC3339, value)
		if err != nil {
			return time.Time{}, time.Time{}, errors.New("from must be RFC3339")
		}
	}
	if !from.Before(to) || to.Sub(from) > time.Duration(s.config.SummaryRetentionDays)*24*time.Hour {
		return time.Time{}, time.Time{}, errors.New("time range must be positive and within retention")
	}
	return from.UTC(), to.UTC(), nil
}

func (s *Server) handleTrafficTimeseries(w http.ResponseWriter, r *http.Request) {
	from, to, err := s.parseTrafficRange(r)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_range", err.Error())
		return
	}
	dimension := r.URL.Query().Get("dimension")
	if dimension == "" {
		dimension = "local_port"
	}
	bucket := time.Minute
	switch r.URL.Query().Get("bucket") {
	case "5m":
		bucket = 5 * time.Minute
	case "1h":
		bucket = time.Hour
	case "", "1m":
	default:
		writeError(w, r, http.StatusBadRequest, "invalid_bucket", "bucket must be 1m, 5m, or 1h")
		return
	}
	filter, err := trafficFilterFromRequest(r, dimension, from, to, queryUint(r, "limit", 5000, 20000))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_filter", err.Error())
		return
	}
	items, err := s.store.TrafficTimeseries(r.Context(), filter, bucket)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "query_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "from": from, "to": to, "dimension": dimension, "bucket": r.URL.Query().Get("bucket")})
}

func (s *Server) handlePresence(w http.ResponseWriter, r *http.Request) {
	from, to, err := s.parseTrafficRange(r)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_range", err.Error())
		return
	}
	ratio := 0.0
	if value := r.URL.Query().Get("minimum_ratio"); value != "" {
		ratio, err = strconv.ParseFloat(value, 64)
		if err != nil || ratio < 0 || ratio > 1 {
			writeError(w, r, http.StatusBadRequest, "invalid_ratio", "minimum_ratio must be between 0 and 1")
			return
		}
	}
	items, err := s.store.Presence(r.Context(), r.URL.Query().Get("device_id"), r.URL.Query().Get("domain"), from, to, ratio, queryUint(r, "limit", 500, 5000))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "query_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "from": from, "to": to, "minimum_ratio": ratio})
}

func (s *Server) handleEnrichment(w http.ResponseWriter, r *http.Request) {
	values := r.URL.Query()["ip"]
	if len(values) == 0 && r.URL.Query().Get("ips") != "" {
		values = strings.Split(r.URL.Query().Get("ips"), ",")
	}
	if len(values) == 0 || len(values) > 100 {
		writeError(w, r, http.StatusBadRequest, "invalid_ip", "provide one to one hundred ip values")
		return
	}
	items := make([]any, 0, len(values))
	for _, value := range values {
		items = append(items, s.store.EnrichIP(strings.TrimSpace(value)))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleDNSRelationships(w http.ResponseWriter, r *http.Request) {
	from, to, err := s.parseTrafficRange(r)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_range", err.Error())
		return
	}
	items, err := s.store.DNSRelationships(r.Context(), r.URL.Query().Get("device_id"), r.URL.Query().Get("ip"), r.URL.Query().Get("domain"), from, to, queryUint(r, "limit", 1000, 5000))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "query_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "from": from, "to": to})
}

func (s *Server) handleCreateOverride(w http.ResponseWriter, r *http.Request) {
	var request struct {
		ConnectionKey string            `json:"connection_key"`
		Attribution   attribution.Value `json:"attribution"`
		Reason        string            `json:"reason"`
	}
	if err := decodeJSON(r.Body, &request); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}
	deviceID, _, ok := attribution.ParseConnectionKey(request.ConnectionKey)
	if !ok || !hasAttribution(request.Attribution) {
		writeError(w, r, http.StatusBadRequest, "invalid_override", "connection_key and at least one attribution field are required")
		return
	}
	if !isUUID(deviceID) {
		writeError(w, r, http.StatusBadRequest, "invalid_connection_key", "connection_key must begin with a valid device UUID")
		return
	}
	value, err := json.Marshal(request.Attribution)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_attribution", "invalid attribution value")
		return
	}
	id, err := s.store.CreateOverride(r.Context(), request.ConnectionKey, value, request.Reason, actorFromRequest(r), requestIDFromRequest(r))
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "save_failed", "could not save attribution override")
		return
	}
	s.manager.SetOverride(request.ConnectionKey, request.Attribution)
	writeJSON(w, http.StatusCreated, map[string]string{"id": id, "connection_key": request.ConnectionKey})
}

func (s *Server) handleDeleteOverride(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	connectionKey, err := s.store.DeleteOverride(r.Context(), id, actorFromRequest(r), requestIDFromRequest(r))
	if err != nil {
		status := http.StatusInternalServerError
		code := "delete_failed"
		if strings.Contains(err.Error(), "not found") {
			status, code = http.StatusNotFound, "not_found"
		}
		writeError(w, r, status, code, err.Error())
		return
	}
	s.manager.DeleteOverride(connectionKey)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListRules(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListRules(r.Context())
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "query_failed", "could not load attribution rules")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

type ruleRequest struct {
	Name        string              `json:"name"`
	Priority    int                 `json:"priority"`
	Enabled     *bool               `json:"enabled"`
	Matcher     attribution.Matcher `json:"matcher"`
	Attribution attribution.Value   `json:"attribution"`
}

func (s *Server) handleCreateRule(w http.ResponseWriter, r *http.Request) {
	var request ruleRequest
	if err := decodeJSON(r.Body, &request); err != nil || !validRule(request) {
		writeError(w, r, http.StatusBadRequest, "invalid_rule", "rule requires a name, at least one match condition, and an attribution value")
		return
	}
	enabled := true
	if request.Enabled != nil {
		enabled = *request.Enabled
	}
	matcher, _ := json.Marshal(request.Matcher)
	value, _ := json.Marshal(request.Attribution)
	record, err := s.store.CreateRule(r.Context(), store.RuleRecord{
		Name: request.Name, Priority: request.Priority, Enabled: enabled, Matcher: matcher, Attribution: value,
		EffectiveFrom: now().UTC(),
	}, actorFromRequest(r), requestIDFromRequest(r))
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "save_failed", "could not save attribution rule")
		return
	}
	if err := s.reloadRules(r.Context()); err != nil {
		s.logger.Error("reload attribution rules after create", "error", err)
	}
	writeJSON(w, http.StatusCreated, record)
}

func (s *Server) handleUpdateRule(w http.ResponseWriter, r *http.Request) {
	var request ruleRequest
	if err := decodeJSON(r.Body, &request); err != nil || !validRule(request) {
		writeError(w, r, http.StatusBadRequest, "invalid_rule", "rule requires a name, at least one match condition, and an attribution value")
		return
	}
	enabled := true
	if request.Enabled != nil {
		enabled = *request.Enabled
	}
	matcher, _ := json.Marshal(request.Matcher)
	value, _ := json.Marshal(request.Attribution)
	if err := s.store.UpdateRule(r.Context(), store.RuleRecord{
		ID: r.PathValue("id"), Name: request.Name, Priority: request.Priority, Enabled: enabled, Matcher: matcher, Attribution: value,
	}, actorFromRequest(r), requestIDFromRequest(r)); err != nil {
		status := http.StatusInternalServerError
		if strings.Contains(err.Error(), "not found") {
			status = http.StatusNotFound
		}
		writeError(w, r, status, "update_failed", err.Error())
		return
	}
	if err := s.reloadRules(r.Context()); err != nil {
		s.logger.Error("reload attribution rules after update", "error", err)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDeleteRule(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.store.DeleteRule(r.Context(), id, actorFromRequest(r), requestIDFromRequest(r)); err != nil {
		status := http.StatusInternalServerError
		if strings.Contains(err.Error(), "not found") {
			status = http.StatusNotFound
		}
		writeError(w, r, status, "delete_failed", err.Error())
		return
	}
	if err := s.reloadRules(r.Context()); err != nil {
		s.logger.Error("reload attribution rules after delete", "error", err)
	}
	w.WriteHeader(http.StatusNoContent)
}

type todoRequest struct {
	Phase       string `json:"phase"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Status      string `json:"status"`
	Priority    string `json:"priority"`
	Progress    int    `json:"progress"`
	Blocker     string `json:"blocker"`
}

func (s *Server) handleListTodos(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListTodos(r.Context(), r.URL.Query().Get("status"))
	if err != nil {
		if strings.Contains(err.Error(), "unsupported todo status") {
			writeError(w, r, http.StatusBadRequest, "invalid_status", err.Error())
			return
		}
		writeError(w, r, http.StatusInternalServerError, "query_failed", "could not load project todos")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func todoRecordFromRequest(request todoRequest, id string) store.TodoRecord {
	phase := strings.TrimSpace(request.Phase)
	if phase == "" {
		phase = "未分类"
	}
	status := strings.TrimSpace(request.Status)
	if status == "" {
		status = "todo"
	}
	priority := strings.TrimSpace(request.Priority)
	if priority == "" {
		priority = "medium"
	}
	return store.TodoRecord{ID: id, Phase: phase, Title: strings.TrimSpace(request.Title), Description: strings.TrimSpace(request.Description), Status: status, Priority: priority, Progress: request.Progress, Blocker: strings.TrimSpace(request.Blocker)}
}

func (s *Server) handleCreateTodo(w http.ResponseWriter, r *http.Request) {
	var request todoRequest
	if err := decodeJSON(r.Body, &request); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}
	record, err := s.store.CreateTodo(r.Context(), todoRecordFromRequest(request, ""), actorFromRequest(r), requestIDFromRequest(r))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_todo", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, record)
}

func (s *Server) handleUpdateTodo(w http.ResponseWriter, r *http.Request) {
	var request todoRequest
	if err := decodeJSON(r.Body, &request); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}
	record, err := s.store.UpdateTodo(r.Context(), todoRecordFromRequest(request, r.PathValue("id")), actorFromRequest(r), requestIDFromRequest(r))
	if err != nil {
		status := http.StatusBadRequest
		if strings.Contains(err.Error(), "not found") {
			status = http.StatusNotFound
		}
		writeError(w, r, status, "update_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, record)
}

func (s *Server) handleDeleteTodo(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteTodo(r.Context(), r.PathValue("id"), actorFromRequest(r), requestIDFromRequest(r)); err != nil {
		status := http.StatusInternalServerError
		if strings.Contains(err.Error(), "not found") {
			status = http.StatusNotFound
		}
		writeError(w, r, status, "delete_failed", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) reloadRules(ctx context.Context) error {
	records, err := s.store.ListRules(ctx)
	if err != nil {
		return err
	}
	rules := make([]attribution.Rule, 0, len(records))
	for _, record := range records {
		var rule attribution.Rule
		rule.ID, rule.Name, rule.Priority, rule.Enabled = record.ID, record.Name, record.Priority, record.Enabled
		rule.EffectiveFrom, rule.CreatedAt = record.EffectiveFrom, record.CreatedAt
		if err := json.Unmarshal(record.Matcher, &rule.Matcher); err != nil {
			return err
		}
		if err := json.Unmarshal(record.Attribution, &rule.Attribution); err != nil {
			return err
		}
		rules = append(rules, rule)
	}
	s.manager.ReplaceRules(rules)
	return nil
}

func validRule(request ruleRequest) bool {
	return strings.TrimSpace(request.Name) != "" && matcherHasCondition(request.Matcher) && hasAttribution(request.Attribution)
}

func matcherHasCondition(m attribution.Matcher) bool {
	return m.DeviceID != "" || m.ObserverType != "" || m.NetworkNamespace != "" || m.Protocol != "" ||
		m.SourceIP != "" || m.SourcePort != 0 || m.DestinationIP != "" || m.DestinationPort != 0 ||
		m.ProcessName != "" || m.ContainerID != ""
}

func hasAttribution(value attribution.Value) bool {
	return value.PID != 0 || value.ProcessName != "" || value.ContainerID != "" || value.ContainerName != "" || value.ServiceName != ""
}

func (s *Server) handleConnectionsSocket(w http.ResponseWriter, r *http.Request) {
	upgrader := websocket.Upgrader{ReadBufferSize: 1024, WriteBufferSize: 4096}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	s.hub.add(conn)
	defer s.hub.remove(conn)
	items, total, err := s.store.ListConnections(r.Context(), r.URL.Query().Get("device_id"), r.URL.Query().Get("protocol"), r.URL.Query().Get("q"), 10_000, 0)
	if err == nil {
		_ = conn.SetWriteDeadline(now().Add(websocketWriteTimeout))
		_ = conn.WriteJSON(map[string]any{"type": "connections.snapshot", "data": items, "total": total})
	}
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
	}
}

func (s *Server) handleWeb(w http.ResponseWriter, r *http.Request) {
	root := os.Getenv("WEB_DIST_DIR")
	if root == "" {
		root = "web/dist"
	}
	if _, err := os.Stat(root + "/index.html"); err != nil {
		writeJSON(w, http.StatusOK, map[string]string{"service": "network-observer", "console": "frontend build not found"})
		return
	}
	if r.URL.Path != "/" {
		if _, err := os.Stat(root + r.URL.Path); err == nil {
			http.FileServer(http.Dir(root)).ServeHTTP(w, r)
			return
		}
	}
	http.ServeFile(w, r, root+"/index.html")
}

func decodeJSON(body io.ReadCloser, target any) error {
	defer body.Close()
	decoder := json.NewDecoder(io.LimitReader(body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("request body must contain one JSON value")
	}
	return nil
}

func queryUint(r *http.Request, key string, fallback, maximum uint32) uint32 {
	value := r.URL.Query().Get(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseUint(value, 10, 32)
	if err != nil || parsed > uint64(maximum) {
		return fallback
	}
	return uint32(parsed)
}

func isUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for i, r := range value {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if r != '-' {
				return false
			}
			continue
		}
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F') {
			return false
		}
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	var envelope errorEnvelope
	envelope.Error.Code = code
	envelope.Error.Message = message
	envelope.Error.RequestID = requestIDFromRequest(r)
	writeJSON(w, status, envelope)
}
