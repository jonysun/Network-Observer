# Network Observer 部署与 Agent 接入实施计划

## 目标

完成 Center 的轻量/专业部署档、一次性 Agent Token、设备添加向导和可复制安装命令，并通过 GitHub Actions 发布固定版本的多架构镜像与 Agent 二进制。

## 交付分解

### 第一阶段：Compose 与发布

1. 将核心服务固定为 `docker-compose.light.yml`：PostgreSQL、ClickHouse、Observer、Web。
2. 新增 `docker-compose.pro.yml`：Prometheus、Node Exporter、Grafana 和监控数据卷；所有镜像使用固定版本。
3. 保留根目录 `docker-compose.yml` 作为 light 兼容入口，并删除核心环境对 Grafana 密码的强制依赖。
4. 新增 CI workflow：Go 测试、vet、Vue 构建和 Compose 校验。
5. 新增 release workflow：`vX.Y.Z` 标签发布 Center/Agent amd64/arm64 镜像、Agent 二进制、校验和和安装脚本。

### 第二阶段：一次性 Token 与 Center API

1. PostgreSQL 增加一次性 Token 表、有效期、类型、消费时间、撤销时间和审计索引。
2. 新增管理员 API 创建/撤销 Token；Token 明文只在创建响应返回。
3. 注册事务原子完成 Token 校验、类型匹配、设备创建、凭证哈希和 ingestion checkpoint；并发注册只能成功一次。
4. Agent 注册成功后将 Center 返回的每设备凭证写入本地配置，后续重连使用该凭证。
5. 保留已注册旧 Agent 的全局 Token 兼容认证，但禁止全局 Token 注册新设备。

### 第三阶段：添加设备与命令生成

1. 设备页增加“添加设备”向导，输入名称、类型、Center 对外地址/端口、TLS 模式、版本和 Token 有效期。
2. 显示证书 SHA-256 指纹；TLS 默认开启，明文模式明确标注风险。
3. 生成同一 Token 对应的 Docker Compose、`docker run` 和 Linux 原生单行命令。
4. Docker 变体提供 `light` 与 `pro` 选择：Agent 本身使用固定版本镜像；`pro` 仅指 Center 的 Prometheus/Grafana 监控部署，不把 Grafana 强行加入 Agent。
5. 复制、撤销、过期和已消费状态在 UI 中明确显示，命令正文不写入日志。

### 第四阶段：Agent 安装器

1. 新增 Bash 安装器，支持交互菜单和非交互参数。
2. 原生模式下载固定版本二进制、校验 SHA-256、安装 systemd、写入 `0600` 配置并启动服务。
3. Docker 模式下载同版本 Agent 二进制完成一次注册，生成固定镜像版本 Compose 或 `docker run` 配置，持久化 WAL。
4. 支持 TLS CA/证书指纹校验、明文模式、Endpoint/Gateway、amd64/arm64；已有配置时拒绝覆盖。
5. OpenWrt v1 使用 Docker；原生 procd 留到后续版本。

## 验证顺序

- `docker compose -f docker-compose.light.yml config` 不需要 Grafana 变量。
- `docker compose -f docker-compose.light.yml -f docker-compose.pro.yml config` 需要并正确使用 Grafana 变量。
- Go 单元测试覆盖 Token 哈希、过期、类型匹配、单次消费和旧 Agent 认证。
- Vue 构建通过，命令生成器的 TLS/明文、light/pro、Endpoint/Gateway 分支有测试或可复现脚本检查。
- Bash `bash -n`、固定版本下载校验和 Docker Compose 语法检查通过。
- 运行 `go test ./...`、`go vet ./...`、`npm run build`，并在可用 Docker 环境执行镜像构建。

## 回滚

关闭新增 Agent 采集开关或使用旧 Center 镜像；数据库新增表和列保持向后兼容。旧 Agent 继续使用旧凭证兼容认证路径。
