# Network Observer

Network Observer is a self-hosted LAN connection and traffic monitor. The central service provides a Vue console, TLS gRPC ingestion, REST/WebSocket APIs, PostgreSQL metadata, ClickHouse traffic summaries, and Prometheus/Grafana monitoring.

The repository delivers the central service plus Linux Endpoint and initial Gateway Agent milestones. The endpoint agent inventories IPv4/IPv6 TCP/UDP sockets from `/proc/net`, uses Linux Socket Diag/TCP_INFO for available TCP counters, associates visible socket inodes with processes and cgroups, optionally enriches container IDs through the Docker Engine API, reports capability/degradation status, and queues observations in a crash-safe local WAL. The gateway agent reads Linux conntrack for IPv4/IPv6 TCP/UDP flow lifecycle and bidirectional counters, derives NAT tuple changes when the kernel exposes complete original/reply tuples, and can optionally receive NetFlow v5, fixed-length IPFIX templates/data records, and standard sFlow v5 sampled raw packets. Exported interface indexes are mapped to host interface names when possible; unavailable details remain blank and are reflected in capability status. Gateway eBPF counters, NetFlow v9, and variable-length IPFIX fields are not implemented in this milestone.

## Deploy the center from GHCR

Requirements: Docker Engine with Compose v2 and a DNS name for the NAS. The example certificate includes `localhost` and the selected DNS name. For a production installation, replace it with a certificate trusted by the devices that will use the console and connect agents. Run the commands from the repository root; the files under `examples/` intentionally contain no `build:` section and pull fixed-version images from GHCR.

1. Copy `.env.example` to `.env` and replace every `change-this-*` value with a unique secret. Keep the PostgreSQL and ClickHouse passwords URL-safe, or percent-encode reserved characters in their DSNs.
2. Generate a development certificate, passing the DNS name clients use:

   ```powershell
   & "C:\Program Files\Git\bin\bash.exe" -lc "./scripts/generate-dev-certificate.sh observer.example.lan"
   ```

3. Start the core `light` services (PostgreSQL、ClickHouse、Center 和 Web):

   ```powershell
   docker compose --env-file .env -f examples/center/light/docker-compose.yml pull
   docker compose --env-file .env -f examples/center/light/docker-compose.yml up -d
   docker compose ps
   ```

   `pro` 在核心部署上叠加 Prometheus、Node Exporter 和 Grafana：

   ```powershell
   docker compose --env-file .env -f examples/center/light/docker-compose.yml -f examples/center/pro/docker-compose.yml pull
   docker compose --env-file .env -f examples/center/light/docker-compose.yml -f examples/center/pro/docker-compose.yml up -d
   ```

   `NETWORK_OBSERVER_VERSION` 可覆盖默认的固定版本，例如在发布新版本后执行：

   ```powershell
   $env:NETWORK_OBSERVER_VERSION = "v0.3.2"
   docker compose --env-file .env -f examples/center/light/docker-compose.yml pull
   ```

Open `https://<NAS-name>:8443` for the console and `https://<NAS-name>:8443/metrics` for low-cardinality service metrics. In `pro`, open `http://<NAS-name>:3000` for Grafana; Prometheus remains reachable only on the internal Compose network. Endpoint and Gateway Agents connect to `<NAS-name>:8444` after registration.

Each Agent chooses its own transport security with the `security_mode` field. The Center's secure gRPC listener is `GRPC_ADDR` (published as `8444` by Compose). The plaintext listener is disabled by default; enable it with the Compose override `deploy/docker-compose.insecure-agent.yml`, which listens on `8445`. An Agent with `security_mode=true` uses TLS plus the persistent enrollment token and needs the Center CA; an Agent with `security_mode=false` uses the same token over plaintext and needs no CA. Plaintext exposes the token to network observers, so use it only on a trusted LAN. The Web console remains HTTPS in both modes.

PostgreSQL and ClickHouse use named volumes in `light`; `pro` adds separate Prometheus and Grafana volumes. `docker compose down` preserves data; `docker compose down -v` deletes the selected deployment volumes.

When an endpoint and gateway report the same normalized TCP/UDP five-tuple within the association window, the center keeps both live perspectives but suppresses the gateway from minute traffic once an exact endpoint sighting is known. Gateway traffic remains the fallback when endpoint counters are unavailable or the gateway sighting arrives first; the association state is persisted in PostgreSQL for restart recovery.

## Agent Docker deployment from GHCR

Center 的“设备 → 添加设备”会生成一次性注册 Token。注册完成后，把对应配置写入示例目录，再直接拉取 GHCR Agent 镜像：

```powershell
New-Item -ItemType Directory -Force examples/agent/config | Out-Null
Copy-Item deploy/tls/server.crt examples/agent/config/ca.crt
docker pull ghcr.io/jonysun/network-observer-agent:v0.3.2
docker run --rm --network host -v "${PWD}/examples/agent/config:/etc/network-observer" ghcr.io/jonysun/network-observer-agent:v0.3.2 register --server <NAS名称>:8444 --ca /etc/network-observer/ca.crt --security-mode=true --token <一次性Token> --name <设备名称> --type endpoint --config /etc/network-observer/agent.json --wal /var/lib/network-observer/agent.wal
docker compose --env-file .env -f examples/agent/docker-compose.yml up -d
```

先在 Center 的“设备 → 添加设备”生成 Endpoint Token，并用 Center 显示的安装命令会更简单；上面的命令展示了如何用示例 Compose 手动注册。注册命令需要在 `examples/agent/config` 中生成 `agent.json`，然后 Compose 会只读挂载该配置。网关把 `--type endpoint` 改为 `--type gateway`，使用 `gateway-docker-compose.yml`，并把生成的配置文件名改为 `gateway.json`。网关容器以 root 运行以读取常见的 conntrack 文件；Endpoint 容器使用主机网络、主机 PID、只读 Docker Socket 和持久化 WAL 卷。

所有示例 Compose 都支持 `NETWORK_OBSERVER_VERSION`，并且固定默认版本，不使用 `latest`。如果需要从源码构建，请使用仓库根目录的 `docker-compose.light.yml` 和 `docker-compose.pro.yml`；这两份开发 Compose 保留了 `build:` 配置。

## Local development

Requirements: Go 1.27+, Node.js 24+, PostgreSQL 18, and ClickHouse 25. Start the Go service with the required environment from `.env`; run the console separately with `cd web && npm ci && npm run dev`. The Vite development server proxies REST, WebSocket, metrics, and health requests to `localhost:8080`.

## Included behavior

- Single-admin login, one-time device enrollment tokens, device registration records, capability status, and individually revocable device credentials. Use the console's `设备 → 添加设备` action to generate a one-time token and copy Docker Compose, `docker run`, or native systemd installation commands. The token is shown only once and is replaced by an independent device credential after successful registration.
- Active connection views only; ClickHouse keeps live snapshots for up to 15 minutes and does not expose per-connection history search.
- One-minute traffic summaries with configurable retention (90 days by default).
- Persistent attribution rules and single-connection overrides with audit records.
- Prometheus service metrics without device, flow, IP, port, process, or container labels. Node Exporter feeds the host-disk dashboard and a 15% free-space alert rule.
- TLS browser access through Nginx; Agents independently choose the secure or optional plaintext gRPC listener.

The center does not alter routes, firewall rules, conntrack settings, Docker networks, or application payloads.

Each Agent can independently set `ebpf_mode` to `auto`, `on`, or `off`, and can opt into DNS metadata with `dns_metadata_enabled`. DNS collection is off by default and stores only visible UDP/53 query names, answer IPs, TTLs, and timestamps; it never stores application payloads. The endpoint now includes a CO-RE cgroup-skb source (`internal/agent/bpf/flow_counter.c`) for bidirectional IPv4/IPv6 TCP/UDP packet and skb-byte counters; the release image still uses the Socket Diag fallback unless a Linux build packages and attaches the generated object. `auto` reports a degraded capability and continues existing Socket Diag/procfs or gateway collectors when eBPF support is absent; `on` refuses startup unless kernel BTF, bpffs, map creation, and the collector prerequisites are available. Endpoint eBPF byte counters use observed kernel skb lengths, not application payload bytes. The Center optionally loads operator-supplied MaxMind-compatible offline MMDB files using `IP_ENRICHMENT_ASN_MMDB` and `IP_ENRICHMENT_CITY_MMDB`; missing, private, reserved, or unmatched IPs are shown as unknown. The operator is responsible for licensing and updating those files.

Traffic history is queryable through `/api/v1/traffic/summary` and `/api/v1/traffic/timeseries`; persistent flow presence is available at `/api/v1/presence`, offline IP enrichment at `/api/v1/enrichment`, and visible DNS relationships at `/api/v1/dns/relationships`. Traffic and presence retain 90 days by default; DNS metadata retains 7 days.

The console's `项目进度` panel reads and writes `/api/v1/project/todos`. It is seeded with the current implementation and rollout tasks, shows completion and blockers, and lets the administrator add, edit, complete, block, or delete personal ToDos. Todo changes are stored in PostgreSQL and audited.

The current Endpoint observation granularity is: connection tuple and lifecycle, source/destination ports, interface name, process PID/name, cgroup/container ID, optional Docker container name and Compose service, and host identity through the registered device ID/name. TCP counters are exact when Socket Diag exposes the extended `TCP_INFO` structure. UDP counters remain unavailable until the eBPF collector is installed; process and container fields can be unknown when host `/proc` permissions prevent inode ownership lookup. Docker metadata is reported as `unavailable` when the socket is absent and `degraded` when the socket or API cannot be read.

## Endpoint agent

Build the agent on a Linux host with Go 1.27.4 or use the supplied agent image. The Center's `设备 → 添加设备` action creates a one-time token. Registration exchanges it for a device-specific credential, writes an owner-readable configuration file, and refuses to overwrite an existing identity:

```bash
sudo install -d -m 700 /etc/network-observer /var/lib/network-observer
sudo /usr/local/bin/network-observer-agent register \
  --server observer.example.lan:8444 \
  --ca /etc/network-observer/ca.crt \
  --token "$ENROLLMENT_TOKEN" \
  --name lab-endpoint-01
sudo systemctl enable --now network-observer-agent
```

For an encrypted Agent, point it at the Center's secure listener and keep `security_mode=true`:

```bash
sudo /usr/local/bin/network-observer-agent register \
  --server observer.example.lan:8444 \
  --ca /etc/network-observer/ca.crt \
  --security-mode=true \
  --token "$ENROLLMENT_TOKEN" \
  --name lab-endpoint-01
```

The resulting Agent config keeps only the device credential and uses it for every reconnect. To use plaintext for one Agent, start the Center with `docker compose -f docker-compose.light.yml -f deploy/docker-compose.insecure-agent.yml up -d`, point that Agent at port `8445`, and register with `--security-mode=false`; omit `--ca`. Other Agents can continue using the secure port `8444`. Devices can still be revoked individually from the console.

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
