# 部署与 Agent 设备接入设计

**状态：** 设计已于 2026-11-15 在对话中确认；中文规格待审阅
**范围：** 精简版 Center Compose、可选主机监控、GitHub 镜像发布、一次性 Agent 注册、中心生成安装命令，以及 Linux 安装方式。

## 目标

- 最小化部署 Center 时，不要求配置或下载 Grafana、Prometheus、Node Exporter。
- 通过独立 Compose overlay 按需启用主机监控。
- 由 GitHub Actions 发布固定版本的 Center/Agent 多架构镜像和 Linux Agent 二进制。
- 在 Center 的“设备”页面点击“添加设备”，生成可以复制到 Agent 主机直接执行的安装命令。
- 安装命令提供 Docker 与 Linux 原生两类；Docker 支持 Compose 配置和 `docker run` 命令，原生方式使用交互式 Bash 安装器及 systemd。
- 安装命令包含设备类型、名称、Center 地址/端口、一次性 Token、固定 Agent 版本和 TLS 引导信息，尽量不再要求用户手工拼接参数。
- 继续支持 Agent 按节点选择 TLS + Token 或明文 Token；TLS 为默认选项。

## 当前行为与约束

- 当前 `docker-compose.yml` 同时定义 PostgreSQL、ClickHouse、Center、HTTPS Web、Prometheus、Node Exporter、Grafana，因此没有使用 Grafana 也需要配置 Grafana 密码。
- Center 当前用全局 `ENROLLMENT_TOKEN` 注册新设备，并将同一个值当作长期 Agent 凭证；Web 可以查看和吊销设备，但不能创建独立接入凭证。
- Agent 已支持 Linux Endpoint/Gateway、配置文件、Docker Compose 和 systemd 单元，但安装步骤仍需人工下载二进制、复制 CA、注册并配置服务。
- v1 发布支持 Linux amd64 和 arm64。OpenWrt 可使用 Docker；原生 procd 安装留到后续版本。
- 保留现有设备身份、凭证吊销、WAL 补传、按 Agent 自选 TLS 模式及已有流量采集行为。
- Center 的 HTTPS Web 端口和 Agent gRPC 端口可能不同，且 NAT/反向代理端口不一定能推导。管理员必须能修改要写入安装命令的外部 Agent 地址与端口。

## 设计决策

### 核心部署与可选监控拆分

`docker-compose.yml` 只保留 PostgreSQL、ClickHouse、Observer Center 和 HTTPS Web。Prometheus、Node Exporter、Grafana 及其数据卷移至 `docker-compose.monitoring.yml`，需显式同时加载两个 Compose 文件。只有启用监控 overlay 时才要求 `GRAFANA_ADMIN_PASSWORD`；核心 `.env.example` 不应包含必填监控变量。

Center 内置控制台和 `/metrics` 在不启用 overlay 时仍可用。PostgreSQL 与 ClickHouse 不可省略：前者保存设备身份、配置和审计，后者保存分钟流量和连接 Presence 历史。

### CI 与固定版本发布

GitHub Actions 在 Pull Request 和 `main` 分支推送时运行 Go 测试与 `go vet`、Vue 生产构建和 Compose 配置校验。推送 `vX.Y.Z` 标签时构建该标签对应提交，并发布：

- `ghcr.io/<owner>/network-observer:vX.Y.Z`，支持 `linux/amd64` 和 `linux/arm64`。
- `ghcr.io/<owner>/network-observer-agent:vX.Y.Z`，支持 `linux/amd64` 和 `linux/arm64`。
- Linux amd64、arm64 静态 Agent 二进制、SHA-256 校验文件及安装器脚本，作为 GitHub Release 附件。

发布和安装均使用明确版本，禁止依赖浮动的 `latest`。Center 生成的命令默认使用当前稳定发布版本，同时允许管理员选择其他已发布版本。命令中展示版本，安装器校验二进制摘要，生成的 Compose 文件固定到同一版本。

### Center 添加设备与命令生成

设备页“添加设备”打开向导。向导包含名称、类型（Endpoint/Gateway）、Agent 对外可访问的 Center 主机/IP 与 gRPC 端口、传输安全模式和 Token 有效期。地址与端口可预填 Center 已配置的外部地址，无法可靠推断时提供空值/建议值并要求管理员确认或手动填写。

点击“生成安装命令”时，Center 创建一枚加密安全随机、仅可使用一次、与 Endpoint/Gateway 类型绑定的短期 Token。默认 24 小时，允许 1 小时至 30 天。Center 数据库仅保存 Token 的 SHA-256 哈希，明文仅在响应及当前向导中展示，不写日志；管理员可以在设备尚未注册前撤销它。Token 创建和消费写入审计日志。

Token 创建后，向导显示安装方式选项：

- **Docker：** 提供可复制的完整 Compose 文件和 `docker run` 命令二选一下拉菜单。Compose 内容包含固定 Agent 镜像、设备类型、Center 地址、一次性 Token、TLS 设置、必要挂载/权限和持久 WAL 卷。Endpoint 可选只读挂载 Docker socket 以读取容器元数据；Gateway 不挂载该 socket。
- **Linux 原生：** 提供一条可复制的安装命令，调用固定版本的安装器，将 Center 地址、Token、名称、类型、安全模式、证书指纹和版本作为参数传入。安装器下载对应架构的二进制、验摘要、注册并配置 systemd。

同一个安装向导的所有命令形式共用同一枚一次性 Token，因此只能成功注册一台设备。向导明确显示这一点，并提供复制按钮和撤销 Token 操作。若用户需要安装第二台设备，必须再次点击“添加设备”生成新 Token。

安装命令含敏感的一次性 Token。页面应提供一键复制、一次性展示和明显提示；不要把命令写入服务端日志、审计详情或遥测。仅保存 Token 哈希和普通配置元数据。安装命令会进入操作者的终端历史记录，界面应提示使用后清理历史记录或通过安全的粘贴方式执行。Token 有效期与单次消费限制用于缩小泄露风险。

### TLS 信任引导与明文模式

TLS 模式默认启用。Center 为当前 gRPC TLS 证书提供可读取的 SHA-256 指纹，命令中携带该指纹。安装器从管理员确认的 Center 地址获取公开证书，并在使用前校验指纹；下载过程可以先建立未验证连接，但下载的证书必须与向导显示的指纹完全一致，随后 Agent 使用该证书验证 gRPC TLS 服务端名称和连接。若 Center 使用公网可信证书，也可以走系统 CA 校验。不得静默跳过证书验证或在证书不匹配时继续。

明文模式由管理员为该 Agent 明确选择。向导显示这不会加密 Token 或流量，生成的命令不含 TLS CA 参数，并应指向已启用的明文 gRPC 端口。Center 不应因创建了明文 Agent 就自动为整个实例开放明文端口；此端口仍由管理员单独启用。

截图中 Beszel 的“公钥”字段对应其 SSH 信任模式，Network Observer 不使用 SSH 执行远程命令，因此本项目不要求用户提供 SSH 公钥。这里显示和验证的是 Center 的 TLS 证书指纹。

### 一次性 Token 与设备凭证换发

`POST /api/v1/devices/enrollment-tokens` 由管理员会话认证，生成 Token 并只返回一次明文。`DELETE /api/v1/devices/enrollment-tokens/{id}` 可撤销尚未使用的 Token。

注册时，Center 在同一个 PostgreSQL 事务中校验 Token 有效性、未使用、未撤销、类型匹配，然后创建设备、设备凭证哈希和 ingestion checkpoint，并标记 Token 已使用。发生类型不匹配或设备创建失败时事务回滚，Token 不会被消耗；并发注册中至多一个请求成功。Center 返回新生成的随机设备 ID 与长期设备凭证，Agent 在将配置以 `0600` 权限写入磁盘前，用长期凭证替换一次性 Token。后续重连仅使用设备凭证；设备吊销会使其失效。

迁移期内，旧 Agent 仍可用其已配置的全局 `ENROLLMENT_TOKEN` 认证；该全局 Token 不得再用于注册新设备。新设备和长期凭证必须使用每设备独立的密钥哈希。

### 交互式安装器

仓库提供 Bash 安装器，GitHub Release 也提供同一脚本。除了手动运行菜单模式，还要支持 Center 生成的非交互式参数模式，使复制的单行命令无需再次输入 Token 或重复填写字段。脚本提供原生 systemd、Docker Compose 两种安装方式；Docker 模式再支持 Compose 文件和 `docker run`。

安装器支持 Endpoint/Gateway，检查 Linux、amd64/arm64、Docker 或 systemd 条件，使用固定版本下载对应 Agent，验证 SHA-256，获取并校验 Center TLS 证书，然后执行一次注册。原生模式需要 root，将二进制放入 `/usr/local/bin`、配置放在 `/etc/network-observer/agent.json` 且权限为 `0600`，安装/启动 systemd 服务。Docker 模式将配置与证书存于权限为 `0700` 的宿主目录，使用宿主 UID/GID 运行容器以读取私有配置，并将 WAL 放入具名卷。Gateway 镜像不挂载 Docker socket。

安装器不得覆盖现有 Agent 身份；发现已有配置时停止并说明如何继续使用已有身份或先吊销旧设备。下载、摘要、证书指纹、注册或服务启动失败时停止并输出恢复所需信息，不得输出 Token 或长期凭证。注册成功但本地配置写入失败时，明确显示已创建的设备 ID，提示管理员吊销后重试。

v1 原生服务只支持 systemd Linux；OpenWrt 可用 Docker 方式安装，原生 procd 暂不实现。

## 安全与兼容性

- Enrollment Token 与设备凭证都由安全随机源生成；Center 只保存哈希。
- Token 必须满足单次使用、有效期、类型绑定和管理员授权；这些限制由服务端强制执行。
- TLS 是安装默认值；命令必须使用经过指纹或系统 CA 验证的 Center 证书，不能忽略验证失败。
- Center 不记录命令正文、一次性 Token、设备凭证或证书私钥；指纹和证书是公开材料。
- Agent 配置文件始终使用所有者可读写权限；Docker 容器以匹配的 UID/GID 读取，不降低文件权限来迁就容器。
- 保留旧 Agent 配置的迁移认证路径；新设备不接受全局长期 Enrollment Token。
- 安装器使用固定版本镜像/二进制并验证二进制校验和，不安装 `latest`。
- 安装命令不可避免地含一次性 Token，应明确提示 shell history 的暴露可能，并通过短期过期、一次消费和可撤销机制限制风险。

## 失败处理

- 不加载监控 overlay 时，核心 Center 正常启动且不要求 Grafana 环境变量；加载 overlay 时未提供 Grafana 密码，Compose 清楚报错。
- Token 已过期、已消费、已撤销或类型不匹配时，注册失败且不创建设备。
- Token 消费、设备、凭证哈希和 checkpoint 创建在同一事务内；失败全部回滚。
- TLS 证书下载失败、指纹不匹配或系统 CA 校验失败时，TLS 安装停止，不提供自动降级到明文。
- 只有管理员明确选择明文模式时才按明文流程生成命令；若 Center 明文端口未启用，应在安装前提示连接失败的原因。
- 版本不存在、平台架构不支持、下载/校验失败、注册失败或服务不能启动时，安装器停止并提示修复步骤，不泄露凭证。
- 对新安装器、向导生成的命令或已存在 Agent 配置，检测结果必须说明下一步，不静默覆盖或重复注册。

## 验收标准

- 只有核心变量时 `docker compose config` 成功，核心部署只启动 PostgreSQL、ClickHouse、Observer 和 Web。
- 监控 overlay 在提供 Grafana 密码时通过校验，并启动 Prometheus、Node Exporter、Grafana；不改变核心部署要求。
- Pull Request 和 main 推送均运行 Go、Vue 和 Compose 校验。
- `vX.Y.Z` 标签发布两个架构的 Center/Agent 镜像、Linux Agent 二进制和 SHA-256 文件；生成的安装命令及 Compose 内容固定到相同版本。
- Center 向导可编辑名称、类型、外部主机/IP、gRPC 端口、安全模式和有效期，并可复制原生命令、Compose 文件或 `docker run` 命令。
- 同一向导生成的所有命令共用一枚单次 Token；复制的任一方式只可成功注册一次。向导可在注册前撤销 Token。
- Token 仅显示一次、数据库仅存哈希、到期/类型/消费/撤销状态正确；并发请求最多注册一个设备；注册失败不会消耗 Token。
- 成功注册后 Agent 在保存 `0600` 配置前切换到设备长期凭证，重连成功；吊销设备后认证失败。
- TLS 安装通过指纹或系统 CA 校验证书；证书错配、TLS 错误不会自动转明文。明文命令只在管理员明确选择且 Center 端口已开启时可用。
- Shell 检查覆盖原生/Docker Compose/`docker run`、Endpoint/Gateway、TLS/明文、amd64/arm64、下载摘要失败、已存在配置和服务启动失败等路径。
- 在 Linux amd64/arm64 干净环境中，管理员从 Center 复制任一命令即可完成对应安装；OpenWrt Docker 流程有文档说明，原生 procd 明确不在 v1 范围。

## 实施顺序

1. 拆分核心和监控 Compose，更新部署文档。
2. 实现 Center 一次性 Token 的创建、撤销、审计、原子消费和每设备凭证换发。
3. 在设备页实现“添加设备”向导、外部地址输入、证书指纹展示、一次性 Token 和多种可复制命令。
4. 实现 TLS 证书下载/指纹校验、非交互 Bash 安装器、systemd/Docker Compose/`docker run` 安装方式。
5. 配置 GitHub Actions CI 与 tagged 多架构镜像/二进制发布。
6. 验证 NAS 核心部署以及单个 Linux Agent，再扩展 Gateway 和 OpenWrt Docker。

## 不纳入 v1

- OpenWrt 原生 procd、Windows/macOS Agent、Center 自动发现 DNS 名称、自动签发/轮换 TLS 证书、SSH 远程执行或未授权的无人值守注册。
