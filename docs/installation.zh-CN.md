# Network Observer 中文安装与运维指南

[返回中文首页](../README.zh-CN.md) · [English README](../README.md)

本指南对应当前仓库的安装文件，默认使用 `v0.3.15` 的 GHCR 镜像和 Linux 二进制。安装脚本可能晚于该版本加入，先获取当前仓库，再选择固定版本运行；不要假定旧标签包含最新脚本。所有 Center 命令都在仓库根目录执行，Agent 命令则在被监控主机执行。

## 安装方式目录

| 场景 | 章节 |
| --- | --- |
| 准备 Docker、目录、密码、端口 | [安装前准备](#prerequisites) |
| 绿联 NAS / Linux，先跑核心功能 | [Center Light](#center-light) |
| 增加 Prometheus、Node Exporter、Grafana | [Center Pro](#center-pro) |
| 不用启动脚本，自己管理 Compose | [手动 Compose](#center-compose) |
| Windows 上测试 Center | [Windows / Git Bash](#center-windows) |
| 已有正式证书或需要换证书 | [TLS 与证书](#certificates) |
| 从 Center 复制命令安装 Agent | [添加设备向导](#agent-wizard) |
| Agent Docker Compose / docker run / 菜单安装 | [Docker 安装器](#agent-docker-installer) |
| Agent 不用远程安装脚本 | [手动 Docker 注册与运行](#agent-docker-manual) |
| Agent 不用 Docker | [原生二进制 + systemd](#agent-native) |
| Linux / OpenWrt 网关 | [Gateway Agent](#agent-gateway) |
| TLS / 明文、eBPF、DNS、容器归属 | [Agent 配置](#agent-options) |
| 离线 ASN / GeoIP 库 | [地址归属库](#enrichment) |
| 修改代码或自行构建镜像 | [源码部署](#source-build) |
| 日常命令、升级、备份、停止与卸载 | [运维](#operations) |
| 常见错误与检查顺序 | [故障排查](#troubleshooting) |

<a id="prerequisites"></a>
## 1. 安装前准备

### 平台与依赖

| 位置 | 要求 |
| --- | --- |
| Center | Linux 主机或 NAS，Docker Engine + Compose v2；镜像支持 amd64 / arm64 |
| Center 启动脚本 | Git、Bash（可选）；证书由 Center 自动初始化 |
| Agent Docker | Linux 主机、Docker；Compose 方式另需 Compose v2 |
| Agent 原生 | Linux amd64 / arm64；systemd 用于后台运行 |
| Agent 安装器 | root、Bash、curl、OpenSSL、coreutils；原生下载校验另用 sha256sum |
| OpenWrt | 先确认架构、Docker、内核与存储能力；v1 不提供 procd 安装 |
| Windows | 仅建议测试 Center；Docker Desktop 使用 Linux 容器，安装 Git for Windows；不提供 Windows 原生 Agent |

Docker 安装按[官方 Linux 安装文档](https://docs.docker.com/engine/install/)及 [Compose 插件文档](https://docs.docker.com/compose/install/linux/)执行。绿联 NAS 可先在应用中心安装并启动 Docker，再开启 SSH，在真实 NAS 文件系统中准备项目目录。NAS 无法使用 `apt-get` 时使用系统已有工具；不要套用其他发行版的包管理命令。

Debian / Ubuntu 可先补齐辅助工具（这一步不安装 Docker）：

```bash
sudo apt-get update
sudo apt-get install -y git bash curl openssl ca-certificates coreutils
docker version
docker compose version
```

本文 Linux 示例要求当前用户能运行 Docker；否则先进入 root 终端，或在 Docker 命令前使用 `sudo`。非 root 用户能访问 Docker daemon 本身就具有较高的宿主机权限。

### 网络与默认端口

示例统一使用 NAS 地址 `192.168.1.20`，请替换为自己的地址。

| 端口 | 用途 | 哪些设备需要访问 |
| --- | --- | --- |
| TCP 8443 | Web 控制台、REST、WebSocket、证书下载 | 浏览器；使用安装器下载 CA 的 Agent |
| TCP 8444 | 加密 gRPC 上报 | `security_mode=true` 的 Agent |
| TCP 8445 | 可选明文 gRPC；默认关闭 | `security_mode=false` 的 Agent |
| TCP 3000 | Pro 的 Grafana；默认 HTTP | 需要指标仪表盘的浏览器 |
| UDP 2055 / 4739 / 6343 | 可选 NetFlow / IPFIX / sFlow 接收 | 向 Gateway Agent 导出流量的设备；配置监听后才启用 |

PostgreSQL、ClickHouse、Prometheus 和 Node Exporter 默认没有发布到宿主机。Agent 主动连接 Center；普通 Endpoint 不需要对外开放接收端口。

NAS 上 `8443` 或 `3000` 已被其他应用占用时，在 `.env` 修改 `WEB_PORT` 或 `GRAFANA_PORT`。改 Web 端口后，添加设备时也要填新的 Web 地址。改 `AGENT_GRPC_PORT` 后，Agent 必须使用新的上报端口。

### 获取项目与配置

```bash
git clone https://github.com/jonysun/Network-Observer.git network-observer
cd network-observer
bash scripts/start-center.sh light
```

首次使用时，脚本会自动创建 `.env`，生成随机数据库、管理员和 Grafana 密码，并尽量检测局域网地址；已有 `.env` 时不会覆盖。需要手动准备或审阅字段时，也可以先执行 `cp .env.example .env`，再运行启动脚本。下面是需要关注的字段：

| 配置项 | 填写说明 |
| --- | --- |
| `POSTGRES_PASSWORD` | PostgreSQL 密码；首次初始化后修改 `.env` 不会自动修改数据库内的密码 |
| `CLICKHOUSE_PASSWORD` | ClickHouse 密码；推荐与其他密码不同 |
| `ADMIN_USERNAME` / `ADMIN_PASSWORD` | 首次管理员账号；数据库已有管理员后不会因改 `.env` 自动重置 |
| `TLS_CERT_HOST` | 实际访问 Center 的域名或 IP，例 `192.168.1.20`；不带 `https://`、端口或斜杠 |
| `WEB_PORT` / `AGENT_GRPC_PORT` | 默认 `8443` / `8444` |
| `NETWORK_OBSERVER_VERSION` | 可自行添加，默认镜像版本是 `v0.3.15`；不使用 `latest` |
| `SUMMARY_RETENTION_DAYS` | 默认 `90`，必须是正整数 |
| `ENROLLMENT_TOKEN` | 新部署留空；只兼容已注册旧设备，不能拿来注册新设备 |
| `GRAFANA_ADMIN_USER` / `GRAFANA_ADMIN_PASSWORD` | 仅 Pro 使用；默认用户名 `admin`，密码自行设置 |

可用 `openssl rand -hex 24` 在本机逐个生成随机密码并写入 `.env`。Compose 直接把数据库密码放进 DSN，首次安装使用十六进制密码最省事。若使用 `@`、`:`、`/`、`#` 等字符，还需要处理 URL 编码，不建议首次测试这样设置。

`SERVER_PUBLIC_URL` 已移除。Compose 自己设置容器内的 `TLS_CERT_FILE` / `TLS_KEY_FILE`；使用示例 Compose 时无需在 `.env` 填它们。`.env` 中的 `HTTP_ADDR`、`GRPC_ADDR`、数据库 DSN 主要用于直接运行 Go 服务，当前 Compose 固定容器内部监听地址并自己组装 DSN；`AGENT_WAL_MAX_BYTES` 也不会自动下发给 Agent，Agent 使用各自 JSON 配置。

<a id="center-light"></a>
## 2. 绿联 NAS / Linux 安装 Center Light

Light 包含 PostgreSQL、ClickHouse、Center、Nginx 共 4 个服务，已经能使用项目自己的实时和历史图表。

### 首次证书准备

不需要手动生成证书。Center 首次启动时在 `tls-data` 持久卷中自动创建自签名证书，Nginx 在 Center 健康后读取同一卷。旧版本如果把证书放在 `deploy/tls/`，首次升级会自动迁移完整证书对并继续使用原身份。请先把 `TLS_CERT_HOST` 设置为 Agent 和浏览器实际访问 Center 的域名或 IP。

`100:101` 是当前 Center 镜像中 `observer` 的 UID/GID。自建镜像若更换用户，可用下列命令查看后调整，而不是放宽私钥到全局可读：

```bash
docker run --rm --entrypoint id ghcr.io/jonysun/network-observer:v0.3.15 observer
```

### 启动与检查

```bash
bash scripts/start-center.sh light
docker compose --env-file .env -f examples/center/light/docker-compose.yml ps
```

`start-center.sh` 现在只负责选择 Light / Pro Compose；直接执行 Compose 也会触发相同的自动证书流程。证书和私钥都在 `tls-data` 卷中，只有一份文件或卷内容损坏时 Center 会报错退出。

启动脚本会在 `up` 前自动执行镜像拉取，默认失败重试 5 次、每次间隔 8 秒，适合 NAS 偶发的 GHCR TLS 握手超时。可按网络情况设置 `NETWORK_OBSERVER_PULL_RETRIES=10 NETWORK_OBSERVER_PULL_RETRY_DELAY=15`。如果日志出现 `write TLS certificate ... read-only file system`，说明 NAS 上的旧容器仍使用了只读 TLS 挂载；更新仓库后再次运行脚本会按当前 Compose 配置重建为可写挂载。

访问 `https://192.168.1.20:8443`。初次自签名证书提示属正常情况，先核对访问的是自己的 NAS，再按浏览器方式信任。使用 `.env` 中的管理员账号登录。

在终端检查服务是否就绪：

```bash
curl -k https://192.168.1.20:8443/readyz
docker compose --env-file .env -f examples/center/light/docker-compose.yml logs --tail=100 observer web
```

`readyz` 预期返回 `status=ready`；数据库第一次初始化需要时间，先查看日志，不必重新注册 Agent。

<a id="center-pro"></a>
## 3. 安装 Center Pro，或从 Light 增加监控组件

先完成 Light 的目录、密码、证书准备，再在 `.env` 设置 Grafana 密码：

```dotenv
GRAFANA_ADMIN_USER=admin
GRAFANA_ADMIN_PASSWORD=替换为独立的随机密码
GRAFANA_PORT=3000
```

```bash
bash scripts/start-center.sh pro
docker compose --env-file .env \
  -f examples/center/light/docker-compose.yml \
  -f examples/center/pro/docker-compose.yml ps
```

Pro 是 Light 加上 Prometheus、Node Exporter、Grafana，共 7 个服务；Agent 的安装方式相同，无需重新注册。访问 `http://192.168.1.20:3000`，使用 Grafana 自己的账号密码。Prometheus 数据源和 Network Observer Dashboard 通过配置自动加载。

Pro 需要更多内存、磁盘与主机挂载权限。先用 Light 验证数据，再根据需要添加 Pro。不要把 ClickHouse 与 Prometheus 混为一谈：前者保存连接流量分析数据，后者保存服务/主机指标。

<a id="center-compose"></a>
## 4. 手动使用 GHCR Compose

如果不使用 `start-center.sh`，填写 `.env` 后直接执行：

```bash
docker compose --env-file .env -f examples/center/light/docker-compose.yml config --quiet
docker compose --env-file .env -f examples/center/light/docker-compose.yml pull
docker compose --env-file .env -f examples/center/light/docker-compose.yml up -d
```

Pro 在每条命令中加上 `-f examples/center/pro/docker-compose.yml`。`pro/docker-compose.yml` 是叠加文件，不能独立启动。

`examples/` 只使用固定版本 `image:`，不会在 NAS 上编译 Go 或前端。根目录的 `docker-compose.light.yml` / `docker-compose.yml` 保留了 `build:`，用于源码开发，两种入口选一套持续使用，避免产生不同项目名的数据卷。

NAS 的图形化 Compose 管理器也可以使用这些文件，但要保留仓库目录结构：Light 文件通过 `../../../deploy/` 引用 Nginx 配置，证书使用命名卷 `tls-data` 自动保存。只复制一个 YAML 到新目录会导致挂载路径错误。若管理器只接受项目根目录的 Compose，复制一份 YAML 并将 Nginx 配置路径改成 NAS 上的绝对路径，同时上传 `.env`；证书卷仍需保留。

<a id="center-windows"></a>
## 5. Windows + Docker Desktop 测试 Center

Windows 示例使用 PowerShell 调用 Git Bash，路径含空格时保留 `&` 和双引号。测试对象是 Center；监控真实 Linux 主机的 Agent 仍应安装在对应 Linux 主机上。

```powershell
git clone https://github.com/jonysun/Network-Observer.git network-observer
Set-Location network-observer
Copy-Item .env.example .env
notepad .env
```

按第 1 节填写密码，把 `TLS_CERT_HOST` 设为测试使用的 IP 或 `localhost`，并清空未使用的 MMDB 路径。Git 在 Windows 上可能检出 CRLF，执行脚本前规范化行尾：

```powershell
& "C:\Program Files\Git\bin\bash.exe" -lc 'sed -i "s/\r$//" .env scripts/start-center.sh scripts/generate-dev-certificate.sh'
& "C:\Program Files\Git\bin\bash.exe" -lc 'bash scripts/start-center.sh light'
```

Pro 把命令中的 `light` 改为 `pro`。只做本机测试可打开 `https://localhost:8443`；要让其他主机连接，应生成包含 Windows 主机局域网地址的证书，并允许相关端口通过 Windows 防火墙。`localhost` 在 Agent 端表示 Agent 自己，不表示 Windows Center。

Windows bind mount 通常不能按 Linux 方式修改文件属主，因此上面的 Linux `chown` 不适用于 Windows 测试。Windows Docker Desktop 里的主机网络、PID、cgroup 也不代表 Windows 宿主机的网络栈，不能用它验证 Linux Agent 对真实主机的覆盖。

<a id="certificates"></a>
## 6. 自签名证书、正式证书与换证

- `TLS_CERT_HOST` 是 Center 自动生成自签名证书时使用的域名或 IP；不会创建 DNS 记录，不会配置 NAS 的域名解析，也不是自动申请公网证书。
- 生成脚本支持一个域名或 IP，附带 `localhost`、`127.0.0.1`。Agent 连接地址必须匹配证书 SAN。
- 自动生成的是有效期 825 天的自签名证书，目前没有自动续期任务。
- 证书与私钥分别保存在 Docker 命名卷 `tls-data` 的 `/run/tls/server.crt`、`/run/tls/server.key`，Nginx 和 Center 挂载同一卷；无需额外容器。
- `server.key` 只留在 Center，Agent 只需要公开证书 / 信任材料，不能复制 Center 私钥给 Agent。

需要使用正式证书时，将匹配的 PEM 证书链和私钥导入 `tls-data` 卷；已有完整证书会被保留。Center 的证书下载接口直接返回 `server.crt`，Agent 将其作为信任材料；正式证书更换 CA、链结构或服务器证书后，应同时验证 Agent 的信任文件。

更换证书时：先保存旧证书备份，确认新证书的域名 / IP 和私钥匹配，再替换文件并重启：

```bash
docker run --rm -v network-observer_tls-data:/tls alpine:3.22 \
  sh -c 'apk add --no-cache openssl >/dev/null && openssl x509 -in /tls/server.crt -noout -dates -ext subjectAltName -fingerprint -sha256'
docker compose --env-file .env -f examples/center/light/docker-compose.yml restart observer web
```

使用 Pro 时带上原来的 Pro 叠加文件。自签名证书更换后，已装 Agent 通常需要更新自己的 `ca.crt` 并重启；保留 `agent.json` 中的设备凭证，不要为换证删除身份重新注册。Center 添加设备界面的指纹也会随证书变化。

<a id="agent-wizard"></a>
## 7. 从 Center 添加设备并复制安装命令

在被监控 Linux 主机上准备 Bash、curl、OpenSSL，以及选择的 Docker 或 systemd 环境，然后：

1. 登录 Center，进入「设备 → 添加设备」。
2. 输入名称；类型选 `endpoint`，监控网关时选 `gateway`。
3. gRPC 填 `192.168.1.20:8444`，Web 填 `192.168.1.20:8443`。两处都填 Agent 能访问的地址，通常不用加协议前缀。
4. Agent 版本选择已发布固定版本，例如 `v0.3.15`；TLS 默认开启，Token 默认 24 小时有效。
5. 生成后，复制 Docker Compose 或 `docker run` 命令，在 Agent 主机的 root 终端执行。
6. 返回 Center 验证设备在线、能力与连接信息。每台设备单独生成 Token。

生成的命令会下载指定版本安装器，带上一次性 Token、设备名称、类型、版本、TLS 模式、证书指纹和证书 SAN 名称。即使 gRPC 地址使用 IP，也会用证书 SAN 名称验证 TLS。Token 仅显示一次且只能注册一台设备；注册后换为独立 `device_credential`。**升级使用已有配置，不能反复执行注册命令。**

原生安装器会先下载固定版本二进制和 `SHA256SUMS`，校验成功后才安装并注册 systemd 服务；校验失败会停止，不会写入可运行的二进制。

<a id="agent-docker-installer"></a>
## 8. Agent Docker Compose、docker run 与菜单安装

### Docker Compose 安装器

优先使用 Center 生成的命令。若想先下载脚本查看，再运行，先进入 root 终端，将向导中自己的参数填入下面命令：

```bash
sudo -i
curl -fL https://raw.githubusercontent.com/jonysun/Network-Observer/v0.3.15/scripts/install-agent.sh -o /tmp/network-observer-install-agent.sh
bash /tmp/network-observer-install-agent.sh \
  --mode docker-compose \
  --version v0.3.15 \
  --server 192.168.1.20:8444 \
  --token '替换为本机的一次性Token' \
  --name 'linux-endpoint-01' \
  --type endpoint \
  --security-mode true \
  --ca-url https://192.168.1.20:8443/.well-known/network-observer-ca.pem \
  --fingerprint '替换为Center显示的SHA256指纹' \
  --tls-server-name 192.168.1.20
```

`--tls-server-name` 必须填写 Center TLS 证书 SAN 中的 DNS 名称或 IP；例子假定证书为 `192.168.1.20` 签发。安装器下载 CA 时先允许自签名 HTTPS，然后核对指纹；指纹不匹配会停止安装。不要用随意下载的证书替代向导显示的信任信息。

脚本默认生成：

| 路径 | 用途 |
| --- | --- |
| `/etc/network-observer/agent.json` | 设备身份、凭证与采集配置 |
| `/etc/network-observer/ca.crt` | Center 信任证书 |
| `/etc/network-observer/docker-compose.yml` | Agent Compose 文件 |
| `/var/lib/network-observer/agent.wal` | 本地未确认上报数据 |

```bash
docker compose -f /etc/network-observer/docker-compose.yml ps
docker compose -f /etc/network-observer/docker-compose.yml logs --tail=100
```

### docker run 安装器

保持同一组参数，只将 `--mode docker-compose` 改为 `--mode docker-run`。它运行名为 `network-observer-agent` 的容器：

```bash
docker logs --tail=100 network-observer-agent
docker restart network-observer-agent
```

安装器生成的 Docker 方式使用 host 网络、host PID 和 `privileged: true`，便于首次验证 eBPF 等能力；这与仓库内细分 capability 的手动示例不同。需要收紧权限或读取 Docker 容器名称时，使用下一节的手动 Compose，并按实际权限验证。安装器当前没有自动挂载 Docker Socket，因此自动安装后容器名称可能未知。

### 菜单交互安装

下载脚本后，不提供 `--mode` 可以显示 `native / docker-compose / docker-run` 菜单。TLS 材料参数仍需要显式传入，脚本只交互询问模式、Center 地址、Token、设备名称：

```bash
sudo bash /tmp/network-observer-install-agent.sh \
  --ca-url https://192.168.1.20:8443/.well-known/network-observer-ca.pem \
  --fingerprint '替换为Center显示的SHA256指纹'
```

当前菜单选择 Docker 方式；原生方式使用手动步骤。已有配置时安装器会拒绝覆盖；需要排错先看日志和现有身份，不要直接删除配置来反复消耗 Token。

<a id="agent-docker-manual"></a>
## 9. 不用安装器：手动注册并运行 Agent Docker

以下在 **Agent 主机**执行，要求已获取仓库，当前目录为仓库根目录，使用 root 或能执行 Docker 的用户。可从 Center 的“添加设备”向导直接使用 `--ca-url` 下载公开证书并核对 SHA-256 指纹；手动流程下面假定证书已保存为 `/tmp/center.crt`。

```bash
sudo install -d -m 700 examples/agent/config
sudo install -m 644 /tmp/center.crt examples/agent/config/ca.crt
openssl x509 -in examples/agent/config/ca.crt -noout -fingerprint -sha256
```

在 Center 生成新的 Endpoint Token，注册时写入宿主机配置目录。不要把示例 JSON 里的虚构 ID 当作已注册身份。

```bash
docker run --rm --network host \
  -v "$(pwd)/examples/agent/config:/etc/network-observer" \
  ghcr.io/jonysun/network-observer-agent:v0.3.15 register \
  --server 192.168.1.20:8444 \
  --tls-server-name 192.168.1.20 \
  --ca /etc/network-observer/ca.crt \
  --security-mode=true \
  --token '替换为Endpoint一次性Token' \
  --name linux-endpoint-01 \
  --type endpoint \
  --config /etc/network-observer/agent.json \
  --wal /var/lib/network-observer/agent.wal
```

如果主机有 Docker Socket，希望显示容器名，可取得它的 GID，再启动示例 Compose：

```bash
export DOCKER_GID="$(stat -c %g /var/run/docker.sock)"
docker compose -f examples/agent/docker-compose.yml up -d
docker compose -f examples/agent/docker-compose.yml logs --tail=100 endpoint-agent
```

主机没有 `/var/run/docker.sock` 时，先从自己的 Compose 副本中移除对应挂载与 `group_add`，否则 Docker 可能创建同名目录，Agent 无法访问 Engine API。Docker Socket 的只读挂载不等于 API 权限隔离，Agent 只发送 GET，但能访问 Socket 的进程仍应被视为有较高权限。

示例通过独立命名卷保存 WAL，配置以只读方式挂载，保留 host 网络 / PID 和按需 capability。较旧内核不认识 `BPF` / `PERFMON` 时，应调整自己的 Compose 并关闭 eBPF，再检查 Socket Diag 降级采集；不要假定所有 NAS 内核都支持它们。

只想用 `docker run` 运行已生成的配置，也可以采用和安装器一致的权限形态：

```bash
docker run -d --name network-observer-agent --restart unless-stopped \
  --network host --pid host --privileged \
  -v "$(pwd)/examples/agent/config:/etc/network-observer:ro" \
  -v network-observer-agent-wal:/var/lib/network-observer \
  -v /sys:/sys:ro \
  ghcr.io/jonysun/network-observer-agent:v0.3.15 \
  run --config /etc/network-observer/agent.json
```

上面 Docker 注册配置中的 `ca_cert_file` 是容器路径，不能原样拿给宿主机原生服务运行。迁移运行方式时应保留设备凭证，调整证书和 WAL 路径。

<a id="agent-native"></a>
## 10. 原生二进制 + systemd 安装 Agent

此方式不需要 Docker。下面命令在 Agent Linux 主机执行，先准备 curl、OpenSSL、sha256sum 和 systemd；也可以直接使用上一节由 Center 生成的原生一键命令。

### 下载固定版本并校验

```bash
AGENT_VERSION=v0.3.15
case "$(uname -m)" in
  x86_64|amd64) AGENT_ARCH=amd64 ;;
  aarch64|arm64) AGENT_ARCH=arm64 ;;
  *) echo '当前发布包仅支持 amd64 / arm64'; exit 1 ;;
esac
AGENT_TMP="$(mktemp -d)"
AGENT_BASE="https://github.com/jonysun/Network-Observer/releases/download/${AGENT_VERSION}"
curl -fL "$AGENT_BASE/network-observer-agent-linux-$AGENT_ARCH" \
  -o "$AGENT_TMP/network-observer-agent-linux-$AGENT_ARCH"
curl -fL "$AGENT_BASE/SHA256SUMS" -o "$AGENT_TMP/SHA256SUMS"
(
  cd "$AGENT_TMP"
  grep " network-observer-agent-linux-${AGENT_ARCH}$" SHA256SUMS | sha256sum -c -
) && sudo install -m 755 "$AGENT_TMP/network-observer-agent-linux-$AGENT_ARCH" /usr/local/bin/network-observer-agent
```

只有校验成功才执行安装；如果任一下载或校验失败，停止后续步骤，先查网络、版本和架构。临时下载目录可在确认完成后自行清理。

### 注册身份

通过可信渠道从 Center 复制公开证书为 `/tmp/center.crt`，并核对其指纹，再准备目录：

```bash
sudo install -d -m 700 /etc/network-observer /var/lib/network-observer
sudo install -m 644 /tmp/center.crt /etc/network-observer/ca.crt
openssl x509 -in /etc/network-observer/ca.crt -noout -fingerprint -sha256
sudo /usr/local/bin/network-observer-agent register \
  --server 192.168.1.20:8444 \
  --ca /etc/network-observer/ca.crt \
  --security-mode=true \
  --token '替换为本机的一次性Token' \
  --name linux-native-01 \
  --type endpoint \
  --config /etc/network-observer/agent.json \
  --wal /var/lib/network-observer/agent.wal
```

不要重复注册到已有 `agent.json`。注册成功后先用前台方式验证：

```bash
sudo /usr/local/bin/network-observer-agent run --config /etc/network-observer/agent.json
```

确认设备在线后用 `Ctrl+C` 停止前台进程，再安装 systemd；不要让两个进程同时使用同一个身份和 WAL。

### 创建 systemd 服务

```bash
sudo tee /etc/systemd/system/network-observer-agent.service >/dev/null <<'EOF'
[Unit]
Description=Network Observer Agent
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=/usr/local/bin/network-observer-agent run --config /etc/network-observer/agent.json
Restart=on-failure
RestartSec=5s
User=root
NoNewPrivileges=true

[Install]
WantedBy=multi-user.target
EOF
sudo systemctl daemon-reload
sudo systemctl enable --now network-observer-agent
sudo systemctl status network-observer-agent --no-pager
sudo journalctl -u network-observer-agent -n 100 --no-pager
```

这里使用便于首次验证的基础服务；仓库另有 `deploy/agent/network-observer-agent.service` 加固示例。该示例的地址族限制未包含 DNS 抓包需要的 `AF_PACKET`，开启 DNS 前应在自己的 unit 中允许它并重载，不能直接照搬后宣称 DNS 可用。

原生发布包当前只含 Agent 二进制，**不附带 eBPF 对象文件**；默认 `auto` 会在对象缺失或内核/权限不满足时降级。需要原生 eBPF 时，另行编译 `internal/agent/bpf/flow_counter.c` 并安装到 `/usr/local/lib/network-observer/flow_counter.o`，或在配置中设置 `ebpf_object_path`，编译方式可参考 `deploy/agent/Dockerfile`；加载成功仍需真实流量验证。官方 Agent 镜像在构建阶段会自动编译并打包该对象，因此 Docker 方式不需要手动复制。只想先接入时保留 `auto` 即可。

<a id="agent-native-upgrade"></a>
### 原生 Agent 一键升级

已经注册并通过 systemd 运行的原生 Agent 不需要重新填写 Token，也不要重新执行注册命令。在 Agent 主机执行下面一条命令即可升级到固定版本：

```bash
curl -fsSL https://raw.githubusercontent.com/jonysun/Network-Observer/v0.3.15/scripts/upgrade-agent.sh | sudo bash
```

脚本会自动识别 amd64 / arm64，下载 Release 二进制和 `SHA256SUMS`，校验成功后停止并重启 `network-observer-agent`。`agent.json`、`ca.crt` 和 `/var/lib/network-observer/agent.wal` 会保留，设备身份不会变化。升级失败会恢复旧二进制。指定其他目标版本时，将管道右侧改为 `sudo bash -s -- --version vX.Y.Z`。

<a id="agent-gateway"></a>
## 11. Linux / OpenWrt 安装 Gateway Agent

### 与 Endpoint 的区别

Endpoint 观察本机 socket 和进程。Gateway 观察经过网关的 conntrack 流及导出流；通常无法知道远端主机上具体哪个进程发起连接。要获得各主机进程归属，应同时安装各自 Endpoint Agent。

在 Center 添加设备时选择 `gateway`，使用向导生成的 Docker 命令即可。手动注册时将第 9 节的 `--type endpoint` 改为 `--type gateway`，使用 Gateway Token，并把 `--config` 改为 `/etc/network-observer/gateway.json`，然后运行：

```bash
docker compose -f examples/agent/gateway-docker-compose.yml up -d
docker compose -f examples/agent/gateway-docker-compose.yml logs --tail=100 gateway-agent
```

向导安装器无论类型都写 `/etc/network-observer/agent.json`；仓库手动网关示例则读取 `gateway.json`，**两套约定不能混用**。原生安装方式同第 10 节，使用 Gateway Token / 类型，仍可统一保留 `agent.json` 和相同 systemd 服务。

### conntrack 与导出协议

在已注册的 Gateway 配置中按需要补充：

```json
{
  "ebpf_mode": "off",
  "conntrack_path": "/proc/net/nf_conntrack",
  "netflow_listen": ":2055",
  "ipfix_listen": ":4739",
  "sflow_listen": ":6343"
}
```

上面只是需要合并的配置片段，不可覆盖整个身份文件。只需 conntrack 时把三个监听字段留空；部分 OpenWrt 使用 `/proc/net/ip_conntrack`，按实际路径修改。不默认修改路由、防火墙、conntrack 内核参数或 Docker 网络。

支持 NetFlow v5、固定长度 IPFIX 模板/记录、标准 sFlow v5；sFlow 等采样数据会标为估算。导出流约 60 秒没有新记录后结束。网关 eBPF、NetFlow v9、可变长度 IPFIX 尚未完成。某些 conntrack 记录不含字节计数，NAT 前后元组也依赖内核实际提供的字段。

OpenWrt 只有在 Docker 可用、架构为 amd64 / arm64、内核能力与磁盘空间满足时才适用此容器路径。常见 32 位 ARM / MIPS 路由器没有对应发布镜像；当前也没有 procd 原生安装脚本，不能把 systemd 步骤直接套上去。

<a id="agent-options"></a>
## 12. Agent 的安全、eBPF、DNS 与归属配置

### 每个 Agent 自主选择 TLS 或明文

| Agent 配置 | Center 端口 | 需要证书 |
| --- | --- | --- |
| `security_mode=true` | TLS 端口，默认 8444 | 需要 Center 信任证书 |
| `security_mode=false` | 明文端口，默认 8445；Center 必须先开启 | 不需要 |

一个 Center 可同时接收两种 Agent。开启明文 listener 时仍然需要 Center 的证书，因为安全 listener 与 HTTPS Web 入口继续存在。启用明文入口：

```bash
docker compose --env-file .env \
  -f examples/center/light/docker-compose.yml \
  -f deploy/docker-compose.insecure-agent.yml up -d
```

Pro 还需带上 Pro 叠加文件，之后的重建、停止、升级也保留同一组 `-f` 参数。向导中关闭 TLS，把 gRPC 地址改为 `192.168.1.20:8445`。手动原生注册用 `--security-mode=false` 并省略 `--ca`；Docker 同理。

当前两种模式都通过一次性 Token 注册，然后使用独立设备凭证重连；关闭 TLS 只改变传输加密，不会退回“所有设备共享全局 Token”。明文网络中凭证和上报数据可被读取，因此只用于自己明确选择的可信网络。

### eBPF、DNS 与 Docker 归属

停止 Agent 后编辑其已经生成的 JSON，保留 `device_id`、`device_credential` 和其他身份字段，只增改需要的选项：

```json
{
  "ebpf_mode": "auto",
  "dns_metadata_enabled": false,
  "docker_socket": "/var/run/docker.sock",
  "wal_max_bytes": 268435456
}
```

| 选项 | 说明 |
| --- | --- |
| `ebpf_mode=auto` | 首次建议值；尝试采集，不支持时使用其他可用来源 |
| `ebpf_mode=off` | 不使用 eBPF；适合内核/权限不满足时排查 |
| `ebpf_mode=on` | 要求 eBPF；当前强制模式仍有实现限制，首次部署不使用它，以免采集直接失败 |
| `dns_metadata_enabled=true` | 本节点开始记录可见 UDP/53 中的域名、答案 IP、TTL 和时间；默认关闭 |
| `docker_socket` | 只读取元数据；需要真实 Socket 挂载与读取权限；没有时保留未知容器名 |
| `disable_process_attribution` | 可显式关闭进程归属读取 |
| `disable_socket_diag` / `disable_conntrack` | 诊断或按需关闭对应来源，不会补出本来缺失的字节计数 |
| `wal_max_bytes` | 本 Agent WAL 大小，默认 256 MiB；不是 Center 的全局配置 |

Docker 安装器修改 `/etc/network-observer/agent.json`，手动 Endpoint 示例修改 `examples/agent/config/agent.json`，手动 Gateway 示例修改 `gateway.json`。编辑后重启对应容器或 systemd 服务。配置片段不能替代完整注册配置，末尾不要留下多余逗号。

DNS 需要 `NET_RAW` / `AF_PACKET` 等能力；DoH、DoT、未经过当前观测点或已缓存的 DNS 不会自动补全域名。一个 IP 可能对应多个域名，域名关联不等于完整访问日志。eBPF 计数基于观察到的 skb 长度，不能和所有其他流量来源或应用有效载荷字节直接等同。`snapshot` 不接收现有 Agent 配置，不能靠它验证某份配置的 DNS / eBPF 开关已生效，应查看实际运行 Agent 的能力报告。

<a id="enrichment"></a>
## 13. ASN / GeoIP 自动下载与自定义

默认不需要准备数据库文件，也不需要在 `.env` 中填写 URL：Center 内置 GeoLite2 ASN / City 下载地址。首次启动时，Center 会读取 [项目配置的 GitHub Release](https://github.com/blocktrace/maxmind-geoip/releases)版本元数据，下载最新 MMDB 并校验 SHA-256 摘要与数据库类型，再缓存到容器内 `/data/enrichment/releases/`。Light 和 Pro 共用这份持久卷。

Center 每 24 小时检查一次轻量 Release 元数据；有新标签时才下载数据库。两份文件完整通过校验后会热切换，不需要重启 Center，并删除之前由 Center 管理的版本。网络不可达、下载失败或目录暂时不可写时，Center 保留正在使用的版本、记录警告并继续工作，后续检查会重试。默认路径由 Compose 持久卷提供写权限，无需初始化容器或手动执行 `chown`。

需要使用自行获取的数据库时，在 `.env` 设置 `IP_ENRICHMENT_ASN_MMDB` / `IP_ENRICHMENT_CITY_MMDB` 容器内路径。有效的自定义路径优先使用，由操作者自行维护更新；空值或无效路径使用项目默认库。通常不需要设置这些变量。

下载源是第三方托管的 MaxMind GeoLite 数据；GitHub 镜像不改变数据许可。MaxMind 要求及时使用更新数据库，并在新版本发布后 30 天内停止使用和销毁旧版本；Center 的自动模式会在成功切换后立即清除它自己管理的旧版本。控制台侧栏显示“本产品包含由 MaxMind 创建的 GeoLite 数据”来源标注。使用前请阅读 [GeoLite 官方说明](https://dev.maxmind.com/geoip/geolite2-free-geolocation-data/)及[使用条款](https://www.maxmind.com/en/geolite/eula)。私网、保留或无匹配 IP 显示未知；GeoIP 是数据库估算位置，不代表设备的精确地理位置，历史汇总也不会因数据库更新而重写。
<a id="source-build"></a>
## 14. 从源码构建

### Docker 构建 Center

完成前面的 `.env` 和证书准备，改用根目录 Compose：

```bash
docker compose --env-file .env -f docker-compose.light.yml up -d --build
```

Pro 使用 `-f docker-compose.light.yml -f docker-compose.pro.yml`。源码 Compose 与 GHCR 示例的项目名和数据卷可能不同；已有数据时不要直接混用，更换前先检查并备份。

Dockerfile 在构建阶段编译 Vue 和 Go，NAS 不需要额外安装 Node / Go。运行镜像中的 Center 仍使用非 root 用户。

### Docker 构建 Agent

```bash
docker build -f deploy/agent/Dockerfile -t network-observer-agent:local .
```

将自己的 Agent Compose 中 `image:` 改为 `network-observer-agent:local`，沿用已注册配置，不重新获取 Token。镜像构建包含 eBPF 对象；能编译不等于目标内核能够加载。

### 直接编译 Linux 原生二进制

仓库当前要求 Go 1.27+；Center 前端另需 Node.js 24+。在 Linux 上：

```bash
go build -o network-observer-agent ./cmd/agent
go test ./...
go vet ./...
```

直接开发 Center 时还要准备 PostgreSQL、ClickHouse、TLS 环境和 Vue 构建。普通部署优先使用固定版本镜像。GitHub Actions 的 Release 由 `vX.Y.Z` 标签触发，构建 amd64 / arm64 镜像和 Agent 二进制；镜像位于 `ghcr.io/jonysun/network-observer` 与 `ghcr.io/jonysun/network-observer-agent`。

<a id="operations"></a>
## 15. 验证、升级、备份与卸载

### 安装完成后验证什么

1. `readyz` 正常，Center 能连接两个数据库，控制台可以登录。
2. Agent 在设备页在线，存在能力报告，而不是只有注册记录。
3. 在被监控主机产生一段持续至少数秒的网络流量，查看连接、端口、进程和上下行来源。
4. 等待至少一个分钟汇总周期，再检查「流量分析 → 最近 24 小时」。
5. 用本地端口筛选本机服务，用远端口筛选访问外部服务；不要只看 TCP 源 / 目标方向猜上传下载。
6. 开启 DNS / MMDB 后分别验证域名与公网 IP 归属；关闭时显示未知属于预期行为。

10 端点、1 网关、10000 活动连接、字节误差、断线补传和跨设备去重属于实机验收内容，不能从镜像构建成功推断已经全部通过。

### 日常命令

```bash
docker compose --env-file .env -f examples/center/light/docker-compose.yml ps
docker compose --env-file .env -f examples/center/light/docker-compose.yml logs --tail=100 observer web
docker compose --env-file .env -f examples/center/light/docker-compose.yml restart observer web
```

Pro 在同一组命令追加 Pro 文件；启用明文 listener 的部署也保留对应叠加文件。后续使用一个固定入口管理同一个项目。

<a id="upgrade"></a>
### 升级与回退

升级前备份，记录当前镜像版本。更新源码安装文件与更新镜像是两件事：`git pull --ff-only` 更新仓库文件，`docker compose pull` 更新选定版本镜像。启动脚本默认不会强制拉取已存在的镜像。

在 `.env` 中设置已经发布的目标版本，例如继续固定到：

```dotenv
NETWORK_OBSERVER_VERSION=v0.3.15
```

```bash
docker compose --env-file .env -f examples/center/light/docker-compose.yml pull
docker compose --env-file .env -f examples/center/light/docker-compose.yml up -d
```

原生 Agent 按第 10 节下载并校验目标版本，停止 systemd、替换二进制、再启动，保留 `/etc/network-observer/agent.json`、`ca.crt`、WAL。Docker 安装器生成的 Compose 已把版本写死在 `image:`，升级时修改该文件的镜像标签，再运行 `pull` / `up -d`；修改 Center 的 `.env` 不会同步升级远端 Agent。

原生 Agent 推荐直接使用第 10 节的[一键升级命令](#agent-native-upgrade)。它只替换二进制，不重新注册设备，也不改动采集配置。若要升级 Docker Agent，修改其 Compose 中的 `NETWORK_OBSERVER_VERSION` 或固定 `image` 标签后执行 `docker compose pull` 和 `up -d`。

回退镜像前先核对数据库格式是否兼容；不能保证所有版本的迁移都支持直接降级。保留旧二进制和备份，避免使用 `latest` 导致目标版本不明确。

### 备份哪些数据

| 数据 | 位置 / 卷 | 说明 |
| --- | --- | --- |
| 配置和证书 | `.env`、Docker `tls-data` 卷、旧部署的 `deploy/tls/` seed 目录、自定义 Compose 与 Nginx 配置 | 含密码和私钥，备份按私密文件保存；升级时 Center 会自动把旧 seed 证书迁移到命名卷 |
| 设备、凭证、规则、ToDo、审计 | PostgreSQL 的 `postgres-data` | 丢失会影响现有 Agent 身份 |
| 流量与 presence 等数据 | ClickHouse 的 `clickhouse-data` | 需要单独备份，不包含在 PostgreSQL 中 |
| Pro 监控数据 | `prometheus-data`、`grafana-data` | 使用 Pro 时另行备份 |
| 离线归属库 | Docker 持久卷 `enrichment-data` | 自动下载缓存或自定义 MMDB |
| Agent 身份和待补传数据 | 每个 Agent 的配置、CA、WAL | 一个身份只能用于对应设备，备份不用于复制部署 |

最直观的冷备方式：用同一组 Compose 参数执行 `stop`，确认数据库容器停止后，使用 NAS 的 Docker 卷备份工具复制相关卷及配置，再执行 `up -d`。卷名可以用 `docker volume ls` 和 `docker inspect` 确认，GHCR 示例默认带 `network-observer_` 项目前缀。不要把运行中数据库的数据目录直接当作一致备份。

恢复到相同版本和兼容架构时，先停止空项目，再恢复配置、权限和对应数据卷；保留原数据库密码与 Center 证书，最后启动服务并逐一验证 Agent。只备份 YAML 不会备份数据库。

### 停止、从 Pro 减为 Light、卸载

只暂停服务：

```bash
docker compose --env-file .env -f examples/center/light/docker-compose.yml stop
```

从 Pro 减为 Light，先只停掉新增组件，保留其数据：

```bash
docker compose --env-file .env \
  -f examples/center/light/docker-compose.yml \
  -f examples/center/pro/docker-compose.yml stop prometheus node-exporter grafana
```

只运行 `start-center.sh light` 不会自动停止已运行的 Pro 组件。卸载容器但保留数据卷：

```bash
docker compose --env-file .env -f examples/center/light/docker-compose.yml down
```

Pro 完整卸载时带上 Pro 文件。`down` 保留卷；**`down -v` 会删除所选项目的数据卷**，只在明确清空并有备份时使用，重装和排错不要随手加 `-v`。

原生 Agent 停止并禁用服务：

```bash
sudo systemctl disable --now network-observer-agent
```

Docker 安装器 Agent 用其 `/etc/network-observer/docker-compose.yml` 执行 `down`，或对 `docker run` 方式执行 `docker stop network-observer-agent`。停探针即可停止采集；不自动删除配置、设备身份和 WAL。确实不再使用时，在 Center 吊销对应设备凭证，再决定是否清理本机文件。

<a id="troubleshooting"></a>
## 16. 常见问题与排查

| 现象 | 常见原因与处理 |
| --- | --- |
| GHCR 返回 `unauthorized` / `denied` | 包可能尚未设 Public；登录有 `read:packages` 权限的账号，或由所有者修改[Center](https://github.com/users/jonysun/packages/container/network-observer/settings) / [Agent](https://github.com/users/jonysun/packages/container/network-observer-agent/settings) 包可见性 |
| `start-center.sh: Permission denied` | 仓库脚本没有可执行位时使用 `bash scripts/start-center.sh light`，而不是直接 `./scripts/start-center.sh` |
| `bash\r` / `$'\r'` / 证书域名末尾异常 | Windows CRLF 行尾；按第 5 节规范化 `.env` 和脚本，不要把反斜杠写在 `.env` 每行末尾 |
| `TLS_CERT_FILE` / 私钥 `permission denied` | Center 为非 root；按第 2 节设置私钥属主和读取权限，另查 NAS ACL、父目录可遍历权限 |
| ASN / GeoIP 显示未知 | 检查 Center 日志中的下载警告与容器网络；确认 `enrichment-data` 持久卷可写。网络恢复后重启 Center 会重试；自定义路径无效也会回退到默认下载 |
| `x509` 域名或 IP 不匹配 | Agent 的 `--server` 地址不在证书 SAN，或证书只为 `localhost` 生成；更换正确证书并更新 Agent 信任文件 |
| TLS 证书指纹不匹配 | 命令来自旧证书，或下载来源不一致；先核对 Center，重新生成命令；不要绕过指纹检查 |
| Agent 连接超时 | 混用了 Web 8443 与 gRPC 8444、NAS 防火墙未放行、Agent 无法解析 Center 域名；明文必须用已启用的 8445 |
| Token 无效 / 已使用 | Token 过期、已被消费、设备类型不匹配；在 Center 重新生成，已有身份正常重连不需要 Token |
| `agent config already exists` | 正在向已有身份重复注册；使用 `run --config ...`，升级不要重新注册 |
| 原生安装出现校验失败 | 检查版本标签、架构和网络；安装器会在校验成功前停止，不要跳过 SHA-256 校验 |
| Center 改了 `.env` 却没变化 | 环境变量修改后需 `up -d` 重建；已有管理员密码不会自动重置，PostgreSQL 已有密码也不会由初始化变量改写 |
| 设备在线但归属未知 | `/proc` 权限、网络命名空间、Socket 不可见或未挂载 Docker Socket；网关通常看不到端点进程 |
| 有连接但 UDP 字节为零 | eBPF / 网关计数不可用；查看来源和 capability，不能解释为 UDP 没流量 |
| 域名为空 | DNS 默认关闭，DoH / DoT、缓存命中或不在观测路径上；按节点开启 DNS 后产生新的可见查询再验证 |
| eBPF `unsupported` / `degraded` | BTF、bpffs、cgroup、对象文件或 capability 不满足；先保持 `auto` / `off`，保留其余采集并记录能力详情 |
| 历史图表为空 | 刚启动未满一个汇总周期、时间/设备/端口过滤不对，或来源根本没有字节计数；查看 Center / Agent 日志 |
| 关闭连接后找不到逐条记录 | v1 只查询活动连接；历史用分钟流量和 presence，这是设计行为 |
| rootless Docker / 老 NAS 启动 Agent 失败 | host PID、网络、eBPF capability 可能受限；评估原生 Agent 或调整能力，不保证等价覆盖 |

排查顺序建议是：Center `ps` / 日志 → `readyz` → 证书和端口 → Agent 日志 → 设备能力 → 实时连接 → 分钟汇总。遇到问题保留具体错误和能力详情，避免清空数据库或重建所有设备身份。
