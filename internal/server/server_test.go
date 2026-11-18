package server

import (
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/network-observer/network-observer/internal/config"
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

func TestOpenWrtBootstrapServesBundledAsset(t *testing.T) {
	root := t.TempDir()
	assetDir := filepath.Join(root, "v0.3.26")
	if err := os.MkdirAll(assetDir, 0o755); err != nil {
		t.Fatal(err)
	}
	assetPath := filepath.Join(assetDir, "install-agent-openwrt.sh")
	if err := os.WriteFile(assetPath, []byte("#!/bin/sh\necho bundled\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	server := &Server{
		config: config.Config{OpenWrtAssetDir: root, OpenWrtReleaseBaseURL: "http://invalid.invalid"},
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	request := httptest.NewRequest("GET", "/bootstrap/openwrt/v0.3.26/install-agent-openwrt.sh", nil)
	request.SetPathValue("version", "v0.3.26")
	request.SetPathValue("asset", "install-agent-openwrt.sh")
	recorder := httptest.NewRecorder()
	server.handleOpenWrtBootstrapAsset(recorder, request)

	if recorder.Code != 200 {
		t.Fatalf("expected bundled asset response, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("X-Network-Observer-Bootstrap-Source"); got != "bundled" {
		t.Fatalf("expected bundled source header, got %q", got)
	}
	if got := recorder.Body.String(); got != "#!/bin/sh\necho bundled\n" {
		t.Fatalf("unexpected asset body %q", got)
	}
}

func TestOpenWrtBootstrapServesUninstallScript(t *testing.T) {
	root := t.TempDir()
	assetDir := filepath.Join(root, "v0.3.30")
	if err := os.MkdirAll(assetDir, 0o755); err != nil {
		t.Fatal(err)
	}
	assetPath := filepath.Join(assetDir, "uninstall-agent.sh")
	if err := os.WriteFile(assetPath, []byte("#!/bin/sh\necho uninstall\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	server := &Server{
		config: config.Config{OpenWrtAssetDir: root, OpenWrtReleaseBaseURL: "http://invalid.invalid"},
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	request := httptest.NewRequest("GET", "/bootstrap/openwrt/v0.3.30/uninstall-agent.sh", nil)
	request.SetPathValue("version", "v0.3.30")
	request.SetPathValue("asset", "uninstall-agent.sh")
	recorder := httptest.NewRecorder()
	server.handleOpenWrtBootstrapAsset(recorder, request)

	if recorder.Code != 200 {
		t.Fatalf("expected bundled uninstaller response, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("X-Network-Observer-Bootstrap-Source"); got != "bundled" {
		t.Fatalf("expected bundled source header, got %q", got)
	}
	if got := recorder.Body.String(); got != "#!/bin/sh\necho uninstall\n" {
		t.Fatalf("unexpected asset body %q", got)
	}
	if got := recorder.Header().Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Fatalf("unexpected content type %q", got)
	}
}
