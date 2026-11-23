# Network Observer

[English](README.md) | [架构说明](docs/architecture.zh-CN.md) | 简体中文 | [详细安装与运维指南](docs/installation.zh-CN.md)

Network Observer 是面向家庭实验室的分布式局域网连接与流量监控系统。在 NAS 或 Linux 服务器上部署 Center，在需要监控的 Linux 主机或网关上安装 Agent，由 Agent 主动向 Center 上报数据。

## 从哪里开始

| 你的需求 | 建议方式 | 文档 |
| --- | --- | --- |
| 绿联 NAS / Linux 首次测试 | Center Light + 一个 Endpoint Agent | [NAS / Linux 安装](docs/installation.zh-CN.md#center-light) |
| 还需要 Grafana 和主机指标 | Center Pro | [Pro 安装](docs/installation.zh-CN.md#center-pro) |
| 已有 Docker Compose 管理习惯 | 使用 GHCR 示例 Compose | [手动 Compose 安装](docs/installation.zh-CN.md#center-compose) |
| 在 Windows 上测试 Center | Docker Desktop + Git Bash | [Windows 测试](docs/installation.zh-CN.md#center-windows) |
| 给 Linux 主机安装探针 | Center「设备 → 添加设备」生成命令 | [Agent 一键安装](docs/installation.zh-CN.md#agent-wizard) |
| 不想使用 Agent 安装脚本 | 手动注册，再运行 Docker / systemd | [手动 Docker](docs/installation.zh-CN.md#agent-docker-manual) · [手动原生安装](docs/installation.zh-CN.md#agent-native) |
| 在原 Center Compose 中监控 NAS 本机 | 增加一个自动注册的 Endpoint Agent | [宿主机自监控](docs/installation.zh-CN.md#center-self-monitor) |
| 监控 Linux / OpenWrt 网关 | Gateway Agent，按能力采集 | [网关安装](docs/installation.zh-CN.md#agent-gateway) |
| 使用源码或修改代码 | 根目录开发 Compose | [源码构建](docs/installation.zh-CN.md#source-build) |
| 更新、迁移、卸载或排错 | 保留身份与持久卷再操作 | [日常运维](docs/installation.zh-CN.md#operations) · [故障排查](docs/installation.zh-CN.md#troubleshooting) |

## 可以看到什么

- **实时连接**：IPv4 / IPv6、TCP / UDP、源和目标 IP / 端口、连接状态、上下行速率、主机、可识别的网卡、进程及容器归属。
- **历史流量**：按分钟汇总，默认保存 90 天；支持设备、网卡、协议、IP、端口、进程、容器等维度。
- **端口分析**：例如某台主机本地 `443` 端口过去 24 小时的上传量、下载量与趋势。
- **持续连接分析**：按观测分钟展示持续出现的 IP / 域名；这是 presence 汇总，不是逐连接历史回放。
- **域名与地址归属**：每个 Agent 自主决定是否采集 DNS 元数据；Center 首次启动时自动下载并缓存离线 ASN / GeoIP 数据库。
- **远端 IP 详情**：连接、排名和总览可打开远端 IP 资料，查看 DNS/PTR 域名、ASN 登记组织、国家/地区/城市和定位来源；管理员可按 IP 全局编辑这些字段并标记可信。
- **总览地球与排名**：实时地球按有坐标的 Agent 与远端 IP 连接绘制路线；过去 24 小时按 Agent、远端域名、Agent+本地端口、Agent+进程排名，也可切换 IP、网卡、ASN、登记组织、国家/地区/城市和容器，并按流量或不同 flow 数排序。
- **归属补录**：单次覆盖、持久规则、自动解析和未知归属；手动规则从启用时生效。
- **ASN / GeoIP**：Center 自动下载并每日检查 GeoLite 更新，验证后热切换；控制台标注 MaxMind 数据来源。
- **设备管理**：一次性注册 Token、独立设备凭证、能力和降级原因、凭证吊销、断线 WAL 补传。
- **项目进度**：Web 中维护实施进度和自己的 ToDo。

采集颗粒度目标包含**连接、端口、网卡、进程、主机**，但可用字段取决于内核、权限和观测位置。无法确认的字段保留未知；看到连接也不等于一定有可用字节计数。Docker 容器的独立网络命名空间、短连接、UDP 与网关转发流量仍需实机验收，不能把安装成功当作完整覆盖证明。

## Light 与 Pro

| 组件 | Light | Pro | 用途 |
| --- | --- | --- | --- |
| Center + Vue 控制台 | ✓ | ✓ | 接收上报、查询、设备与归属管理 |
| Nginx Web 入口 | ✓ | ✓ | 控制台 HTTPS 和 WebSocket 代理 |
| PostgreSQL | ✓ | ✓ | 设备、凭证、规则、配置、审计与 ToDo |
| ClickHouse | ✓ | ✓ | 分钟流量与 presence 等分析数据 |
| Prometheus | — | ✓ | 抓取服务及主机指标 |
| Node Exporter | — | ✓ | 主机资源和磁盘指标 |
| Grafana | — | ✓ | 额外的指标仪表盘 |

Light 运行 4 个服务，Pro 运行 7 个服务。Center 首次启动会自动在持久卷生成自签名证书，Nginx 复用同一份证书，**不需要证书专用容器，也不需要手动执行证书命令**。Light 已包含自己的 Web 图表，测试时不需要安装 Grafana、Prometheus 或 Node Exporter。ClickHouse 是现有历史分析存储，当前 Light 仍需要它。

## 快速安装 Center

以下命令在 **NAS 的 SSH 终端或 Linux Bash** 中执行。先安装 Docker Engine 和 Compose v2；完整步骤和 Windows 命令见[安装指南](docs/installation.zh-CN.md#prerequisites)。

```bash
git clone https://github.com/jonysun/Network-Observer.git network-observer
cd network-observer
bash scripts/start-center.sh light
```

首次运行会自动创建 `.env`，生成随机数据库、管理员和 Grafana 密码，检测局域网地址并生成 TLS 证书；管理员密码写在 `.env` 中。若使用 DNS 名称，可执行 `TLS_CERT_HOST=observer.example.lan bash scripts/start-center.sh light`。ASN / GeoIP 数据库也会自动下载并缓存。

启动完成后访问 `https://192.168.1.20:8443`，使用 `.env` 中的 `ADMIN_USERNAME` / `ADMIN_PASSWORD` 登录。Agent 的 TLS 上报地址是 `192.168.1.20:8444`，不要和 Web 端口混用。自签名证书首次访问会出现浏览器信任提示。

需要 Pro 时，填写 Grafana 密码后执行：

```bash
bash scripts/start-center.sh pro
```

Grafana 默认地址为 `http://192.168.1.20:3000`。

所有安装命令均从仓库根目录运行。`examples/` 的 Compose 拉取固定版本 GHCR 镜像，默认版本为 `v0.3.40`；根目录 Compose 用于源码构建。启动脚本会先自动拉取镜像，网络临时超时时默认重试 5 次，再启动服务；可用 `NETWORK_OBSERVER_PULL_RETRIES` 和 `NETWORK_OBSERVER_PULL_RETRY_DELAY` 调整。启动脚本位于当前源码分支中，旧版本标签不一定包含它。更新镜像见[升级步骤](docs/installation.zh-CN.md#upgrade)。

## 让 Center 监控 NAS 宿主机

Center 服务自身运行在容器网络和 PID 命名空间中，不能直接代表 NAS 宿主机采集连接。可在现有 Center Compose 项目内增加一个独立的 Endpoint Agent；不需要再部署 Center、数据库或其他服务。仓库提供了可叠加的 [自监控 Compose 文件](examples/center/light/docker-compose.self-monitor.yml)，适用于真实 Linux/NAS 上的 Light 或 Pro 部署。

以下命令从仓库根目录执行。Center 应已启动，默认示例使用 `examples/center/light` 中的 Compose：

1. 在 Center Web 的「设备 → 添加设备」生成一个 Endpoint 一次性 Token，设备名可设为 `center-host`。
2. 创建配置目录并复制 Center 的公开证书。Center 已自动生成证书；Agent 只需要 `server.crt`，不要复制 `server.key` 私钥：

   ```bash
   mkdir -p examples/center/light/center-agent
   docker compose --env-file .env \
     -f examples/center/light/docker-compose.yml \
     cp observer:/run/tls/server.crt examples/center/light/center-agent/ca.crt
   ```

3. 在仓库根目录的 `.env` 中填写一次性 Token：

   ```dotenv
   CENTER_AGENT_ENROLLMENT_TOKEN=替换为刚生成的一次性Token
   ```

   默认连接地址为 `127.0.0.1:8444`，TLS 名称为 `127.0.0.1`。这是自动生成的 Center 证书包含的回环地址；如果导入的正式证书不包含它，在 `.env` 添加 `CENTER_AGENT_TLS_SERVER_NAME`，值填写 Center 添加设备页面显示的证书名称。host 网络下的 Agent 不能使用 Compose 内部服务名 `observer`。

4. 在原 Compose 上叠加自监控服务：

   ```bash
   docker compose --env-file .env \
     -f examples/center/light/docker-compose.yml \
     -f examples/center/light/docker-compose.self-monitor.yml \
     up -d
   ```

   查看启动状态和日志：

   ```bash
   docker compose --env-file .env \
     -f examples/center/light/docker-compose.yml \
     -f examples/center/light/docker-compose.self-monitor.yml \
     logs --tail=100 center-agent
   ```

Agent 首次启动会自动使用 Token 注册，之后在 `center-agent-config` 命名卷中保存和复用设备凭证，WAL 位于 `center-agent-wal`。此 Agent 使用固定版本 GHCR 镜像，Compose 会拉取镜像，不需要在 NAS 上构建源码。确认设备在线后，清空 `.env` 中的一次性 Token，再以 `--force-recreate center-agent` 重建容器；身份卷会保留，Token 不再保存在容器配置中。升级时不要删除这两个命名卷。

该服务使用 `privileged`、host 网络和 host PID，以读取 NAS 宿主机可见的连接、端口、网卡、进程和 Docker 容器归属；`/sys`、证书和 Docker Socket 挂载为只读。该权限较高，Agent 的 Docker Socket 调用只用于查询容器元数据。eBPF 是否可用仍取决于 NAS 内核与 BTF，Agent 会在设备能力中报告降级。此 Agent 不提供主机 CPU、内存或磁盘指标；Pro 的 Node Exporter/Grafana 负责主机资源图表。

若你使用的 Center Compose 不同，可把 overlay 中的 `center-agent` 服务和 `center-agent-config`、`center-agent-wal` 卷合并到现有 YAML；保留当前 Compose 项目名和数据库卷。更完整的证书与自监控说明见[安装指南](docs/installation.zh-CN.md#center-self-monitor)。

## 安装第一个 Agent

1. 在 Center 打开「设备 → 添加设备」。
2. 填写设备名称、Endpoint / Gateway 类型、Center gRPC 地址和 Web 地址。
3. 保持 TLS 开启，生成一次性 Token 与安装命令。
4. 在 **Agent 所在的 Linux 主机**以 root 身份执行对应的 Docker Compose 或 `docker run` 命令。
5. 返回设备页确认在线、能力报告、实时连接，以及至少一分钟后的历史汇总。

每个 Token 只能注册一台设备。注册成功后使用独立设备凭证重连；换版本不需要重新注册，也不能把同一份 `agent.json` 克隆给其他主机。证书会由安装器下载并校验 Center 显示的指纹。

Docker 与 systemd 原生方式均有[详细安装步骤](docs/installation.zh-CN.md#agent-wizard)。原生安装器会下载对应架构并校验 SHA-256，再创建 systemd 服务。

原生 Agent 后续升级只需在 Agent 主机执行一条命令，不需要 Token 或重新注册：

```bash
curl -fsSL https://raw.githubusercontent.com/jonysun/Network-Observer/v0.3.40/scripts/upgrade-agent.sh | sudo bash
```

脚本会自动识别架构、校验固定版本并重启服务，同时保留设备配置、证书和 WAL。使用其他目标版本时，将命令末尾改为 `| sudo bash -s -- --version vX.Y.Z`。

原生 Agent 需要完整移除时，在对应 Agent 主机执行卸载脚本：

```bash
curl -fsSL https://raw.githubusercontent.com/jonysun/Network-Observer/main/scripts/uninstall-agent.sh \
  | sudo bash -s -- --yes
```

该命令会停止并删除 systemd/procd 服务、Agent 二进制、eBPF 对象、配置、Center CA、设备凭证和 WAL。OpenWrt 没有 Bash 时，使用同一个 POSIX 脚本：

```sh
uclient-fetch -O- http://Center地址:8080/bootstrap/openwrt/Center版本/uninstall-agent.sh \
  | sh -s -- --yes
```

此 Center HTTP 引导命令需要使用已包含卸载资产的新 Center 镜像；旧版 Center 的引导白名单不包含该脚本。

去掉 `--yes` 会在交互终端要求确认；`--dry-run` 只显示计划。需要保留身份或离线数据时可加 `--keep-config` 或 `--keep-wal`。脚本会自动识别常见的 `/tmp/network-observer` 配置布局；其他自定义目录使用 `--config-dir DIR` 和 `--state-dir DIR` 指定。脚本只删除 Agent 已知文件，目录中其他文件会保留；不会删除 Center 数据或自动吊销 Center 中的设备记录，卸载后请在 Web「设备」页吊销对应凭证。

## 数据与采集边界

| 项目 | 当前行为 |
| --- | --- |
| 安全模式 | 每个 Agent 自主设置；`true` 使用 TLS，`false` 使用 Center 额外启用的明文端口；Web 始终使用 HTTPS |
| eBPF | 默认 `ebpf_mode=auto`；镜像包含采集对象，实际挂载与覆盖以能力报告和实机验证为准；首次部署不要强制 `on` |
| TCP 字节 | 可用时使用 eBPF 或 Socket Diag / TCP_INFO；来源和能力状态应结合查看 |
| UDP 字节 | 依赖可用的 eBPF 或网关计数；不可用时不能把零值当成没有流量 |
| DNS | 默认 `dns_metadata_enabled=false`；开启后仅采集可见 UDP/53 元数据，不解析 DoH / DoT 或普通应用载荷 |
| ASN / GeoIP | Center 首次启动时自动下载兼容 MMDB 并缓存到持久卷；不联网查询观测 IP；历史汇总不会随数据库更新自动重写 |
| IP 手工资料与可信 | 按规范化 IP 全局覆盖显示字段；可信只用于标注和筛选；原始自动值、来源和数据库版本仍保留 |
| PTR 反查 | 仅对公网 IP 异步查询并缓存；Agent DNS 关系优先；没有 PTR 或 Center DNS 不可用时显示未知 |
| 实时地球路线 | 仅画 Agent 和远端 IP 都有坐标的活动连接；最多 100 条；GeoIP 位置是近似位置，不代表设备实际所在地 |
| 24 小时排名 | 流量来自分钟汇总，连接数按不同 `(device_id, flow_id)` 计；升级前的本地端口/进程等历史维度可能不可用，不回填推测值 |
| 历史连接 | 不提供已结束连接的逐条检索；保留分钟流量和 presence 等汇总 |
| 保留时间 | 流量和 presence 默认 90 天，DNS 关系默认 7 天 |
| WAL | 默认 256 MiB，上报 24 小时窗口和容量问题时明确报告数据缺口，不能保证无限离线缓存 |
| 空闲连接优化 | 本地仍按配置采样；新 Center/Agent 握手后，完全无变化的 Endpoint/conntrack 连接每 10 秒保活，流量、速率归零及元数据变化立即上报；历史活跃时长按覆盖区间结算 |
| 网关协议 | conntrack、NetFlow v5、固定长度 IPFIX、sFlow v5；NetFlow v9、可变长度 IPFIX 和网关 eBPF 尚未完成 |
| 平台 | Center：Linux Docker；Agent：Linux amd64 / arm64；OpenWrt Gateway Lite：ARM64 / ARMv7 原生 procd；Windows / macOS 不支持 |

### eBPF 与 DNS 能力状态

能力页显示 `ebpf_byte_counters · unavailable` 时，表示本次 Agent 没有成功加载并挂载 cgroup-skb eBPF 对象。`v0.3.17` 起原生安装器也会自动下载并校验对应架构的 `flow_counter.o`；运行时仍需要 Linux 内核 BTF、可用的 cgroup v2、bpffs 和相应的 capability。缺少任一条件时，Socket Diag、procfs、conntrack 等其他采集仍会继续，字节字段会按来源标为未知或不可用，不应把零值解释为没有流量。先保持 `ebpf_mode=auto`，查看能力详情；只有确认实机挂载成功后再考虑 `on`。

`dns_metadata · disabled` 是因为每个 Agent 默认 `dns_metadata_enabled=false`。开启后只记录当前观测点可见的 UDP/53 查询/响应元数据（域名、答案 IP、TTL、时间），需要 `NET_RAW` / `AF_PACKET`；DoH、DoT、缓存命中和未经过该 Agent 的 DNS 不会出现。该开关由 Agent 自主决定，不会因为 Center 开启而自动改变。

OpenWrt 无 Docker 时，在 Center「设备 → 添加设备」中选择 `gateway`，直接复制 OpenWrt Gateway Lite 命令。安装器是 POSIX `sh`，自动检查 ARM64/ARMv7、procd、下载工具、`/tmp` 空间、conntrack 路径和 `nf_conntrack_acct`，再下载固定版本并校验摘要。Agent TLS 关闭时，向导会改用 Center 独立的 HTTP 引导端口，不要求路由器先安装 TLS 下载包；该端口只代理固定发布资产。conntrack、NAT、ARP/NDP 能力缺失会降级上报，不会阻止其他采集；Lite 不加载网关 eBPF，也不能提供其他主机的进程/容器归属。默认 WAL 位于 `/tmp`，路由器重启后的未补传区间会显示数据缺口。

### 局域网资产与多个 Agent

「局域网资产」页使用 Agent 只读上报的 Linux ARP/NDP 邻居表和本机地址快照，不做主动全网扫描。Center 以推断的网络段和 MAC 合并多个 Agent 的重复观察，并列出所有观察 Agent；没有 MAC 的邻居按 IP 与观察 Agent 保留。IPv6 链路本地地址按接口作用域区分。同一网络段同一 IP 出现多个 MAC 时标记 IP 冲突，过期邻居标记为陈旧。

Docker Agent 需要 `network_mode: host`、主机 `/sys` 只读挂载和读取邻居表的 `NET_ADMIN` 能力，才能看到宿主的接口、ARP/NDP 和连接；桥接网络中的容器只能看到自己的网络命名空间。快照来自各节点当时的邻居缓存，不能保证发现从未被本机访问过的设备。资产名称可在页面手动设置，连接页和资产详情可以相互跳转。若页面为空，请先在 Agent 设备详情确认 `lan_neighbors` 能力，再升级 Agent 并重建容器；`degraded` 状态会显示具体读取失败原因。

## 安装后的常用操作

| 操作 | 入口 |
| --- | --- |
| 查看某端口过去 24 小时上下行 | Web「流量分析」选择设备、最近 24 小时、端口角色和端口号 |
| 看哪些 IP / 域名持续出现 | Web「流量分析」的持续连接列表；需要域名时先在相应 Agent 开启 DNS 元数据 |
| 开启 DNS 或调整 eBPF | 修改该 Agent 的已注册配置并重启，见[采集配置](docs/installation.zh-CN.md#agent-options) |
| 查看或编辑远端 IP 域名、ASN 登记组织、地理和可信状态 | 在实时连接或排名中点击远端 IP；公网自动值需 Center 已下载并匹配 MMDB，手工资料按 IP 全局生效 |
| 查看实时连接地球与多维排名 | Web「网络总览」；路线要求 Agent 手动设置坐标且远端 IP 有 GeoIP 坐标，排名使用过去 24 小时数据 |
| 启用 ASN / GeoIP | Center 默认自动下载；自定义数据库见[离线归属库](docs/installation.zh-CN.md#enrichment) |
| 查看日志、升级与备份 | [运维指南](docs/installation.zh-CN.md#operations) |
| 设备不在线、证书报错、数据为空 | [故障排查](docs/installation.zh-CN.md#troubleshooting) |

更完整的规格与实施记录位于 [`docs/superpowers/`](docs/superpowers/)。其中规划项不等于已完成的实机验收；请结合 Web「项目进度」记录当前设备上的验证结果。
