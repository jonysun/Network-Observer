//go:build linux

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	networkobserverv1 "github.com/network-observer/network-observer/gen/networkobserver/v1"
)

const defaultDockerSocket = "/var/run/docker.sock"

type dockerContainerMetadata struct {
	name    string
	service string
}

type dockerMetadataResult struct {
	containers map[string]dockerContainerMetadata
	status     string
	detail     string
}

type dockerInspectResponse struct {
	Name   string `json:"Name"`
	Config struct {
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
}

type dockerNetworkResponse struct {
	ID         string `json:"Id"`
	Name       string `json:"Name"`
	Driver     string `json:"Driver"`
	Scope      string `json:"Scope"`
	IPAM       struct {
		Config []struct {
			Subnet string `json:"Subnet"`
		} `json:"Config"`
	} `json:"IPAM"`
	Containers map[string]struct {
		Name        string `json:"Name"`
		IPv4Address string `json:"IPv4Address"`
		IPv6Address string `json:"IPv6Address"`
		MacAddress  string `json:"MacAddress"`
	} `json:"Containers"`
}

func collectDockerNetworkSnapshot(ctx context.Context, socket string) ([]*networkobserverv1.DockerNetwork, *networkobserverv1.Capability) {
	if socket == "" {
		socket = defaultDockerSocket
	}
	info, err := os.Stat(socket)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, capability("docker_topology", "unavailable", "Docker Engine socket is not present")
		}
		return nil, capability("docker_topology", "degraded", fmt.Sprintf("cannot inspect Docker Engine socket: %v", err))
	}
	if info.Mode()&os.ModeSocket == 0 {
		return nil, capability("docker_topology", "degraded", "Docker Engine path is not a Unix socket")
	}
	client := dockerHTTPClient(socket)
	defer client.CloseIdleConnections()
	if err := dockerGetJSON(ctx, client, "/version", &map[string]any{}); err != nil {
		return nil, capability("docker_topology", "degraded", fmt.Sprintf("Docker Engine API is unavailable: %v", err))
	}
	var raw []dockerNetworkResponse
	if err := dockerGetJSON(ctx, client, "/networks", &raw); err != nil {
		return nil, capability("docker_topology", "degraded", fmt.Sprintf("Docker networks are unavailable: %v", err))
	}
	networks := make([]*networkobserverv1.DockerNetwork, 0, len(raw))
	for _, item := range raw {
		network := &networkobserverv1.DockerNetwork{Id: item.ID, Name: item.Name, Driver: item.Driver, Scope: item.Scope}
		for _, config := range item.IPAM.Config {
			if _, _, err := net.ParseCIDR(config.Subnet); err == nil {
				network.SubnetCidrs = append(network.SubnetCidrs, config.Subnet)
			}
		}
		if len(network.SubnetCidrs) == 0 {
			continue
		}
		for id, container := range item.Containers {
			network.Containers = append(network.Containers, &networkobserverv1.DockerContainer{
				Id: id, Name: strings.TrimPrefix(container.Name, "/"),
				Ipv4Address: dockerAddressWithoutPrefix(container.IPv4Address),
				Ipv6Address: dockerAddressWithoutPrefix(container.IPv6Address),
				MacAddress: strings.ToLower(container.MacAddress),
			})
		}
		sort.Slice(network.SubnetCidrs, func(i, j int) bool { return network.SubnetCidrs[i] < network.SubnetCidrs[j] })
		sort.Slice(network.Containers, func(i, j int) bool { return network.Containers[i].Name < network.Containers[j].Name })
		networks = append(networks, network)
	}
	sort.Slice(networks, func(i, j int) bool { return networks[i].Name < networks[j].Name })
	return networks, capability("docker_topology", "supported", fmt.Sprintf("%d Docker network(s) with configured subnets", len(networks)))
}

func dockerAddressWithoutPrefix(value string) string {
	if index := strings.IndexByte(value, '/'); index >= 0 {
		return value[:index]
	}
	return value
}

// enrichDockerMetadata checks the Docker socket once per snapshot, then looks
// up each distinct container ID discovered from process cgroups.
func enrichDockerMetadata(ctx context.Context, socket string, processes map[uint64]processMetadata) dockerMetadataResult {
	if socket == "" {
		socket = defaultDockerSocket
	}
	result := dockerMetadataResult{containers: make(map[string]dockerContainerMetadata)}
	info, err := os.Stat(socket)
	if err != nil {
		if os.IsNotExist(err) {
			result.status = "unavailable"
			result.detail = fmt.Sprintf("Docker Engine socket %s is not present", socket)
		} else {
			result.status = "degraded"
			result.detail = fmt.Sprintf("cannot inspect Docker Engine socket %s: %v", socket, err)
		}
		return result
	}
	if info.Mode()&os.ModeSocket == 0 {
		result.status = "degraded"
		result.detail = fmt.Sprintf("Docker Engine path %s is not a Unix socket", socket)
		return result
	}

	client := dockerHTTPClient(socket)
	defer client.CloseIdleConnections()
	if err := dockerGetJSON(ctx, client, "/version", &map[string]any{}); err != nil {
		result.status = "degraded"
		result.detail = fmt.Sprintf("Docker Engine API is unavailable: %v", err)
		return result
	}

	containerIDs := make(map[string]struct{})
	for _, metadata := range processes {
		if metadata.containerID != "" {
			containerIDs[metadata.containerID] = struct{}{}
		}
	}
	failures := 0
	for containerID := range containerIDs {
		var inspect dockerInspectResponse
		path := "/containers/" + url.PathEscape(containerID) + "/json"
		if err := dockerGetJSON(ctx, client, path, &inspect); err != nil {
			failures++
			continue
		}
		name := strings.TrimPrefix(strings.TrimSpace(inspect.Name), "/")
		service := strings.TrimSpace(inspect.Config.Labels["com.docker.compose.service"])
		if name != "" || service != "" {
			result.containers[containerID] = dockerContainerMetadata{name: name, service: service}
		}
	}

	result.status = "supported"
	if failures > 0 {
		result.status = "degraded"
		result.detail = fmt.Sprintf("Docker Engine metadata resolved for %d of %d container IDs; %d lookup(s) failed", len(result.containers), len(containerIDs), failures)
	} else if len(containerIDs) == 0 {
		result.detail = "Docker Engine API is reachable; no container IDs were found in socket owners"
	} else {
		result.detail = fmt.Sprintf("Docker Engine metadata resolved for %d container IDs", len(result.containers))
	}
	return result
}

func dockerHTTPClient(socket string) *http.Client {
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "unix", socket)
		},
	}
	return &http.Client{Transport: transport, Timeout: 3 * time.Second}
}

func dockerGetJSON(ctx context.Context, client *http.Client, path string, target any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker"+path, nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("HTTP %s", response.Status)
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 1<<20))
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}
