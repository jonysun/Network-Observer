package server

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/network-observer/network-observer/internal/store"
)

func (s *Server) handleAnalyticsDashboard(w http.ResponseWriter, r *http.Request) {
	from, to, err := s.parseTrafficRange(r)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_range", err.Error())
		return
	}
	query := r.URL.Query()
	limit := uint64(10)
	if query.Has("limit") {
		limit, err = strconv.ParseUint(query.Get("limit"), 10, 32)
		if err != nil || limit == 0 || limit > 50 {
			writeError(w, r, http.StatusBadRequest, "invalid_limit", "limit must be between 1 and 50")
			return
		}
	}
	dimensions := query["dimension"]
	if len(dimensions) == 0 && query.Get("dimensions") != "" {
		dimensions = strings.Split(query.Get("dimensions"), ",")
	}
	hideInternal := false
	if query.Has("hide_internal") {
		hideInternal, err = strconv.ParseBool(query.Get("hide_internal"))
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_filter", "hide_internal must be a boolean")
			return
		}
	}
	trust := query.Get("trusted")
	if trust == "" {
		trust = "all"
	}
	filter := store.AnalyticsFilter{
		From: from, To: to, DeviceID: strings.TrimSpace(query.Get("device_id")),
		Dimensions: dimensions, Limit: uint32(limit), Trust: trust, HideInternal: hideInternal,
		SkipRelationships: query.Get("include_relationships") == "false",
		DetailDimension:   query.Get("detail_dimension"), DetailValue: query.Get("detail_value"),
	}
	if err := store.ValidateAnalyticsFilter(filter); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_filter", err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	select {
	case s.rankingSem <- struct{}{}:
		defer func() { <-s.rankingSem }()
	default:
		writeError(w, r, http.StatusServiceUnavailable, "query_busy", "分析查询繁忙，请稍后重试")
		return
	}
	result, err := s.store.AnalyticsDashboard(ctx, filter)
	switch {
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded):
		writeError(w, r, http.StatusGatewayTimeout, "query_timeout", "分析查询超时，请缩短时间范围后重试。")
	case err != nil:
		writeError(w, r, http.StatusInternalServerError, "query_failed", "无法加载多维分析数据")
	default:
		writeJSON(w, http.StatusOK, result)
	}
}
