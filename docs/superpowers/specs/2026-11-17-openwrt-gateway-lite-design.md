# OpenWrt Gateway Lite 设计

**状态：** 对话中已确认总体方案；规格待用户审阅  
**日期：** 2026-11-17  
**范围：** 为无 Docker 的 ARM OpenWrt 网关提供轻量原生 Agent，复用 Network Observer Center 的现有注册、认证和数据协议。

## 目标

- 在 ARM64 与 ARMv7 OpenWrt 路由器上运行原生 Gateway Agent，不要求 Docker、systemd、Bash 或 curl。
- 通过 conntrack 观察 IPv4/IPv6 TCP/UDP 流、双向计数和可用的 NAT 前后元组；只读上报 ARP/NDP 邻居快照。
- 复用现有 Center gRPC 协议、设备一次性注册、独立设备凭证、设备吊销和数据幂等机制，不要求 Center 新增协议端点。
- 为每个 Agent 保留独立的 TLS/明文选择；默认使用 TLS，明文模式只连接管理员已显式开放的 Center gRPC 明文 listener。
- 提供 OpenWrt `procd` 服务、POSIX `sh` 一键安装器及 Center 生成的 OpenWrt 安装命令。
- 将路由器写入限制在 RAM 文件系统的本地 WAL 中；明确报告重启造成的补传缺口，不默默把缺失区间显示为完整数据。

## 当前代码与约束

- Gateway Agent 已有 Linux conntrack 文本解析、双向累计计数和 NAT 元组处理，并可通过 Linux netlink 读取邻居表。
- Agent 已有流式 gRPC、设备注册/认证、TLS 与明文 transport 选择、WAL、能力状态和 Center 兼容的 FlowObservation/NeighborSnapshot。
- 当前 conntrack 默认只读 `/proc/net/nf_conntrack`；部分 OpenWrt 使用 `/proc/net/ip_conntrack`，目前需要手工指定。
- 当前 Linux 发布只有 amd64/arm64；Linux 原生安装器依赖 Bash、curl 和 systemd。OpenWrt procd 与 ARMv7 尚未发布。
- 当前运行时每秒采样一次；默认 WAL 上限 256 MiB，适合普通 Linux 主机，不适合默认写入路由器闪存。
- eBPF、端点进程/容器归属不适用于 Lite 网关首期目标，不应因缺少这些内核能力而阻止 Gateway Lite 工作。

## 设计决策

### 构建目标与协议兼容

在同一仓库增加 `openwrt-lite` 构建目标，复用现有 Agent 注册、gRPC 客户端、凭证、WAL、Gateway conntrack 解析和邻居快照代码。Lite 构建固定为 `gateway` 类型；配置或命令若要求 endpoint 能力，应在启动前给出明确错误。

构建使用纯 Go 静态 Linux 二进制，`CGO_ENABLED=0`，并通过构建标签使 Lite 运行路径不启用 endpoint 进程扫描、Docker 元数据、DNS、Socket Diag 和 eBPF；这些通用代码暂不从 Go 模块中物理拆除，以保持协议和测试共享。Agent 仍上报现有 protobuf 消息，Center 无需升级即可接收；注册时报告 `GOOS=linux` 与 Go 架构（ARMv7 报告 `arm`）。

首期发布两个独立资产：

- `network-observer-agent-openwrt-linux-arm64`
- `network-observer-agent-openwrt-linux-armv7`

ARMv7 使用 `GOARCH=arm GOARM=7`。暂不支持 MIPS、低于 ARMv7 的 32 位 ARM、其他 OpenWrt 目标架构；检测到不支持的架构时安装器必须停止并打印识别到的 `uname -m`，不能尝试安装错误二进制。

### Gateway Lite 采集边界

- 启动时自动选择第一个可读取的 conntrack 路径：`/proc/net/nf_conntrack`，其次 `/proc/net/ip_conntrack`。显式 `conntrack_path` 仍可覆盖自动选择。
- 继续解析 conntrack 原始与 reply tuple、TCP/UDP 状态、双向字节/包累计计数和内核提供的 NAT 元组。每轮按计数差生成双向速率；没有字节计数时如实标记不可用，不从包数推造字节数。
- 继续使用只读 Linux netlink 读取本地地址、接口、ARP/NDP 邻居；邻居表为空、权限不足、接口 API 不支持和路径缺失均报告到 `lan_neighbors` 或对应 Gateway capability。
- Lite 首期不加载 eBPF，不采集端点进程、容器、DNS 或应用载荷。NetFlow/IPFIX/sFlow 监听不属于首期 Lite 安装配置；需要这些 exporter 的现有 Linux Gateway 可继续使用通用 Agent。
- Gateway 没有进程所有权，连接的进程/容器字段保持未知。源/目标方向、NAT tuple 和上下行计数继续遵守现有模型；不得将 conntrack 的 tuple 起点误报为主机主动发起。
- Lite 默认使用 2 秒采样周期，可通过本地配置调整；普通 Linux Agent 保持现有默认周期。Center 通过现有实时流接收观察数据，不增加新的 Center API。

### OpenWrt 内核差异和颗粒度边界

OpenWrt 修改版能否采集，不由 SSH 或是否安装 curl 决定；决定因素是运行内核公开了哪些 conntrack、计数、netlink 和邻居表能力。SSH root 权限只方便安装和诊断，不能补出内核未提供或被硬件卸载路径绕开的统计。安装器不得根据“能 SSH”就显示全部能力可用。

| 维度 | Gateway Lite 可见内容 | 主要限制 |
| --- | --- | --- |
| 连接、协议、源/目的 IP 与端口 | conntrack 活动 TCP/UDP tuple；内核提供时保留 NAT 前后 tuple | conntrack 模块/接口缺失时不可用；连接跟踪表不等于逐包抓取 |
| 每连接上下行流量 | conntrack 暴露双向 `bytes`/`packets` 计数时计算增量和速率 | 依赖内核连接跟踪计数；只看到流但没有计数时只能显示连接/tuple，不能推导字节量 |
| 内网 IP、MAC、设备名 | ARP/NDP 邻居快照可给出近期 IP/MAC/接口；设备名来自已有手动别名或后续 DHCP/DNS 数据源 | ARP/NDP 只包含近期解析/活动邻居，不是完整设备清单；邻居表本身通常不提供可靠主机名 |
| 外部 ASN、组织和地理位置 | Center 使用已配置的本地 MMDB 对观察到的公网 IP 做归属 | 属于 IP 级估算/登记资料，不表示物理链路或个人位置；私网 IP 无公网归属 |
| 域名 | Center 可用现有 PTR 反查作为尽力而为的名称提示 | PTR 不等于客户端实际查询域名；OpenWrt DNS 查询关联不在 Lite 首期，DoH/DoT 和 CDN 共享 IP 不能从 conntrack 还原域名 |
| 发起方向 | 在原始 tuple 与 LAN 前缀能唯一判定时标记本地/LAN 或外部发起，否则未知 | NAT、双 LAN、双 WAN、对称或不完整 tuple 会降低判定置信度；上下行字节方向仍独立计算 |
| 网卡总量与流量归属 | `/sys/class/net/*/statistics` 可读每接口 RX/TX 总量；conntrack 提供 flow 级元组 | Linux conntrack proc 行通常不携带可靠的实际 ingress/egress ifindex；接口总量不能直接拆分到某条连接 |
| 进程、容器 | 无 | 网关不持有其他 LAN 主机的进程表；需要在对应端点安装 Endpoint Agent |

安装/运行诊断必须只读检查并上报下列状态：内核版本和架构、可读取的 conntrack 路径、conntrack 行/计数器样例、`nf_conntrack_acct` 可见状态、邻居 netlink 可用性、内存与 `/tmp` 空间，以及可识别时 OpenWrt 软件/硬件 flow offloading 配置。报告能力名称分别标识 conntrack 流、字节计数、NAT tuple、邻居表和 offload 检测；诊断输出说明“支持、降级、未知”的证据，而不是只给笼统的“Agent 在线”。

OpenWrt 的软件/硬件 flow offloading 可能使经典 conntrack 字节计数不覆盖全部转发数据，实际行为依赖内核、驱动和 offload 实现。Agent 只读取并告警，不自动关闭 flow offloading、不写 firewall/UCI/sysctl、不安装或加载内核模块。若用户优先要求准确的 conntrack 流量计数，可在路由器管理界面关闭相应软件/硬件 offloading 后实测；这可能降低转发性能。首期不通过注入 nftables/iptables 规则绕过该限制，因为这会改变防火墙并可能产生额外开销或重复计数。

每接口总 RX/TX 计数虽可从 sysfs 读取，但现有 Center 数据模型没有独立接口计数消息，且它与 flow 级字节量不能直接相加。Lite 首期因此不把接口总量伪装成连接流量；如需在 Center 展示，后续应增加独立接口计量数据和查询视图。进程归属使用端点 Agent，DNS 查询域名使用可配置 DNS 元数据源，二者不能靠路由器 SSH 或 conntrack 补齐。

### TLS、注册和身份

- TLS 模式沿用已有 TLS + 一次性 Token 注册；Agent 收到每设备凭证后擦除配置中的一次性 Token。证书由安装器下载后，必须按 Center 向导提供的 SHA-256 指纹验证，再用于 gRPC TLS 校验。
- 为避免 OpenWrt 必须安装 OpenSSL，Agent 增加基于 Go `crypto/x509` 的证书指纹校验子命令，供 POSIX 安装器验证下载到的 Center CA。证书下载可使用 `uclient-fetch` 的跳过初始链校验选项，但指纹不匹配时必须停止，不能降级继续。
- 安装器下载 GitHub Release 文件时优先使用系统 CA 验证 HTTPS；若设备没有可用 CA/HTTPS 下载组件，提供将 Release 文件经 SCP 放入临时目录后继续安装的明确方式，不引入静默的不安全下载。
- 明文模式复用现有 Agent 行为，不下载或保存 Center CA；生成命令须使用管理员指定的明文 gRPC 地址。Center 未开放明文 listener 时，注册应明确失败。
- 每设备身份、一次性 Token 消费、凭证吊销和流量上报行为与当前 Center 协议一致。

### OpenWrt 安装和服务

- 新增纯 POSIX `sh` 安装脚本，交互调用可询问 Center 地址、设备名、Token、设备类型和安全模式；Center 生成的非交互命令提供相同参数。
- 获取脚本和发布资产时先使用 `uclient-fetch`，其次使用具有 HTTPS 支持的 BusyBox `wget`。不调用 Bash、curl、systemctl、Docker 或 OpenWrt 额外包管理器。
- 依据 `uname -m` 映射 `aarch64/arm64` 到 arm64、`armv7*` 到 armv7；下载固定版本资产与 `SHA256SUMS`，校验后再安装。下载、架构、摘要、证书、注册任一步失败均不得启动服务。
- 安装 Agent 到 `/usr/bin/network-observer-agent`，配置和 CA（TLS 模式）放在 `/etc/network-observer/`，文件权限分别为可执行文件 `0755`、凭证配置 `0600`、公开 CA `0644`。WAL 放在 `/tmp/network-observer/`，默认最大 16 MiB。
- 新增 `/etc/init.d/network-observer` procd 服务，使用 `USE_PROCD=1`、重启策略和 Agent `run` 命令；安装完成后启用并启动服务。升级需保留现有设备配置与 WAL 行为；禁止重新注册或覆盖已有身份。
- OpenWrt 安装器在写入配置或停止现有服务前检测既有 Agent 文件，默认拒绝覆盖。安装日志不得输出 Token、设备凭证或私钥。
- Center 设备向导增加 OpenWrt ARM64/ARMv7 安装方式，生成固定版本的一行 POSIX 命令；该命令复用当前设备向导创建的同一枚一次性 Token，并注明只能注册一台设备。

### RAM WAL 与重启数据缺口

Gateway Lite 的 WAL 默认放在 `/tmp`，避免每个采样周期对有限写入寿命的闪存执行 append、fsync 和压缩。默认容量为 16 MiB，可在配置中调整；容量达到上限时沿用现有 data-gap 能力状态。重启或掉电会丢弃尚未得到 Center ACK 的观察。

普通 Agent 必须继续严格拒绝 WAL 序列与 Center checkpoint 不匹配。只有显式启用的 volatile-WAL 模式可以在本地 WAL 为空、Center 返回更高 checkpoint 时从 Center 的下一个序列继续，并报告一条可见的 `data_gap`：此前最后 ACK 后的观察可能丢失，具体记录数未知。首个设备注册、Center checkpoint 为零时不报告重启缺口。该行为必须有单元测试，确保不能用于默认持久 WAL 的 Agent。

WAL 模式与路径应写入设备配置，避免操作者之后把普通 Agent 错误切换为易失 WAL。用户若选择持久 `/overlay` WAL，文档需提示数据可靠性更高，但采样写入会增加闪存写入；首期默认不选它。

### 采样周期与资源

配置增加本地 `snapshot_interval_seconds`，合法范围为 1 至 60 秒。缺省配置对现有 endpoint/gateway 保持一秒；OpenWrt Lite 安装器将值设为 2 秒。配置非法时启动失败并解释范围。较长周期降低 conntrack 扫描、protobuf 编码和 WAL 写入频率，也降低速率图时间分辨率。

不宣称所有 OpenWrt 设备都能承载 10,000 条活动流。文档要求在目标路由器上观察 RSS、CPU、conntrack 表大小和内存水位；若采集开销过高可把采样周期调至 5 秒或更长。Lite 不通过静默截断流表或伪造计数来降低负载。

## 错误处理和降级

- 两个 conntrack 默认路径均不可读：Agent 仍建立 gRPC 连接并上报 `gateway_conntrack=unavailable`，在能力详情列出检查过的路径及原因；不导致 procd 无限崩溃重启。
- conntrack 文件可读但部分行无法解析：继续上报可解析流，并将 capability 标记为 degraded，给出跳过数量。
- 缺少字节累计计数或发现 `nf_conntrack_acct` 未开启：保留连接与元组信息，counter/confidence 标记不可用，并将字节计数能力降级。
- 软件/硬件 flow offloading 已启用：继续采集并在 capability/安装诊断中提示计数可能不完整；未知时明确报告无法确认，不自动改动防火墙。
- netlink 邻居查询失败：连接采集继续，`lan_neighbors` 报告 degraded/unavailable。
- WAL RAM 容量耗尽：采用现有 WAL gap 记录规则；服务继续采集和上报可接受的部分，数据缺口状态可在 Center 能力信息中见到。
- TLS 下载、证书指纹或 Center TLS 校验失败：停止安装，不自动切换明文。
- Agent 设备凭证被吊销：沿用现有 unauthenticated 停止行为，procd 可重启进程，但不重新注册或生成新身份。

## 验收标准

- CI 可在 Linux runner 上使用 `CGO_ENABLED=0` 编译 arm64 与 ARMv7 Lite 二进制，且发布资产、SHA-256 文件和 Release 说明包含两个架构。
- OpenWrt Lite 构建不包含 endpoint 采集运行路径，不要求 eBPF 对象、Docker socket、Bash、curl、systemd 或 OpenSSL。
- 单元测试覆盖 conntrack 自动路径选择、IPv4/IPv6、TCP/UDP、NAT tuple、缺计数、proc ARP/NDP 能力降级、采样周期范围和 volatile WAL checkpoint 恢复/缺口标记。
- OpenWrt 诊断对 conntrack 缺失、计数关闭、邻居 netlink 不可用、flow offload 开/关/无法识别给出不同且不夸大的状态；运行不会自动改变防火墙、sysctl 或模块。
- POSIX 安装脚本通过 `sh -n`，在模拟的 `uclient-fetch`/`wget`、架构、校验和、TLS/明文参数和已存在配置场景中验证正确行为。
- Center 向导生成的命令携带准确架构选择策略、固定版本、Gateway 类型、地址、安全模式、CA 指纹（TLS）和单次 Token，且不要求人工编辑命令。
- 在 OpenWrt 实机完成 ARM64 与 ARMv7 各一次安装、procd 启停、网络断开重连、WAL 补传、重启缺口标记和卸载/回滚验收后，才宣称硬件验收完成。
- Center 能显示 Gateway 在线状态、网关流、NAT 元组、LAN ARP/NDP 记录、能力降级和数据缺口；现有 endpoint 与通用 Linux Gateway 行为不回归。

## 发布顺序

1. 先合并 OpenWrt Lite 构建标签、架构发布资产、conntrack 自动路径、采样周期与 volatile WAL 语义，并通过 CI。
2. 加入 POSIX 安装器和 procd init 服务，完善安全下载、摘要/证书校验、重复安装防护和原生升级。
3. 扩展 Center 添加设备向导与文档，产出 OpenWrt 命令并进行 Linux 模拟安装验证。
4. 发布固定版本供用户在一台 OpenWrt 路由器试装；依据实机 CPU、RSS、conntrack 字节能力和稳定性调整默认值。
5. 在 ARM64、ARMv7 实机验收后再将对应架构列为稳定支持。

## 不纳入首期

- MIPS、ARMv6 或其他未确认 CPU 架构。
- OpenWrt eBPF、端点进程/容器归属、DNS 元数据、应用载荷、NetFlow/IPFIX/sFlow 监听。
- 改写 Center gRPC/protobuf 协议，修改路由、防火墙、conntrack 内核参数或 Docker 网络。
- OpenWrt 包仓库/IPK 多目标发行、自动内核模块安装、自动 SSH 部署。
- 无数据缺口提示的 RAM WAL 丢失、默认向闪存高频写 WAL，或不经授权自动切换 TLS/明文。
