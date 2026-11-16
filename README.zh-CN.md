# Network Observer

[English](README.md) | 简体中文 | [详细安装与运维指南](docs/installation.zh-CN.md)

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
| 监控 Linux / OpenWrt 网关 | Gateway Agent，按能力采集 | [网关安装](docs/installation.zh-CN.md#agent-gateway) |
| 使用源码或修改代码 | 根目录开发 Compose | [源码构建](docs/installation.zh-CN.md#source-build) |
| 更新、迁移、卸载或排错 | 保留身份与持久卷再操作 | [日常运维](docs/installation.zh-CN.md#operations) · [故障排查](docs/installation.zh-CN.md#troubleshooting) |

## 可以看到什么

- **实时连接**：IPv4 / IPv6、TCP / UDP、源和目标 IP / 端口、连接状态、上下行速率、主机、可识别的网卡、进程及容器归属。
- **历史流量**：按分钟汇总，默认保存 90 天；支持设备、网卡、协议、IP、端口、进程、容器等维度。
- **端口分析**：例如某台主机本地 `443` 端口过去 24 小时的上传量、下载量与趋势。
- **持续连接分析**：按观测分钟展示持续出现的 IP / 域名；这是 presence 汇总，不是逐连接历史回放。
- **域名与地址归属**：每个 Agent 自主决定是否采集 DNS 元数据；Center 首次启动时自动下载并缓存离线 ASN / GeoIP 数据库。
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

所有安装命令均从仓库根目录运行。`examples/` 的 Compose 拉取固定版本 GHCR 镜像，默认版本为 `v0.3.6`；根目录 Compose 用于源码构建。启动脚本会先自动拉取镜像，网络临时超时时默认重试 5 次，再启动服务；可用 `NETWORK_OBSERVER_PULL_RETRIES` 和 `NETWORK_OBSERVER_PULL_RETRY_DELAY` 调整。启动脚本位于当前源码分支中，旧版本标签不一定包含它。更新镜像见[升级步骤](docs/installation.zh-CN.md#upgrade)。

## 安装第一个 Agent

1. 在 Center 打开「设备 → 添加设备」。
2. 填写设备名称、Endpoint / Gateway 类型、Center gRPC 地址和 Web 地址。
3. 保持 TLS 开启，生成一次性 Token 与安装命令。
4. 在 **Agent 所在的 Linux 主机**以 root 身份执行对应的 Docker Compose 或 `docker run` 命令。
5. 返回设备页确认在线、能力报告、实时连接，以及至少一分钟后的历史汇总。

每个 Token 只能注册一台设备。注册成功后使用独立设备凭证重连；换版本不需要重新注册，也不能把同一份 `agent.json` 克隆给其他主机。证书会由安装器下载并校验 Center 显示的指纹。

Docker 与 systemd 原生方式均有[详细安装步骤](docs/installation.zh-CN.md#agent-wizard)。原生安装器会下载对应架构并校验 SHA-256，再创建 systemd 服务。

## 数据与采集边界

| 项目 | 当前行为 |
| --- | --- |
| 安全模式 | 每个 Agent 自主设置；`true` 使用 TLS，`false` 使用 Center 额外启用的明文端口；Web 始终使用 HTTPS |
| eBPF | 默认 `ebpf_mode=auto`；镜像包含采集对象，实际挂载与覆盖以能力报告和实机验证为准；首次部署不要强制 `on` |
| TCP 字节 | 可用时使用 eBPF 或 Socket Diag / TCP_INFO；来源和能力状态应结合查看 |
| UDP 字节 | 依赖可用的 eBPF 或网关计数；不可用时不能把零值当成没有流量 |
| DNS | 默认 `dns_metadata_enabled=false`；开启后仅采集可见 UDP/53 元数据，不解析 DoH / DoT 或普通应用载荷 |
| ASN / GeoIP | Center 首次启动时自动下载兼容 MMDB 并缓存到持久卷；不联网查询观测 IP；历史汇总不会随数据库更新自动重写 |
| 历史连接 | 不提供已结束连接的逐条检索；保留分钟流量和 presence 等汇总 |
| 保留时间 | 流量和 presence 默认 90 天，DNS 关系默认 7 天 |
| WAL | 默认 256 MiB，上报 24 小时窗口和容量问题时明确报告数据缺口，不能保证无限离线缓存 |
| 网关协议 | conntrack、NetFlow v5、固定长度 IPFIX、sFlow v5；NetFlow v9、可变长度 IPFIX 和网关 eBPF 尚未完成 |
| 平台 | Center：Linux Docker；Agent：Linux amd64 / arm64；Windows / macOS 原生 Agent 与 OpenWrt procd 尚不支持 |

## 安装后的常用操作

| 操作 | 入口 |
| --- | --- |
| 查看某端口过去 24 小时上下行 | Web「流量分析」选择设备、最近 24 小时、端口角色和端口号 |
| 看哪些 IP / 域名持续出现 | Web「流量分析」的持续连接列表；需要域名时先在相应 Agent 开启 DNS 元数据 |
| 开启 DNS 或调整 eBPF | 修改该 Agent 的已注册配置并重启，见[采集配置](docs/installation.zh-CN.md#agent-options) |
| 启用 ASN / GeoIP | Center 默认自动下载；自定义数据库见[离线归属库](docs/installation.zh-CN.md#enrichment) |
| 查看日志、升级与备份 | [运维指南](docs/installation.zh-CN.md#operations) |
| 设备不在线、证书报错、数据为空 | [故障排查](docs/installation.zh-CN.md#troubleshooting) |

更完整的规格与实施记录位于 [`docs/superpowers/`](docs/superpowers/)。其中规划项不等于已完成的实机验收；请结合 Web「项目进度」记录当前设备上的验证结果。
