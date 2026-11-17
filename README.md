# Network Observer

English | [简体中文](README.zh-CN.md) | [中文详细安装与运维指南](docs/installation.zh-CN.md)

Network Observer is a self-hosted LAN connection and traffic monitor. The central service provides a Vue console, TLS gRPC ingestion, REST/WebSocket APIs, PostgreSQL metadata, ClickHouse traffic summaries, and Prometheus/Grafana monitoring.

The repository delivers the central service plus Linux Endpoint and initial Gateway Agent milestones. The endpoint agent inventories IPv4/IPv6 TCP/UDP sockets from `/proc/net`, uses Linux Socket Diag/TCP_INFO for available TCP counters, associates visible socket inodes with processes and cgroups, optionally enriches container IDs through the Docker Engine API, reports capability/degradation status, and queues observations in a crash-safe local WAL. The gateway agent reads Linux conntrack for IPv4/IPv6 TCP/UDP flow lifecycle and bidirectional counters, derives NAT tuple changes when the kernel exposes complete original/reply tuples, and can optionally receive NetFlow v5, fixed-length IPFIX templates/data records, and standard sFlow v5 sampled raw packets. Exported interface indexes are mapped to host interface names when possible; unavailable details remain blank and are reflected in capability status. Gateway eBPF counters, NetFlow v9, and variable-length IPFIX fields are not implemented in this milestone.

## Deploy the center from GHCR

Requirements: Docker Engine with Compose v2 and a reachable DNS name or IP for the NAS. Center automatically creates a self-signed certificate in a persistent Docker volume on first startup. For a production installation, replace it with a suitable trusted certificate. Run the commands from the repository root; the files under `examples/` contain no `build:` section and pull fixed-version images from GHCR.

Choose the installation instructions for your environment:

| Installation | Guide |
| --- | --- |
| NAS / Linux, core services | [Center Light](docs/installation.zh-CN.md#center-light) |
| Core services + Prometheus / Node Exporter / Grafana | [Center Pro](docs/installation.zh-CN.md#center-pro) |
| Manage the GHCR Compose files directly | [Manual Compose](docs/installation.zh-CN.md#center-compose) |
| Test the Center on Windows | [Docker Desktop + Git Bash](docs/installation.zh-CN.md#center-windows) |
| Agent install commands from the console | [Device wizard](docs/installation.zh-CN.md#agent-wizard) |
| Agent without an installer | [Manual Docker](docs/installation.zh-CN.md#agent-docker-manual) / [native systemd](docs/installation.zh-CN.md#agent-native) |
| Linux / OpenWrt gateway | [Gateway Agent](docs/installation.zh-CN.md#agent-gateway) |
| Compile your own images | [Source deployment](docs/installation.zh-CN.md#source-build) |
| Certificates, collection switches, upgrades and troubleshooting | [Full installation and operations guide](docs/installation.zh-CN.md) |

第一次发布后，若 GHCR 包是 Private，请在 GitHub 的 [Center 镜像包设置](https://github.com/users/jonysun/packages/container/network-observer/settings) 和 [Agent 镜像包设置](https://github.com/users/jonysun/packages/container/network-observer-agent/settings) 中将 Visibility 设为 Public。此设置由 GitHub 包所有者管理，Release workflow 只负责发布镜像，不会修改包权限。也可以保留 Private 并在部署机先登录 GHCR。

1. Copy `.env.example` to `.env`, set `TLS_CERT_HOST` to the NAS DNS name or IP without a scheme or port, and replace every `change-this-*` secret. Use URL-safe database passwords. ASN / GeoIP downloads automatically on first Center startup; no enrichment environment variables are needed.
2. Start Light (PostgreSQL, ClickHouse, Center and Web) from the NAS / Linux terminal:

   ```bash
   bash scripts/start-center.sh light
   ```

   Center creates the missing self-signed certificate before opening its gRPC and HTTP services. Nginx reads the same persistent TLS volume after Center becomes healthy. No certificate command or certificate container is required.

   For Pro, set the Grafana password and add the monitoring services:

   ```bash
   bash scripts/start-center.sh pro
   ```

   Windows PowerShell must explicitly invoke Git Bash; see the [Windows guide](docs/installation.zh-CN.md#center-windows) for line-ending preparation:

   ```powershell
   & "C:\Program Files\Git\bin\bash.exe" -lc 'bash scripts/start-center.sh light'
   ```

   The first run creates `.env` with random database, admin, and Grafana passwords, detects a usable LAN address for `TLS_CERT_HOST`, and starts the Center. Read the generated admin password from `.env`. To use a DNS name instead, run `TLS_CERT_HOST=observer.example.lan bash scripts/start-center.sh light`.

   Set `NETWORK_OBSERVER_VERSION=v0.3.20` in `.env` to pin or change the published version. To fetch images explicitly:

   ```bash
   docker compose --env-file .env -f examples/center/light/docker-compose.yml pull
   ```

Open `https://<NAS-name>:8443` for the console and `https://<NAS-name>:8443/metrics` for low-cardinality service metrics. In `pro`, open `http://<NAS-name>:3000` for Grafana; Prometheus remains reachable only on the internal Compose network. Endpoint and Gateway Agents connect to `<NAS-name>:8444` after registration.

Each Agent chooses its own transport security with `security_mode`. The Center's secure gRPC listener is published as `8444`. The plaintext listener is disabled by default; enable `deploy/docker-compose.insecure-agent.yml` to publish `8445`. Both modes register using a one-time token and reconnect with an independent device credential. `true` requires the Center trust certificate; `false` sends credentials and observations over plaintext. The Web console remains HTTPS in both modes. See [transport configuration](docs/installation.zh-CN.md#agent-options).

PostgreSQL and ClickHouse use named volumes in `light`; `pro` adds separate Prometheus and Grafana volumes. `docker compose down` preserves data; `docker compose down -v` deletes the selected deployment volumes.

When an endpoint and gateway report the same normalized TCP/UDP five-tuple within the association window, the center keeps both live perspectives but suppresses the gateway from minute traffic once an exact endpoint sighting is known. Gateway traffic remains the fallback when endpoint counters are unavailable or the gateway sighting arrives first; the association state is persisted in PostgreSQL for restart recovery.

## Agent Docker deployment from GHCR

For Linux commands, registration, persistent paths, and systemd alternatives, use the [Chinese installation guide](docs/installation.zh-CN.md#agent-wizard). Run Agent commands on the monitored Linux host. The native installer downloads and verifies the matching architecture binary before creating the systemd service.

Center 的“设备 → 添加设备”会生成一次性注册 Token。注册完成后，把对应配置写入示例目录，再直接拉取 GHCR Agent 镜像：

```powershell
New-Item -ItemType Directory -Force examples/agent/config | Out-Null
curl.exe -k https://<NAS名称>:8443/.well-known/network-observer-ca.pem -o examples/agent/config/ca.crt
docker pull ghcr.io/jonysun/network-observer-agent:v0.3.20
docker run --rm --network host -v "${PWD}/examples/agent/config:/etc/network-observer" ghcr.io/jonysun/network-observer-agent:v0.3.20 register --server <NAS名称>:8444 --tls-server-name <证书SAN名称> --ca /etc/network-observer/ca.crt --security-mode=true --token <一次性Token> --name <设备名称> --type endpoint --config /etc/network-observer/agent.json --wal /var/lib/network-observer/agent.wal
docker compose --env-file .env -f examples/agent/docker-compose.yml up -d
```

先在 Center 的“设备 → 添加设备”生成 Endpoint Token，并用 Center 显示的安装命令会更简单；上面的命令展示了如何用示例 Compose 手动注册。注册命令需要在 `examples/agent/config` 中生成 `agent.json`，然后 Compose 会只读挂载该配置。网关把 `--type endpoint` 改为 `--type gateway`，使用 `gateway-docker-compose.yml`，并把生成的配置文件名改为 `gateway.json`。网关容器以 root 运行以读取常见的 conntrack 文件；Endpoint 容器使用主机网络、主机 PID、只读 Docker Socket 和持久化 WAL 卷。

所有示例 Compose 都支持 `NETWORK_OBSERVER_VERSION`，并且固定默认版本，不使用 `latest`。如果需要从源码构建，请使用仓库根目录的 `docker-compose.light.yml` 和 `docker-compose.pro.yml`；这两份开发 Compose 保留了 `build:` 配置。

## Local development

Requirements: Go 1.27+, Node.js 24+, PostgreSQL 18, and ClickHouse 25. Start the Go service with the required environment from `.env`; run the console separately with `cd web && npm ci && npm run dev`. The Vite development server proxies REST, WebSocket, metrics, and health requests to `localhost:8080`.

## Included behavior

- Single-admin login, one-time device enrollment tokens, device registration records, capability status, and individually revocable device credentials. Use `设备 → 添加设备` to generate a one-time token and Docker installation commands. Native systemd installation is documented separately and verifies release checksums. Tokens are shown only once and are replaced by independent device credentials after registration.
- Active connection views only; ClickHouse keeps live snapshots for up to 15 minutes and does not expose per-connection history search.
- One-minute traffic summaries with configurable retention (90 days by default).
- Persistent attribution rules and single-connection overrides with audit records.
- Prometheus service metrics without device, flow, IP, port, process, or container labels. Node Exporter feeds the host-disk dashboard and a 15% free-space alert rule.
- TLS browser access through Nginx; Agents independently choose the secure or optional plaintext gRPC listener.

The center does not alter routes, firewall rules, conntrack settings, Docker networks, or application payloads.

Each Agent can independently set `ebpf_mode` and `dns_metadata_enabled`. DNS is off by default and stores only visible UDP/53 names, answer IPs, TTLs and timestamps. The Agent image packages the cgroup-skb eBPF object, while native release binaries require it to be installed separately. Use `auto` for initial deployment; unavailable hooks fall back to other collectors, and forced `on` mode still has implementation limitations. Actual coverage and byte accuracy require Linux validation. Counters use observed skb lengths rather than application payload bytes. The Center downloads GeoLite2 ASN and City MMDB files on first startup into a persistent Docker volume and checks for updates daily. Validated updates switch in memory without restarting the Center; failures keep the active version. Private, reserved or unmatched IPs remain unknown. The console attributes GeoLite data to MaxMind. See [collection settings](docs/installation.zh-CN.md#agent-options) and [offline enrichment](docs/installation.zh-CN.md#enrichment).

Traffic history is queryable through `/api/v1/traffic/summary` and `/api/v1/traffic/timeseries`; persistent flow presence is available at `/api/v1/presence`, offline IP enrichment at `/api/v1/enrichment`, and visible DNS relationships at `/api/v1/dns/relationships`. Traffic and presence retain 90 days by default; DNS metadata retains 7 days.

The console's `项目进度` panel reads and writes `/api/v1/project/todos`. It is seeded with the current implementation and rollout tasks, shows completion and blockers, and lets the administrator add, edit, complete, block, or delete personal ToDos. Todo changes are stored in PostgreSQL and audited.

The Endpoint observes connection tuples and lifecycle, ports, available interface names, PID/process names, cgroup/container IDs, optional Docker names and Compose services, and registered host identity. Socket Diag can supply TCP counters; UDP counters depend on usable eBPF or gateway counters. Unknown owners, inaccessible namespaces and missing Socket metadata remain explicit. Installation alone does not verify all container namespaces, short connections or counter accuracy.

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
