<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { Box, Clock3, Cpu, HardDrive, Laptop, Minus, Monitor, Network, Plus, Router, Server, Smartphone, Tablet, Tv, ZoomIn, ZoomOut } from '@lucide/vue'

type Point = { x: number; y: number }
type TopologyEdge = { id: string; d: string; className: string; strokeWidth?: number; title?: string }
type LANDevice = { key: string; network_id?: string; name: string; device_category: string; category_source?: string; addresses: { ip: string }[]; mac: string; stale: boolean; ip_conflict: boolean }
type DockerNetwork = { id: string; name: string; subnet_cidrs: string[]; containers: { id: string; name: string; ipv4_address?: string }[] }
type Agent = { id: string; name: string; type: string; device_category: string; last_seen: string; interfaces: LANDevice[]; docker_networks: DockerNetwork[] }
type Infra = { id: string; name: string; category: string; x: number; y: number }
type Layout = { version: number; mode?: 'hierarchy' | 'nebula'; positions: Record<string, Point>; infrastructure: Infra[]; confirmed_links: { id: string; from: string; to: string }[] }
type Snapshot = { id: number; captured_at: string }
type Center = { id: string; name: string; known: boolean }
type Topology = { agents: Agent[]; groups: { id?: string; network_id: string; network_prefix: string; root_device_key?: string; root_agent_id?: string; center?: Center; agents: Agent[]; assets: LANDevice[] }[]; unassigned: { center?: Center; assets: LANDevice[] }; layout: Layout; settings: { snapshot_interval_minutes: number; retention_days: number }; snapshot?: Snapshot }

const props = defineProps<{ topology: Topology; snapshots: Snapshot[]; staleCount: number; loading?: boolean; loadError?: string }>()
const emit = defineEmits<{
  (event: 'open-agent', agent: Agent): void
  (event: 'open-asset', asset: LANDevice): void
  (event: 'select-snapshot', id: number | null): void
  (event: 'save-layout', layout: Layout): void
  (event: 'save-settings', settings: { snapshot_interval_minutes: number; retention_days: number }): void
}>()

const expanded = ref(new Set<string>())
const showStale = ref(false)
const scale = ref(1)
const offset = ref<Point>({ x: 0, y: 0 })
const mapElement = ref<HTMLElement | null>(null)
const mapPointers = new Map<number, Point>()
let pinchStart: { distance: number; scale: number; center: Point; world: Point } | null = null
const dragging = ref<{ id: string; pointerX: number; pointerY: number; startX: number; startY: number; moved: boolean } | null>(null)
const panning = ref<{ pointerX: number; pointerY: number; startX: number; startY: number } | null>(null)
const connectMode = ref(false)
const showDeviceLinks = ref(false)
const linkStart = ref('')
const infraDialog = ref(false)
const infraName = ref('')
const infraCategory = ref('router')
const interval = ref(5)
const retention = ref(7)
const settingsDirty = ref(false)
const positions = ref<Record<string, Point>>({})
const layoutMode = ref<'hierarchy' | 'nebula'>('hierarchy')
const topologyLayoutVersion = 3
// Match the signed coordinate range accepted by the layout API.
const layoutCoordinateLimit = 1_000_000
let fittedSnapshot: number | null | undefined
const infrastructure = ref<Infra[]>([])
const links = ref<Layout['confirmed_links']>([])
const isHistorical = computed(() => Boolean(props.topology.snapshot))

watch(() => [props.topology.layout, props.loading] as const, ([layout, loading]) => {
  infrastructure.value = [...(layout?.infrastructure ?? [])]
  links.value = [...(layout?.confirmed_links ?? [])]
  layoutMode.value = layout?.mode === 'nebula' ? 'nebula' : 'hierarchy'
  if ((layout?.version ?? 0) < topologyLayoutVersion) {
    // Discard obsolete auto-generated coordinates once; preserve manual infrastructure and confirmed links.
    positions.value = {}
    if (!loading && !isHistorical.value) {
      emit('save-layout', { version: topologyLayoutVersion, mode: layoutMode.value, positions: {}, infrastructure: infrastructure.value, confirmed_links: links.value })
    }
  } else {
    const savedPositions = layout?.positions ?? {}
    positions.value = layout?.mode === 'nebula'
      ? Object.fromEntries(Object.entries(savedPositions).map(([id, point]) => [id.startsWith('nebula:') ? id : ('nebula:' + id), point]))
      : { ...savedPositions }
  }
}, { deep: true })
watch(isHistorical, () => {
  connectMode.value = false
  linkStart.value = ''
})
watch(() => props.topology.settings, (settings) => {
  interval.value = settings?.snapshot_interval_minutes ?? 5
  retention.value = settings?.retention_days ?? 7
  settingsDirty.value = false
}, { immediate: true, deep: true })

function isExpired(lastSeen?: string): boolean {
  if (isHistorical.value || showStale.value || !lastSeen) return false
  const timestamp = Date.parse(lastSeen)
  return Number.isFinite(timestamp) && Date.now() - timestamp > 2 * 60 * 1000
}
function visibleAsset(asset: LANDevice): boolean {
  return isHistorical.value || showStale.value || !asset.stale
}
const agents = computed(() => (props.topology.agents ?? [])
  .filter((agent) => !isExpired(agent.last_seen))
  .map((agent) => ({
    ...agent,
    interfaces: (agent.interfaces ?? []).filter(visibleAsset),
  })))
const visibleNodeKey = computed(() => [...visibleNodes.value].sort().join('|'))
const groups = computed(() => {
  const visibleAgentIDs = new Set(agents.value.map((agent) => agent.id))
  return (props.topology.groups ?? [])
    .map((group) => ({
      ...group,
      agents: (group.agents ?? []).filter((agent) => visibleAgentIDs.has(agent.id)),
      assets: (group.assets ?? []).filter(visibleAsset),
    }))
    .filter((group) => group.agents.length > 0 || group.assets.length > 0 || group.center?.known)
})
const assets = computed(() => {
  const all = [...groups.value.flatMap((group) => group.assets ?? []), ...(props.topology.unassigned?.assets ?? [])]
  const byKey = new Map(all.map((asset) => [asset.key, asset]))
  return [...byKey.values()].filter(visibleAsset)
})
const center = computed(() => groups.value.find((group) => group.center?.known)?.center ?? props.topology.unassigned?.center)
const roots = computed(() => {
  const visibleAgents = new Set(agents.value.map((agent) => agent.id))
  const byGroup = new Map<string, string>()
  for (const group of groups.value) {
    if (group.root_agent_id && visibleAgents.has(group.root_agent_id)) {
      byGroup.set(groupKey(group), 'agent:' + group.root_agent_id)
    } else if (group.root_device_key && group.assets.some((asset) => asset.key === group.root_device_key)) {
      byGroup.set(groupKey(group), 'asset:' + group.root_device_key)
    }
  }
  return byGroup
})
const rootNodes = computed(() => [...new Set(roots.value.values())])
const regularNodes = computed(() => {
  const rootIDs = new Set(rootNodes.value)
  return [...agents.value.map((agent) => 'agent:' + agent.id), ...assets.value.map((asset) => 'asset:' + asset.key)]
    .filter((id) => !rootIDs.has(id))
})
const visibleNodes = computed(() => new Set([
  ...rootNodes.value, ...regularNodes.value,
  ...groups.value.map(networkNodeID), ...infrastructure.value.map((item) => 'infra:' + item.id),
  ...(center.value ? ['center'] : []),
]))
const visibleLinks = computed(() => links.value.flatMap((link) => {
  const from = resolveVisibleNode(link.from)
  const to = resolveVisibleNode(link.to)
  return from && to && from !== to ? [{ ...link, from, to }] : []
}))

function nodeHeight(id: string): number {
  if (!id.startsWith('agent:') || !expanded.value.has(id.slice('agent:'.length))) return 80
  const agent = agents.value.find((item) => item.id === id.slice('agent:'.length))
  if (!agent) return 80
  const interfacesHeight = (agent.interfaces?.length ?? 0) * 36
  const dockerHeight = (agent.docker_networks ?? []).reduce((height, network) => height + 58 + network.containers.length * 24, 0)
  return Math.max(520, 132 + interfacesHeight + dockerHeight)
}

function positionKey(id: string, mode = layoutMode.value): string {
  // Preserve legacy hierarchy keys while isolating manually saved nebula coordinates.
  return mode === 'nebula' ? `nebula:${id}` : id
}

const layoutClusters = computed<Array<{ group: Topology['groups'][number] | null; id: string; members: string[] }>>(() => {
  const assigned = new Set<string>()
  const clusters: Array<{ group: Topology['groups'][number] | null; id: string; members: string[] }> = groups.value.map((group) => {
    const members = [...group.agents.map((agent) => `agent:${agent.id}`), ...group.assets.map((asset) => `asset:${asset.key}`)]
      .filter((id, index, all) => all.indexOf(id) === index && !assigned.has(id) && visibleNodes.value.has(id))
    members.forEach((id) => assigned.add(id))
    return { group, id: networkNodeID(group), members }
  })
  const unassigned = [...agents.value.map((agent) => `agent:${agent.id}`), ...assets.value.map((asset) => `asset:${asset.key}`)]
    .filter((id, index, all) => all.indexOf(id) === index && !assigned.has(id) && visibleNodes.value.has(id))
  if (unassigned.length) clusters.push({ group: null, id: 'unassigned', members: unassigned })
  return clusters
})
const automaticPoints = computed(() => {
  const result: Record<string, Point> = {}
  if (layoutMode.value === 'nebula') {
    let centerX = 520
    const centerY = 430
    for (const cluster of layoutClusters.value) {
      const radius = Math.max(330, cluster.members.length * 55)
      if (cluster.group) result[cluster.id] = { x: centerX, y: centerY }
      cluster.members.forEach((id, index) => {
        const angle = -Math.PI / 2 + (Math.PI * 2 * index) / Math.max(1, cluster.members.length)
        result[id] = { x: centerX + Math.cos(angle) * radius, y: centerY + Math.sin(angle) * radius }
      })
      centerX += radius * 2 + 420
    }
    if (center.value) result.center = { x: 48, y: centerY }
    infrastructure.value.forEach((item) => { result[`infra:${item.id}`] = positions.value[positionKey(`infra:${item.id}`)] ?? { x: 48, y: 100 + itemIndex(item) * 120 } })
    return result
  }
  if (layoutMode.value === 'hierarchy') {
    let cursorX = 48
    const networkXs: number[] = []
    for (const cluster of layoutClusters.value) {
      const blockWidth = Math.max(340, cluster.members.length * 340)
      if (cluster.group) {
        const networkX = cursorX + (blockWidth - 290) / 2
        result[cluster.id] = { x: networkX, y: 280 }
        networkXs.push(networkX)
      }
      cluster.members.forEach((id, index) => { result[id] = { x: cursorX + index * 340, y: 560 } })
      cursorX += blockWidth + 90
    }
    const networkMinX = Math.min(...networkXs)
    const networkMaxX = Math.max(...networkXs)
    result.center = { x: networkXs.length ? (networkMinX + networkMaxX) / 2 : 48, y: 48 }
    infrastructure.value.forEach((item) => {
      result[`infra:${item.id}`] = positions.value[positionKey(`infra:${item.id}`)] ?? { x: cursorX + itemIndex(item) * 340, y: 560 }
    })
    return result
  }
  const networkX = 430
  const memberX = 920
  const memberColumns = 3
  const columnGap = 360
  const rowGap = 42
  const clusterGap = 90
  let cursorY = 150
  const networkYs: number[] = []
  for (const cluster of layoutClusters.value) {
    const rows: Array<{ ids: string[]; height: number }> = []
    for (let index = 0; index < cluster.members.length; index += memberColumns) {
      const ids = cluster.members.slice(index, index + memberColumns)
      rows.push({ ids, height: Math.max(80, ...ids.map(nodeHeight)) })
    }
    const clusterHeight = rows.reduce((height, row) => height + row.height, 0) + Math.max(0, rows.length - 1) * rowGap
    let rowY = cursorY
    for (const row of rows) {
      row.ids.forEach((id, column) => { result[id] = { x: memberX + column * columnGap, y: rowY } })
      rowY += row.height + rowGap
    }
    const networkY = rows.length ? cursorY + clusterHeight / 2 : cursorY + 40
    if (cluster.group) {
      result[cluster.id] = { x: networkX, y: networkY - 30 }
      networkYs.push(networkY)
    }
    cursorY += clusterHeight + clusterGap
  }
  const centerY = networkYs.length ? networkYs.reduce((sum, value) => sum + value, 0) / networkYs.length : 190
  result.center = { x: 48, y: centerY - 40 }
  infrastructure.value.forEach((item) => { result[`infra:${item.id}`] = positions.value[positionKey(`infra:${item.id}`)] ?? { x: 48, y: 80 + itemIndex(item) * 120 } })
  return result
})
const topologyEdges = computed<TopologyEdge[]>(() => {
  const edges: TopologyEdge[] = []
  const networks = layoutClusters.value.filter((cluster) => cluster.group)
  if (showDeviceLinks.value && center.value && networks.some((cluster) => cluster.group?.center?.known)) {
    const from = nodePoint('center')
    const lane = 370
    const centers = networks.filter((cluster) => cluster.group?.center?.known).map((cluster) => {
      const point = nodePoint(cluster.id)
      return { x: point.x, y: point.y + 30 }
    })
    const minY = Math.min(...centers.map((point) => point.y))
    const maxY = Math.max(...centers.map((point) => point.y))
    const branches = centers.map((point) => `M ${lane} ${point.y} H ${point.x}`).join(' ')
    edges.push({ id: 'center:network-bus', className: 'observed-link center-link', d: `M ${from.x + 290} ${from.y + 40} H ${lane} M ${lane} ${minY} V ${maxY} ${branches}` })
  }
  if (showDeviceLinks.value || layoutMode.value === 'nebula') {
    for (const cluster of networks) {
      if (!cluster.group || !cluster.members.length) continue
      const network = nodePoint(cluster.id)
      const points = cluster.members.map((id) => nodePoint(id))
      const branchX = 790
      const memberCenters = points.map((point) => point.y + 40)
      const minY = Math.min(network.y + 30, ...memberCenters)
      const maxY = Math.max(network.y + 30, ...memberCenters)
      const branches = points.map((point) => `M ${branchX} ${point.y + 40} H ${point.x}`).join(' ')
      edges.push({ id: `members:${cluster.id}`, className: 'observed-link member-bus', d: `M ${network.x + 290} ${network.y + 30} H ${branchX} M ${branchX} ${minY} V ${maxY} ${branches}` })
    }
  }
  visibleLinks.value.forEach((link) => {
    const fromPoint = nodePoint(link.from)
    const toPoint = nodePoint(link.to)
    const fromRight = fromPoint.x <= toPoint.x
    const from = { x: fromPoint.x + (fromRight ? 290 : 0), y: fromPoint.y + 40 }
    const to = { x: toPoint.x + (fromRight ? 0 : 290), y: toPoint.y + 40 }
    const curve = Math.max(64, Math.abs(to.x - from.x) * 0.45)
    edges.push({ id: link.id, className: 'confirmed-link', d: `M ${from.x} ${from.y} C ${from.x + (fromRight ? curve : -curve)} ${from.y}, ${to.x - (fromRight ? curve : -curve)} ${to.y}, ${to.x} ${to.y}` })
  })
  return edges
})
const canvasBounds = computed(() => {
  const nodeIDs = [...visibleNodes.value]
  const points = nodeIDs.map(nodePoint)
  const padding = 96
  let minX = Math.min(0, ...points.map((point) => point.x)) - padding
  let minY = Math.min(0, ...points.map((point) => point.y)) - padding
  let maxX = Math.max(1400, ...points.map((point) => point.x + 310)) + padding
  let maxY = Math.max(900, ...nodeIDs.map((id) => nodePoint(id).y + nodeHeight(id))) + padding
  if (layoutMode.value === 'hierarchy' && showDeviceLinks.value) {
    minX = Math.min(minX, 370)
    maxX = Math.max(maxX, 790)
  }
  return { x: minX, y: minY, width: maxX - minX, height: maxY - minY }
})
const worldTransform = computed(() => `translate(${offset.value.x + canvasBounds.value.x * scale.value}px, ${offset.value.y + canvasBounds.value.y * scale.value}px) scale(${scale.value})`)
const mapStyle = computed(() => ({
  backgroundPosition: `${offset.value.x}px ${offset.value.y}px`,
  backgroundSize: `${22 * scale.value}px ${22 * scale.value}px`,
}))
function nodeStyle(id: string): Record<string, string> {
  const point = nodePoint(id)
  return { left: `${point.x - canvasBounds.value.x}px`, top: `${point.y - canvasBounds.value.y}px` }
}

function itemIndex(item: Infra): number { return infrastructure.value.findIndex((candidate) => candidate.id === item.id) }
function groupKey(group: Topology['groups'][number]): string { return group.id || group.network_id }
function networkNodeID(group: Topology['groups'][number]): string { return 'network:' + groupKey(group) }
function networkPoint(group: Topology['groups'][number], _index: number): Point { return positions.value[positionKey(networkNodeID(group))] ?? automaticPoints.value[networkNodeID(group)] ?? { x: 430, y: 220 } }
function resolveVisibleNode(id: string): string | undefined {
  if (visibleNodes.value.has(id)) return id
  if (!id.startsWith('network:')) return
  const matches = props.topology.groups.filter((group) => {
    const legacyID = 'network:' + group.network_id
    return legacyID === id || (group.root_device_key && legacyID + '|root|' + group.root_device_key === id)
  })
  if (matches.length === 1) {
    const resolved = networkNodeID(matches[0])
    if (visibleNodes.value.has(resolved)) return resolved
  }
}
function rootNode(group: Topology['groups'][number]): string | undefined { return roots.value.get(groupKey(group)) }
function deviceIcon(category: string, fallback: typeof Cpu) {
  const icons: Record<string, typeof Cpu> = {
    router: Router, gateway: Router, switch: Network, nas: HardDrive, server: Server,
    computer: Monitor, laptop: Laptop, tablet: Tablet, phone: Smartphone, tv: Tv,
  }
  return icons[category] ?? fallback
}
function categoryLabel(category: string): string {
  return ({ router: '路由器', gateway: '网关', switch: '交换机', nas: 'NAS', server: '服务器', computer: '计算机', laptop: '笔记本', tablet: '平板', phone: '手机', tv: '电视', other: '其他', unknown: '未指定' } as Record<string, string>)[category] || '未指定'
}
function categorySourceLabel(source?: string): string {
  return source === 'manual' ? '手动标注' : source === 'detected' ? 'Agent 自动识别' : source === 'agent' ? 'Agent 类别' : ''
}
function rootLabel(group: Topology['groups'][number]): string {
  const node = rootNode(group)
  const agent = group.agents.find((item) => 'agent:' + item.id === node)
  if (agent) return '根设备：' + agent.name
  const asset = group.assets.find((item) => 'asset:' + item.key === node)
  return asset ? '根设备：' + asset.name : '根路由器待确认'
}
function nodePoint(id: string): Point {
  return positions.value[positionKey(id)] ?? automaticPoints.value[id] ?? { x: 48, y: 40 }
}
function toggleExpanded(id: string): void {
  const next = new Set(expanded.value)
  next.has(id) ? next.delete(id) : next.add(id)
  expanded.value = next
}
function startNodeDrag(event: PointerEvent, id: string): void {
  if (event.button !== 0 || connectMode.value || isHistorical.value) return
  const element = event.currentTarget as HTMLElement
  const interactive = (event.target as HTMLElement).closest('button, input, select')
  if (interactive && interactive !== element) return
  const current = nodePoint(id)
  dragging.value = { id, pointerX: event.clientX, pointerY: event.clientY, startX: current.x, startY: current.y, moved: false }
  element.setPointerCapture(event.pointerId)
}
function moveNode(event: PointerEvent): void {
  if (!dragging.value) return
  const drag = dragging.value
  const deltaX = event.clientX - drag.pointerX
  const deltaY = event.clientY - drag.pointerY
  if (!drag.moved && Math.hypot(deltaX, deltaY) < 4) return
  drag.moved = true
  const x = drag.startX + deltaX / scale.value
  const y = drag.startY + deltaY / scale.value
  positions.value = {
    ...positions.value,
    [positionKey(drag.id)]: {
      x: Math.max(-layoutCoordinateLimit, Math.min(layoutCoordinateLimit, x)),
      y: Math.max(-layoutCoordinateLimit, Math.min(layoutCoordinateLimit, y)),
    },
  }
}
function endNodeDrag(): void {
  if (!dragging.value) return
  const moved = dragging.value.moved
  dragging.value = null
  if (moved) persistLayout()
}
function startPan(event: PointerEvent): void {
  if (event.pointerType === 'mouse' && (event.button !== 0 || (event.target as HTMLElement).closest('.map-node, button, input, select'))) return
  mapElement.value?.setPointerCapture(event.pointerId)
  mapPointers.set(event.pointerId, { x: event.clientX, y: event.clientY })
  if (mapPointers.size === 2) {
    const [a, b] = [...mapPointers.values()]
    const rect = mapElement.value?.getBoundingClientRect()
    if (!rect) return
    const center = { x: (a.x + b.x) / 2 - rect.left, y: (a.y + b.y) / 2 - rect.top }
    pinchStart = { distance: Math.hypot(a.x - b.x, a.y - b.y), scale: scale.value, center, world: { x: (center.x - offset.value.x) / scale.value, y: (center.y - offset.value.y) / scale.value } }
    panning.value = null
    return
  }
  panning.value = { pointerX: event.clientX, pointerY: event.clientY, startX: offset.value.x, startY: offset.value.y }
}
function movePan(event: PointerEvent): void {
  if (mapPointers.has(event.pointerId)) mapPointers.set(event.pointerId, { x: event.clientX, y: event.clientY })
  if (pinchStart && mapPointers.size >= 2) {
    const [a, b] = [...mapPointers.values()]
    const distance = Math.hypot(a.x - b.x, a.y - b.y)
    const nextScale = Math.min(2, Math.max(0.15, pinchStart.scale * distance / Math.max(1, pinchStart.distance)))
    scale.value = nextScale
    offset.value = { x: pinchStart.center.x - pinchStart.world.x * nextScale, y: pinchStart.center.y - pinchStart.world.y * nextScale }
    return
  }
  if (!panning.value) return
  offset.value = { x: panning.value.startX + event.clientX - panning.value.pointerX, y: panning.value.startY + event.clientY - panning.value.pointerY }
}
function endPan(event?: PointerEvent): void { if (event) mapPointers.delete(event.pointerId); pinchStart = null; panning.value = null }
function zoomTo(nextScale: number, point?: Point): void {
  const rect = mapElement.value?.getBoundingClientRect()
  if (!rect) return
  const anchor = point ?? { x: rect.width / 2, y: rect.height / 2 }
  const world = { x: (anchor.x - offset.value.x) / scale.value, y: (anchor.y - offset.value.y) / scale.value }
  scale.value = Math.min(2, Math.max(0.15, nextScale))
  offset.value = { x: anchor.x - world.x * scale.value, y: anchor.y - world.y * scale.value }
}
function zoom(delta: number): void { zoomTo(scale.value + delta) }
function onMapWheel(event: WheelEvent): void {
  const rect = mapElement.value?.getBoundingClientRect()
  if (rect) zoomTo(scale.value * Math.exp(-event.deltaY * 0.0015), { x: event.clientX - rect.left, y: event.clientY - rect.top })
}
function fitView(): void {
  const rect = mapElement.value?.getBoundingClientRect()
  if (!rect) return
  const points = [...visibleNodes.value].map(nodePoint)
  if (!points.length) { scale.value = 1; offset.value = { x: 0, y: 0 }; return }
  const minX = Math.min(...points.map((point) => point.x)), minY = Math.min(...points.map((point) => point.y))
  const maxX = Math.max(...points.map((point) => point.x + 310)), maxY = Math.max(...[...visibleNodes.value].map((id) => nodePoint(id).y + nodeHeight(id)))
  const nextScale = Math.min(1.25, Math.max(0.15, Math.min((rect.width - 96) / (maxX - minX), (rect.height - 96) / (maxY - minY))))
  scale.value = nextScale
  offset.value = { x: (rect.width - (maxX - minX) * nextScale) / 2 - minX * nextScale, y: (rect.height - (maxY - minY) * nextScale) / 2 - minY * nextScale }
}
async function setLayoutMode(mode: 'hierarchy' | 'nebula'): Promise<void> {
  if (layoutMode.value === mode) return
  layoutMode.value = mode
  connectMode.value = false
  linkStart.value = ''
  persistLayout()
  await nextTick()
  fitView()
}
function toggleLink(id: string): void {
  if (!connectMode.value || isHistorical.value) return
  if (!linkStart.value) { linkStart.value = id; return }
  if (linkStart.value !== id) links.value = [...links.value, { id: crypto.randomUUID(), from: linkStart.value, to: id }]
  linkStart.value = ''
  persistLayout()
}
function persistLayout(): void {
  if (isHistorical.value) return
  emit('save-layout', { version: topologyLayoutVersion, mode: layoutMode.value, positions: positions.value, infrastructure: infrastructure.value, confirmed_links: links.value })
}
function addInfrastructure(): void {
  if (isHistorical.value) return
  const name = infraName.value.trim()
  if (!name) return
  const id = crypto.randomUUID()
  const item = { id, name, category: infraCategory.value, x: 1500 + (infrastructure.value.length % 3) * 300, y: 90 + Math.floor(infrastructure.value.length / 3) * 180 }
  infrastructure.value = [...infrastructure.value, item]
  positions.value = { ...positions.value, [positionKey(`infra:${id}`)]: { x: item.x, y: item.y } }
  persistLayout()
  infraName.value = ''
  infraDialog.value = false
}
function removeInfrastructure(id: string): void {
  if (isHistorical.value) return
  infrastructure.value = infrastructure.value.filter((item) => item.id !== id)
  const nextPositions = { ...positions.value }
  delete nextPositions[`infra:${id}`]
  delete nextPositions[`nebula:infra:${id}`]
  positions.value = nextPositions
  links.value = links.value.filter((link) => link.from !== `infra:${id}` && link.to !== `infra:${id}`)
  persistLayout()
}
function removeLink(id: string): void {
  if (isHistorical.value) return
  links.value = links.value.filter((link) => link.id !== id)
  persistLayout()
}
watch(
  () => [mapElement.value, props.loading, visibleNodeKey.value, props.topology.snapshot?.id ?? null] as const,
  ([element, loading, nodeKey, snapshot]) => {
    if (!element || loading || !nodeKey || snapshot === fittedSnapshot) return
    fittedSnapshot = snapshot
    fitView()
  },
  { flush: 'post', immediate: true },
)
onBeforeUnmount(() => { dragging.value = null; panning.value = null; mapPointers.clear() })
</script>

<template>
  <div class="topology-toolbar">
    <div class="topology-tool-group">
      <div class="topology-layout-toggle" role="group" aria-label="局域网视图">
        <button type="button" :aria-pressed="layoutMode === 'hierarchy'" title="同一层级对齐排列" @click="setLayoutMode('hierarchy')">LAN 拓扑</button>
        <button type="button" :aria-pressed="layoutMode === 'nebula'" title="按局域网网段环绕排列资产；连线只表示网段观察分组" @click="setLayoutMode('nebula')">星云布局</button>
      </div>
      <button class="icon-button" type="button" aria-label="缩小拓扑" title="缩小" @click="zoom(-0.15)"><ZoomOut :size="15" /></button>
      <span class="zoom-value">{{ Math.round(scale * 100) }}%</span>
      <input class="topology-zoom-slider" type="range" min="0.15" max="2" step="0.05" :value="scale" aria-label="画布缩放" @input="zoomTo(Number(($event.target as HTMLInputElement).value))" />
      <button class="icon-button" type="button" aria-label="放大拓扑" title="放大" @click="zoom(0.15)"><ZoomIn :size="15" /></button><button class="subtle-button" type="button" @click="fitView">适应画布</button>
      <button v-if="layoutMode === 'hierarchy'" class="subtle-button" type="button" :aria-pressed="showDeviceLinks" @click="showDeviceLinks = !showDeviceLinks">{{ showDeviceLinks ? '隐藏网段观察线' : '显示网段观察线' }}</button>
      <template>
        <button v-if="!isHistorical" class="subtle-button" type="button" :aria-pressed="connectMode" @click="connectMode = !connectMode; linkStart = ''"><Network :size="14" />{{ connectMode ? '取消连线' : '标记物理连接' }}</button>
        <button v-if="!isHistorical" class="subtle-button" type="button" @click="infraDialog = true"><Plus :size="14" />添加路由器 / 交换机</button>
        <label v-if="!isHistorical" class="stale-toggle"><input v-model="showStale" type="checkbox" />显示过期设备 <span v-if="staleCount">{{ staleCount }}</span></label>
      </template>
    </div>
    <label class="history-select"><Clock3 :size="14" /><select aria-label="拓扑历史快照" :value="topology.snapshot?.id ?? ''" @change="emit('select-snapshot', ($event.target as HTMLSelectElement).value ? Number(($event.target as HTMLSelectElement).value) : null)"><option value="">当前拓扑</option><option v-for="snapshot in snapshots" :key="snapshot.id" :value="snapshot.id">{{ new Date(snapshot.captured_at).toLocaleString() }}</option></select></label>
  </div>
  <div class="topology-history-meta" v-if="topology.snapshot">历史快照 · {{ new Date(topology.snapshot.captured_at).toLocaleString() }}</div>
  <div v-else class="topology-history-meta">每 {{ topology.settings.snapshot_interval_minutes }} 分钟保存一次 · 保留 {{ topology.settings.retention_days }} 天</div>
  <div class="topology-legend"><span><i class="legend-line observed"></i>虚线：Center / 网段观察分组关系，不代表物理线缆</span><span><i class="legend-line confirmed"></i>实线：人工确认的物理连接</span><span>Docker 子网只挂在所属 Agent 下，不作为独立 LAN 设备</span><span><i class="legend-conflict"></i>红色标记：同一 Agent 网卡近期同时看到同 IP 对应多个 MAC（疑似冲突）</span></div>
  <div ref="mapElement" class="topology-map" :style="mapStyle" :class="{ 'connect-mode': connectMode }" @pointerdown="startPan" @pointermove="movePan" @pointerup="endPan($event)" @pointercancel="endPan($event)" @wheel.prevent="onMapWheel">
    <div class="topology-map-world" :style="{ width: `${canvasBounds.width}px`, height: `${canvasBounds.height}px`, transform: worldTransform }">
      <svg class="topology-map-links" :width="canvasBounds.width" :height="canvasBounds.height" :viewBox="`${canvasBounds.x} ${canvasBounds.y} ${canvasBounds.width} ${canvasBounds.height}`" aria-hidden="true">
        <path v-for="edge in topologyEdges" :key="edge.id" :class="edge.className" :d="edge.d" :style="edge.strokeWidth ? { strokeWidth: `${edge.strokeWidth}px` } : undefined"><title>{{ edge.title || (edge.className === 'confirmed-link' ? '人工确认的物理连接' : '网段观察分组关系') }}</title></path>
      </svg>
      <template>
      <article v-if="center" class="map-node center-map-node" :class="{ selected: linkStart === 'center' }" :style="nodeStyle('center')" @pointerdown.stop="startNodeDrag($event, 'center')" @pointermove="moveNode" @pointerup="endNodeDrag" @pointercancel="endNodeDrag" @click="toggleLink('center')"><span class="map-node-icon center-icon"><Cpu :size="16" /></span><span class="map-node-title"><strong>{{ center.name }}</strong><small>{{ center.known ? '当前 Center · 已匹配网段' : '当前 Center · 网段未知' }}</small></span></article>
      <button v-for="group in groups" :key="networkNodeID(group)" class="map-node network-map-node" :class="{ selected: linkStart === networkNodeID(group) }" :style="nodeStyle(networkNodeID(group))" @pointerdown.stop="startNodeDrag($event, networkNodeID(group))" @pointermove="moveNode" @pointerup="endNodeDrag" @pointercancel="endNodeDrag" @click="toggleLink(networkNodeID(group))"><span class="map-node-icon network-icon"><Network :size="16" /></span><span class="map-node-title"><strong>{{ group.network_prefix }}</strong><small>{{ group.agents.length }} 个 Agent · {{ group.assets.length }} 台观察设备 · {{ rootLabel(group) }}</small></span></button>
      <article v-for="agent in agents" :key="agent.id" class="map-node agent-map-node" :class="{ expanded: expanded.has(agent.id), 'root-map-node': rootNodes.includes(`agent:${agent.id}`), selected: linkStart === `agent:${agent.id}` }" :style="nodeStyle(`agent:${agent.id}`)" @pointerdown.stop="startNodeDrag($event, `agent:${agent.id}`)" @pointermove="moveNode" @pointerup="endNodeDrag" @pointercancel="endNodeDrag" @click="toggleLink(`agent:${agent.id}`)">
        <header><button class="expand-node" :aria-label="`${expanded.has(agent.id) ? '收起' : '展开'} ${agent.name}`" @click.stop="toggleExpanded(agent.id)"><Minus v-if="expanded.has(agent.id)" :size="14" /><Plus v-else :size="14" /></button><span class="map-node-icon"><component :is="deviceIcon(agent.device_category, Cpu)" :size="16" /></span><button class="map-node-title" @click.stop="connectMode ? toggleLink(`agent:${agent.id}`) : emit('open-agent', agent)"><strong>{{ agent.name }}</strong><small>{{ agent.type }} · {{ categoryLabel(agent.device_category) }}{{ agent.device_category && agent.device_category !== 'unknown' ? ' · 人工标注' : ' · 待标注' }}</small></button></header>
        <div class="map-node-counts"><span>{{ agent.interfaces?.length ?? 0 }} 个网卡</span><span>{{ agent.docker_networks?.length ?? 0 }} 个 Docker 网络</span></div>
        <div v-if="expanded.has(agent.id)" class="map-node-children">
          <button v-for="iface in agent.interfaces" :key="iface.key" class="map-child" @click.stop="emit('open-asset', iface)"><Network :size="13" /><span><strong>{{ iface.addresses[0]?.ip ?? iface.mac ?? iface.name }}</strong><small>{{ iface.network_id }}</small></span></button>
          <div v-for="network in agent.docker_networks" :key="network.id" class="docker-child"><div><Box :size="13" /><span><strong>{{ network.name }}</strong><small>{{ network.subnet_cidrs.join(', ') }}</small><small class="docker-scope-note">仅归属此 Agent · 不作为共享 LAN</small></span></div><small v-for="container in network.containers" :key="container.id" class="container-line">{{ container.name }} · {{ container.ipv4_address || 'IP 未知' }}</small></div>
        </div>
      </article>
      <button v-for="asset in assets" :key="asset.key" class="map-node asset-map-node" :class="{ stale: asset.stale, 'root-map-node': rootNodes.includes(`asset:${asset.key}`), conflict: asset.ip_conflict, selected: linkStart === `asset:${asset.key}` }" :style="nodeStyle(`asset:${asset.key}`)" @pointerdown.stop="startNodeDrag($event, `asset:${asset.key}`)" @pointermove="moveNode" @pointerup="endNodeDrag" @pointercancel="endNodeDrag" @click="connectMode ? toggleLink(`asset:${asset.key}`) : emit('open-asset', asset)"><span class="map-node-icon asset-icon"><component :is="deviceIcon(asset.device_category, Server)" :size="16" /></span><span class="map-node-title"><strong>{{ asset.name }}</strong><small>{{ asset.addresses[0]?.ip || asset.mac || '地址未知' }}</small><small v-if="asset.category_source">{{ categorySourceLabel(asset.category_source) }} · {{ categoryLabel(asset.device_category) }}</small></span><span v-if="asset.ip_conflict" class="map-status">疑似冲突</span></button>
      <article v-for="item in infrastructure" :key="item.id" class="map-node infra-map-node" :class="{ selected: linkStart === `infra:${item.id}` }" :style="nodeStyle(`infra:${item.id}`)" @pointerdown.stop="startNodeDrag($event, `infra:${item.id}`)" @pointermove="moveNode" @pointerup="endNodeDrag" @pointercancel="endNodeDrag" @click="toggleLink(`infra:${item.id}`)"><span class="map-node-icon infra-icon"><component :is="deviceIcon(item.category, Server)" :size="16" /></span><span class="map-node-title"><strong>{{ item.name }}</strong><small>{{ item.category === 'router' ? '路由器' : '交换机' }} · 手动标注</small></span><button class="remove-link" aria-label="删除设备" :disabled="isHistorical" @click.stop="removeInfrastructure(item.id)">×</button></article>
      <button v-if="agents.length === 0 && assets.length === 0" class="map-empty" @click.stop>{{ props.loadError ? '拓扑加载失败，暂无可用数据' : props.loading ? '正在加载局域网拓扑…' : '等待 Agent 上报局域网快照' }}</button>
      </template>
    </div>
  </div>
  <div class="topology-settings">
    <template v-if="!isHistorical">
      <label>快照间隔 <input v-model.number="interval" type="number" min="1" max="1440" @input="settingsDirty = true" /> 分钟</label>
      <label>保留历史 <input v-model.number="retention" type="number" min="1" max="365" @input="settingsDirty = true" /> 天</label>
      <button class="subtle-button" :disabled="!settingsDirty" @click="emit('save-settings', { snapshot_interval_minutes: interval, retention_days: retention })">保存设置</button>
      <button v-for="link in links" :key="`remove-${link.id}`" class="remove-link-row" @click="removeLink(link.id)">移除连线 {{ link.id.slice(0, 6) }}</button>
    </template>
  </div>
  <dialog :open="infraDialog" class="infra-dialog" @click.self="infraDialog = false"><form @submit.prevent="addInfrastructure"><h3>添加基础设施</h3><label>名称<input v-model="infraName" maxlength="128" required autofocus /></label><label>类别<select v-model="infraCategory"><option value="router">路由器 / 网关</option><option value="switch">交换机</option></select></label><div><button type="button" class="subtle-button" @click="infraDialog = false">取消</button><button class="primary-button">添加</button></div></form></dialog>
</template>
