# Network Observer

Network Observer is a self-hosted LAN connection and traffic monitor. The central service provides a Vue console, TLS gRPC ingestion, REST/WebSocket APIs, PostgreSQL metadata, ClickHouse traffic summaries, and Prometheus/Grafana monitoring.

The repository delivers the central service plus Linux Endpoint and initial Gateway Agent milestones. The endpoint agent inventories IPv4/IPv6 TCP/UDP sockets from `/proc/net`, uses Linux Socket Diag/TCP_INFO for available TCP counters, associates visible socket inodes with processes and cgroups, optionally enriches container IDs through the Docker Engine API, reports capability/degradation status, and queues observations in a crash-safe local WAL. The gateway agent reads Linux conntrack for IPv4/IPv6 TCP/UDP flow lifecycle and bidirectional counters, derives NAT tuple changes when the kernel exposes complete original/reply tuples, and can optionally receive NetFlow v5, fixed-length IPFIX templates/data records, and standard sFlow v5 sampled raw packets. Exported interface indexes are mapped to host interface names when possible; unavailable details remain blank and are reflected in capability status. Gateway eBPF counters, NetFlow v9, and variable-length IPFIX fields are not implemented in this milestone.

## Deploy the center

Requirements: Docker Engine with Compose v2 and a DNS name for the NAS. The example certificate includes `localhost` and the selected DNS name. For a production installation, replace it with a certificate trusted by the devices that will use the console and connect agents.

1. Copy `.env.example` to `.env` and replace every `change-this-*` value with a unique secret. Keep the PostgreSQL and ClickHouse passwords URL-safe, or percent-encode reserved characters in their DSNs.
2. Generate a development certificate, passing the DNS name clients use:

   ```powershell
   & "C:\Program Files\Git\bin\bash.exe" -lc "./scripts/generate-dev-certificate.sh observer.example.lan"
   ```

3. Start the services:

   ```powershell
   docker compose up --build -d
   docker compose ps
   ```

Open `https://<NAS-name>:8443` for the console, `https://<NAS-name>:8443/metrics` for low-cardinality service metrics, and `http://<NAS-name>:3000` for Grafana. Prometheus is only reachable on the internal Compose network. Endpoint and initial Gateway Agents connect to `<NAS-name>:8444` after registration.

PostgreSQL, ClickHouse, Prometheus, and Grafana each use a separate named volume. `docker compose down` preserves the data; `docker compose down -v` deletes all four volumes.

When an endpoint and gateway report the same normalized TCP/UDP five-tuple within the association window, the center keeps both live perspectives but suppresses the gateway from minute traffic once an exact endpoint sighting is known. Gateway traffic remains the fallback when endpoint counters are unavailable or the gateway sighting arrives first; the association state is persisted in PostgreSQL for restart recovery.

## Local development

Requirements: Go 1.27+, Node.js 24+, PostgreSQL 18, and ClickHouse 25. Start the Go service with the required environment from `.env`; run the console separately with `cd web && npm ci && npm run dev`. The Vite development server proxies REST, WebSocket, metrics, and health requests to `localhost:8080`.

## Included behavior

- Single-admin login, device registration records, capability status, and individually revocable device credentials.
- Active connection views only; ClickHouse keeps live snapshots for up to 15 minutes and does not expose per-connection history search.
- One-minute traffic summaries with configurable retention (90 days by default).
- Persistent attribution rules and single-connection overrides with audit records.
- Prometheus service metrics without device, flow, IP, port, process, or container labels. Node Exporter feeds the host-disk dashboard and a 15% free-space alert rule.
- TLS browser access through Nginx and a separate TLS gRPC listener for agents.

The center does not alter routes, firewall rules, conntrack settings, Docker networks, or application payloads.

The current Endpoint observation granularity is: connection tuple and lifecycle, source/destination ports, interface name, process PID/name, cgroup/container ID, optional Docker container name and Compose service, and host identity through the registered device ID/name. TCP counters are exact when Socket Diag exposes the extended `TCP_INFO` structure. UDP counters remain unavailable until the eBPF collector is installed; process and container fields can be unknown when host `/proc` permissions prevent inode ownership lookup. Docker metadata is reported as `unavailable` when the socket is absent and `degraded` when the socket or API cannot be read.

## Endpoint agent

Build the agent on a Linux host with Go 1.27.4 or use the supplied agent image. The registration command creates a device-scoped credential and writes an owner-readable configuration file; it refuses to overwrite an existing identity:

```bash
sudo install -d -m 700 /etc/network-observer /var/lib/network-observer
sudo /usr/local/bin/network-observer-agent register \
  --server observer.example.lan:8444 \
  --ca /etc/network-observer/ca.crt \
  --token "$ENROLLMENT_TOKEN" \
  --name lab-endpoint-01
sudo systemctl enable --now network-observer-agent
```

Install `deploy/agent/network-observer-agent.service` and the agent binary under `/usr/local/bin`. The service runs with access to host `/proc` and `/sys`, keeps the WAL at `/var/lib/network-observer/agent.wal`, and reconnects with exponential backoff. It never discards an old WAL record silently: a full WAL or an unacknowledged record older than 24 hours is reported as a `data_gap` capability. `deploy/agent/docker-compose.yml` is available for a host-network, host-PID Docker deployment; copy `config.example.json` into `deploy/agent/config/agent.json` after registration and mount the center CA as `ca.crt`.

For a local Linux capability check without connecting to a center:

```bash
network-observer-agent snapshot
```

The JSON output includes the current socket observations and the exact capability states. TCP observations use `counter_source=socket_diag_tcp_info` when supported; UDP observations remain `counter_source=unavailable` until the eBPF collector milestone is installed.

The same command can inspect a gateway conntrack file without connecting to a center:

```bash
network-observer-agent snapshot --type gateway --conntrack /proc/net/nf_conntrack
```

Register a gateway with `register --type gateway`, then use `deploy/agent/gateway-config.example.json` as the starting configuration. OpenWrt systems may expose conntrack at `/proc/net/ip_conntrack`; set `conntrack_path` accordingly. The NetFlow/IPFIX/sFlow listeners are optional UDP inputs; leave the fields empty to disable them, and expose the selected UDP ports on the gateway host. A listener continues to report records if conntrack is unavailable; receiver capabilities include datagram, parse-error, and queue-drop counts. Exported flows close after 60 seconds without another record. NetFlow/IPFIX records are exact unless their exporter indicates sampling; sFlow records are marked estimated when the sampling rate is greater than one. `deploy/agent/gateway-docker-compose.yml` provides the host-network Docker form and runs as root because conntrack files are commonly root-readable only; the systemd unit uses the same configuration-driven collector.

When running the agent with `deploy/agent/docker-compose.yml`, set `DOCKER_GID` to the group ID of the host Docker socket (for example, `stat -c %g /var/run/docker.sock`) so the non-root container can read Docker metadata. The agent only issues GET requests to the socket; the socket itself still grants whatever access the host Docker group permits.
