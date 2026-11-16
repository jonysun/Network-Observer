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

func TestDeviceConnectionStateTracksConcurrentStreams(t *testing.T) {
	server := &Server{hub: newHub(), deviceConnections: make(map[string]int)}

	server.markDeviceConnected("device-1")
	server.markDeviceConnected("device-1")
	if !server.deviceConnected("device-1") {
		t.Fatal("device should be online while a stream is connected")
	}

	server.markDeviceDisconnected("device-1")
	if !server.deviceConnected("device-1") {
		t.Fatal("device should remain online while another stream is connected")
	}
	server.markDeviceDisconnected("device-1")
	if server.deviceConnected("device-1") {
		t.Fatal("device should be offline after its last stream closes")
	}
}
