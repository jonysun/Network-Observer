# OpenWrt Gateway Lite 实施计划

本计划依据已确认的 [OpenWrt Gateway Lite 设计规格](../specs/2026-11-17-openwrt-gateway-lite-design.md)。目标是发布无 Docker 的 OpenWrt 原生 Gateway Agent，兼容当前 Center 注册、TLS/明文、gRPC 数据流和设备管理。

## 阶段 1：梳理 Agent 配置、周期和 conntrack 降级

1. 给 Agent 配置增加采样周期、易失 WAL 策略和 WAL 大小校验；默认行为保持现有 Linux Agent 不变，OpenWrt 安装器显式选 2 秒与 16 MiB。
2. 让采集循环使用配置周期，覆盖合法范围和非法值测试。
3. conntrack 路径未指定时依次选择 `/proc/net/nf_conntrack`、`/proc/net/ip_conntrack`；明确报告实际路径、缺失双向计数、NAT tuple 不可用和 malformed 行。
4. 只读检测 `nf_conntrack_acct` 与 OpenWrt flow-offload 状态；提供独立能力项，无法识别就报告 unknown，不改系统配置。
5. 对 RAM WAL 重启后 Center checkpoint 超前的情形仅在明确启用 `volatile_wal` 时恢复序列，并生成未知数量的数据缺口；持久 WAL 和普通 Agent 保持严格序列错误保护。

阶段退出条件：单元测试证明默认 Linux 行为不变；OpenWrt 配置下两种 conntrack 路径可用；无计数/卸载等限制准确降级；易失 WAL 只能在显式模式下跳过已被 Center 确认的旧序列并留下数据缺口。

## 阶段 2：构建专用 Lite 二进制

1. 增加 `openwrtlite` 构建标签，使运行路径不启用端点进程扫描、Docker、DNS、Socket Diag、eBPF 探测/加载；Gateway、邻居采集、TLS/gRPC/WAL 保留。通用代码继续共享，避免协议分叉。
2. Lite 构建只接受 `observer_type=gateway`；错误地要求 endpoint 时清楚退出。
3. CI 交叉构建静态 ARM64 (`GOARCH=arm64`) 与 ARMv7 (`GOARCH=arm GOARM=7`) 二进制，运行可执行的 Go 单测、检查文件类型并上传 Release artifact。
4. Release workflow 发布固定命名的两个 OpenWrt 二进制与统一 `SHA256SUMS`；现有 amd64/arm64 Agent、eBPF 对象和 Docker 镜像保持原路径。

阶段退出条件：两个目标稳定构建；不带 eBPF 对象仍能运行；Lite 产物没有端点采集依赖；现有 amd64/arm64 发布不回归。

## 阶段 3：OpenWrt 安装器与 procd

1. 新增 POSIX `sh` 安装器，支持交互与 Center 参数化命令；用 `uname -m` 选择 arm64/armv7，检查 OpenWrt/procd、root、下载工具、`/tmp` 可用空间和目标文件冲突。
2. 优先通过 `uclient-fetch`，再选择带 HTTPS 的 BusyBox `wget` 下载 Release；按 SHA-256SUMS 校验；不具备安全下载能力时给出 SCP 继续方式或停止，不安装不可信二进制。
3. TLS 模式用 Agent 自带子命令验证中心 CA SHA-256 指纹，明文模式跳过证书步骤且只连接指定明文端口。
4. 只有下载校验、证书检查和一次性 Token 注册全部成功后，才安装配置与程序、写 procd 服务并启动。
5. 默认 Agent `/usr/bin/network-observer-agent`、配置 `/etc/network-observer/agent.json`、RAM WAL `/tmp/network-observer/agent.wal`；配置 0600，证书 0644，WAL 上限 16 MiB；既有设备身份禁止覆盖。
6. 提供安全的一键升级与卸载/回滚步骤：保留身份配置，不重复注册；升级前备份旧二进制，启动失败恢复旧版本。
7. CI 用 shellcheck（如环境可用）、`sh -n` 和模拟命令验证下载器选择、架构映射、参数 quoting、失败清理及不输出凭证。

阶段退出条件：POSIX shell 语法正确；所有失败路径不会写入半成品配置或启动服务；ARM64/ARMv7 模拟安装能生成正确 procd 服务并保留凭证。

## 阶段 4：Center 添加设备向导和中文文档

1. 在添加设备向导增加 “OpenWrt Gateway Lite” 安装方式，限定为 gateway 类型；设备类型选择 endpoint 时不展示此项。
2. 生成固定版本命令，携带名称、Center gRPC 地址、Token、安全模式、TLS Web 地址/指纹和 TLS server name；命令通过 `sh` 而非 Bash 执行。
3. 显示 OpenWrt Lite 的能力限制、架构要求、明文传输提示、Token 单次使用提示及 conntrack/offload 可能导致计数不完整的说明。
4. 更新中文安装手册：目标架构、环境预检、procd 管理、升级/卸载、常见 conntrack 路径、流量卸载诊断、Endpoint Agent 配合与不支持能力。
5. 运行前端生产构建与 UI 相关测试；在桌面和窄屏下确认命令可复制、可换行阅读、错误状态明确。

阶段退出条件：用户从 Center 生成命令后可在符合条件的路由器直接执行；命令不会假设 curl/Bash/systemd/Docker；文档清楚区分设备可用性与计数精确性。

## 阶段 5：集成验收与发布

1. 运行 `go test ./...`、`go vet ./...`、前端构建、Compose 校验、Lite 交叉编译与发布 workflow 静态检查。
2. 使用样例 conntrack 文件验证 IPv4/IPv6 TCP/UDP、NAT、计数缺失、malformed 行、conntrack 文件缺失和 ARP fallback；验证 Runtime 能显示能力降级。
3. 使用模拟 `/proc`、`/sys`、`/etc/config/firewall` 检查卸载识别；不实际改动宿主机防火墙。
4. 发布固定预览版本给一台用户 OpenWrt 设备试装，收集架构、内核、conntrack 字节计数、offload、CPU/RSS、内存和 `/tmp` 使用数据；真实设备通过前不宣称硬件验收完成。
5. 实机验收包括首次注册、TLS/明文各自路径（明文需 Center 已开放）、procd 重启、Center 暂时离线及恢复补传、路由器重启后的未知数量 gap、升级保留身份、卸载回滚。

阶段退出条件：CI 与模拟测试通过；真实 ARM64/ARMv7 设备数据逐项记录；已知限制与该固件可用能力一并交付。

## 风险与控制

- OpenWrt 衍生固件差异大：由 Agent 运行时能力检测和清晰降级说明，不以型号/架构推断 conntrack 计数必然可用。
- 硬件 offload 可能绕过计数：只检测并提示，不自动关闭或配置 firewall。
- OpenWrt BusyBox HTTPS 能力不一致：使用经过验证的下载实现；不可安全下载时停止并给 SCP 导入方式。
- `/tmp` 会因重启清空：易失 WAL 模式只在 Lite 构建开启，Center 序列恢复记录未知数量 gap；要求长期补传的用户可选择持久 overlay WAL 并接受闪存写入。
- ARMv7 Go 兼容性：通过真实 ARMv7 OpenWrt 设备测试后再列为稳定支持。
- 爆大活动表影响路由器：默认 2 秒刷新，不截断数据；记录 CPU/RSS/conntrack 项数，并允许用户加大周期。

## 本轮不做

- MIPS/ARMv6、eBPF 网关 hook、进程/容器采集、应用载荷、通用 DNS 采集。
- 自动写 UCI、iptables/nftables、sysctl 或加载内核模块。
- 新的 Center 协议或数据库服务；不将接口 RX/TX 总数混入连接流量。
- 面向任意固件的 IPK/软件源构建、远程 SSH 执行或自动发现路由器型号。
