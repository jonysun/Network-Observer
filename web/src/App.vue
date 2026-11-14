<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import {
  Activity, ArrowDownToLine, ArrowUpFromLine, Box, Check, ChevronDown,
  CircleHelp, Clock3, Cpu, Database, EthernetPort, Filter, Gauge,
  LayoutDashboard, ListFilter, LoaderCircle, LogOut, Network, Plus,
  RefreshCw, Search, Settings, ShieldCheck, SlidersHorizontal, Trash2,
  Unplug, X,
} from '@lucide/vue'

type Capability = { name: string; status: string; detail?: string; checked_at: string }
type Device = {
  id: string; name: string; type: string; os: string; architecture: string
  agent_version: string; online: boolean; last_seen: string; capabilities: Capability[]
}
type Connection = {
  id: string; flow_id: string; device_id: string; observer_id: string; observer_type: string
  local_is_source: boolean; observed_at: string; interface: string; network_namespace: string
  protocol: string; state: string; source_ip: string; source_port: number
  destination_ip: string; destination_port: number; bytes_source_to_destination: number
  bytes_destination_to_source: number; total_bytes_source_to_destination: number
  total_bytes_destination_to_source: number; rate_bytes_source_to_destination: number
  rate_bytes_destination_to_source: number; pid: number; process_name: string
  container_id: string; container_name: string; service_name: string
  attribution_source: string; attribution_reason: string; sampling_rate: number
  counter_source: string; confidence: string; active: boolean
}
type Rule = {
  id: string; name: string; priority: number; enabled: boolean
  matcher: Record<string, unknown>; attribution: Record<string, unknown>
  effective_from: string; created_at: string
}
type TrafficPoint = {
  bucket: string; dimension: string; value: string; protocol: string
  direction: string; bytes: number; packets: number; estimated: boolean
}
const CONNECTION_PAGE_SIZE = 100

const navItems = [
  { key: 'overview', label: '网络总览', icon: LayoutDashboard },
  { key: 'devices', label: '设备', icon: Network },
  { key: 'connections', label: '实时连接', icon: Activity },
  { key: 'traffic', label: '流量分析', icon: Gauge },
  { key: 'rules', label: '归属规则', icon: ListFilter },
]
const view = ref('overview')
const username = ref('admin')
const password = ref('')
const signedIn = ref(false)
const loading = ref(true)
const busy = ref(false)
const errorMessage = ref('')
const devices = ref<Device[]>([])
const connections = ref<Connection[]>([])
const connectionsTotal = ref(0)
const rules = ref<Rule[]>([])
const traffic = ref<TrafficPoint[]>([])
const searchText = ref('')
const selectedDevice = ref('')
const selectedProtocol = ref('')
const attributionFilter = ref('')
const trafficDimension = ref('destination_port')
const trafficPeriod = ref('24h')
const sortBy = ref<'rate' | 'age'>('rate')
const connectionPage = ref(0)
const showAttribution = ref(false)
const editingConnection = ref<Connection | null>(null)
const overrideForm = ref({ process_name: '', container_name: '', service_name: '', reason: '' })
const showRuleForm = ref(false)
const ruleForm = ref({
  name: '', device_id: '', protocol: '', destination_port: '', process_name: '',
  container_id: '', service_name: '', container_name: '',
})
const lastUpdated = ref<Date | null>(null)
const revokingDeviceID = ref('')
let socket: WebSocket | null = null
let trafficTimer: number | undefined
let deviceStatusTimer: number | undefined

const onlineDevices = computed(() => devices.value.filter((device) => device.online).length)
const tcpCount = computed(() => connections.value.filter((connection) => connection.protocol === 'tcp').length)
const udpCount = computed(() => connections.value.filter((connection) => connection.protocol === 'udp').length)
const unknownCount = computed(() => connections.value.filter((connection) => connection.attribution_source === 'unknown').length)
const receiveRate = computed(() => connections.value.reduce((sum, connection) => sum + receive(connection), 0))
const sendRate = computed(() => connections.value.reduce((sum, connection) => sum + send(connection), 0))
const filteredConnections = computed(() => {
  const needle = searchText.value.trim().toLowerCase()
  return connections.value.filter((connection) => {
    const device = devices.value.find((item) => item.id === connection.device_id)
    const matchesDevice = !selectedDevice.value || connection.device_id === selectedDevice.value
    const matchesProtocol = !selectedProtocol.value || connection.protocol === selectedProtocol.value
    const matchesAttribution = !attributionFilter.value || connection.attribution_source === attributionFilter.value
    const searchable = [connection.source_ip, connection.source_port, connection.destination_ip,
      connection.destination_port, connection.process_name, connection.container_name,
      connection.container_id, connection.service_name, device?.name].join(' ').toLowerCase()
    return matchesDevice && matchesProtocol && matchesAttribution && (!needle || searchable.includes(needle))
  }).sort((a, b) => {
    if (sortBy.value === 'age') return Date.parse(a.observed_at) - Date.parse(b.observed_at)
    return send(b) + receive(b) - send(a) - receive(a)
  })
})
const visibleConnections = computed(() => filteredConnections.value.slice(
  connectionPage.value * CONNECTION_PAGE_SIZE,
  (connectionPage.value + 1) * CONNECTION_PAGE_SIZE,
))
const connectionPageCount = computed(() => Math.max(1, Math.ceil(filteredConnections.value.length / CONNECTION_PAGE_SIZE)))
const ruleRows = computed(() => rules.value)
const trafficTotals = computed(() => {
  const totals = new Map<string, { bytes: number; estimated: boolean }>()
  for (const point of traffic.value) {
    const total = totals.get(point.value) ?? { bytes: 0, estimated: false }
    total.bytes += point.bytes
    total.estimated ||= point.estimated
    totals.set(point.value, total)
  }
  return [...totals.entries()].map(([value, total]) => ({ value, ...total })).sort((a, b) => b.bytes - a.bytes).slice(0, 20)
})

function formatBytes(value: number): string {
  if (!Number.isFinite(value) || value <= 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let current = value
  let unit = 0
  while (current >= 1024 && unit < units.length - 1) { current /= 1024; unit += 1 }
  return `${current.toFixed(current >= 100 ? 0 : current >= 10 ? 1 : 2)} ${units[unit]}`
}
function formatRate(value: number): string { return `${formatBytes(value)}/s` }
function formatTime(value?: string): string {
  if (!value) return '—'
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? '—' : new Intl.DateTimeFormat('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit' }).format(date)
}
function receive(connection: Connection): number {
  return connection.local_is_source ? connection.rate_bytes_destination_to_source : connection.rate_bytes_source_to_destination
}
function send(connection: Connection): number {
  return connection.local_is_source ? connection.rate_bytes_source_to_destination : connection.rate_bytes_destination_to_source
}
function endpoint(connection: Connection, local: boolean): string {
  const ip = local ? connection.local_is_source ? connection.source_ip : connection.destination_ip : connection.local_is_source ? connection.destination_ip : connection.source_ip
  const port = local ? connection.local_is_source ? connection.source_port : connection.destination_port : connection.local_is_source ? connection.destination_port : connection.source_port
  const formattedIP = ip.includes(':') ? `[${ip}]` : ip || '—'
  return `${formattedIP}:${port || 0}`
}
function observedAt(connection: Connection): string { return formatTime(connection.observed_at) }
function deviceName(id: string): string { return devices.value.find((device) => device.id === id)?.name ?? id.slice(0, 8) }
function sourceName(connection: Connection): string {
  if (connection.attribution_source === 'unknown') return '未知归属'
  return connection.service_name || connection.container_name || connection.process_name || '—'
}
function rateClass(connection: Connection): string { return connection.confidence === 'estimated' || connection.sampling_rate > 1 ? 'estimated' : '' }
function safeNumber(value: string): number | undefined {
  if (!value.trim()) return undefined
  const parsed = Number(value)
  return Number.isFinite(parsed) && parsed > 0 ? parsed : undefined
}

async function api<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(path, {
    credentials: 'same-origin',
    headers: { 'Content-Type': 'application/json', ...(init?.headers ?? {}) },
    ...init,
  })
  if (response.status === 401) {
    signedIn.value = false
    closeSocket()
  }
  if (!response.ok) {
    const body = await response.json().catch(() => null)
    throw new Error(body?.error?.message ?? `请求失败 (${response.status})`)
  }
  if (response.status === 204) return undefined as T
  return response.json() as Promise<T>
}

async function checkSession(): Promise<boolean> {
  try {
    await api<{ username: string }>('/api/v1/auth/session')
    signedIn.value = true
    return true
  } catch {
    signedIn.value = false
    return false
  }
}
async function login(): Promise<void> {
  busy.value = true
  errorMessage.value = ''
  try {
    await api('/api/v1/auth/login', { method: 'POST', body: JSON.stringify({ username: username.value, password: password.value }) })
    password.value = ''
    signedIn.value = true
    await loadDashboard()
    openSocket()
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : '登录失败'
  } finally { busy.value = false }
}
async function logout(): Promise<void> {
  try { await api('/api/v1/auth/logout', { method: 'POST' }) } catch { /* session may already be expired */ }
  signedIn.value = false
  closeSocket()
}
async function loadDashboard(): Promise<void> {
  loading.value = true
  errorMessage.value = ''
  try {
    const [deviceResponse, connectionResponse, ruleResponse] = await Promise.all([
      api<{ items: Device[] }>('/api/v1/devices'),
      api<{ items: Connection[]; total: number }>('/api/v1/connections?limit=10000'),
      api<{ items: Rule[] }>('/api/v1/attribution/rules'),
    ])
    devices.value = deviceResponse.items
    connections.value = connectionResponse.items
    connectionsTotal.value = connectionResponse.total
    rules.value = ruleResponse.items
    lastUpdated.value = new Date()
    if (view.value === 'traffic') await loadTraffic()
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : '无法加载监控数据'
  } finally { loading.value = false }
}
function openSocket(): void {
  closeSocket()
  const scheme = location.protocol === 'https:' ? 'wss:' : 'ws:'
  socket = new WebSocket(`${scheme}//${location.host}/ws/v1/connections`)
  socket.onmessage = (event) => {
    try {
      const message = JSON.parse(event.data) as { type: string; data?: Connection | Connection[]; total?: number }
      if (message.type === 'connections.snapshot' && Array.isArray(message.data)) {
        connections.value = message.data
        connectionsTotal.value = message.total ?? message.data.length
      } else if (message.type === 'connection.updated' && message.data && !Array.isArray(message.data)) {
        const observation = message.data
        const index = connections.value.findIndex((item) => item.device_id === observation.device_id && item.flow_id === observation.flow_id)
        if (observation.active) {
          if (index >= 0) connections.value[index] = observation
          else { connections.value.unshift(observation); connectionsTotal.value += 1 }
        } else if (index >= 0) { connections.value.splice(index, 1); connectionsTotal.value = Math.max(0, connectionsTotal.value - 1) }
        if (connections.value.length > 10_000) connections.value.length = 10_000
        lastUpdated.value = new Date()
      } else if (message.type === 'device.updated' && message.data && !Array.isArray(message.data)) {
        const update = message.data as Partial<Device> & { id: string }
        const device = devices.value.find((item) => item.id === update.id)
        if (device) {
          device.online = update.online ?? device.online
          device.last_seen = update.last_seen ?? device.last_seen
          if (update.capabilities) device.capabilities = update.capabilities
        }
      }
    } catch { /* ignore malformed push payloads */ }
  }
  socket.onclose = () => {
    if (signedIn.value) window.setTimeout(() => { if (signedIn.value) openSocket() }, 2500)
  }
}
function closeSocket(): void {
  if (socket) { socket.onclose = null; socket.close(); socket = null }
}
async function loadTraffic(): Promise<void> {
  const to = new Date()
  const hours = trafficPeriod.value === '7d' ? 168 : trafficPeriod.value === '30d' ? 720 : 24
  const from = new Date(to.getTime() - hours * 60 * 60 * 1000)
  const params = new URLSearchParams({ dimension: trafficDimension.value, from: from.toISOString(), to: to.toISOString(), limit: '20000' })
  try {
    const response = await api<{ items: TrafficPoint[] }>(`/api/v1/traffic/summary?${params}`)
    traffic.value = response.items
  } catch (error) { errorMessage.value = error instanceof Error ? error.message : '无法加载流量汇总' }
}
function openOverride(connection: Connection): void {
  editingConnection.value = connection
  overrideForm.value = { process_name: connection.process_name || '', container_name: connection.container_name || '', service_name: connection.service_name || '', reason: '' }
  showAttribution.value = true
}
async function saveOverride(): Promise<void> {
  const connection = editingConnection.value
  if (!connection) return
  const attribution: Record<string, string> = {}
  for (const key of ['process_name', 'container_name', 'service_name'] as const) {
    if (overrideForm.value[key].trim()) attribution[key] = overrideForm.value[key].trim()
  }
  if (Object.keys(attribution).length === 0) { errorMessage.value = '至少填写进程、容器或服务名称之一'; return }
  busy.value = true
  try {
    await api('/api/v1/attribution/overrides', { method: 'POST', body: JSON.stringify({
      connection_key: `${connection.device_id}:${connection.flow_id}`, attribution, reason: overrideForm.value.reason,
    }) })
    showAttribution.value = false
    await loadDashboard()
  } catch (error) { errorMessage.value = error instanceof Error ? error.message : '无法保存归属' }
  finally { busy.value = false }
}
async function saveRule(): Promise<void> {
  const matcher: Record<string, unknown> = {}
  if (ruleForm.value.device_id) matcher.device_id = ruleForm.value.device_id
  if (ruleForm.value.protocol) matcher.protocol = ruleForm.value.protocol
  if (ruleForm.value.destination_port) matcher.destination_port = safeNumber(ruleForm.value.destination_port)
  if (ruleForm.value.process_name) matcher.process_name = ruleForm.value.process_name
  if (ruleForm.value.container_id) matcher.container_id = ruleForm.value.container_id
  const attribution: Record<string, unknown> = {}
  if (ruleForm.value.service_name) attribution.service_name = ruleForm.value.service_name
  if (ruleForm.value.container_name) attribution.container_name = ruleForm.value.container_name
  if (!ruleForm.value.name.trim() || Object.keys(matcher).length === 0 || Object.keys(attribution).length === 0) {
    errorMessage.value = '规则名称、至少一个匹配条件及一种归属值均为必填'
    return
  }
  busy.value = true
  try {
    await api('/api/v1/attribution/rules', { method: 'POST', body: JSON.stringify({ name: ruleForm.value.name, priority: 100, enabled: true, matcher, attribution }) })
    showRuleForm.value = false
    ruleForm.value = { name: '', device_id: '', protocol: '', destination_port: '', process_name: '', container_id: '', service_name: '', container_name: '' }
    const response = await api<{ items: Rule[] }>('/api/v1/attribution/rules')
    rules.value = response.items
  } catch (error) { errorMessage.value = error instanceof Error ? error.message : '无法保存规则' }
  finally { busy.value = false }
}
async function deleteRule(rule: Rule): Promise<void> {
  try {
    await api(`/api/v1/attribution/rules/${rule.id}`, { method: 'DELETE' })
    rules.value = rules.value.filter((item) => item.id !== rule.id)
  } catch (error) { errorMessage.value = error instanceof Error ? error.message : '无法删除规则' }
}
async function revokeDevice(device: Device): Promise<void> {
  if (!window.confirm(`吊销设备“${device.name}”的凭证？该探针需要重新注册才能上报。`)) return
  revokingDeviceID.value = device.id
  try {
    await api(`/api/v1/devices/${device.id}/credential`, { method: 'DELETE' })
    await loadDashboard()
  } catch (error) { errorMessage.value = error instanceof Error ? error.message : '无法吊销设备凭证' }
  finally { revokingDeviceID.value = '' }
}
function selectView(key: string): void { view.value = key; errorMessage.value = '' }
function capabilityClass(status: string): string {
  if (status === 'supported' || status === 'available' || status === 'enabled') return 'good'
  if (status === 'unsupported' || status === 'unavailable' || status === 'disabled') return 'bad'
  return 'warn'
}
watch([trafficDimension, trafficPeriod], () => { if (signedIn.value && view.value === 'traffic') void loadTraffic() })
watch([selectedDevice, selectedProtocol, attributionFilter, searchText], () => { connectionPage.value = 0 })
watch(connectionPageCount, (count) => { if (connectionPage.value >= count) connectionPage.value = count - 1 })
watch(view, (next) => {
  if (next === 'traffic' && signedIn.value) void loadTraffic()
})
onMounted(async () => {
  const authenticated = await checkSession()
  if (authenticated) { await loadDashboard(); openSocket() }
  loading.value = false
  trafficTimer = window.setInterval(() => { if (signedIn.value && view.value === 'traffic') void loadTraffic() }, 30_000)
  deviceStatusTimer = window.setInterval(() => {
    const cutoff = Date.now() - 45_000
    for (const device of devices.value) {
      if (Date.parse(device.last_seen) < cutoff) device.online = false
    }
  }, 5_000)
})
onBeforeUnmount(() => {
  closeSocket()
  if (trafficTimer) window.clearInterval(trafficTimer)
  if (deviceStatusTimer) window.clearInterval(deviceStatusTimer)
})
</script>

<template>
  <main v-if="!signedIn" class="login-screen">
    <form class="login-form" @submit.prevent="login">
      <div class="brand-mark"><Network :size="20" /></div>
      <div class="eyebrow">NETWORK OBSERVER</div>
      <h1>登录监控中心</h1>
      <label>管理员账号<input v-model="username" autocomplete="username" required /></label>
      <label>密码<input v-model="password" type="password" autocomplete="current-password" required /></label>
      <p v-if="errorMessage" class="form-error">{{ errorMessage }}</p>
      <button class="primary-button login-button" :disabled="busy"><LoaderCircle v-if="busy" class="spin" :size="16" />{{ busy ? '正在登录' : '登录' }}</button>
    </form>
  </main>

  <div v-else class="app-shell">
    <aside class="sidebar">
      <div class="brand"><span class="brand-mark"><Network :size="18" /></span><span>Network Observer</span></div>
      <div class="workspace-label">MONITORING</div>
      <nav aria-label="主导航">
        <button v-for="item in navItems" :key="item.key" class="nav-item" :class="{ selected: view === item.key }" @click="selectView(item.key)">
          <component :is="item.icon" :size="17" stroke-width="1.8" /><span>{{ item.label }}</span>
        </button>
      </nav>
      <div class="sidebar-bottom">
        <button class="nav-item" @click="selectView('settings')"><Settings :size="17" /><span>设置</span></button>
        <div class="account-row"><span class="avatar">A</span><span class="account-name">管理员</span><button class="icon-button" aria-label="退出登录" title="退出登录" @click="logout"><LogOut :size="16" /></button></div>
      </div>
    </aside>

    <div class="main-column">
      <header class="topbar">
        <div class="breadcrumbs"><span>监控中心</span><span class="crumb-sep">/</span><strong>{{ navItems.find((item) => item.key === view)?.label ?? '设置' }}</strong></div>
        <div class="topbar-actions">
          <span class="connection-state"><span class="state-dot"></span>{{ loading ? '同步中' : '中心已连接' }}</span>
          <span v-if="lastUpdated" class="updated-at">更新于 {{ formatTime(lastUpdated.toISOString()) }}</span>
          <button class="icon-button" aria-label="刷新数据" title="刷新数据" @click="loadDashboard"><RefreshCw :size="16" /></button>
        </div>
      </header>

      <div v-if="errorMessage" class="notice error-notice"><CircleHelp :size="16" /><span>{{ errorMessage }}</span><button class="icon-button" aria-label="关闭提示" @click="errorMessage = ''"><X :size="16" /></button></div>

      <section v-if="view === 'overview'" class="page-content">
        <div class="page-heading"><div><h1>网络总览</h1><p>设备、活动连接与流量状态</p></div><button class="subtle-button" @click="selectView('connections')"><Activity :size="15" />查看实时连接</button></div>
        <div class="metric-grid">
          <article class="metric"><div class="metric-top"><span>在线设备</span><Network :size="16" /></div><strong>{{ onlineDevices }}<small> / {{ devices.length }}</small></strong><span class="metric-foot">Endpoint 与 Gateway Agent</span></article>
          <article class="metric"><div class="metric-top"><span>活动连接</span><Activity :size="16" /></div><strong>{{ connections.length.toLocaleString() }}</strong><span class="metric-foot">TCP {{ tcpCount }} · UDP {{ udpCount }}</span></article>
          <article class="metric"><div class="metric-top"><span>当前接收</span><ArrowDownToLine :size="16" /></div><strong>{{ formatRate(receiveRate) }}</strong><span class="metric-foot">按观测设备方向统计</span></article>
          <article class="metric"><div class="metric-top"><span>当前发送</span><ArrowUpFromLine :size="16" /></div><strong>{{ formatRate(sendRate) }}</strong><span class="metric-foot">按观测设备方向统计</span></article>
        </div>
        <div class="overview-grid">
          <section class="data-section">
            <div class="section-heading"><div><h2>高流量连接</h2><p>活动连接按双向速率排序</p></div><button class="text-button" @click="selectView('connections')">全部连接 <ChevronDown :size="14" /></button></div>
            <ConnectionTable :items="filteredConnections.slice(0, 8)" :device-name="deviceName" :receive="receive" :send="send" :format-rate="formatRate" :endpoint="endpoint" :observed-at="observedAt" :source-name="sourceName" :rate-class="rateClass" :open-override="openOverride" :show-actions="false" />
          </section>
          <section class="data-section device-summary">
            <div class="section-heading"><div><h2>设备状态</h2><p>最近心跳与采集能力</p></div><button class="text-button" @click="selectView('devices')">查看设备 <ChevronDown :size="14" /></button></div>
            <div v-if="!devices.length" class="empty-state compact-empty"><Network :size="22" /><strong>暂无探针</strong><span>完成 Agent 注册后设备会显示在这里</span></div>
            <div v-for="device in devices.slice(0, 6)" :key="device.id" class="device-summary-row">
              <span class="device-status" :class="device.online ? 'online' : 'offline'"></span><div class="device-summary-name"><strong>{{ device.name }}</strong><span>{{ device.type }} · {{ device.os || '未知系统' }}</span></div><span class="small-status" :class="device.online ? 'good' : 'bad'">{{ device.online ? '在线' : '离线' }}</span>
            </div>
          </section>
        </div>
      </section>

      <section v-else-if="view === 'connections'" class="page-content">
        <div class="page-heading"><div><h1>实时连接</h1><p>TCP / UDP 活动连接 · 每 2 秒更新</p></div><div class="heading-tools"><span class="live-pill"><span class="state-dot"></span>实时</span><button class="subtle-button" @click="loadDashboard"><RefreshCw :size="15" />刷新</button></div></div>
        <div class="metric-grid compact-metrics">
          <article class="metric"><div class="metric-top"><span>活跃连接</span><Activity :size="15" /></div><strong>{{ connections.length.toLocaleString() }}</strong></article>
          <article class="metric"><div class="metric-top"><span>TCP</span><Unplug :size="15" /></div><strong>{{ tcpCount.toLocaleString() }}</strong></article>
          <article class="metric"><div class="metric-top"><span>UDP</span><Network :size="15" /></div><strong>{{ udpCount.toLocaleString() }}</strong></article>
          <article class="metric"><div class="metric-top"><span>未解析归属</span><CircleHelp :size="15" /></div><strong>{{ unknownCount.toLocaleString() }}</strong></article>
        </div>
        <div class="toolbar">
          <label class="select-control"><Network :size="15" /><select v-model="selectedDevice"><option value="">全部设备</option><option v-for="device in devices" :key="device.id" :value="device.id">{{ device.name }}</option></select><ChevronDown :size="14" /></label>
          <label class="select-control"><Filter :size="15" /><select v-model="selectedProtocol"><option value="">TCP + UDP</option><option value="tcp">TCP</option><option value="udp">UDP</option></select><ChevronDown :size="14" /></label>
          <label class="select-control"><ShieldCheck :size="15" /><select v-model="attributionFilter"><option value="">全部归属</option><option value="auto">自动</option><option value="manual">手动</option><option value="unknown">未知</option></select><ChevronDown :size="14" /></label>
          <label class="search-control"><Search :size="16" /><input v-model="searchText" placeholder="搜索 IP、端口、进程或容器" /></label>
          <label class="select-control sort-control"><SlidersHorizontal :size="15" /><select v-model="sortBy"><option value="rate">按速率排序</option><option value="age">按最近活动排序</option></select><ChevronDown :size="14" /></label>
        </div>
        <div class="table-meta"><span>{{ filteredConnections.length }} 条活动连接</span><span v-if="lastUpdated">最后采样 {{ formatTime(lastUpdated.toISOString()) }}</span></div>
        <ConnectionTable :items="visibleConnections" :device-name="deviceName" :receive="receive" :send="send" :format-rate="formatRate" :endpoint="endpoint" :observed-at="observedAt" :source-name="sourceName" :rate-class="rateClass" :open-override="openOverride" :show-actions="true" />
        <div class="pagination"><span>显示 {{ filteredConnections.length ? connectionPage * CONNECTION_PAGE_SIZE + 1 : 0 }}–{{ Math.min((connectionPage + 1) * CONNECTION_PAGE_SIZE, filteredConnections.length) }} / {{ filteredConnections.length }} 条<span v-if="connectionsTotal > connections.length">（服务端共 {{ connectionsTotal.toLocaleString() }} 条）</span></span><div><button class="icon-button" aria-label="上一页" title="上一页" :disabled="connectionPage === 0" @click="connectionPage -= 1"><ChevronDown :size="15" class="rotate-up" /></button><span>{{ connectionPage + 1 }} / {{ connectionPageCount }}</span><button class="icon-button" aria-label="下一页" title="下一页" :disabled="connectionPage + 1 >= connectionPageCount" @click="connectionPage += 1"><ChevronDown :size="15" class="rotate-down" /></button></div></div>
      </section>

      <section v-else-if="view === 'devices'" class="page-content">
        <div class="page-heading"><div><h1>设备</h1><p>{{ devices.length }} 台已注册探针 · 凭证可单独吊销</p></div></div>
        <div class="table-wrap"><table class="data-table"><thead><tr><th>设备</th><th>类型</th><th>系统 / 架构</th><th>版本</th><th>能力</th><th>最近心跳</th><th>状态</th><th></th></tr></thead><tbody>
          <tr v-for="device in devices" :key="device.id"><td><strong>{{ device.name }}</strong><div class="sub-cell">{{ device.id }}</div></td><td>{{ device.type }}</td><td>{{ device.os || '—' }}<div class="sub-cell">{{ device.architecture || '—' }}</div></td><td>{{ device.agent_version || '—' }}</td><td><div class="capability-list"><span v-for="capability in device.capabilities" :key="capability.name" class="capability" :class="capabilityClass(capability.status)" :title="capability.detail">{{ capability.name }} · {{ capability.status }}</span><span v-if="!device.capabilities.length" class="sub-cell">等待能力报告</span></div></td><td>{{ formatTime(device.last_seen) }}</td><td><span class="small-status" :class="device.online ? 'good' : 'bad'">{{ device.online ? '在线' : '离线' }}</span></td><td><button class="icon-button danger-icon" :aria-label="`吊销${device.name}凭证`" title="吊销设备凭证" :disabled="revokingDeviceID === device.id" @click="revokeDevice(device)"><LoaderCircle v-if="revokingDeviceID === device.id" class="spin" :size="15" /><ShieldCheck v-else :size="15" /></button></td></tr>
        </tbody></table><div v-if="!devices.length && !loading" class="empty-state"><Network :size="26" /><strong>暂无设备</strong><span>通过 Endpoint 或 Gateway Agent 注册设备</span></div></div>
      </section>

      <section v-else-if="view === 'traffic'" class="page-content">
        <div class="page-heading"><div><h1>流量分析</h1><p>分钟级汇总 · 汇总数据默认保留 90 天</p></div><button class="subtle-button" @click="loadTraffic"><RefreshCw :size="15" />更新</button></div>
        <div class="toolbar traffic-toolbar">
          <label class="select-control"><Database :size="15" /><select v-model="trafficDimension"><option value="device">设备</option><option value="interface">网卡</option><option value="source_ip">源 IP</option><option value="destination_ip">目标 IP</option><option value="source_port">源端口</option><option value="destination_port">目标端口</option><option value="process">进程</option><option value="container">容器</option><option value="service">服务</option></select><ChevronDown :size="14" /></label>
          <label class="select-control"><Clock3 :size="15" /><select v-model="trafficPeriod"><option value="24h">最近 24 小时</option><option value="7d">最近 7 天</option><option value="30d">最近 30 天</option></select><ChevronDown :size="14" /></label>
        </div>
        <div v-if="trafficTotals.length" class="traffic-list">
          <div class="traffic-header"><span>{{ trafficDimension }} · TOP 20</span><span>累计流量</span></div>
          <div v-for="(row, index) in trafficTotals" :key="row.value" class="traffic-row"><span class="traffic-rank">{{ String(index + 1).padStart(2, '0') }}</span><span class="traffic-value">{{ row.value }}</span><span class="traffic-bar-track"><span class="traffic-bar" :style="{ width: `${Math.max(2, row.bytes / (trafficTotals[0]?.bytes || 1) * 100)}%` }"></span></span><strong>{{ formatBytes(row.bytes) }}<small v-if="row.estimated" class="estimate-mark">估算</small></strong></div>
        </div>
        <div v-else-if="!loading" class="empty-state empty-band"><Gauge :size="28" /><strong>暂无历史流量</strong><span>探针开始上报后，这里会显示 IP、端口、进程和容器汇总</span></div>
      </section>

      <section v-else-if="view === 'rules'" class="page-content">
        <div class="page-heading"><div><h1>归属规则</h1><p>持久规则用于自动补全进程、容器或服务归属</p></div><button class="primary-button" @click="showRuleForm = true"><Plus :size="16" />新建规则</button></div>
        <div class="table-wrap"><table class="data-table"><thead><tr><th>规则</th><th>匹配条件</th><th>归属</th><th>优先级</th><th>状态</th><th>生效时间</th><th></th></tr></thead><tbody>
          <tr v-for="rule in ruleRows" :key="rule.id"><td><strong>{{ rule.name }}</strong><div class="sub-cell">{{ rule.id.slice(0, 8) }}</div></td><td><span v-for="[key, value] in Object.entries(rule.matcher)" :key="key" class="rule-chip">{{ key }}={{ value }}</span></td><td><span v-for="[key, value] in Object.entries(rule.attribution)" :key="key" class="rule-chip applied">{{ key }}={{ value }}</span></td><td>{{ rule.priority }}</td><td><span class="small-status" :class="rule.enabled ? 'good' : 'bad'">{{ rule.enabled ? '启用' : '停用' }}</span></td><td>{{ formatTime(rule.effective_from) }}</td><td><button class="icon-button danger-icon" :aria-label="`删除${rule.name}`" title="删除规则" @click="deleteRule(rule)"><Trash2 :size="15" /></button></td></tr>
        </tbody></table><div v-if="!ruleRows.length" class="empty-state"><ListFilter :size="25" /><strong>暂无归属规则</strong><span>未知归属仍可在连接列表中单独补录</span></div></div>
      </section>

      <section v-else class="page-content"><div class="page-heading"><div><h1>设置</h1><p>监控中心运行状态</p></div></div><div class="settings-list"><div><span><ShieldCheck :size="17" />登录方式</span><strong>单管理员账号</strong></div><div><span><Database :size="17" />历史汇总</span><strong>ClickHouse · 90 天</strong></div><div><span><Box :size="17" />管理数据</span><strong>PostgreSQL</strong></div><div><span><EthernetPort :size="17" />指标接口</span><strong>/metrics</strong></div></div></section>
    </div>

    <div v-if="showAttribution" class="modal-backdrop" @click.self="showAttribution = false">
      <form class="modal" @submit.prevent="saveOverride">
        <div class="modal-head"><div><h2>指定连接归属</h2><p>{{ editingConnection ? `${endpoint(editingConnection, true)} → ${endpoint(editingConnection, false)}` : '' }}</p></div><button type="button" class="icon-button" aria-label="关闭" @click="showAttribution = false"><X :size="18" /></button></div>
        <label>进程名称<input v-model="overrideForm.process_name" placeholder="例如 nginx" /></label>
        <label>容器名称<input v-model="overrideForm.container_name" placeholder="例如 media-server" /></label>
        <label>服务名称<input v-model="overrideForm.service_name" placeholder="例如 媒体服务" /></label>
        <label>原因或备注<input v-model="overrideForm.reason" placeholder="选填" /></label>
        <div class="modal-actions"><button type="button" class="subtle-button" @click="showAttribution = false">取消</button><button class="primary-button" :disabled="busy"><Check :size="15" />保存覆盖</button></div>
      </form>
    </div>

    <div v-if="showRuleForm" class="modal-backdrop" @click.self="showRuleForm = false">
      <form class="modal rule-modal" @submit.prevent="saveRule">
        <div class="modal-head"><div><h2>新建归属规则</h2><p>规则从创建时间起作用，不修改历史汇总</p></div><button type="button" class="icon-button" aria-label="关闭" @click="showRuleForm = false"><X :size="18" /></button></div>
        <label>规则名称<input v-model="ruleForm.name" required placeholder="例如 NAS HTTPS 服务" /></label>
        <div class="form-grid"><label>设备 ID<input v-model="ruleForm.device_id" placeholder="留空匹配所有设备" /></label><label>协议<select v-model="ruleForm.protocol"><option value="">任意</option><option value="tcp">TCP</option><option value="udp">UDP</option></select></label></div>
        <div class="form-grid"><label>目标端口<input v-model="ruleForm.destination_port" inputmode="numeric" placeholder="例如 443" /></label><label>进程名<input v-model="ruleForm.process_name" placeholder="可选匹配条件" /></label></div>
        <label>容器 ID<input v-model="ruleForm.container_id" placeholder="可选匹配条件" /></label>
        <div class="form-grid"><label>补全服务名<input v-model="ruleForm.service_name" placeholder="例如 Web 服务" /></label><label>补全容器名<input v-model="ruleForm.container_name" placeholder="例如 frontend" /></label></div>
        <div class="modal-actions"><button type="button" class="subtle-button" @click="showRuleForm = false">取消</button><button class="primary-button" :disabled="busy"><Plus :size="15" />创建规则</button></div>
      </form>
    </div>
  </div>
</template>

<script lang="ts">
import { defineComponent, h, type PropType } from 'vue'
import { ArrowDownToLine as TableArrowDownToLine, ArrowUpFromLine as TableArrowUpFromLine, Boxes, CircleHelp as TableCircleHelp } from '@lucide/vue'

type TableConnection = {
  id: string; flow_id: string; device_id: string; observer_id: string; observer_type: string; local_is_source: boolean; observed_at: string; interface: string
  source_ip: string; source_port: number; destination_ip: string; destination_port: number
  network_namespace: string; protocol: string; state: string; attribution_source: string
  bytes_source_to_destination: number; bytes_destination_to_source: number
  total_bytes_source_to_destination: number; total_bytes_destination_to_source: number
  process_name: string; pid: number; container_id: string; container_name: string; service_name: string
  attribution_reason: string; counter_source: string; active: boolean
  rate_bytes_source_to_destination: number; rate_bytes_destination_to_source: number
  confidence: string; sampling_rate: number
}

export default defineComponent({
  components: {
    ConnectionTable: defineComponent({
      props: {
        items: { type: Array as PropType<TableConnection[]>, required: true },
        deviceName: { type: Function as PropType<(id: string) => string>, required: true },
        receive: { type: Function as PropType<(value: TableConnection) => number>, required: true },
        send: { type: Function as PropType<(value: TableConnection) => number>, required: true },
        formatRate: { type: Function as PropType<(value: number) => string>, required: true },
        endpoint: { type: Function as PropType<(value: TableConnection, local: boolean) => string>, required: true },
        observedAt: { type: Function as PropType<(value: TableConnection) => string>, required: true },
        sourceName: { type: Function as PropType<(value: TableConnection) => string>, required: true },
        rateClass: { type: Function as PropType<(value: TableConnection) => string>, required: true },
        openOverride: { type: Function as PropType<(value: TableConnection) => void>, required: true },
        showActions: { type: Boolean, default: true },
      },
      setup(props) {
        return () => h('div', { class: 'table-wrap' }, [
          props.items.length ? h('table', { class: 'data-table connection-table' }, [
            h('thead', [h('tr', ['设备', '协议 / 状态', '本地端点', '远端端点', '进程 / 容器 / 服务', '接收速率', '发送速率', '最近观测', ...(props.showActions ? [''] : [])].map((label) => h('th', label)))]),
            h('tbody', props.items.map((connection) => {
              const attribution = connection.attribution_source === 'unknown'
              return h('tr', { key: `${connection.device_id}:${connection.flow_id}` }, [
                h('td', { class: 'device-cell' }, [h('strong', props.deviceName(connection.device_id)), h('small', connection.interface || connection.observer_type)]),
                h('td', [h('span', { class: ['protocol-pill', connection.protocol] }, connection.protocol.toUpperCase()), h('small', { class: 'state-text' }, connection.state || 'ACTIVE')]),
                h('td', { class: 'endpoint-cell' }, [h('span', props.endpoint(connection, true)), h('small', connection.network_namespace || '主机网络')]),
                h('td', { class: 'endpoint-cell' }, [h('span', props.endpoint(connection, false)), h('small', '远端')]),
                h('td', { class: attribution ? 'attribution-cell unknown-text' : 'attribution-cell' }, [
                  h('span', attribution ? '未知归属' : props.sourceName(connection)),
                  h('small', connection.container_name || (connection.pid ? `PID ${connection.pid}` : connection.attribution_source || '—')),
                  attribution && props.showActions ? h('button', { class: 'inline-action', onClick: () => props.openOverride(connection) }, '指定归属') : null,
                ]),
                h('td', { class: ['numeric-cell', props.rateClass(connection)] }, [h(TableArrowDownToLine, { size: 13 }), props.formatRate(props.receive(connection))]),
                h('td', { class: ['numeric-cell', props.rateClass(connection)] }, [h(TableArrowUpFromLine, { size: 13 }), props.formatRate(props.send(connection))]),
                h('td', { class: 'duration-cell' }, props.observedAt(connection)),
                ...(props.showActions ? [h('td', { class: 'row-menu' }, h(TableCircleHelp, { size: 15, title: connection.confidence || '采集质量未知' }))] : []),
              ])
            })),
          ]) : h('div', { class: 'empty-state table-empty' }, [h(Boxes, { size: 26 }), h('strong', '暂无活动连接'), h('span', '探针开始上报后，连接会显示在这里')]),
        ])
      },
    }),
  },
})
</script>
