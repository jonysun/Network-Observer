package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
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
	"github.com/network-observer/network-observer/internal/domain"
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
	for i := range items {
		if s.deviceConnected(items[i].ID) {
			items[i].Online = true
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleDeviceProfile(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Name           string          `json:"name"`
		DeviceCategory string          `json:"device_category"`
		Latitude       json.RawMessage `json:"latitude"`
		Longitude      json.RawMessage `json:"longitude"`
	}
	if err := decodeJSON(r.Body, &request); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}
	coordinatesProvided := request.Latitude != nil || request.Longitude != nil
	var latitude, longitude *float64
	var err error
	if coordinatesProvided {
		if request.Latitude == nil || request.Longitude == nil {
			writeError(w, r, http.StatusBadRequest, "invalid_profile", "latitude and longitude must be set or cleared together")
			return
		}
		latitude, err = decodeNullableFloat(request.Latitude)
		if err == nil {
			longitude, err = decodeNullableFloat(request.Longitude)
		}
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_profile", "latitude and longitude must be numbers or null")
			return
		}
	}
	item, err := s.store.UpdateDeviceProfile(r.Context(), r.PathValue("id"), request.Name, request.DeviceCategory,
		actorFromRequest(r), requestIDFromRequest(r), latitude, longitude, coordinatesProvided)
	if err != nil {
		status, code := http.StatusBadRequest, "invalid_profile"
		if strings.Contains(err.Error(), "not found") {
			status, code = http.StatusNotFound, "not_found"
		}
		writeError(w, r, status, code, err.Error())
		return
	}
	if s.deviceConnected(item.ID) {
		item.Online = true
	}
	writeJSON(w, http.StatusOK, item)
}

func decodeNullableFloat(raw json.RawMessage) (*float64, error) {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	var value float64
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	return &value, nil
}

func (s *Server) handleLANDevices(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListLANDevices(r.Context())
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "query_failed", "could not load LAN neighbor inventory")
		return
	}
	networks, err := s.store.ListLANNetworks(r.Context())
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "query_failed", "could not load LAN network ranges")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "networks": networks})
}

func (s *Server) handleSaveLANAlias(w http.ResponseWriter, r *http.Request) {
	var request struct {
		NetworkID      string `json:"network_id"`
		MAC            string `json:"mac"`
		Name           string `json:"name"`
		DeviceCategory string `json:"device_category"`
	}
	if err := decodeJSON(r.Body, &request); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}
	request.NetworkID = strings.TrimSpace(request.NetworkID)
	request.Name = strings.TrimSpace(request.Name)
	request.DeviceCategory = strings.TrimSpace(request.DeviceCategory)
	mac, err := net.ParseMAC(request.MAC)
	if request.NetworkID == "" || err != nil || len(request.Name) > 128 || (request.DeviceCategory != "" && !domain.ValidDeviceCategory(strings.ToLower(request.DeviceCategory))) {
		writeError(w, r, http.StatusBadRequest, "invalid_alias", "network_id, a valid MAC address, a name up to 128 characters, and a supported device category are required")
		return
	}
	if err := s.store.SaveLANProfile(r.Context(), request.NetworkID, strings.ToLower(mac.String()), request.Name, request.DeviceCategory, actorFromRequest(r), requestIDFromRequest(r)); err != nil {
		writeError(w, r, http.StatusInternalServerError, "save_failed", "could not save LAN device profile")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleLANTopology(w http.ResponseWriter, r *http.Request) {
	assets, err := s.store.ListLANDevices(r.Context())
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "query_failed", "could not load LAN topology assets")
		return
	}
	devices, err := s.store.ListDevices(r.Context())
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "query_failed", "could not load topology agents")
		return
	}
	addresses := centerAddresses(r.Context(), s.config.TLSCertHost)
	result := buildLANTopology(assets, devices, addresses)
	if filter := strings.TrimSpace(r.URL.Query().Get("network_id")); filter != "" {
		filtered := result.Groups[:0]
		for _, group := range result.Groups {
			if group.NetworkID == filter {
				filtered = append(filtered, group)
			}
		}
		result.Groups = filtered
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleAgentCACertificate(w http.ResponseWriter, r *http.Request) {
	data, err := os.ReadFile(s.config.TLSCertFile)
	if err != nil {
		writeError(w, r, http.StatusServiceUnavailable, "certificate_unavailable", "center TLS certificate is unavailable")
		return
	}
	block, _ := pem.Decode(data)
	if block == nil {
		writeError(w, r, http.StatusServiceUnavailable, "certificate_unavailable", "center TLS certificate is invalid")
		return
	}
	if _, err := x509.ParseCertificate(block.Bytes); err != nil {
		writeError(w, r, http.StatusServiceUnavailable, "certificate_unavailable", "center TLS certificate is invalid")
		return
	}
	w.Header().Set("Content-Type", "application/x-pem-file")
	w.Header().Set("Cache-Control", "public, max-age=300")
	_, _ = w.Write(data)
}

type enrollmentTokenRequest struct {
	Name           string `json:"name"`
	ObserverType   string `json:"observer_type"`
	ExpiresInHours int    `json:"expires_in_hours"`
}

func (s *Server) handleCreateEnrollmentToken(w http.ResponseWriter, r *http.Request) {
	var request enrollmentTokenRequest
	if err := decodeJSON(r.Body, &request); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}
	request.Name = strings.TrimSpace(request.Name)
	request.ObserverType = strings.TrimSpace(strings.ToLower(request.ObserverType))
	if request.Name == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_name", "name is required")
		return
	}
	if request.ObserverType != string(domain.ObserverEndpoint) && request.ObserverType != string(domain.ObserverGateway) {
		writeError(w, r, http.StatusBadRequest, "invalid_observer_type", "observer_type must be endpoint or gateway")
		return
	}
	if request.ExpiresInHours == 0 {
		request.ExpiresInHours = 24
	}
	if request.ExpiresInHours < 1 || request.ExpiresInHours > 24*30 {
		writeError(w, r, http.StatusBadRequest, "invalid_expiry", "expires_in_hours must be between 1 and 720")
		return
	}
	fingerprint, err := certificateFingerprint(s.config.TLSCertFile)
	if err != nil {
		writeError(w, r, http.StatusServiceUnavailable, "certificate_unavailable", "could not fingerprint center TLS certificate")
		return
	}
	tlsServerName, err := certificateTLSServerName(s.config.TLSCertFile)
	if err != nil {
		writeError(w, r, http.StatusServiceUnavailable, "certificate_unavailable", "Center TLS certificate must include a remote DNS name or non-loopback IP address")
		return
	}
	token, err := randomToken()
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "token_generation_failed", "could not generate enrollment token")
		return
	}
	record, err := s.store.CreateEnrollmentToken(r.Context(), request.ObserverType, request.Name, actorFromRequest(r), requestIDFromRequest(r), now().UTC().Add(time.Duration(request.ExpiresInHours)*time.Hour), token)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "save_failed", "could not save enrollment token")
		return
	}
	// plaintext_token is intentionally present only in this creation response.
	writeJSON(w, http.StatusCreated, map[string]any{"token": record, "plaintext_token": token, "tls_fingerprint": fingerprint, "tls_server_name": tlsServerName})
}

func certificateTLSServerName(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return "", errors.New("invalid PEM certificate")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return "", err
	}
	for _, name := range certificate.DNSNames {
		if !strings.EqualFold(name, "localhost") {
			return name, nil
		}
	}
	for _, address := range certificate.IPAddresses {
		if !address.IsLoopback() {
			return address.String(), nil
		}
	}
	return "", errors.New("certificate has no remote DNS name or non-loopback IP subject alternative name")
}

func certificateFingerprint(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return "", errors.New("invalid PEM certificate")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(certificate.Raw)
	return strings.ToUpper(hex.EncodeToString(digest[:])), nil
}

func (s *Server) handleListEnrollmentTokens(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListEnrollmentTokens(r.Context())
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "query_failed", "could not load enrollment tokens")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleRevokeEnrollmentToken(w http.ResponseWriter, r *http.Request) {
	if err := s.store.RevokeEnrollmentToken(r.Context(), r.PathValue("id"), actorFromRequest(r), requestIDFromRequest(r)); err != nil {
		status := http.StatusInternalServerError
		if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "already") {
			status = http.StatusNotFound
		}
		writeError(w, r, status, "revoke_failed", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDeviceDelete(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/devices/")
	if strings.HasPrefix(path, "enrollment-tokens/") {
		id := strings.TrimPrefix(path, "enrollment-tokens/")
		if id != "" && !strings.Contains(id, "/") {
			r.SetPathValue("id", id)
			s.handleRevokeEnrollmentToken(w, r)
			return
		}
	}
	if strings.HasSuffix(path, "/credential") {
		id := strings.TrimSuffix(path, "/credential")
		if id != "" && !strings.Contains(id, "/") {
			r.SetPathValue("id", id)
			s.handleRevokeDevice(w, r)
			return
		}
	}
	http.NotFound(w, r)
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
	hideInternal, err := queryHideInternal(r)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_filter", err.Error())
		return
	}
	var networks []string
	if hideInternal {
		networks, err = s.store.ListLANNetworks(r.Context())
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "query_failed", "could not load LAN network ranges")
			return
		}
	}
	deviceID := r.URL.Query().Get("device_id")
	var items []domain.FlowObservation
	var total uint64
	items, total, err = s.listConnectionsMerged(r.Context(), deviceID, protocol, r.URL.Query().Get("q"), hideInternal, networks, limit, offset)
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
	filter, err := s.trafficFilterFromRequest(r, dimension, from, to, limit)
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

func (s *Server) handleOverviewRankings(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	dimensions := query["dimension"]
	if len(dimensions) == 0 && query.Get("dimensions") != "" {
		dimensions = strings.Split(query.Get("dimensions"), ",")
	}
	if len(dimensions) == 0 {
		dimensions = []string{"device", "domain", "local_port", "process"}
	}
	if len(dimensions) > 5 {
		writeError(w, r, http.StatusBadRequest, "invalid_dimension", "request at most five ranking dimensions")
		return
	}
	for i := range dimensions {
		dimensions[i] = strings.TrimSpace(dimensions[i])
	}
	limit := queryUint(r, "limit", 10, 50)
	trustFilter := strings.ToLower(strings.TrimSpace(query.Get("trusted")))
	if trustFilter == "" {
		trustFilter = "all"
	}
	if trustFilter != "all" && trustFilter != "trusted" && trustFilter != "untrusted" {
		writeError(w, r, http.StatusBadRequest, "invalid_filter", "trusted must be all, trusted, or untrusted")
		return
	}
	sortBy := strings.ToLower(strings.TrimSpace(query.Get("sort")))
	if sortBy == "" {
		sortBy = "traffic"
	}
	if sortBy != "traffic" && sortBy != "connections" {
		writeError(w, r, http.StatusBadRequest, "invalid_sort", "sort must be traffic or connections")
		return
	}
	to := now().UTC()
	from := to.Add(-24 * time.Hour)
	if value := query.Get("from"); value != "" {
		parsed, err := time.Parse(time.RFC3339, value)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_range", "from must be RFC3339")
			return
		}
		from = parsed.UTC()
	}
	if value := query.Get("to"); value != "" {
		parsed, err := time.Parse(time.RFC3339, value)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_range", "to must be RFC3339")
			return
		}
		to = parsed.UTC()
	}
	if !from.Before(to) || to.Sub(from) > 24*time.Hour || to.After(now().UTC().Add(time.Minute)) {
		writeError(w, r, http.StatusBadRequest, "invalid_range", "ranking window must be positive and no longer than 24 hours")
		return
	}
	select {
	case s.rankingSem <- struct{}{}:
		defer func() { <-s.rankingSem }()
	default:
		writeError(w, r, http.StatusServiceUnavailable, "query_busy", "ranking query capacity is full; retry shortly")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	items, partial, unavailableDimensions, err := s.store.OverviewRankings(ctx, dimensions, limit, trustFilter, sortBy, from, to)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "query_failed", "could not load overview rankings")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "from": from, "to": to, "trusted": trustFilter, "sort": sortBy, "partial": partial, "unavailable_dimensions": unavailableDimensions, "limit": limit})
}

func (s *Server) trafficFilterFromRequest(r *http.Request, dimension string, from, to time.Time, limit uint32) (store.TrafficFilter, error) {
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
	hideInternal, err := queryHideInternal(r)
	if err != nil {
		return filter, err
	}
	filter.HideInternal = hideInternal
	if hideInternal {
		filter.LANNetworks, err = s.store.ListLANNetworks(r.Context())
		if err != nil {
			return filter, fmt.Errorf("load LAN network ranges: %w", err)
		}
	}
	return filter, nil
}

func queryHideInternal(r *http.Request) (bool, error) {
	value := r.URL.Query().Get("hide_internal")
	if value == "" {
		return false, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, errors.New("hide_internal must be true or false")
	}
	return parsed, nil
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
	filter, err := s.trafficFilterFromRequest(r, dimension, from, to, queryUint(r, "limit", 5000, 20000))
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
	hideInternal, err := queryHideInternal(r)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_filter", err.Error())
		return
	}
	var networks []string
	if hideInternal {
		networks, err = s.store.ListLANNetworks(r.Context())
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "query_failed", "could not load LAN network ranges")
			return
		}
	}
	items, err := s.store.PresenceFiltered(r.Context(), r.URL.Query().Get("device_id"), r.URL.Query().Get("domain"), from, to, ratio, queryUint(r, "limit", 500, 5000), hideInternal, networks)
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
		item, err := s.store.EnrichedIP(r.Context(), strings.TrimSpace(value))
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "query_failed", "could not load IP metadata")
			return
		}
		items = append(items, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleUpdateIPMetadata(w http.ResponseWriter, r *http.Request) {
	patch, err := decodeIPMetadataPatch(r.Body)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	item, err := s.store.UpdateIPMetadata(r.Context(), patch, actorFromRequest(r), requestIDFromRequest(r))
	if err != nil {
		if store.IsIPMetadataValidationError(err) {
			writeError(w, r, http.StatusBadRequest, "invalid_ip_metadata", err.Error())
			return
		}
		writeError(w, r, http.StatusInternalServerError, "update_failed", "could not save IP metadata")
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func decodeIPMetadataPatch(body io.ReadCloser) (domain.IPMetadataPatch, error) {
	var fields map[string]json.RawMessage
	if err := decodeJSON(body, &fields); err != nil {
		return domain.IPMetadataPatch{}, fmt.Errorf("invalid JSON body")
	}
	allowed := map[string]bool{
		"ip": true, "domain": true, "asn": true, "organization": true, "country": true,
		"region": true, "city": true, "latitude": true, "longitude": true, "trusted": true,
	}
	for key := range fields {
		if !allowed[key] {
			return domain.IPMetadataPatch{}, fmt.Errorf("unknown field %q", key)
		}
	}
	var patch domain.IPMetadataPatch
	if err := json.Unmarshal(fields["ip"], &patch.IP); err != nil || patch.IP == "" {
		return domain.IPMetadataPatch{}, fmt.Errorf("ip is required")
	}
	var err error
	if patch.Domain, patch.SetDomain, err = decodeOptionalField[string](fields, "domain"); err != nil {
		return domain.IPMetadataPatch{}, err
	}
	if patch.ASN, patch.SetASN, err = decodeOptionalField[uint32](fields, "asn"); err != nil {
		return domain.IPMetadataPatch{}, err
	}
	if patch.Organization, patch.SetOrganization, err = decodeOptionalField[string](fields, "organization"); err != nil {
		return domain.IPMetadataPatch{}, err
	}
	if patch.Country, patch.SetCountry, err = decodeOptionalField[string](fields, "country"); err != nil {
		return domain.IPMetadataPatch{}, err
	}
	if patch.Region, patch.SetRegion, err = decodeOptionalField[string](fields, "region"); err != nil {
		return domain.IPMetadataPatch{}, err
	}
	if patch.City, patch.SetCity, err = decodeOptionalField[string](fields, "city"); err != nil {
		return domain.IPMetadataPatch{}, err
	}
	if patch.Latitude, patch.SetLatitude, err = decodeOptionalField[float64](fields, "latitude"); err != nil {
		return domain.IPMetadataPatch{}, err
	}
	if patch.Longitude, patch.SetLongitude, err = decodeOptionalField[float64](fields, "longitude"); err != nil {
		return domain.IPMetadataPatch{}, err
	}
	if raw, exists := fields["trusted"]; exists {
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return domain.IPMetadataPatch{}, fmt.Errorf("trusted must be true or false")
		}
		var trusted bool
		if err := json.Unmarshal(raw, &trusted); err != nil {
			return domain.IPMetadataPatch{}, fmt.Errorf("trusted must be true or false")
		}
		patch.Trusted = &trusted
	}
	return patch, nil
}

func decodeOptionalField[T any](fields map[string]json.RawMessage, key string) (*T, bool, error) {
	raw, exists := fields[key]
	if !exists {
		return nil, false, nil
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, true, nil
	}
	var value T
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, true, fmt.Errorf("invalid %s value", key)
	}
	return &value, true, nil
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
	deviceID := r.URL.Query().Get("device_id")
	var items []domain.FlowObservation
	var total uint64
	items, total, err = s.listConnectionsMerged(r.Context(), deviceID, r.URL.Query().Get("protocol"), r.URL.Query().Get("q"), false, nil, 10_000, 0)
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
