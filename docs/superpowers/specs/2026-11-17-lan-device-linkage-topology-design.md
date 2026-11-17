# 局域网设备联动与分网段拓扑设计

## 背景

当前 Center 已保存两类相关数据：

- `devices`：已注册的 Endpoint / Gateway Agent，已有名称、Agent 类型、系统、能力和在线状态；
- `lan_neighbor_sightings`：各 Agent 只读上报的 ARP/NDP 邻居快照，Center 已按网段、IP、MAC 合并为局域网资产。

当前设备名称只能在注册时设置，局域网资产只有按网段和 MAC 保存的名称别名，没有统一的人类可读设备类别，也没有拓扑查询和展示。

## 目标

1. 在“设备”页面修改已注册 Agent 的自定义名称和人类可读设备类别。
2. 在“局域网资产”页面修改发现设备的自定义名称和设备类别。
3. 对 Agent 自己上报的本机邻居记录，将设备页面的名称和类别带到对应局域网资产。
4. 按 `network_id` / 网段聚合拓扑。同一网段只显示一张拓扑；多个 Agent 观察同一网段时合并重复节点；不同网段分别显示。
5. 拓扑明确表示 ARP/NDP 的观察关系，不声称发现了交换机或真实物理线路。
6. 保留现有 LAN 资产、连接筛选、设备吊销和多 Agent 合并行为的兼容性。

## 非目标

- 不主动扫描局域网，不发送 ARP、ICMP 或端口探测；
- 不推断交换机端口、无线 AP、路由器物理连接或链路带宽；
- 不把注册 Agent 的 `endpoint/gateway` 角色替换成人类可读设备类别；
- 不把历史流量汇总按名称修改。名称和类别只影响当前设备/资产展示与之后的查询结果。

## 术语与类别

`observer_type` 继续只表示 `endpoint` 或 `gateway`。新增 `device_category` 表示用户可读类别，允许值为：

`unknown`、`computer`、`router`、`gateway`、`laptop`、`tablet`、`phone`、`tv`、`server`、`nas`、`other`。

Web 使用中文标签显示这些值。数据库保存稳定的英文枚举值，便于 API 和未来国际化。缺失值按 `unknown` 处理。

## 名称和类别归属规则

### 已注册 Agent

- `devices.name` 继续作为设备显示名称，并允许管理员编辑；
- `devices.device_category` 保存管理员选择的类别；
- 原有注册名称作为初始名称，原有 Agent 不需要重新注册；
- 编辑名称或类别必须写入审计日志，设备凭证和 Agent 配置不变。

### 局域网资产

局域网资产的身份仍由现有聚合规则决定：有 MAC 时使用 `network_id + MAC`，没有 MAC 时使用 `network_id + IP + observer_device_id`。名称和类别采用以下优先级：

1. 局域网资产手动设置；
2. 该资产是某个注册 Agent 的 `is_local=true` 记录时，使用该 Agent 的自定义名称和类别；
3. 该 Agent 的现有注册名称；
4. `未知设备` / `unknown`。

当多个 Agent 对同一资产有本机记录时，手动设置仍然最高；没有手动设置时选最新 `last_seen` 的本机 Agent 资料。远端资产不会因为“观察它的 Agent”而继承观察 Agent 的名称。

### 手动清空

清空 LAN 名称或类别会删除对应手动字段，使其回退到上述自动优先级。设备页面清空名称时回退为注册时名称；类别清空时回退为 `unknown`。

## 数据模型变更

### PostgreSQL

在 `devices` 增加：

```sql
device_category TEXT NOT NULL DEFAULT 'unknown'
```

在 `lan_device_aliases` 增加：

```sql
device_category TEXT NOT NULL DEFAULT 'unknown'
```

现有表通过 `ALTER TABLE ... ADD COLUMN IF NOT EXISTS` 兼容升级。旧行按 `unknown` 读取。名称仍使用已有 `lan_device_aliases.alias` 字段，不新建重复的名称表。

### 拓扑数据

拓扑不持久化，按当前邻居快照实时计算。接口返回多个网段组：

```json
{
  "groups": [
    {
      "network_id": "192.168.31.0/24",
      "network_prefix": "192.168.31.0/24",
      "center": {"id": "center", "name": "Center", "known": true},
      "agents": [{"id": "...", "name": "NAS Agent", "type": "gateway", "last_seen": "..."}],
      "assets": [{"key": "...", "name": "客厅电视", "category": "tv", "mac": "...", "addresses": [], "stale": false, "ip_conflict": false}],
      "edges": [
        {"from": "agent:...", "to": "network:192.168.31.0/24", "kind": "observes"},
        {"from": "asset:...", "to": "network:192.168.31.0/24", "kind": "neighbor"}
      ]
    }
  ],
  "unassigned": {"agents": [], "assets": []}
}
```

同一个资产只在所属网段组出现一次；`observers` 仍保留在资产详情中，用于显示多个观察点。无 MAC 的记录按现有规则保留，不强行与其他 IP 合并。

## Center 网段判定

Center 不是 Agent，不会凭空产生 ARP/NDP 观察记录。为满足“Agent 与 Center 同网段只显示一个拓扑”的要求，Center 使用以下自动候选地址：

1. `TLS_CERT_HOST` 是 IP 时直接使用；
2. `TLS_CERT_HOST` 是主机名时解析其 A/AAAA 记录；
3. Center 容器可见的非回环接口地址作为补充候选。

候选地址落入某个 Agent 上报的 `network_prefix` 时，将 Center 放入该网段组；多个候选命中同一网段仍只显示一个 Center 节点。无法确认时不猜测归属，放入 `unassigned` 并标记“Center 网段未确认”。

## API

### 更新设备资料

`PATCH /api/v1/devices/{id}/profile`

请求体：

```json
{"name":"客厅主机","device_category":"computer"}
```

服务端校验名称长度和类别枚举，更新 `devices`，写入 `audit_log`，返回完整设备对象。不存在或已吊销的设备返回 404。

### 更新 LAN 资产资料

继续使用 `PATCH /api/v1/lan/devices/alias`，向后兼容已有 `network_id`、`mac`、`name` 字段，并增加 `device_category`。空名称和 `unknown` 类别删除手动覆盖。返回 204，审计对象仍为 `lan_device_alias`。

### 查询拓扑

`GET /api/v1/lan/topology`

复用管理员认证和现有 30 天邻居数据、2 分钟网段发现窗口。拓扑接口只返回当前快照，不写入 ClickHouse，不改变流量统计。支持 `network_id` 查询参数以便从资产详情打开指定网段。

## Web 行为

- “设备”表新增设备名称和类别列，使用行内编辑或详情编辑；保存后刷新设备列表和 LAN 资产列表；
- “局域网资产”详情新增名称和类别表单，显示名称来源和类别来源；
- LAN 表格显示设备类别，设备页注册 Agent 对应的本机资产显示相同名称和类别；
- 设备、LAN 资产、实时连接之间保留现有跳转；拓扑节点点击可跳转到设备详情、LAN 资产详情或连接筛选；
- LAN 页面增加“拓扑”区域，按网段显示独立 SVG 图。网段数量变化、刷新、名称/类别保存后重新布局；
- 布局使用确定性分层：Center/Agent 在上层，网段在中层，LAN 资产在下层；同层节点按稳定 key 排序，避免每次刷新跳动；
- 设备过期、IP 冲突、未确认 Center 网段使用明确状态样式和文本；空数据、权限错误和部分拓扑失败都有可见状态。

## 错误处理与兼容性

- 资料更新失败不修改本地页面状态，保留用户输入并显示服务端错误；
- 拓扑查询失败不影响 LAN 表格和实时连接；拓扑区域显示错误并允许单独刷新；
- 旧 Center 数据库迁移后所有旧设备和 LAN 资产都能正常显示；
- API 响应中的未知字段由前端忽略，旧 Agent 不需要升级；
- 不改变现有 ARP/NDP 采集权限、快照周期、重复观测合并和 IP 冲突判定。

## 测试与验收

- PostgreSQL migration：旧表、已有设备、已有 LAN alias 升级后类别默认为 `unknown`；
- Store：设备资料更新、LAN 名称/类别覆盖和清空、手动优先级、最新本机 Agent 回退、多 Agent 合并、无 MAC 资产和 IP 冲突；
- API：认证、404、无效类别、名称长度、审计记录、拓扑按网段分组、Center 命中/未命中/无法解析；
- Web：设备资料编辑、LAN 资料编辑、两页联动、同网段合并、不同网段分组、节点跳转、空/过期/冲突状态；
- 运行 `go test ./...`、`go vet ./...`、`npm run build` 和 Compose 配置校验；
- 在至少两个 Agent 上报同一网段和两个不同网段的样本数据，确认不会生成重复拓扑组或错误跨网段边。

## 发布与回滚

这是 PostgreSQL 与 Web/API 的向后兼容新增能力，沿用固定版本发布。回滚 Center 镜像后新增列和旧 alias 数据保留，不影响 Agent 采集；回滚前端时旧页面忽略新增字段。
