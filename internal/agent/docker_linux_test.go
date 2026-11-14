//go:build linux

package agent

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"testing"
)

func TestEnrichDockerMetadata(t *testing.T) {
	socket := t.TempDir() + "/docker.sock"
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/version":
			_, _ = writer.Write([]byte(`{"ApiVersion":"1.41"}`))
		case "/containers/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/json":
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"Name":   "/web-1",
				"Config": map[string]any{"Labels": map[string]string{"com.docker.compose.service": "web"}},
			})
		default:
			http.NotFound(writer, request)
		}
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Close()
		_ = listener.Close()
	})

	containerID := strings.Repeat("a", 64)
	result := enrichDockerMetadata(context.Background(), socket, map[uint64]processMetadata{
		1: {pid: 10, containerID: containerID},
	})
	if result.status != "supported" {
		t.Fatalf("status=%q detail=%q", result.status, result.detail)
	}
	metadata, ok := result.containers[containerID]
	if !ok || metadata.name != "web-1" || metadata.service != "web" {
		t.Fatalf("unexpected metadata: %+v", result.containers)
	}
}

func TestEnrichDockerMetadataMissingSocket(t *testing.T) {
	result := enrichDockerMetadata(context.Background(), t.TempDir()+"/missing.sock", nil)
	if result.status != "unavailable" {
		t.Fatalf("status=%q detail=%q", result.status, result.detail)
	}
}
