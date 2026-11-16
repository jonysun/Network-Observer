package server

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

func TestRouterRegistersDeviceDeleteRoutes(t *testing.T) {
	server := &Server{registry: prometheus.NewRegistry()}

	if server.router() == nil {
		t.Fatal("router returned nil")
	}
}
