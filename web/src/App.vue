<script setup lang="ts">
import { computed, defineAsyncComponent, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import type { GlobeRoute } from './components/ConnectionGlobe.vue'
import {
  Activity, ArrowDownToLine, ArrowUpFromLine, Box, Check, ChevronDown,
  CircleAlert, CircleHelp, Clock3, Copy, Cpu, Database, EthernetPort, Eye, EyeOff, Filter, Gauge,
  LayoutDashboard, ListFilter, LoaderCircle, LogOut, Network, Plus,
  Pencil, RefreshCw, Router, Search, Settings, ShieldCheck, SlidersHorizontal, Trash2,
  Unplug, X,
} from '@lucide/vue'

type Capability = { name: string; status: string; detail?: string; checked_at: string }
type Device = {
  id: string; name: string; type: string; os: string; architecture: string
  registered_name: string; agent_version: string; online: boolean; last_seen: string; capabilities: Capability[]; device_category: string; latitude?: number | null; longitude?: number | null
}
type LANAddress = { ip: string; interfaces: string[]; state: string; last_seen: string }
type LANObserver = { id: string; name: string; last_seen: string }
type LANDevice = {
  key: string; network_id: string; mac: string; name: string; name_source: string
  device_category: string; category_source: string; addresses: LANAddress[]; observers: LANObserver[]; last_seen: string; stale: boolean; ip_conflict: boolean
}
type TopologyAgent = { id: string; name: string; type: string; device_category: string; last_seen: string }
type TopologyGroup = { network_id: string; network_prefix: string; center?: { id: string; name: string; known: boolean }; agents: TopologyAgent[]; assets: LANDevice[] }
type TopologyResponse = { groups: TopologyGroup[]; unassigned: { center?: { id: string; name: string; known: boolean }; agents: TopologyAgent[]; assets: LANDevice[] } }
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
  domain?: string; asn?: number; organization?: string; country?: string; region?: string; city?: string
  latitude?: number; longitude?: number; enrichment_version?: string; gap_reason?: string
}
type RankingRow = { dimension: string; value: string; device_id?: string; remote_ip?: string; upload_bytes: number; download_bytes: number; connections: number; estimated: boolean; gap_reason?: string; history_dimension_available: boolean }
type Enrichment = {
  ip: string; domain?: string; domain_source?: string; domain_candidates?: string[]; ptr_status?: string; ptr_reason?: string; ptr_updated_at?: string
  asn?: number; organization?: string; country?: string; region?: string; city?: string; latitude?: number; longitude?: number; coordinates_available: boolean; version?: string; updated_at?: string
  status: string; reason?: string; trusted: boolean; automatic: { asn?: number; organization?: string; country?: string; region?: string; city?: string; latitude?: number; longitude?: number; coordinates_available: boolean }
  sources?: Record<string, string>; manual?: { domain?: string; asn?: number; organization?: string; country?: string; region?: string; city?: string; latitude?: number; longitude?: number; trusted: boolean }
}
type Rule = {
  id: string; name: string; priority: number; enabled: boolean
  matcher: Record<string, unknown>; attribution: Record<string, unknown>
  effective_from: string; created_at: string
}
type TrafficPoint = {
  bucket: string; dimension: string; value: string; protocol: string
  direction: string; bytes: number; packets: number; estimated: boolean; upload_bytes?: number; download_bytes?: number; gap_reason?: string
}
type PresencePoint = {
  key: string; device_id: string; flow_id?: string; protocol: string; source_ip: string; source_port: number
  destination_ip: string; destination_port: number; domain?: string; first_seen: string; last_seen: string
  observed_minutes: number; active_seconds: number; continuity_ratio: number; upload_bytes: number; download_bytes: number
  asn?: number; organization?: string; country?: string; region?: string; city?: string; estimated: boolean; gap_reason?: string
}
type Todo = {
  id: string; phase: string; title: string; description: string; status: 'todo' | 'in_progress' | 'done' | 'blocked'
  priority: 'low' | 'medium' | 'high'; progress: number; blocker: string; source: 'plan' | 'user'
  completed_at?: string; created_at: string; updated_at: string
}
type EnrollmentResponse = {
  token: { id: string; observer_type: string; requested_name: string; expires_at: string }
  plaintext_token: string
  tls_fingerprint: string
  tls_server_name: string
}
const ConnectionGlobe = defineAsyncComponent(() => import('./components/ConnectionGlobe.vue'))
const CONNECTION_PAGE_SIZE = 100
const DEVICE_ONLINE_TIMEOUT_MS = 45_000

function asArray<T>(value: unknown): T[] {
  return Array.isArray(value) ? value.filter(Boolean) as T[] : []
}

function asRecord(value: unknown): Record<string, unknown> {
  return value && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : {}
}

function normalizeDevice(value: Device): Device {
  const device = (value ?? {}) as Device
  return { ...device, registered_name: device.registered_name || device.name, device_category: device.device_category || 'unknown', capabilities: asArray<Capability>(device.capabilities) }
}

function normalizeLANDevice(value: LANDevice): LANDevice {
  const device = (value ?? {}) as LANDevice
  return {
    ...device,
    device_category: device.device_category || 'unknown',
    category_source: device.category_source || 'unknown',
    addresses: asArray<LANAddress>(device.addresses).map((address) => ({
      ...address,
      interfaces: asArray<string>(address.interfaces),
    })),
    observers: asArray<LANObserver>(device.observers),
  }
}

function normalizeRule(value: Rule): Rule {
  const rule = (value ?? {}) as Rule
  return { ...rule, matcher: asRecord(rule.matcher), attribution: asRecord(rule.attribution) }
}

const navItems = [
  { key: 'overview', label: '网络总览', icon: LayoutDashboard },
  { key: 'devices', label: '设备', icon: Network },
  { key: 'lan', label: '局域网资产', icon: EthernetPort },
  { key: 'connections', label: '实时连接', icon: Activity },
  { key: 'traffic', label: '流量分析', icon: Gauge },
  { key: 'progress', label: '项目进度', icon: ListFilter },
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
const lanDevices = ref<LANDevice[]>([])
const lanNetworks = ref<string[]>([])
const lanSearch = ref('')
const selectedLANDevice = ref<LANDevice | null>(null)
const lanAlias = ref('')
const lanCategory = ref('unknown')
const topology = ref<TopologyResponse>({ groups: [], unassigned: { agents: [], assets: [] } })
const topologyError = ref('')
const topologyLoading = ref(false)
const editingDeviceID = ref('')
const deviceProfileForm = ref({ name: '', device_category: 'unknown', latitude: '', longitude: '' })
const editingDevice = computed(() => devices.value.find((item) => item.id === editingDeviceID.value) ?? null)
const deviceProfileDialog = ref<HTMLDialogElement | null>(null)
const hideInternalTraffic = ref(localStorage.getItem('observer-hide-internal') === 'true')
const connections = ref<Connection[]>([])
const rankingRows = ref<Record<string, RankingRow[]>>({})
const rankingDimension = ref('remote_ip')
const rankingTrust = ref('all')
const rankingSort = ref('traffic')
const rankingPartial = ref(false)
const rankingUnavailableDimensions = ref<string[]>([])
const enrichmentCache = ref<Record<string, Enrichment>>({})
const enrichmentFetchedAt = ref<Record<string, number>>({})
const enrichmentLoading = ref(false)
const globeDeviceFilter = ref('')
const globeTrustFilter = ref('all')
const editingIP = ref('')
const showIPMetadata = ref(false)
const ipMetadataForm = ref({ domain: '', asn: '', organization: '', country: '', region: '', city: '', latitude: '', longitude: '', trusted: false })
let rankingTimer: number | undefined
let rankingRequestID = 0
let enrichmentRequestID = 0
const connectionsTotal = ref(0)
const rules = ref<Rule[]>([])
const traffic = ref<TrafficPoint[]>([])
const trafficSeries = ref<TrafficPoint[]>([])
const presence = ref<PresencePoint[]>([])
const todos = ref<Todo[]>([])
const searchText = ref('')
const selectedDevice = ref('')
const selectedProtocol = ref('')
const attributionFilter = ref('')
const trafficDimension = ref('destination_port')
const trafficPeriod = ref('24h')
const trafficPortRole = ref('local_port')
const trafficPort = ref('')
const trafficDomain = ref('')
const trafficDevice = ref('')
const trafficInterface = ref('')
const trafficProtocol = ref('')
const presenceMinimum = ref('0.8')
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
const showEnrollmentForm = ref(false)
const enrollment = ref<EnrollmentResponse | null>(null)
const enrollmentCopyStatus = ref('')
const enrollmentForm = ref({ name: '', observer_type: 'endpoint', server: location.hostname + ':8444', web_server: location.host, bootstrap_server: location.hostname + ':8080', security_mode: true, version: 'v0.3.23', expires_in_hours: 24 })
const showTodoForm = ref(false)
const editingTodo = ref<Todo | null>(null)
const todoForm = ref({ phase: '', title: '', description: '', status: 'todo' as Todo['status'], priority: 'medium' as Todo['priority'], progress: 0, blocker: '' })
let socket: WebSocket | null = null
let trafficTimer: number | undefined
let deviceStatusTimer: number | undefined
let todoTimer: number | undefined
let lanTimer: number | undefined
let socketReconnectTimer: number | undefined
let socketReconnectDelay = 2_500

const onlineDevices = computed(() => devices.value.filter((device) => device.online).length)
const visibleActiveConnections = computed(() => connections.value.filter((connection) => !hideInternalTraffic.value || !isInternalFlow(connection)))
const tcpCount = computed(() => visibleActiveConnections.value.filter((connection) => connection.protocol === 'tcp').length)
const udpCount = computed(() => visibleActiveConnections.value.filter((connection) => connection.protocol === 'udp').length)
const unknownCount = computed(() => visibleActiveConnections.value.filter((connection) => connection.attribution_source === 'unknown').length)
const receiveRate = computed(() => visibleActiveConnections.value.reduce((sum, connection) => sum + receive(connection), 0))
const sendRate = computed(() => visibleActiveConnections.value.reduce((sum, connection) => sum + send(connection), 0))
const globeRoutes = computed(() => {
  const grouped = new Map<string, { agent: Device; ip: string; flows: number; upload: number; download: number; trusted: boolean }>()
  let missing = 0
  const candidateConnections = visibleActiveConnections.value.filter((connection) => {
    if (globeDeviceFilter.value && connection.device_id !== globeDeviceFilter.value) return false
    const ip = connection.local_is_source ? connection.destination_ip : connection.source_ip
    const remote = enrichmentCache.value[ip]
    if (globeTrustFilter.value === 'trusted' && !remote?.trusted) return false
    if (globeTrustFilter.value === 'untrusted' && remote?.trusted) return false
    return true
  })
  for (const connection of candidateConnections) {
    const agent = devices.value.find((item) => item.id === connection.device_id)
    const ip = connection.local_is_source ? connection.destination_ip : connection.source_ip
    const remote = enrichmentCache.value[ip]
    const sourceCoordinates: [number, number] | undefined = agent?.latitude != null && agent.longitude != null ? [agent.longitude, agent.latitude] : undefined
    const destinationCoordinates: [number, number] | undefined = remote?.coordinates_available ? [remote.longitude ?? 0, remote.latitude ?? 0] : undefined
    if (!agent || !sourceCoordinates || !destinationCoordinates) { missing += 1; continue }
    const key = `${agent.id}|${ip}`
    const group = grouped.get(key) ?? { agent, ip, flows: 0, upload: 0, download: 0, trusted: Boolean(remote.trusted) }
    group.flows += 1
    group.upload += send(connection)
    group.download += receive(connection)
    group.trusted ||= Boolean(remote.trusted)
    grouped.set(key, group)
  }
  const routes: GlobeRoute[] = [...grouped.entries()].map(([key, group]) => ({
    key, from: [group.agent.longitude!, group.agent.latitude!] as [number, number], to: [enrichmentCache.value[group.ip].longitude ?? 0, enrichmentCache.value[group.ip].latitude ?? 0] as [number, number],
    label: group.agent.name, detail: [enrichmentCache.value[group.ip].domain || group.ip, enrichmentCache.value[group.ip].asn ? `AS${enrichmentCache.value[group.ip].asn}` : '', enrichmentCache.value[group.ip].organization, [enrichmentCache.value[group.ip].country, enrichmentCache.value[group.ip].region, enrichmentCache.value[group.ip].city].filter(Boolean).join(' / ')].filter(Boolean).join(' · '),
    upload: group.upload, download: group.download, rate: group.upload + group.download, flows: group.flows, trusted: group.trusted,
  })).sort((a, b) => b.rate - a.rate)
  return { routes: routes.slice(0, 100), missing }
})
const activeRankingRows = computed(() => rankingRows.value[rankingDimension.value] ?? [])
const filteredConnections = computed(() => {
  const needle = searchText.value.trim().toLowerCase()
  return visibleActiveConnections.value.filter((connection) => {
    const device = devices.value.find((item) => item.id === connection.device_id)
    const matchesDevice = !selectedDevice.value || connection.device_id === selectedDevice.value
    const matchesProtocol = !selectedProtocol.value || connection.protocol === selectedProtocol.value
    const matchesAttribution = !attributionFilter.value || connection.attribution_source === attributionFilter.value
    const searchable = [connection.source_ip, connection.source_port, connection.destination_ip,
      connection.destination_port, connection.process_name, connection.container_name,
      connection.container_id, connection.service_name, connection.domain, connection.organization,
      connection.country, connection.city, device?.name].join(' ').toLowerCase()
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
  const totals = new Map<string, { bytes: number; upload: number; download: number; estimated: boolean }>()
  for (const point of traffic.value) {
    const total = totals.get(point.value) ?? { bytes: 0, upload: 0, download: 0, estimated: false }
    total.bytes += point.bytes
    total.upload += point.direction === 'upload' ? point.bytes : point.upload_bytes ?? 0
    total.download += point.direction === 'download' ? point.bytes : point.download_bytes ?? 0
    total.estimated ||= point.estimated
    totals.set(point.value, total)
  }
  return [...totals.entries()].map(([value, total]) => ({ value, ...total })).sort((a, b) => b.bytes - a.bytes).slice(0, 20)
})
const trafficChartRows = computed(() => {
  const buckets = new Map<string, { bucket: string; upload: number; download: number; estimated: boolean; gap: boolean }>()
  for (const point of trafficSeries.value) {
    const row = buckets.get(point.bucket) ?? { bucket: point.bucket, upload: 0, download: 0, estimated: false, gap: false }
    row.upload += point.upload_bytes ?? (point.direction === 'upload' ? point.bytes : 0)
    row.download += point.download_bytes ?? (point.direction === 'download' ? point.bytes : 0)
    row.estimated ||= point.estimated
    row.gap ||= Boolean(point.gap_reason)
    buckets.set(point.bucket, row)
  }
  return [...buckets.values()].sort((a, b) => Date.parse(a.bucket) - Date.parse(b.bucket))
})
const seriesMax = computed(() => Math.max(1, ...trafficChartRows.value.flatMap((point) => [point.upload, point.download])))
const chartLine = (key: 'upload' | 'download') => trafficChartRows.value.map((point, index) => {
  const x = trafficChartRows.value.length <= 1 ? 0 : index / (trafficChartRows.value.length - 1) * 100
  return `${x},${100 - point[key] / seriesMax.value * 92}`
}).join(' ')
const trafficUpload = computed(() => trafficTotals.value.reduce((sum, point) => sum + point.upload, 0))
const trafficDownload = computed(() => trafficTotals.value.reduce((sum, point) => sum + point.download, 0))
const trafficPeak = computed(() => Math.max(0, ...trafficChartRows.value.map((point) => (point.upload + point.download) / chartBucketSeconds.value)))
const chartBucketSeconds = computed(() => trafficPeriod.value === '24h' ? 60 : trafficPeriod.value === '7d' ? 300 : 3600)
const trafficAverage = computed(() => (trafficUpload.value + trafficDownload.value) / (trafficPeriod.value === '24h' ? 86400 : trafficPeriod.value === '7d' ? 604800 : 2592000))
const presenceTop = computed(() => presence.value.slice(0, 20))
const filteredLANDevices = computed(() => {
  const needle = lanSearch.value.trim().toLowerCase()
  if (!needle) return lanDevices.value
  return lanDevices.value.filter((device) => [device.name, device.device_category, device.mac, device.network_id, ...device.addresses.map((address) => address.ip), ...device.observers.map((observer) => observer.name)].join(' ').toLowerCase().includes(needle))
})
const topologyGroups = computed(() => topology.value.groups)
const hasUnassignedTopology = computed(() => Boolean(topology.value.unassigned.center) || topology.value.unassigned.agents.length > 0 || topology.value.unassigned.assets.length > 0)
const lanCurrentCount = computed(() => lanDevices.value.filter((device) => !device.stale).length)
const lanStaleCount = computed(() => lanDevices.value.filter((device) => device.stale).length)
const lanConflictCount = computed(() => lanDevices.value.filter((device) => device.ip_conflict).length)
const todoSummary = computed(() => {
  const done = todos.value.filter((todo) => todo.status === 'done').length
  const blocked = todos.value.filter((todo) => todo.status === 'blocked').length
  const inProgress = todos.value.filter((todo) => todo.status === 'in_progress').length
  const percent = todos.value.length ? Math.round(todos.value.reduce((sum, todo) => sum + todo.progress, 0) / todos.value.length) : 0
  return { done, blocked, inProgress, todo: todos.value.length - done - blocked - inProgress, percent }
})
const todoPhases = computed(() => {
  const groups = new Map<string, Todo[]>()
  for (const todo of todos.value) groups.set(todo.phase, [...(groups.get(todo.phase) ?? []), todo])
  return [...groups.entries()].map(([phase, items]) => ({ phase, items }))
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
  const ip = endpointIP(connection, local)
  const port = local ? connection.local_is_source ? connection.source_port : connection.destination_port : connection.local_is_source ? connection.destination_port : connection.source_port
  const formattedIP = ip.includes(':') ? `[${ip}]` : ip || '—'
  return `${formattedIP}:${port || 0}`
}
function endpointIP(connection: Connection, local: boolean): string {
  return local ? connection.local_is_source ? connection.source_ip : connection.destination_ip : connection.local_is_source ? connection.destination_ip : connection.source_ip
}
type ParsedAddress = { version: 4 | 6; bits: number; value: bigint }
function parseAddress(value: string): ParsedAddress | null {
  const address = value.toLowerCase().split('%')[0]
  if (!address.includes(':')) {
    const octets = address.split('.')
    if (octets.length !== 4 || octets.some((part) => !/^\d{1,3}$/.test(part) || Number(part) > 255)) return null
    return { version: 4, bits: 32, value: octets.reduce((result, part) => (result << 8n) | BigInt(Number(part)), 0n) }
  }
  let normalized = address
  if (normalized.includes('.')) {
    const separator = normalized.lastIndexOf(':')
    if (separator < 0) return null
    const ipv4 = parseAddress(normalized.slice(separator + 1))
    if (!ipv4 || ipv4.version !== 4) return null
    const high = Number((ipv4.value >> 16n) & 0xffffn).toString(16)
    const low = Number(ipv4.value & 0xffffn).toString(16)
    normalized = `${normalized.slice(0, separator)}:${high}:${low}`
  }
  const halves = normalized.split('::')
  if (halves.length > 2) return null
  const left = halves[0] ? halves[0].split(':') : []
  const right = halves.length === 2 && halves[1] ? halves[1].split(':') : []
  const missing = halves.length === 2 ? 8 - left.length - right.length : 0
  if (halves.length === 1 && left.length !== 8 || halves.length === 2 && missing < 1) return null
  const groups = [...left, ...Array.from({ length: missing }, () => '0'), ...right]
  if (groups.length !== 8 || groups.some((part) => !/^[0-9a-f]{1,4}$/.test(part))) return null
  const parsed = groups.reduce((result, part) => (result << 16n) | BigInt(`0x${part}`), 0n)
  return { version: 6, bits: 128, value: parsed }
}
function addressInCIDR(address: string, cidr: string): boolean {
  const [base, prefixText] = cidr.split('/')
  const parsedAddress = parseAddress(address)
  const parsedBase = parseAddress(base)
  const prefixLength = Number(prefixText)
  if (!parsedAddress || !parsedBase || parsedAddress.version !== parsedBase.version || !Number.isInteger(prefixLength) || prefixLength < 0 || prefixLength > parsedAddress.bits) return false
  const shift = BigInt(parsedAddress.bits - prefixLength)
  return parsedAddress.value >> shift === parsedBase.value >> shift
}
const builtInLANNetworks = ['10.0.0.0/8', '172.16.0.0/12', '192.168.0.0/16', '127.0.0.0/8', '169.254.0.0/16', 'fc00::/7', 'fe80::/10', '::1/128']
function isLANAddress(ip: string): boolean {
  return [...builtInLANNetworks, ...lanNetworks.value].some((network) => addressInCIDR(ip, network))
}
function isInternalFlow(connection: Connection): boolean {
  return isLANAddress(connection.source_ip) && isLANAddress(connection.destination_ip)
}
function toggleInternalTraffic(): void {
  hideInternalTraffic.value = !hideInternalTraffic.value
  localStorage.setItem('observer-hide-internal', String(hideInternalTraffic.value))
  if (view.value === 'traffic') void loadTraffic()
}
function openLANForIP(ip: string): void {
  selectedLANDevice.value = lanDevices.value.find((device) => device.addresses.some((address) => address.ip === ip)) ?? null
  lanAlias.value = selectedLANDevice.value?.name_source === 'manual' ? selectedLANDevice.value.name : ''
  lanCategory.value = selectedLANDevice.value?.category_source === 'manual' ? selectedLANDevice.value.device_category : 'unknown'
  lanSearch.value = ip
  selectView('lan')
}
function openLANDevice(device: LANDevice): void {
  selectedLANDevice.value = device
  lanAlias.value = device.name_source === 'manual' ? device.name : ''
  lanCategory.value = device.category_source === 'manual' ? device.device_category : 'unknown'
}
function showDeviceConnections(device: LANDevice): void {
  const address = device.addresses.find((item) => !item.ip.startsWith('fe80:')) ?? device.addresses[0]
  if (!address) return
  searchText.value = address.ip
  selectedDevice.value = ''
  selectedProtocol.value = ''
  attributionFilter.value = ''
  selectView('connections')
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

function openIPMetadata(ip: string): void {
  editingIP.value = ip
  showIPMetadata.value = true
  void loadEnrichment(ip)
}
async function loadEnrichment(ip: string): Promise<void> {
  enrichmentLoading.value = true
  try {
    const response = await api<{ items: Enrichment[] }>(`/api/v1/enrichment?ip=${encodeURIComponent(ip)}`)
    const item = response.items?.[0]
    if (!item) return
    enrichmentCache.value = { ...enrichmentCache.value, [ip]: item }
    enrichmentFetchedAt.value = { ...enrichmentFetchedAt.value, [ip]: Date.now() }
    ipMetadataForm.value = {
      domain: item.manual?.domain ?? '', asn: item.manual?.asn?.toString() ?? '', organization: item.manual?.organization ?? '',
      country: item.manual?.country ?? '', region: item.manual?.region ?? '', city: item.manual?.city ?? '',
      latitude: item.manual?.latitude?.toString() ?? '', longitude: item.manual?.longitude?.toString() ?? '', trusted: item.trusted,
    }
  } catch (error) { errorMessage.value = error instanceof Error ? error.message : '无法读取 IP 归属信息' }
  finally { enrichmentLoading.value = false }
}
async function loadEnrichmentsForConnections(): Promise<void> {
  const now = Date.now()
  const requestID = ++enrichmentRequestID
  const ips = [...new Set(visibleActiveConnections.value.map((connection) => connection.local_is_source ? connection.destination_ip : connection.source_ip))]
    .filter((ip) => ip && (!enrichmentCache.value[ip] || now - (enrichmentFetchedAt.value[ip] ?? 0) >= 60_000)).slice(0, 100)
  if (!ips.length) return
  try {
    const params = new URLSearchParams()
    ips.forEach((ip) => params.append('ip', ip))
    const response = await api<{ items: Enrichment[] }>(`/api/v1/enrichment?${params}`)
    if (requestID !== enrichmentRequestID) return
    const updated = { ...enrichmentCache.value }
    const fetchedAt = { ...enrichmentFetchedAt.value }
    for (const item of asArray<Enrichment>(response.items)) {
      if ((fetchedAt[item.ip] ?? 0) > now) continue
      updated[item.ip] = item
      fetchedAt[item.ip] = Date.now()
    }
    enrichmentCache.value = updated
    enrichmentFetchedAt.value = fetchedAt
  } catch { /* enrichment is optional; core overview remains usable */ }
}
async function loadRankings(): Promise<void> {
  if (!signedIn.value) return
  const requestID = ++rankingRequestID
  try {
    const dimensions = ['device', 'domain', 'local_port', 'process']
    if (!dimensions.includes(rankingDimension.value)) dimensions.push(rankingDimension.value)
    const params = new URLSearchParams({ dimensions: dimensions.join(','), limit: '10', trusted: rankingTrust.value, sort: rankingSort.value })
    const response = await api<{ items: Record<string, RankingRow[]>; partial: boolean; unavailable_dimensions?: string[] }>(`/api/v1/overview/rankings?${params}`)
    if (requestID !== rankingRequestID) return
    rankingRows.value = Object.fromEntries(Object.entries(asRecord(response.items)).map(([key, value]) => [key, asArray<RankingRow>(value)]))
    rankingPartial.value = response.partial
    rankingUnavailableDimensions.value = asArray<string>(response.unavailable_dimensions)
  } catch (error) { if (requestID === rankingRequestID) errorMessage.value = error instanceof Error ? error.message : '无法加载 24 小时排名' }
}
function rankingLabel(dimension: string): string { return ({ device: 'Agent', domain: '远端域名', local_port: 'Agent · 本地端口', process: 'Agent · 进程', remote_ip: '远端 IP', interface: '网卡', asn: 'ASN', organization: 'ASN 登记组织', country: '国家', region: '地区', city: '城市', container: '容器' } as Record<string, string>)[dimension] ?? dimension }
function rankingName(row: RankingRow): string {
  if (row.dimension === 'device') return deviceName(row.value)
  const device = row.device_id ? ` · ${deviceName(row.device_id)}` : ''
  return `${row.value}${device}`
}
async function saveIPMetadata(): Promise<void> {
  const ip = editingIP.value
  if (!ip) return
  const optional = (value: string): string | null => value.trim() || null
  const body: Record<string, unknown> = { ip, trusted: ipMetadataForm.value.trusted }
  for (const key of ['domain', 'organization', 'country', 'region', 'city'] as const) body[key] = optional(ipMetadataForm.value[key])
  const asn = ipMetadataForm.value.asn.trim()
  body.asn = asn ? Number(asn) : null
  const latitude = ipMetadataForm.value.latitude.trim(), longitude = ipMetadataForm.value.longitude.trim()
  body.latitude = latitude ? Number(latitude) : null
  body.longitude = longitude ? Number(longitude) : null
  busy.value = true
  try {
    await api('/api/v1/ip-metadata', { method: 'PATCH', body: JSON.stringify(body) })
    await loadEnrichment(ip)
    await Promise.all([loadRankings(), loadEnrichmentsForConnections()])
    showIPMetadata.value = false
  } catch (error) { errorMessage.value = error instanceof Error ? error.message : '无法保存 IP 归属信息' }
  finally { busy.value = false }
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
    const connectionParams = new URLSearchParams({ limit: '10000' })
    if (hideInternalTraffic.value) connectionParams.set('hide_internal', 'true')
    const [deviceResponse, connectionResponse, ruleResponse, todoResponse, lanResponse] = await Promise.all([
      api<{ items: Device[] }>('/api/v1/devices'),
      api<{ items: Connection[]; total: number }>(`/api/v1/connections?${connectionParams}`),
      api<{ items: Rule[] }>('/api/v1/attribution/rules'),
      api<{ items: Todo[] }>('/api/v1/project/todos'),
      api<{ items: LANDevice[]; networks: string[] }>('/api/v1/lan/devices'),
    ])
    devices.value = asArray<Device>(deviceResponse.items).map(normalizeDevice)
    connections.value = asArray<Connection>(connectionResponse.items)
    connectionsTotal.value = connectionResponse.total
    rules.value = asArray<Rule>(ruleResponse.items).map(normalizeRule)
    todos.value = asArray<Todo>(todoResponse.items)
    const normalizedLANDevices = asArray<LANDevice>(lanResponse.items).map(normalizeLANDevice)
    lanDevices.value = normalizedLANDevices
    lanNetworks.value = asArray<string>(lanResponse.networks)
    if (selectedLANDevice.value) selectedLANDevice.value = normalizedLANDevices.find((item) => item.key === selectedLANDevice.value?.key) ?? null
    lastUpdated.value = new Date()
    if (view.value === 'traffic') await loadTraffic()
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : '无法加载监控数据'
  } finally { loading.value = false }
}
async function loadLAN(): Promise<void> {
  const responseResult = await Promise.allSettled([
    api<{ items: LANDevice[]; networks: string[] }>('/api/v1/lan/devices'),
    loadTopology(),
  ])
  try {
    if (responseResult[0].status === 'rejected') throw responseResult[0].reason
    const response = responseResult[0].value
    const normalizedLANDevices = asArray<LANDevice>(response.items).map(normalizeLANDevice)
    lanDevices.value = normalizedLANDevices
    lanNetworks.value = asArray<string>(response.networks)
    if (selectedLANDevice.value) selectedLANDevice.value = normalizedLANDevices.find((item) => item.key === selectedLANDevice.value?.key) ?? null
  } catch (error) { errorMessage.value = error instanceof Error ? error.message : '无法加载局域网设备' }
}
async function loadTopology(): Promise<void> {
  topologyLoading.value = true
  topologyError.value = ''
  try {
    const response = await api<TopologyResponse>('/api/v1/lan/topology')
    topology.value = {
      groups: asArray<TopologyGroup>(response.groups).map((group) => ({
        ...group,
        agents: asArray<TopologyAgent>(group.agents),
        assets: asArray<LANDevice>(group.assets).map(normalizeLANDevice),
      })),
      unassigned: {
        center: response.unassigned?.center,
        agents: asArray<TopologyAgent>(response.unassigned?.agents),
        assets: asArray<LANDevice>(response.unassigned?.assets).map(normalizeLANDevice),
      },
    }
  } catch (error) {
    topologyError.value = error instanceof Error ? error.message : '无法加载拓扑数据'
  } finally { topologyLoading.value = false }
}
async function saveLANAlias(): Promise<void> {
  const device = selectedLANDevice.value
  if (!device?.mac) { errorMessage.value = '该设备没有可用于持久命名的 MAC 地址'; return }
  busy.value = true
  try {
    await api('/api/v1/lan/devices/alias', { method: 'PATCH', body: JSON.stringify({ network_id: device.network_id, mac: device.mac, name: lanAlias.value.trim(), device_category: lanCategory.value }) })
    await Promise.all([loadLAN(), loadDashboard()])
  } catch (error) { errorMessage.value = error instanceof Error ? error.message : '无法保存设备名称' }
  finally { busy.value = false }
}
function beginDeviceProfile(device: Device): void {
  editingDeviceID.value = device.id
  deviceProfileForm.value = { name: device.name, device_category: device.device_category || 'unknown', latitude: device.latitude?.toString() ?? '', longitude: device.longitude?.toString() ?? '' }
}
function closeDeviceProfileDialog(): void {
  deviceProfileDialog.value?.close()
  editingDeviceID.value = ''
}
function onDeviceProfileDialogClose(): void {
  editingDeviceID.value = ''
}
async function saveDeviceProfile(device: Device): Promise<void> {
  busy.value = true
  try {
    const latitude = deviceProfileForm.value.latitude.trim(), longitude = deviceProfileForm.value.longitude.trim()
    if (Boolean(latitude) !== Boolean(longitude)) throw new Error('纬度和经度必须一起填写或清空')
    const body = {
      name: deviceProfileForm.value.name,
      device_category: deviceProfileForm.value.device_category,
      latitude: latitude ? Number(latitude) : null,
      longitude: longitude ? Number(longitude) : null,
    }
    await api(`/api/v1/devices/${device.id}/profile`, { method: 'PATCH', body: JSON.stringify(body) })
    editingDeviceID.value = ''
    await Promise.all([loadDashboard(), loadLAN()])
  } catch (error) { errorMessage.value = error instanceof Error ? error.message : '无法保存设备资料' }
  finally { busy.value = false }
}
function categoryLabel(value: string): string {
  return ({ unknown: '未指定', computer: '计算机', router: '路由器', gateway: '网关', laptop: '笔记本', tablet: '平板', phone: '手机', tv: '电视', server: '服务器', nas: 'NAS', other: '其他' } as Record<string, string>)[value] ?? '未指定'
}
function topologyAssetPosition(index: number): { x: number; y: number } {
  return { x: 28 + (index % 4) * 190, y: 250 + Math.floor(index / 4) * 112 }
}
function topologyCanvasWidth(group: TopologyGroup): number {
  return Math.max(760, (group.agents.length + (group.center ? 1 : 0)) * 190 + 48, Math.min(4, group.assets.length) * 190 + 56)
}
function topologyTopStart(group: TopologyGroup): number {
  const count = group.agents.length + (group.center ? 1 : 0)
  return Math.max(18, (topologyCanvasWidth(group) - Math.max(1, count) * 190) / 2 + 10)
}
function topologyCenterPosition(group: TopologyGroup): { x: number; y: number } { return { x: topologyTopStart(group), y: 24 } }
function topologyAgentPosition(index: number, group: TopologyGroup): { x: number; y: number } {
  return { x: topologyTopStart(group) + (index + (group.center ? 1 : 0)) * 190, y: 24 }
}
function topologyHubPosition(group: TopologyGroup): { x: number; y: number } { return { x: topologyCanvasWidth(group) / 2 - 76, y: 142 } }
function topologyHubAnchor(group: TopologyGroup): { x: number; y: number } { return { x: topologyCanvasWidth(group) / 2, y: 172 } }
function topologyHeight(group: TopologyGroup): number { return Math.max(260, 246 + Math.ceil(group.assets.length / 4) * 112) }
function topologyAssetAnchor(index: number): { x: number; y: number } { const p = topologyAssetPosition(index); return { x: p.x + 80, y: p.y } }
function topologyAgentAnchor(index: number, group: TopologyGroup): { x: number; y: number } { const p = topologyAgentPosition(index, group); return { x: p.x + 80, y: p.y + 70 } }
function openTopologyAgent(agent: TopologyAgent): void {
  const device = devices.value.find((item) => item.id === agent.id)
  if (!device) return
  beginDeviceProfile(device)
  selectView('devices')
}
function openTopologyAsset(asset: LANDevice): void {
  const device = lanDevices.value.find((item) => item.key === asset.key) ?? asset
  openLANDevice(device)
  lanSearch.value = ''
}
async function loadTodos(): Promise<void> {
  try {
    const response = await api<{ items: Todo[] }>('/api/v1/project/todos')
    todos.value = asArray<Todo>(response.items)
    lastUpdated.value = new Date()
  } catch (error) { errorMessage.value = error instanceof Error ? error.message : '无法加载项目进度' }
}
function todoStatusLabel(status: Todo['status']): string { return ({ todo: '待办', in_progress: '进行中', done: '已完成', blocked: '阻塞' })[status] }
function todoPriorityLabel(priority: Todo['priority']): string { return ({ low: '低', medium: '中', high: '高' })[priority] }
function todoStatusClass(status: Todo['status']): string { return status === 'done' ? 'good' : status === 'blocked' ? 'bad' : status === 'in_progress' ? 'warn' : '' }
function openTodoForm(todo?: Todo): void {
  editingTodo.value = todo ?? null
  todoForm.value = todo ? { phase: todo.phase, title: todo.title, description: todo.description, status: todo.status, priority: todo.priority, progress: todo.progress, blocker: todo.blocker } : { phase: '', title: '', description: '', status: 'todo', priority: 'medium', progress: 0, blocker: '' }
  showTodoForm.value = true
}
async function saveTodo(): Promise<void> {
  if (!todoForm.value.title.trim()) { errorMessage.value = 'ToDo 标题不能为空'; return }
  busy.value = true
  try {
    const method = editingTodo.value ? 'PATCH' : 'POST'
    const path = editingTodo.value ? `/api/v1/project/todos/${editingTodo.value.id}` : '/api/v1/project/todos'
    await api<Todo>(path, { method, body: JSON.stringify(todoForm.value) })
    showTodoForm.value = false
    await loadDashboard()
  } catch (error) { errorMessage.value = error instanceof Error ? error.message : '无法保存 ToDo' }
  finally { busy.value = false }
}
async function setTodoStatus(todo: Todo, status: Todo['status']): Promise<void> {
  busy.value = true
  try {
    await api<Todo>(`/api/v1/project/todos/${todo.id}`, { method: 'PATCH', body: JSON.stringify({ phase: todo.phase, title: todo.title, description: todo.description, status, priority: todo.priority, progress: status === 'done' ? 100 : todo.progress, blocker: todo.blocker }) })
    await loadDashboard()
  } catch (error) { errorMessage.value = error instanceof Error ? error.message : '无法更新 ToDo' }
  finally { busy.value = false }
}
async function deleteTodo(todo: Todo): Promise<void> {
  if (!window.confirm(`删除 ToDo“${todo.title}”？`)) return
  try { await api(`/api/v1/project/todos/${todo.id}`, { method: 'DELETE' }); todos.value = todos.value.filter((item) => item.id !== todo.id) }
  catch (error) { errorMessage.value = error instanceof Error ? error.message : '无法删除 ToDo' }
}
function openSocket(): void {
  if (socketReconnectTimer) {
    window.clearTimeout(socketReconnectTimer)
    socketReconnectTimer = undefined
  }
  closeSocket()
  const scheme = location.protocol === 'https:' ? 'wss:' : 'ws:'
  socket = new WebSocket(`${scheme}//${location.host}/ws/v1/connections`)
  socket.onopen = () => { socketReconnectDelay = 2_500 }
  socket.onmessage = (event) => {
    try {
      const message = JSON.parse(event.data) as { type: string; data?: Connection | Connection[]; total?: number }
      if (message.type === 'connections.snapshot' && Array.isArray(message.data)) {
        connections.value = message.data as Connection[]
        connectionsTotal.value = message.total ?? message.data.length
      } else if (message.type === 'connection.updated' && message.data && !Array.isArray(message.data)) {
        const observation = message.data as Connection
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
          if (update.capabilities) device.capabilities = asArray<Capability>(update.capabilities)
        }
      } else if (message.type === 'lan.updated') {
        void loadLAN()
      }
    } catch { /* ignore malformed push payloads */ }
  }
  socket.onclose = () => {
    if (signedIn.value && !socketReconnectTimer) {
      const delay = socketReconnectDelay
      socketReconnectDelay = Math.min(socketReconnectDelay * 2, 30_000)
      socketReconnectTimer = window.setTimeout(() => {
        socketReconnectTimer = undefined
        if (signedIn.value) openSocket()
      }, delay)
    }
  }
}
function closeSocket(): void {
  if (socketReconnectTimer) {
    window.clearTimeout(socketReconnectTimer)
    socketReconnectTimer = undefined
  }
  if (socket) { socket.onclose = null; socket.close(); socket = null }
}
async function loadTraffic(): Promise<void> {
  const to = new Date()
  const hours = trafficPeriod.value === '7d' ? 168 : trafficPeriod.value === '30d' ? 720 : 24
  const from = new Date(to.getTime() - hours * 60 * 60 * 1000)
  try {
    const common = new URLSearchParams({ from: from.toISOString(), to: to.toISOString(), limit: '20000' })
    if (hideInternalTraffic.value) common.set('hide_internal', 'true')
    if (trafficDevice.value) common.set('device_id', trafficDevice.value)
    if (trafficInterface.value.trim()) common.set('interface', trafficInterface.value.trim())
    if (trafficProtocol.value) common.set('protocol', trafficProtocol.value)
    if (trafficDomain.value.trim()) common.set('domain', trafficDomain.value.trim())
    if (trafficPort.value.trim()) common.set(trafficPortRole.value, trafficPort.value.trim())
    const summaryParams = new URLSearchParams(common)
    summaryParams.set('dimension', trafficDimension.value)
    const seriesParams = new URLSearchParams(summaryParams)
    seriesParams.set('bucket', trafficPeriod.value === '24h' ? '1m' : trafficPeriod.value === '7d' ? '5m' : '1h')
    const presenceParams = new URLSearchParams({ from: from.toISOString(), to: to.toISOString(), minimum_ratio: presenceMinimum.value, limit: '100' })
    if (hideInternalTraffic.value) presenceParams.set('hide_internal', 'true')
    if (trafficDevice.value) presenceParams.set('device_id', trafficDevice.value)
    if (trafficDomain.value.trim()) presenceParams.set('domain', trafficDomain.value.trim())
    const [summary, series, persistent] = await Promise.all([
      api<{ items: TrafficPoint[] }>(`/api/v1/traffic/summary?${summaryParams}`),
      api<{ items: TrafficPoint[] }>(`/api/v1/traffic/timeseries?${seriesParams}`),
      api<{ items: PresencePoint[] }>(`/api/v1/presence?${presenceParams}`),
    ])
    traffic.value = asArray<TrafficPoint>(summary.items)
    trafficSeries.value = asArray<TrafficPoint>(series.items)
    presence.value = asArray<PresencePoint>(persistent.items)
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
async function createEnrollment(): Promise<void> {
  if (!enrollmentForm.value.name.trim() || !enrollmentForm.value.server.trim()) { errorMessage.value = '设备名称和 Center 地址不能为空'; return }
  busy.value = true
  try {
    enrollment.value = await api<EnrollmentResponse>('/api/v1/devices/enrollment-tokens', { method: 'POST', body: JSON.stringify({ name: enrollmentForm.value.name.trim(), observer_type: enrollmentForm.value.observer_type, expires_in_hours: enrollmentForm.value.expires_in_hours }) })
  } catch (error) { errorMessage.value = error instanceof Error ? error.message : '无法创建一次性 Token' }
  finally { busy.value = false }
}
function closeEnrollment(): void { showEnrollmentForm.value = false; enrollment.value = null; enrollmentCopyStatus.value = '' }
type EnrollmentMode = 'native' | 'docker-compose' | 'docker-run' | 'openwrt-lite'
function enrollmentCommand(mode: EnrollmentMode): string {
  if (!enrollment.value) return ''
  const e = enrollmentForm.value
  const security = e.security_mode ? 'true' : 'false'
  const ca = e.security_mode ? ` --ca-url https://${e.web_server}/.well-known/network-observer-ca.pem --fingerprint ${enrollment.value.tls_fingerprint} --tls-server-name ${enrollment.value.tls_server_name}` : ''
  if (mode === 'openwrt-lite') {
    const base = e.security_mode ? 'https://github.com/jonysun/Network-Observer/releases/download' : `http://${e.bootstrap_server}/bootstrap/openwrt`
    const assetURL = `${base}/${e.version}/install-agent-openwrt.sh`
    const defaultBootstrap = `${location.hostname}:8080`
    const baseArg = !e.security_mode && e.bootstrap_server !== defaultBootstrap ? ` --download-base ${base}` : ''
    return `uclient-fetch -O- ${assetURL} | sh -s -- --version ${e.version}${baseArg} --server ${e.server} --token ${enrollment.value.plaintext_token} --name '${e.name}' --security-mode ${security}${ca}`
  }
  return `curl -fsSL https://github.com/jonysun/Network-Observer/releases/download/${e.version}/install-agent.sh | bash -s -- --mode ${mode} --version ${e.version} --server ${e.server} --token ${enrollment.value.plaintext_token} --name '${e.name}' --type ${e.observer_type} --security-mode ${security}${ca}`
}
async function copyEnrollmentCommand(mode: EnrollmentMode): Promise<void> {
  const text = enrollmentCommand(mode)
  try { await navigator.clipboard.writeText(text) } catch {
    const textarea = document.createElement('textarea')
    textarea.value = text; textarea.setAttribute('readonly', ''); textarea.style.position = 'fixed'; textarea.style.opacity = '0'
    document.body.appendChild(textarea); textarea.select()
    const copied = document.execCommand('copy'); textarea.remove()
    if (!copied) throw new Error('浏览器拒绝访问剪贴板，请手动复制命令')
  }
  enrollmentCopyStatus.value = '安装命令已复制'
}
async function revokeEnrollment(): Promise<void> {
  if (!enrollment.value || !window.confirm('确认撤销这枚一次性 Token？撤销后安装命令将失效。')) return
  busy.value = true
  try {
    await api(`/api/v1/devices/enrollment-tokens/${enrollment.value.token.id}`, { method: 'DELETE' })
    closeEnrollment()
  } catch (error) { errorMessage.value = error instanceof Error ? error.message : '无法撤销 Token' }
  finally { busy.value = false }
}
function selectView(key: string): void { view.value = key; errorMessage.value = '' }
function capabilityClass(status: string): string {
  if (status === 'supported' || status === 'available' || status === 'enabled') return 'good'
  if (status === 'unsupported' || status === 'unavailable' || status === 'disabled') return 'bad'
  return 'warn'
}
watch([trafficDimension, trafficPeriod, trafficPortRole, trafficPort, trafficDomain, trafficDevice, trafficInterface, trafficProtocol, presenceMinimum], () => { if (signedIn.value && view.value === 'traffic') void loadTraffic() })
watch([rankingDimension, rankingTrust, rankingSort], () => { if (signedIn.value) void loadRankings() })
watch([selectedDevice, selectedProtocol, attributionFilter, searchText], () => { connectionPage.value = 0 })
watch(connectionPageCount, (count) => { if (connectionPage.value >= count) connectionPage.value = count - 1 })
watch(editingDevice, (device) => {
  if (device) void nextTick(() => {
    if (deviceProfileDialog.value && !deviceProfileDialog.value.open) deviceProfileDialog.value.showModal()
  })
})
watch(view, (next) => {
  if (next === 'traffic' && signedIn.value) void loadTraffic()
  if (next === 'lan' && signedIn.value) void loadLAN()
  if (next === 'overview' && signedIn.value) { void loadRankings(); void loadEnrichmentsForConnections() }
})
onMounted(async () => {
  const authenticated = await checkSession()
  if (authenticated) { await loadDashboard(); await loadRankings(); await loadEnrichmentsForConnections(); openSocket() }
  loading.value = false
  trafficTimer = window.setInterval(() => { if (signedIn.value && view.value === 'traffic') void loadTraffic() }, 30_000)
  lanTimer = window.setInterval(() => { if (signedIn.value && view.value === 'lan') void loadLAN() }, 30_000)
  todoTimer = window.setInterval(() => { if (signedIn.value && view.value === 'progress') void loadTodos() }, 15_000)
  rankingTimer = window.setInterval(() => { if (signedIn.value && view.value === 'overview') { void loadRankings(); void loadEnrichmentsForConnections() } }, 15_000)
  deviceStatusTimer = window.setInterval(() => {
    const cutoff = Date.now() - DEVICE_ONLINE_TIMEOUT_MS
    for (const device of devices.value) {
      if (Date.parse(device.last_seen) < cutoff) device.online = false
    }
  }, 5_000)
})
onBeforeUnmount(() => {
  closeSocket()
  if (trafficTimer) window.clearInterval(trafficTimer)
  if (lanTimer) window.clearInterval(lanTimer)
  if (todoTimer) window.clearInterval(todoTimer)
  if (rankingTimer) window.clearInterval(rankingTimer)
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
        <div class="data-attribution">本产品包含<a href="https://www.maxmind.com" target="_blank" rel="noreferrer">由 MaxMind 创建的 GeoLite 数据</a></div>
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
          <article class="metric"><div class="metric-top"><span>活动连接</span><Activity :size="16" /></div><strong>{{ visibleActiveConnections.length.toLocaleString() }}</strong><span class="metric-foot">TCP {{ tcpCount }} · UDP {{ udpCount }}</span></article>
          <article class="metric"><div class="metric-top"><span>当前接收</span><ArrowDownToLine :size="16" /></div><strong>{{ formatRate(receiveRate) }}</strong><span class="metric-foot">按观测设备方向统计</span></article>
          <article class="metric"><div class="metric-top"><span>当前发送</span><ArrowUpFromLine :size="16" /></div><strong>{{ formatRate(sendRate) }}</strong><span class="metric-foot">按观测设备方向统计</span></article>
        </div>
        <ConnectionGlobe :routes="globeRoutes.routes" :missing="globeRoutes.missing">
          <template #filters>
            <label class="select-control"><Network :size="14" /><select v-model="globeDeviceFilter" aria-label="按 Agent 筛选地球路线"><option value="">全部 Agent</option><option v-for="device in devices" :key="device.id" :value="device.id">{{ device.name }}</option></select><ChevronDown :size="14" /></label>
            <label class="select-control"><ShieldCheck :size="14" /><select v-model="globeTrustFilter" aria-label="按远端可信状态筛选地球路线"><option value="all">全部远端</option><option value="trusted">仅可信</option><option value="untrusted">排除可信</option></select><ChevronDown :size="14" /></label>
          </template>
        </ConnectionGlobe>
        <section class="data-section ranking-section">
          <div class="section-heading"><div><h2>过去 24 小时排名</h2><p>流量按分钟汇总；连接数按窗口内不同 flow_id 计数</p></div><div class="heading-tools"><label class="select-control"><Activity :size="14" /><select v-model="rankingSort" aria-label="排名依据"><option value="traffic">按流量排名</option><option value="connections">按连接数排名</option></select><ChevronDown :size="14" /></label><label class="select-control"><ShieldCheck :size="14" /><select v-model="rankingTrust" aria-label="按可信状态筛选"><option value="all">全部远端</option><option value="trusted">仅可信</option><option value="untrusted">排除可信</option></select></label><label class="select-control"><Database :size="14" /><select v-model="rankingDimension" aria-label="排名维度"><option value="device">Agent</option><option value="domain">远端域名</option><option value="local_port">Agent · 本地端口</option><option value="process">Agent · 进程</option><option value="remote_ip">远端 IP</option><option value="interface">网卡</option><option value="asn">ASN</option><option value="organization">ASN 登记组织</option><option value="country">国家</option><option value="region">地区</option><option value="city">城市</option><option value="container">容器</option></select><ChevronDown :size="14" /></label></div></div>
          <p v-if="rankingPartial" class="ranking-notice" role="status">数据规模超过单次查询上限，当前排名为部分结果。</p>
          <div class="ranking-dimension-grid">
            <section v-for="dimension in ['device', 'domain', 'local_port', 'process']" :key="dimension" class="ranking-dimension-card">
              <h3>{{ rankingLabel(dimension) }}</h3>
              <p v-if="rankingUnavailableDimensions.includes(dimension)" class="ranking-notice">升级前的历史连接缺少此维度数据，仅统计新增记录。</p>
              <div v-if="rankingRows[dimension]?.length" class="ranking-list">
                <article v-for="(row, index) in rankingRows[dimension].slice(0, 10)" :key="`${dimension}:${row.device_id || ''}:${row.value}`" class="ranking-row">
                  <span class="ranking-position">{{ String(index + 1).padStart(2, '0') }}</span>
                  <div class="ranking-main"><button class="endpoint-link ranking-name" type="button" :title="row.remote_ip ? `编辑 ${row.remote_ip} 的域名和归属` : row.value" @click="row.remote_ip ? openIPMetadata(row.remote_ip) : null">{{ rankingName(row) }}</button><small>{{ row.connections.toLocaleString() }} 条观测流<span v-if="row.estimated"> · 含估算</span><span v-if="row.gap_reason"> · 有数据缺口</span><button v-if="row.remote_ip" class="inline-action" type="button" @click="openIPMetadata(row.remote_ip)">IP 归属 / 可信设置</button></small></div>
                  <div class="ranking-volume"><strong>{{ formatBytes(row.upload_bytes + row.download_bytes) }}</strong><small>↑ {{ formatBytes(row.upload_bytes) }} · ↓ {{ formatBytes(row.download_bytes) }}</small></div>
                </article>
              </div>
              <p v-else class="ranking-empty">暂无汇总数据</p>
            </section>
          </div>
          <div class="ranking-extra">
            <h3>其他维度</h3>
          <div v-if="activeRankingRows.length" class="ranking-grid">
            <article v-for="(row, index) in activeRankingRows" :key="`${row.device_id || ''}:${row.value}`" class="ranking-row">
              <span class="ranking-position">{{ String(index + 1).padStart(2, '0') }}</span>
              <div class="ranking-main"><button class="endpoint-link ranking-name" type="button" :title="row.remote_ip ? `编辑 ${row.remote_ip} 的域名和归属` : row.value" @click="row.remote_ip ? openIPMetadata(row.remote_ip) : null">{{ rankingName(row) }}</button><small>{{ row.connections.toLocaleString() }} 条观测流<span v-if="row.estimated"> · 含估算</span><span v-if="row.gap_reason"> · 有数据缺口</span><button v-if="row.remote_ip" class="inline-action" type="button" @click="openIPMetadata(row.remote_ip)">IP 归属 / 可信设置</button></small></div>
              <div class="ranking-volume"><strong>{{ formatBytes(row.upload_bytes + row.download_bytes) }}</strong><small>↑ {{ formatBytes(row.upload_bytes) }} · ↓ {{ formatBytes(row.download_bytes) }}</small></div>
            </article>
          </div>
          <div v-else class="empty-state compact-empty"><Gauge :size="21" /><strong>暂无排名数据</strong><span>新汇总数据到达后会出现在这里</span></div>
          </div>
        </section>
        <div class="overview-grid">
          <section class="data-section">
            <div class="section-heading"><div><h2>高流量连接</h2><p>活动连接按双向速率排序</p></div><button class="text-button" @click="selectView('connections')">全部连接 <ChevronDown :size="14" /></button></div>
        <ConnectionTable :items="filteredConnections.slice(0, 8)" :device-name="deviceName" :receive="receive" :send="send" :format-rate="formatRate" :endpoint="endpoint" :open-lan-ip="openLANForIP" :open-ip-metadata="openIPMetadata" :observed-at="observedAt" :source-name="sourceName" :rate-class="rateClass" :open-override="openOverride" :show-actions="false" />
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
        <div class="page-heading"><div><h1>实时连接</h1><p>TCP / UDP 活动连接 · 每 2 秒更新</p></div><div class="heading-tools"><button class="subtle-button" :aria-pressed="hideInternalTraffic" @click="toggleInternalTraffic"><component :is="hideInternalTraffic ? Eye : EyeOff" :size="15" />{{ hideInternalTraffic ? '显示内网互访' : '隐藏内网互访' }}</button><span class="live-pill"><span class="state-dot"></span>实时</span><button class="subtle-button" @click="loadDashboard"><RefreshCw :size="15" />刷新</button></div></div>
        <div class="metric-grid compact-metrics">
          <article class="metric"><div class="metric-top"><span>活跃连接</span><Activity :size="15" /></div><strong>{{ visibleActiveConnections.length.toLocaleString() }}</strong></article>
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
        <ConnectionTable :items="visibleConnections" :device-name="deviceName" :receive="receive" :send="send" :format-rate="formatRate" :endpoint="endpoint" :open-lan-ip="openLANForIP" :open-ip-metadata="openIPMetadata" :observed-at="observedAt" :source-name="sourceName" :rate-class="rateClass" :open-override="openOverride" :show-actions="true" />
        <div class="pagination"><span>显示 {{ filteredConnections.length ? connectionPage * CONNECTION_PAGE_SIZE + 1 : 0 }}–{{ Math.min((connectionPage + 1) * CONNECTION_PAGE_SIZE, filteredConnections.length) }} / {{ filteredConnections.length }} 条<span v-if="connectionsTotal > connections.length">（服务端共 {{ connectionsTotal.toLocaleString() }} 条）</span></span><div><button class="icon-button" aria-label="上一页" title="上一页" :disabled="connectionPage === 0" @click="connectionPage -= 1"><ChevronDown :size="15" class="rotate-up" /></button><span>{{ connectionPage + 1 }} / {{ connectionPageCount }}</span><button class="icon-button" aria-label="下一页" title="下一页" :disabled="connectionPage + 1 >= connectionPageCount" @click="connectionPage += 1"><ChevronDown :size="15" class="rotate-down" /></button></div></div>
      </section>

      <section v-else-if="view === 'devices'" class="page-content">
        <div class="page-heading"><div><h1>设备</h1><p>{{ devices.length }} 台已注册探针 · 凭证可单独吊销</p></div><button class="primary-button" @click="showEnrollmentForm = true"><Plus :size="16" />添加设备</button></div>
        <div class="table-wrap"><table class="data-table"><thead><tr><th>设备</th><th>Agent 角色</th><th>设备类别</th><th>系统 / 架构</th><th>版本</th><th>能力</th><th>最近心跳</th><th>状态</th><th></th></tr></thead><tbody>
          <tr v-for="device in devices" :key="device.id"><td><strong>{{ device.name }}</strong><div class="sub-cell">{{ device.id }}</div></td><td>{{ device.type }}</td><td>{{ categoryLabel(device.device_category) }}</td><td>{{ device.os || '—' }}<div class="sub-cell">{{ device.architecture || '—' }}</div></td><td>{{ device.agent_version || '—' }}</td><td><div class="capability-list"><span v-for="capability in device.capabilities" :key="capability.name" class="capability" :class="capabilityClass(capability.status)" :title="capability.detail">{{ capability.name }} · {{ capability.status }}</span><span v-if="!device.capabilities.length" class="sub-cell">等待能力报告</span></div></td><td>{{ formatTime(device.last_seen) }}</td><td><span class="small-status" :class="device.online ? 'good' : 'bad'">{{ device.online ? '在线' : '离线' }}</span></td><td class="device-actions"><button class="icon-button" :aria-label="`编辑${device.name}名称和类别`" title="编辑名称和设备类别" @click="beginDeviceProfile(device)"><Pencil :size="15" /></button><button class="icon-button danger-icon" :aria-label="`吊销${device.name}凭证`" title="吊销设备凭证" :disabled="revokingDeviceID === device.id" @click="revokeDevice(device)"><LoaderCircle v-if="revokingDeviceID === device.id" class="spin" :size="15" /><ShieldCheck v-else :size="15" /></button></td></tr>
        </tbody></table><div v-if="!devices.length && !loading" class="empty-state"><Network :size="26" /><strong>暂无设备</strong><span>通过 Endpoint 或 Gateway Agent 注册设备</span></div></div>
      </section>

      <section v-else-if="view === 'lan'" class="page-content">
        <div class="page-heading"><div><h1>局域网资产</h1><p>由各 Linux Agent 汇总 ARP / NDP 邻居记录 · 设备名可手动补录</p></div><div class="heading-tools"><button class="subtle-button" :aria-pressed="hideInternalTraffic" @click="toggleInternalTraffic"><component :is="hideInternalTraffic ? Eye : EyeOff" :size="15" />{{ hideInternalTraffic ? '显示内网互访' : '隐藏内网互访' }}</button><button class="subtle-button" @click="loadLAN"><RefreshCw :size="15" />刷新</button></div></div>
        <div class="metric-grid compact-metrics">
          <article class="metric"><div class="metric-top"><span>发现设备</span><Network :size="15" /></div><strong>{{ lanDevices.length.toLocaleString() }}</strong></article>
          <article class="metric"><div class="metric-top"><span>近期可见</span><Activity :size="15" /></div><strong>{{ lanCurrentCount.toLocaleString() }}</strong></article>
          <article class="metric"><div class="metric-top"><span>过期记录</span><Clock3 :size="15" /></div><strong>{{ lanStaleCount.toLocaleString() }}</strong></article>
          <article class="metric"><div class="metric-top"><span>IP 冲突</span><CircleAlert :size="15" /></div><strong>{{ lanConflictCount.toLocaleString() }}</strong></article>
        </div>
        <section class="topology-section" aria-labelledby="topology-title">
          <div class="section-heading"><div><h2 id="topology-title">网络拓扑</h2><p>按网段合并 Agent 的邻居观察关系，不代表物理布线或交换机连接</p></div><button class="subtle-button" :disabled="topologyLoading" @click="loadTopology"><LoaderCircle v-if="topologyLoading" class="spin" :size="14" /><RefreshCw v-else :size="14" />刷新拓扑</button></div>
          <div v-if="topologyError" class="topology-error" role="status"><CircleAlert :size="16" /><span>拓扑暂时无法加载：{{ topologyError }}</span><button class="text-button" @click="loadTopology">重试</button></div>
          <div v-else-if="!topologyGroups.length && !hasUnassignedTopology && !topologyLoading" class="topology-empty"><Router :size="20" /><span>收到邻居快照后，会按网段生成拓扑</span></div>
          <article v-for="group in topologyGroups" :key="group.network_id" class="topology-card">
            <header><div><strong>{{ group.network_prefix }}</strong><span>{{ group.agents.length }} 个观察 Agent · {{ group.assets.length }} 台 LAN 资产</span></div><span class="topology-hint">同一网段仅显示一张图</span></header>
            <div class="topology-scroll"><div class="topology-canvas" :style="{ width: `${topologyCanvasWidth(group)}px`, height: `${topologyHeight(group)}px` }">
              <svg class="topology-lines" :viewBox="`0 0 ${topologyCanvasWidth(group)} ${topologyHeight(group)}`" aria-hidden="true">
                <line v-if="group.center" :x1="topologyCenterPosition(group).x + 80" y1="94" :x2="topologyHubAnchor(group).x" y2="142" />
                <line v-for="(agent, index) in group.agents" :key="`agent-line-${agent.id}`" :x1="topologyAgentAnchor(index, group).x" :y1="topologyAgentAnchor(index, group).y" :x2="topologyHubAnchor(group).x" y2="142" />
                <line v-for="(asset, index) in group.assets" :key="`asset-line-${asset.key}`" :x1="topologyHubAnchor(group).x" y1="202" :x2="topologyAssetAnchor(index).x" :y2="topologyAssetAnchor(index).y" />
              </svg>
              <button v-if="group.center" class="topology-node center-node" :class="{ known: group.center.known }" :style="{ left: `${topologyCenterPosition(group).x}px`, top: `${topologyCenterPosition(group).y}px` }" type="button" title="Center 容器接口地址与此网段匹配"><span class="topology-node-icon"><Network :size="15" /></span><span><strong>Center</strong><small>监控中心 · 已确认同网段</small></span></button>
              <button v-for="(agent, index) in group.agents" :key="agent.id" class="topology-node agent-node" :style="{ left: `${topologyAgentPosition(index, group).x}px`, top: `${topologyAgentPosition(index, group).y}px` }" type="button" :title="`编辑 ${agent.name} 的名称与设备类别`" @click="openTopologyAgent(agent)"><span class="topology-node-icon"><Cpu :size="15" /></span><span><strong>{{ agent.name }}</strong><small>{{ agent.type }} · {{ categoryLabel(agent.device_category) }}</small></span><Pencil :size="12" /></button>
              <div class="topology-network" :style="{ left: `${topologyHubPosition(group).x}px`, top: `${topologyHubPosition(group).y}px` }"><Network :size="15" /><strong>{{ group.network_id }}</strong><small>观察网段</small></div>
              <button v-for="(asset, index) in group.assets" :key="asset.key" class="topology-node asset-node" :class="{ stale: asset.stale, conflict: asset.ip_conflict }" :style="{ left: `${topologyAssetPosition(index).x}px`, top: `${topologyAssetPosition(index).y}px` }" type="button" :title="`查看或编辑 ${asset.name}`" @click="openTopologyAsset(asset)"><span class="topology-node-icon"><Router :size="15" /></span><span><strong>{{ asset.name }}</strong><small>{{ categoryLabel(asset.device_category) }} · {{ asset.addresses[0]?.ip || asset.mac || '地址未知' }}</small></span><span v-if="asset.ip_conflict" class="topology-state">IP 冲突</span><span v-else-if="asset.stale" class="topology-state">过期</span></button>
            </div></div>
          </article>
          <article v-if="hasUnassignedTopology" class="topology-card unassigned-card"><header><div><strong>未确认网段</strong><span>无法从已知网段可靠确定归属</span></div></header><div class="unassigned-nodes"><div v-if="topology.unassigned.center" class="unassigned-center"><CircleHelp :size="15" />Center · 网段未确认</div><button v-for="agent in topology.unassigned.agents" :key="agent.id" class="unassigned-agent" type="button" @click="openTopologyAgent(agent)"><Cpu :size="14" />{{ agent.name }}<small>{{ agent.type }} · 尚无网段观察</small></button><button v-for="asset in topology.unassigned.assets" :key="asset.key" class="unassigned-agent" type="button" @click="openTopologyAsset(asset)"><Router :size="14" />{{ asset.name }}<small>网段信息缺失</small></button></div></article>
        </section>
        <div class="toolbar"><label class="search-control"><Search :size="16" /><input v-model="lanSearch" placeholder="搜索设备名、IP、MAC、网段或观察 Agent" /></label><span class="lan-network-summary">{{ lanNetworks.join(' · ') || '尚未发现本机网段' }}</span></div>
        <div class="lan-layout" :class="{ 'with-detail': selectedLANDevice }">
          <div class="table-wrap"><table class="data-table lan-table"><thead><tr><th>设备 / 网段</th><th>类别</th><th>IPv4 / IPv6</th><th>观察 Agent</th><th>最后出现</th><th>状态</th></tr></thead><tbody>
            <tr v-for="device in filteredLANDevices" :key="device.key" :class="{ 'selected-row': selectedLANDevice?.key === device.key }" @click="openLANDevice(device)">
              <td><button class="lan-name-link" @click.stop="openLANDevice(device)">{{ device.name }}</button><div class="sub-cell"><code>{{ device.mac || 'MAC 未知' }}</code></div><div class="sub-cell">{{ device.network_id }}</div></td><td><span class="category-pill">{{ categoryLabel(device.device_category) }}</span><small class="category-source">{{ device.category_source === 'manual' ? '手动' : device.category_source === 'agent' ? 'Agent 同步' : '未设置' }}</small></td>
              <td><div v-for="address in device.addresses" :key="address.ip" class="lan-address"><button class="endpoint-link" :title="`查看 ${address.ip} 的活动连接`" @click.stop="openLANForIP(address.ip)">{{ address.ip }}</button><small>{{ address.interfaces.join(', ') || '接口未知' }} · {{ address.state }}</small></div></td>
              <td><span v-if="device.observers.length" class="observer-count" :title="device.observers.map((observer) => observer.name).join(', ')">{{ device.observers.length }} 个观察点</span><span v-else class="sub-cell">—</span><small class="observer-names">{{ device.observers.map((observer) => observer.name || observer.id.slice(0, 8)).join(', ') }}</small></td>
              <td>{{ formatTime(device.last_seen) }}</td>
              <td><span v-if="device.ip_conflict" class="small-status bad">IP 冲突</span><span v-else class="small-status" :class="device.stale ? 'warn' : 'good'">{{ device.stale ? '过期' : '近期可见' }}</span></td>
            </tr>
          </tbody></table><div v-if="!filteredLANDevices.length && !loading" class="empty-state"><Router :size="25" /><strong>{{ lanDevices.length ? '没有匹配的设备' : '暂无邻居记录' }}</strong><span>{{ lanDevices.length ? '调整搜索条件' : '等待 Agent 上报邻居表；网关通常能看到更多近期活动设备' }}</span></div></div>
          <aside v-if="selectedLANDevice" class="lan-detail">
            <div class="lan-detail-heading"><div><span class="eyebrow">设备详情</span><h2>{{ selectedLANDevice.name }}</h2></div><button class="icon-button" title="关闭详情" aria-label="关闭详情" @click="selectedLANDevice = null"><X :size="16" /></button></div>
            <dl class="lan-detail-list"><div><dt>MAC</dt><dd><code>{{ selectedLANDevice.mac || '未知' }}</code></dd></div><div><dt>网段</dt><dd>{{ selectedLANDevice.network_id }}</dd></div><div><dt>名称来源</dt><dd>{{ selectedLANDevice.name_source === 'manual' ? '手动别名' : selectedLANDevice.name_source === 'agent' ? '已注册 Agent' : '未知' }}</dd></div><div><dt>设备类别</dt><dd>{{ categoryLabel(selectedLANDevice.device_category) }} · {{ selectedLANDevice.category_source === 'manual' ? '手动' : selectedLANDevice.category_source === 'agent' ? 'Agent 同步' : '未设置' }}</dd></div><div><dt>观察点</dt><dd>{{ selectedLANDevice.observers.map((observer) => observer.name || observer.id).join(', ') || '—' }}</dd></div></dl>
            <div class="lan-detail-addresses"><h3>地址</h3><div v-for="address in selectedLANDevice.addresses" :key="address.ip"><button class="endpoint-link" @click="openLANForIP(address.ip)">{{ address.ip }}</button><small>{{ address.interfaces.join(', ') || '接口未知' }} · {{ address.state }}</small></div></div>
            <form class="lan-alias-form" @submit.prevent="saveLANAlias"><label>设备名称<input v-model="lanAlias" maxlength="128" placeholder="例如：客厅电视" /></label><label>设备类别<select v-model="lanCategory"><option value="unknown">未指定</option><option value="computer">计算机</option><option value="router">路由器</option><option value="gateway">网关</option><option value="laptop">笔记本</option><option value="tablet">平板</option><option value="phone">手机</option><option value="tv">电视</option><option value="server">服务器</option><option value="nas">NAS</option><option value="other">其他</option></select></label><button class="primary-button" :disabled="busy || !selectedLANDevice.mac"><Pencil :size="15" />保存名称和类别</button><small>清空名称或选择“未指定”后保存，会分别移除对应手动覆盖。</small></form>
            <button class="subtle-button lan-connections-button" @click="showDeviceConnections(selectedLANDevice)"><Activity :size="15" />查看该设备连接</button>
          </aside>
        </div>
      </section>

      <section v-else-if="view === 'traffic'" class="page-content">
        <div class="page-heading"><div><h1>流量分析</h1><p>分钟级汇总 · 汇总数据默认保留 90 天</p></div><div class="heading-tools"><button class="subtle-button" :aria-pressed="hideInternalTraffic" @click="toggleInternalTraffic"><component :is="hideInternalTraffic ? Eye : EyeOff" :size="15" />{{ hideInternalTraffic ? '显示内网互访' : '隐藏内网互访' }}</button><button class="subtle-button" @click="loadTraffic"><RefreshCw :size="15" />更新</button></div></div>
        <div class="toolbar traffic-toolbar">
          <label class="select-control"><Database :size="15" /><select v-model="trafficDimension"><option value="device">设备</option><option value="interface">网卡</option><option value="protocol">协议</option><option value="source_ip">源 IP</option><option value="destination_ip">目标 IP</option><option value="local_port">本地端口</option><option value="remote_port">远端口</option><option value="process">进程</option><option value="container">容器</option><option value="domain">域名</option><option value="asn">ASN</option><option value="country">国家</option><option value="city">城市</option></select><ChevronDown :size="14" /></label>
          <label class="select-control"><Clock3 :size="15" /><select v-model="trafficPeriod"><option value="24h">最近 24 小时</option><option value="7d">最近 7 天</option><option value="30d">最近 30 天</option></select><ChevronDown :size="14" /></label>
          <label class="select-control"><Network :size="15" /><select v-model="trafficDevice"><option value="">全部设备</option><option v-for="device in devices" :key="device.id" :value="device.id">{{ device.name }}</option></select><ChevronDown :size="14" /></label>
          <label class="select-control"><Filter :size="15" /><select v-model="trafficProtocol"><option value="">TCP + UDP</option><option value="tcp">TCP</option><option value="udp">UDP</option></select><ChevronDown :size="14" /></label>
          <label class="search-control"><Search :size="16" /><input v-model="trafficPort" inputmode="numeric" placeholder="端口（按角色）" /></label>
          <label class="select-control"><select v-model="trafficPortRole"><option value="local_port">本地端口</option><option value="remote_port">远端口</option></select><ChevronDown :size="14" /></label>
          <label class="search-control"><Search :size="16" /><input v-model="trafficDomain" placeholder="域名过滤" /></label>
        </div>
        <div class="traffic-kpis"><article class="metric"><span>上传</span><strong>{{ formatBytes(trafficUpload) }}</strong></article><article class="metric"><span>下载</span><strong>{{ formatBytes(trafficDownload) }}</strong></article><article class="metric"><span>峰值速率</span><strong>{{ formatRate(trafficPeak) }}</strong></article><article class="metric"><span>平均速率</span><strong>{{ formatRate(trafficAverage) }}</strong></article></div>
        <div v-if="trafficChartRows.length" class="traffic-chart-card"><div class="traffic-header"><span>上传 / 下载时间序列</span><span>{{ trafficChartRows.length }} 个时间桶</span></div><svg class="traffic-chart" viewBox="0 0 100 100" preserveAspectRatio="none" role="img" aria-label="上传下载流量趋势"><polyline :points="chartLine('upload')" fill="none" stroke="#4d9b73" stroke-width="1.6" vector-effect="non-scaling-stroke" /><polyline :points="chartLine('download')" fill="none" stroke="#4a78b5" stroke-width="1.6" vector-effect="non-scaling-stroke" /></svg><div class="chart-legend"><span><i class="legend-upload" />上传</span><span><i class="legend-download" />下载</span><span v-if="trafficChartRows.some((row) => row.estimated)" class="estimate-mark">含估算</span><span v-if="trafficChartRows.some((row) => row.gap)" class="gap-mark">存在数据缺口</span></div><table class="sr-table"><caption>流量时间序列数据</caption><thead><tr><th>时间</th><th>上传</th><th>下载</th></tr></thead><tbody><tr v-for="row in trafficChartRows" :key="row.bucket"><td>{{ formatTime(row.bucket) }}</td><td>{{ formatBytes(row.upload) }}</td><td>{{ formatBytes(row.download) }}</td></tr></tbody></table></div>
        <div v-if="trafficTotals.length" class="traffic-list">
          <div class="traffic-header"><span>{{ trafficDimension }} · TOP 20</span><span>累计流量</span></div>
          <div v-for="(row, index) in trafficTotals" :key="row.value" class="traffic-row"><span class="traffic-rank">{{ String(index + 1).padStart(2, '0') }}</span><span class="traffic-value">{{ row.value }}</span><span class="traffic-bar-track"><span class="traffic-bar" :style="{ width: `${Math.max(2, row.bytes / (trafficTotals[0]?.bytes || 1) * 100)}%` }"></span></span><strong>{{ formatBytes(row.bytes) }}<small v-if="row.estimated" class="estimate-mark">估算</small><small class="sub-cell">↑{{ formatBytes(row.upload) }} ↓{{ formatBytes(row.download) }}</small></strong></div>
        </div>
        <div v-if="presenceTop.length" class="table-wrap presence-table"><div class="traffic-header"><span>持续连接 / IP / 域名</span><span>连续度 ≥ {{ Math.round(Number(presenceMinimum) * 100) }}%</span></div><table class="data-table"><thead><tr><th>端点</th><th>域名</th><th>首次 / 最近</th><th>观测分钟</th><th>连续度</th><th>ASN / 地理</th><th>上下行</th></tr></thead><tbody><tr v-for="row in presenceTop" :key="row.key"><td><code>{{ row.source_ip }}:{{ row.source_port }}</code><br /><code>{{ row.destination_ip }}:{{ row.destination_port }}</code></td><td>{{ row.domain || '未知域名' }}</td><td>{{ formatTime(row.first_seen) }}<br />{{ formatTime(row.last_seen) }}</td><td>{{ row.observed_minutes }}</td><td>{{ Math.round(row.continuity_ratio * 100) }}%</td><td>{{ row.asn || '未知' }}<br /><span class="sub-cell">{{ [row.country, row.region, row.city].filter(Boolean).join(' / ') || '未知归属' }}</span></td><td>↑{{ formatBytes(row.upload_bytes) }}<br />↓{{ formatBytes(row.download_bytes) }}</td></tr></tbody></table></div>
        <div v-else-if="!loading" class="empty-state empty-band"><Gauge :size="28" /><strong>暂无历史流量</strong><span>探针开始上报后，这里会显示 IP、端口、进程和容器汇总</span></div>
      </section>

      <section v-else-if="view === 'progress'" class="page-content">
        <div class="page-heading"><div><h1>项目进度</h1><p>实现计划、验收阻塞项和个人 ToDo 会持久保存在中心数据库</p></div><div class="heading-tools"><button class="subtle-button" @click="loadDashboard"><RefreshCw :size="15" />刷新</button><button class="primary-button" @click="openTodoForm()"><Plus :size="15" />新增 ToDo</button></div></div>
        <div class="metric-grid progress-metrics">
          <article class="metric"><div class="metric-top"><span>整体完成率</span><Check :size="16" /></div><strong>{{ todoSummary.percent }}%</strong><span class="metric-foot">按任务进度平均值</span></article>
          <article class="metric"><div class="metric-top"><span>已完成</span><Check :size="16" /></div><strong>{{ todoSummary.done }}</strong><span class="metric-foot">阶段任务已交付</span></article>
          <article class="metric"><div class="metric-top"><span>进行中</span><Activity :size="16" /></div><strong>{{ todoSummary.inProgress }}</strong><span class="metric-foot">当前正在推进</span></article>
          <article class="metric"><div class="metric-top"><span>阻塞 / 待办</span><CircleHelp :size="16" /></div><strong>{{ todoSummary.blocked }} / {{ todoSummary.todo }}</strong><span class="metric-foot">需要外部环境或后续安排</span></article>
        </div>
        <div class="progress-overview"><div class="progress-track"><span :style="{ width: `${todoSummary.percent}%` }"></span></div><span>{{ todoSummary.percent }}% 已完成</span></div>
        <div v-for="group in todoPhases" :key="group.phase" class="todo-phase">
          <div class="section-heading"><div><h2>{{ group.phase }}</h2><p>{{ group.items.filter((todo) => todo.status === 'done').length }} / {{ group.items.length }} 项完成</p></div></div>
          <div class="todo-list">
            <article v-for="todo in group.items" :key="todo.id" class="todo-card" :class="`todo-${todo.status}`">
              <div class="todo-card-main"><div class="todo-card-title"><span class="small-status" :class="todoStatusClass(todo.status)">{{ todoStatusLabel(todo.status) }}</span><strong>{{ todo.title }}</strong><span class="todo-priority" :class="`priority-${todo.priority}`">{{ todoPriorityLabel(todo.priority) }}优先级</span></div><p v-if="todo.description">{{ todo.description }}</p><p v-if="todo.blocker" class="todo-blocker"><CircleHelp :size="13" />{{ todo.blocker }}</p><div class="todo-progress"><span class="progress-track"><i :style="{ width: `${todo.progress}%` }"></i></span><span>{{ todo.progress }}%</span></div></div>
              <div class="todo-card-actions"><select :value="todo.status" aria-label="任务状态" @change="setTodoStatus(todo, ($event.target as HTMLSelectElement).value as Todo['status'])"><option value="todo">待办</option><option value="in_progress">进行中</option><option value="done">已完成</option><option value="blocked">阻塞</option></select><button class="icon-button" title="编辑 ToDo" aria-label="编辑 ToDo" @click="openTodoForm(todo)"><SlidersHorizontal :size="15" /></button><button class="icon-button danger-icon" title="删除 ToDo" aria-label="删除 ToDo" @click="deleteTodo(todo)"><Trash2 :size="15" /></button></div>
            </article>
          </div>
        </div>
        <div v-if="!todos.length && !loading" class="empty-state"><ListFilter :size="25" /><strong>暂无项目任务</strong><span>可以新增第一个 ToDo</span></div>
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

    <div v-if="showIPMetadata" class="modal-backdrop" @click.self="showIPMetadata = false">
      <form class="modal ip-metadata-modal" @submit.prevent="saveIPMetadata">
        <div class="modal-head"><div><h2>远端 IP 归属</h2><p><code>{{ editingIP }}</code> · 全局应用到所有 Agent 观察</p></div><button type="button" class="icon-button" aria-label="关闭 IP 归属编辑" @click="showIPMetadata = false"><X :size="18" /></button></div>
        <p v-if="enrichmentLoading" class="profile-help">正在读取自动归属和手工覆盖…</p>
        <section v-if="enrichmentCache[editingIP]" class="automatic-metadata">
          <h3>自动识别</h3>
          <div class="metadata-grid"><span>DNS / PTR</span><strong>{{ enrichmentCache[editingIP].domain || enrichmentCache[editingIP].domain_candidates?.join('、') || '未知' }} <small>{{ enrichmentCache[editingIP].domain_source || enrichmentCache[editingIP].ptr_status || 'unknown' }}</small></strong><span>ASN / 登记组织</span><strong>{{ enrichmentCache[editingIP].automatic.asn || '未知' }} · {{ enrichmentCache[editingIP].automatic.organization || '未知' }}</strong><span>地理</span><strong>{{ [enrichmentCache[editingIP].automatic.country, enrichmentCache[editingIP].automatic.region, enrichmentCache[editingIP].automatic.city].filter(Boolean).join(' / ') || '未知' }}</strong><span>坐标 / MMDB</span><strong>{{ enrichmentCache[editingIP].automatic.coordinates_available ? `${enrichmentCache[editingIP].automatic.latitude}, ${enrichmentCache[editingIP].automatic.longitude}` : '未定位' }} · {{ enrichmentCache[editingIP].version || enrichmentCache[editingIP].reason || '未知' }}</strong><span v-if="enrichmentCache[editingIP].ptr_status">PTR 状态</span><strong v-if="enrichmentCache[editingIP].ptr_status">{{ enrichmentCache[editingIP].ptr_status }}<small>{{ enrichmentCache[editingIP].ptr_reason || '' }} · {{ formatTime(enrichmentCache[editingIP].ptr_updated_at) }}</small></strong></div>
          <small v-if="(enrichmentCache[editingIP].domain_candidates?.length ?? 0) > 1" class="form-help">DNS 同时观察到多个名称，保留歧义，不自动选定单一域名。</small>
        </section>
        <h3>手工覆盖（留空后保存可清除该字段）</h3>
        <label>域名<input v-model="ipMetadataForm.domain" maxlength="253" placeholder="DNS 或 PTR 无法确定时可填写" /></label>
        <div class="form-grid"><label>ASN<input v-model="ipMetadataForm.asn" type="number" min="1" max="4294967295" step="1" placeholder="例如 13335" /></label><label>ASN 登记组织<input v-model="ipMetadataForm.organization" maxlength="256" placeholder="例如 Cloudflare, Inc." /></label></div>
        <div class="form-grid"><label>国家/地区代码<input v-model="ipMetadataForm.country" maxlength="64" placeholder="例如 CN" /></label><label>行政区<input v-model="ipMetadataForm.region" maxlength="128" placeholder="选填" /></label></div>
        <div class="form-grid"><label>城市<input v-model="ipMetadataForm.city" maxlength="128" placeholder="选填" /></label><label class="checkbox-label"><input v-model="ipMetadataForm.trusted" type="checkbox" /> 标记为可信（仅标签与筛选）</label></div>
        <div class="form-grid"><label>地理纬度<input v-model="ipMetadataForm.latitude" type="number" min="-90" max="90" step="any" placeholder="自动坐标可由手工覆盖" /></label><label>地理经度<input v-model="ipMetadataForm.longitude" type="number" min="-180" max="180" step="any" placeholder="纬度和经度需一起填写" /></label></div>
        <p class="profile-help">ASN/地理自动值来自 Center 本地 MMDB；DNS 优先于缓存的 PTR。手工覆盖只影响显示和分组，不改写已保存流量。</p>
        <div class="modal-actions"><button type="button" class="subtle-button" @click="loadEnrichment(editingIP)">刷新自动识别</button><button type="button" class="subtle-button" @click="showIPMetadata = false">取消</button><button class="primary-button" :disabled="busy"><Check :size="15" />保存覆盖</button></div>
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

    <div v-if="showTodoForm" class="modal-backdrop" @click.self="showTodoForm = false">
      <form class="modal todo-modal" @submit.prevent="saveTodo">
        <div class="modal-head"><div><h2>{{ editingTodo ? '编辑 ToDo' : '新增 ToDo' }}</h2><p>状态和进度会立即同步到项目进度面板</p></div><button type="button" class="icon-button" aria-label="关闭" @click="showTodoForm = false"><X :size="18" /></button></div>
        <div class="form-grid"><label>阶段<input v-model="todoForm.phase" placeholder="例如 Linux 验收" /></label><label>优先级<select v-model="todoForm.priority"><option value="high">高</option><option value="medium">中</option><option value="low">低</option></select></label></div>
        <label>标题<input v-model="todoForm.title" required maxlength="200" placeholder="例如 在 OpenWrt 上验证 UDP 计数" /></label>
        <label>描述<textarea v-model="todoForm.description" rows="3" maxlength="4000" placeholder="验收目标或执行说明" /></label>
        <div class="form-grid"><label>状态<select v-model="todoForm.status"><option value="todo">待办</option><option value="in_progress">进行中</option><option value="done">已完成</option><option value="blocked">阻塞</option></select></label><label>完成度（0-100）<input v-model.number="todoForm.progress" type="number" min="0" max="100" /></label></div>
        <label>阻塞原因<input v-model="todoForm.blocker" maxlength="1000" placeholder="可选，例如等待测试设备" /></label>
        <div class="modal-actions"><button type="button" class="subtle-button" @click="showTodoForm = false">取消</button><button class="primary-button" :disabled="busy"><Check :size="15" />保存 ToDo</button></div>
      </form>
    </div>

    <dialog v-if="editingDevice" ref="deviceProfileDialog" class="native-modal profile-modal" aria-labelledby="device-profile-title" @click.self="closeDeviceProfileDialog" @close="onDeviceProfileDialogClose">
      <form @submit.prevent="saveDeviceProfile(editingDevice)">
        <div class="modal-head"><div><h2 id="device-profile-title">编辑设备资料</h2><p>{{ editingDevice.type }} Agent · 修改后会同步到对应的本机 LAN 资产</p></div><button type="button" class="icon-button" aria-label="关闭设备资料编辑" @click="closeDeviceProfileDialog"><X :size="18" /></button></div>
        <label>设备名称<input v-model="deviceProfileForm.name" maxlength="128" placeholder="留空以恢复注册时名称" autocomplete="off" /></label>
        <label>设备类别<select v-model="deviceProfileForm.device_category"><option value="unknown">未指定</option><option value="computer">计算机</option><option value="router">路由器</option><option value="gateway">网关</option><option value="laptop">笔记本</option><option value="tablet">平板</option><option value="phone">手机</option><option value="tv">电视</option><option value="server">服务器</option><option value="nas">NAS</option><option value="other">其他</option></select></label>
        <div class="form-grid"><label>Agent 纬度<input v-model="deviceProfileForm.latitude" type="number" min="-90" max="90" step="any" placeholder="例如 31.2304" /></label><label>Agent 经度<input v-model="deviceProfileForm.longitude" type="number" min="-180" max="180" step="any" placeholder="例如 121.4737" /></label></div>
        <p class="profile-help">地图起点使用手工坐标。清空纬度和经度后保存会移除坐标；缺少坐标时地球不绘制该 Agent 的路线。</p>
        <div class="modal-actions"><button type="button" class="subtle-button" @click="closeDeviceProfileDialog">取消</button><button class="primary-button" :disabled="busy"><Check :size="15" />保存资料</button></div>
      </form>
    </dialog>

    <div v-if="showEnrollmentForm" class="modal-backdrop" @click.self="closeEnrollment">
      <form v-if="!enrollment" class="modal" @submit.prevent="createEnrollment">
        <div class="modal-head"><div><h2>添加设备</h2><p>生成一次性 Token；注册成功后自动换发设备凭证</p></div><button type="button" class="icon-button" aria-label="关闭" @click="closeEnrollment"><X :size="18" /></button></div>
        <label>设备名称<input v-model="enrollmentForm.name" required placeholder="例如 nas-endpoint-01" /></label>
        <div class="form-grid"><label>类型<select v-model="enrollmentForm.observer_type"><option value="endpoint">Endpoint</option><option value="gateway">Gateway</option></select></label><label>Agent 版本<input v-model="enrollmentForm.version" required /></label></div>
        <label>Center gRPC 地址<input v-model="enrollmentForm.server" required placeholder="observer.example.lan:8444" /></label>
        <label>Center Web 地址（用于下载 TLS 证书）<input v-model="enrollmentForm.web_server" required placeholder="observer.example.lan:8443" /></label>
        <label v-if="enrollmentForm.observer_type === 'gateway' && !enrollmentForm.security_mode">Center HTTP 引导地址<input v-model="enrollmentForm.bootstrap_server" required placeholder="observer.example.lan:8080" /></label>
        <div class="form-grid"><label>Token 有效期（小时）<input v-model.number="enrollmentForm.expires_in_hours" type="number" min="1" max="720" /></label><label class="checkbox-label"><input v-model="enrollmentForm.security_mode" type="checkbox" /> 使用 TLS 加密</label></div>
        <div class="modal-actions"><button type="button" class="subtle-button" @click="closeEnrollment">取消</button><button class="primary-button" :disabled="busy"><LoaderCircle v-if="busy" class="spin" :size="15" />生成安装命令</button></div>
      </form>
      <div v-else class="modal enrollment-result">
        <div class="modal-head"><div><h2>安装命令已生成</h2><p>Token 只显示这一次；同一 Token 只能注册一台 Agent</p></div><button type="button" class="icon-button" aria-label="关闭" @click="closeEnrollment"><X :size="18" /></button></div>
        <div class="notice"><ShieldCheck :size="16" /><span>一次性 Token：<code>{{ enrollment.plaintext_token }}</code><br />TLS SHA-256 指纹：<code>{{ enrollment.tls_fingerprint }}</code><br />TLS 证书名称：<code>{{ enrollment.tls_server_name }}</code></span></div>
        <label v-if="enrollmentForm.observer_type === 'gateway'">OpenWrt Gateway Lite（ARM64/ARMv7，无 Docker）<button type="button" class="copy-command" aria-label="复制 OpenWrt Gateway Lite 安装命令" @click="copyEnrollmentCommand('openwrt-lite')"><code>{{ enrollmentCommand('openwrt-lite') }}</code><Copy :size="15" aria-hidden="true" /></button></label>
        <label>Linux 原生 systemd<button type="button" class="copy-command" aria-label="复制 Linux 原生安装命令" @click="copyEnrollmentCommand('native')"><code>{{ enrollmentCommand('native') }}</code><Copy :size="15" aria-hidden="true" /></button></label>
        <label>Docker Compose（可用于 light/pro Center）<button type="button" class="copy-command" aria-label="复制 Docker Compose 安装命令" @click="copyEnrollmentCommand('docker-compose')"><code>{{ enrollmentCommand('docker-compose') }}</code><Copy :size="15" aria-hidden="true" /></button></label>
        <label>docker run<button type="button" class="copy-command" aria-label="复制 docker run 安装命令" @click="copyEnrollmentCommand('docker-run')"><code>{{ enrollmentCommand('docker-run') }}</code><Copy :size="15" aria-hidden="true" /></button></label>
        <p v-if="enrollmentCopyStatus" class="form-success" role="status" aria-live="polite">{{ enrollmentCopyStatus }}</p>
        <div class="modal-actions"><button type="button" class="subtle-button danger-button" :disabled="busy" @click="revokeEnrollment">撤销 Token</button><button type="button" class="primary-button" @click="closeEnrollment">完成</button></div>
      </div>
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
        openLanIp: { type: Function as PropType<(ip: string) => void>, required: true },
        openIpMetadata: { type: Function as PropType<(ip: string) => void>, required: true },
        observedAt: { type: Function as PropType<(value: TableConnection) => string>, required: true },
        sourceName: { type: Function as PropType<(value: TableConnection) => string>, required: true },
        rateClass: { type: Function as PropType<(value: TableConnection) => string>, required: true },
        openOverride: { type: Function as PropType<(value: TableConnection) => void>, required: true },
        showActions: { type: Boolean, default: true },
      },
      setup(props) {
        const addressIP = (connection: TableConnection, local: boolean) => local
          ? connection.local_is_source ? connection.source_ip : connection.destination_ip
          : connection.local_is_source ? connection.destination_ip : connection.source_ip
        return () => h('div', { class: 'table-wrap' }, [
          props.items.length ? h('table', { class: 'data-table connection-table' }, [
            h('thead', [h('tr', ['设备', '协议 / 状态', '本地端点', '远端端点', '进程 / 容器 / 服务', '接收速率', '发送速率', '最近观测', ...(props.showActions ? [''] : [])].map((label) => h('th', label)))]),
            h('tbody', props.items.map((connection) => {
              const attribution = connection.attribution_source === 'unknown'
              return h('tr', { key: `${connection.device_id}:${connection.flow_id}` }, [
                h('td', { class: 'device-cell' }, [h('strong', props.deviceName(connection.device_id)), h('small', connection.interface || connection.observer_type)]),
                h('td', [h('span', { class: ['protocol-pill', connection.protocol] }, connection.protocol.toUpperCase()), h('small', { class: 'state-text' }, connection.state || 'ACTIVE')]),
                h('td', { class: 'endpoint-cell' }, [h('button', { class: 'endpoint-link', title: '在局域网资产中查看此 IP', onClick: () => props.openLanIp(addressIP(connection, true)) }, props.endpoint(connection, true)), h('small', connection.network_namespace || '主机网络')]),
                h('td', { class: 'endpoint-cell' }, [h('button', { class: 'endpoint-link', title: '查看远端 IP 域名、ASN、地理信息和可信状态', onClick: () => props.openIpMetadata(addressIP(connection, false)) }, props.endpoint(connection, false)), h('small', '远端 IP 归属 / 可信')]),
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
