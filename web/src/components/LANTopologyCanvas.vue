<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { Box, Clock3, Cpu, HardDrive, Laptop, Minus, Monitor, Network, Plus, Router, Server, Smartphone, Tablet, Tv, ZoomIn, ZoomOut } from '@lucide/vue'

type Point = { x: number; y: number }
type LANDevice = { key: string; network_id?: string; name: string; device_category: string; category_source?: string; addresses: { ip: string }[]; mac: string; stale: boolean; ip_conflict: boolean }
type DockerNetwork = { id: string; name: string; subnet_cidrs: string[]; containers: { id: string; name: string; ipv4_address?: string }[] }
type Agent = { id: string; name: string; type: string; device_category: string; last_seen: string; interfaces: LANDevice[]; docker_networks: DockerNetwork[] }
type Infra = { id: string; name: string; category: string; x: number; y: number }
type Layout = { positions: Record<string, Point>; infrastructure: Infra[]; confirmed_links: { id: string; from: string; to: string }[] }
type Snapshot = { id: number; captured_at: string }
type Center = { id: string; name: string; known: boolean }
type Topology = { agents: Agent[]; groups: { id?: string; network_id: string; network_prefix: string; root_device_key?: string; root_agent_id?: string; center?: Center; agents: Agent[]; assets: LANDevice[] }[]; unassigned: { center?: Center; assets: LANDevice[] }; layout: Layout; settings: { snapshot_interval_minutes: number; retention_days: number }; snapshot?: Snapshot }

const props = defineProps<{ topology: Topology; snapshots: Snapshot[]; staleCount: number; loading?: boolean }>()
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
const linkStart = ref('')
const infraDialog = ref(false)
const infraName = ref('')
const infraCategory = ref('router')
const interval = ref(5)
const retention = ref(7)
const settingsDirty = ref(false)
const positions = ref<Record<string, Point>>({})
const infrastructure = ref<Infra[]>([])
const links = ref<Layout['confirmed_links']>([])
const isHistorical = computed(() => Boolean(props.topology.snapshot))

watch(() => props.topology.layout, (layout) => {
  positions.value = { ...(layout?.positions ?? {}) }
  infrastructure.value = [...(layout?.infrastructure ?? [])]
  links.value = [...(layout?.confirmed_links ?? [])]
}, { immediate: true, deep: true })
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
const canvasSize = computed(() => {
  const points = [
    ...Array.from(visibleNodes.value, nodePoint),
  ]
  return { width: Math.max(1400, ...points.map((item) => item.x + 330)), height: Math.max(900, ...points.map((item) => item.y + (expanded.value.size ? 560 : 300))) }
})

function gridPoint(index: number, top: number): Point {
  return { x: 100 + (index % 4) * 350, y: top + Math.floor(index / 4) * 180 }
}
function groupKey(group: Topology['groups'][number]): string { return group.id || group.network_id }
function networkNodeID(group: Topology['groups'][number]): string { return 'network:' + groupKey(group) }
function networkTop(): number { return 270 + Math.max(0, Math.ceil(rootNodes.value.length / 4) - 1) * 180 }
function networkRowHeight(row: number): number {
  const spacing = expanded.value.size ? 360 : 135
  const count = Math.max(0, ...groups.value.slice(row * 2, row * 2 + 2).map((group) => group.agents.length + group.assets.length))
  return Math.max(330, 145 + Math.ceil(count / 3) * spacing + 100)
}
function networkPoint(group: Topology['groups'][number], index: number): Point {
  const legacyPosition = resolveVisibleNode('network:' + group.network_id) === networkNodeID(group)
    ? positions.value['network:' + group.network_id]
    : undefined
  const legacyRoot = group.root_device_key ? 'network:' + group.network_id + '|root|' + group.root_device_key : undefined
  const legacyRootPosition = legacyRoot && resolveVisibleNode(legacyRoot) === networkNodeID(group)
    ? positions.value[legacyRoot]
    : undefined
  const row = Math.floor(index / 2)
  const rowOffset = Array.from({ length: row }, (_, previous) => networkRowHeight(previous)).reduce((sum, height) => sum + height, 0)
  const automatic = { x: 250 + (index % 2) * 1250, y: networkTop() + rowOffset }
  return positions.value[networkNodeID(group)] ?? legacyPosition ?? legacyRootPosition ?? automatic
}
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
  if (positions.value[id]) return positions.value[id]
  if (id === 'center') return { x: 100, y: 18 }
  const rootIndex = rootNodes.value.indexOf(id)
  if (rootIndex !== -1) return gridPoint(rootIndex, 120)
  const groupIndex = groups.value.findIndex((group) => networkNodeID(group) === id)
  if (groupIndex !== -1) return networkPoint(groups.value[groupIndex], groupIndex)
  for (const [index, group] of groups.value.entries()) {
    const members = [...group.agents.map((agent) => `agent:${agent.id}`), ...group.assets.map((asset) => `asset:${asset.key}`)]
      .filter((member) => member !== rootNode(group))
    const memberIndex = members.indexOf(id)
    if (memberIndex !== -1) {
      const subnet = networkPoint(group, index)
      return { x: subnet.x - 170 + (memberIndex % 3) * 340, y: subnet.y + 145 + Math.floor(memberIndex / 3) * (expanded.value.size ? 360 : 135) }
    }
  }
  const regularIndex = regularNodes.value.indexOf(id)
  if (regularIndex !== -1) return gridPoint(regularIndex, networkTop() + Math.ceil(groups.value.length / 4) * 180)
  return infrastructure.value.find((item) => 'infra:' + item.id === id) ?? { x: 100, y: 90 }
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
  positions.value = { ...positions.value, [drag.id]: { x: Math.max(0, drag.startX + deltaX / scale.value), y: Math.max(0, drag.startY + deltaY / scale.value) } }
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
  const maxX = Math.max(...points.map((point) => point.x + 310)), maxY = Math.max(...points.map((point) => point.y + (expanded.value.size ? 500 : 150)))
  const nextScale = Math.min(1.25, Math.max(0.15, Math.min((rect.width - 48) / (maxX - minX), (rect.height - 48) / (maxY - minY))))
  scale.value = nextScale
  offset.value = { x: (rect.width - (maxX - minX) * nextScale) / 2 - minX * nextScale, y: (rect.height - (maxY - minY) * nextScale) / 2 - minY * nextScale }
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
  emit('save-layout', { positions: positions.value, infrastructure: infrastructure.value, confirmed_links: links.value })
}
function addInfrastructure(): void {
  if (isHistorical.value) return
  const name = infraName.value.trim()
  if (!name) return
  const id = crypto.randomUUID()
  const item = { id, name, category: infraCategory.value, x: 1500 + (infrastructure.value.length % 3) * 300, y: 90 + Math.floor(infrastructure.value.length / 3) * 180 }
  infrastructure.value = [...infrastructure.value, item]
  positions.value = { ...positions.value, [`infra:${id}`]: { x: item.x, y: item.y } }
  persistLayout()
  infraName.value = ''
  infraDialog.value = false
}
function removeInfrastructure(id: string): void {
  if (isHistorical.value) return
  infrastructure.value = infrastructure.value.filter((item) => item.id !== id)
  const nextPositions = { ...positions.value }
  delete nextPositions[`infra:${id}`]
  positions.value = nextPositions
  links.value = links.value.filter((link) => link.from !== `infra:${id}` && link.to !== `infra:${id}`)
  persistLayout()
}
function removeLink(id: string): void {
  if (isHistorical.value) return
  links.value = links.value.filter((link) => link.id !== id)
  persistLayout()
}
onMounted(() => requestAnimationFrame(fitView))
onBeforeUnmount(() => { dragging.value = null; panning.value = null; mapPointers.clear() })
</script>

<template>
  <div class="topology-toolbar">
    <div class="topology-tool-group">
      <button class="icon-button" type="button" aria-label="缩小拓扑" title="缩小" @click="zoom(-0.15)"><ZoomOut :size="15" /></button>
      <span class="zoom-value">{{ Math.round(scale * 100) }}%</span>
      <button class="icon-button" type="button" aria-label="放大拓扑" title="放大" @click="zoom(0.15)"><ZoomIn :size="15" /></button><button class="subtle-button" type="button" @click="fitView">适应画布</button>
      <button v-if="!isHistorical" class="subtle-button" type="button" :aria-pressed="connectMode" @click="connectMode = !connectMode; linkStart = ''"><Network :size="14" />{{ connectMode ? '取消连线' : '标记物理连接' }}</button>
      <button v-if="!isHistorical" class="subtle-button" type="button" @click="infraDialog = true"><Plus :size="14" />添加路由器 / 交换机</button>
      <label v-if="!isHistorical" class="stale-toggle"><input v-model="showStale" type="checkbox" />显示过期设备 <span v-if="staleCount">{{ staleCount }}</span></label>
    </div>
    <label class="history-select"><Clock3 :size="14" /><select aria-label="拓扑历史快照" :value="topology.snapshot?.id ?? ''" @change="emit('select-snapshot', ($event.target as HTMLSelectElement).value ? Number(($event.target as HTMLSelectElement).value) : null)"><option value="">当前拓扑</option><option v-for="snapshot in snapshots" :key="snapshot.id" :value="snapshot.id">{{ new Date(snapshot.captured_at).toLocaleString() }}</option></select></label>
  </div>
  <div class="topology-history-meta" v-if="topology.snapshot">历史快照 · {{ new Date(topology.snapshot.captured_at).toLocaleString() }}</div>
  <div v-else class="topology-history-meta">每 {{ topology.settings.snapshot_interval_minutes }} 分钟保存一次 · 保留 {{ topology.settings.retention_days }} 天</div>
  <div class="topology-legend"><span><i class="legend-line observed"></i>虚线：逻辑网段关系</span><span><i class="legend-line confirmed"></i>实线：人工确认的物理连接</span><span>Docker 子网只挂在所属 Agent 下，不作为独立 LAN 设备</span><span><i class="legend-conflict"></i>红色标记：同一 Agent 网卡近期同时看到同 IP 对应多个 MAC（疑似冲突）</span></div>
  <div ref="mapElement" class="topology-map" :class="{ 'connect-mode': connectMode }" @pointerdown="startPan" @pointermove="movePan" @pointerup="endPan($event)" @pointercancel="endPan($event)" @wheel.prevent="onMapWheel">
    <div class="topology-map-world" :style="{ width: `${canvasSize.width}px`, height: `${canvasSize.height}px`, transform: `translate(${offset.x}px, ${offset.y}px) scale(${scale})` }">
      <svg class="topology-map-links" :width="canvasSize.width" :height="canvasSize.height" aria-hidden="true">
        <template v-for="(group, groupIndex) in groups" :key="`observed:${groupKey(group)}`">
          <line v-if="group.center?.known" class="observed-link" :x1="nodePoint('center').x + 145" :y1="nodePoint('center').y + 80" :x2="networkPoint(group, groupIndex).x + 145" :y2="networkPoint(group, groupIndex).y" />
          <line v-if="rootNode(group)" class="observed-link root-link" :x1="nodePoint(rootNode(group)!).x + 145" :y1="nodePoint(rootNode(group)!).y + 80" :x2="networkPoint(group, groupIndex).x + 145" :y2="networkPoint(group, groupIndex).y" />
          <template v-for="(agent, agentIndex) in group.agents" :key="`agent:${agent.id}`">
            <line v-if="rootNode(group) !== `agent:${agent.id}`" class="observed-link" :x1="networkPoint(group, groupIndex).x + 50 + (agentIndex % 3) * 90" :y1="networkPoint(group, groupIndex).y + 60" :x2="nodePoint(`agent:${agent.id}`).x + 145" :y2="nodePoint(`agent:${agent.id}`).y" />
          </template>
          <template v-for="(asset, assetIndex) in group.assets" :key="`asset:${asset.key}`">
            <line v-if="rootNode(group) !== `asset:${asset.key}`" class="observed-link" :x1="networkPoint(group, groupIndex).x + 50 + ((assetIndex + group.agents.length) % 3) * 90" :y1="networkPoint(group, groupIndex).y + 60" :x2="nodePoint(`asset:${asset.key}`).x + 145" :y2="nodePoint(`asset:${asset.key}`).y" />
          </template>
        </template>
        <line v-for="link in visibleLinks" :key="link.id" class="confirmed-link" :x1="nodePoint(link.from).x + 145" :y1="nodePoint(link.from).y + 40" :x2="nodePoint(link.to).x + 145" :y2="nodePoint(link.to).y + 40" />
      </svg>
      <article v-if="center" class="map-node center-map-node" :class="{ selected: linkStart === 'center' }" :style="{ left: `${nodePoint('center').x}px`, top: `${nodePoint('center').y}px` }" @pointerdown.stop="startNodeDrag($event, 'center')" @pointermove="moveNode" @pointerup="endNodeDrag" @pointercancel="endNodeDrag" @click="toggleLink('center')"><span class="map-node-icon center-icon"><Cpu :size="16" /></span><span class="map-node-title"><strong>{{ center.name }}</strong><small>{{ center.known ? '当前 Center · 已匹配网段' : '当前 Center · 网段未知' }}</small></span></article>
      <button v-for="(group, index) in groups" :key="networkNodeID(group)" class="map-node network-map-node" :class="{ selected: linkStart === networkNodeID(group) }" :style="{ left: `${networkPoint(group, index).x}px`, top: `${networkPoint(group, index).y}px` }" @pointerdown.stop="startNodeDrag($event, networkNodeID(group))" @pointermove="moveNode" @pointerup="endNodeDrag" @pointercancel="endNodeDrag" @click="toggleLink(networkNodeID(group))"><span class="map-node-icon network-icon"><Network :size="16" /></span><span class="map-node-title"><strong>{{ group.network_prefix }}</strong><small>{{ group.agents.length }} 个 Agent · {{ group.assets.length }} 台观察设备 · {{ rootLabel(group) }}</small></span></button>
      <article v-for="agent in agents" :key="agent.id" class="map-node agent-map-node" :class="{ expanded: expanded.has(agent.id), 'root-map-node': rootNodes.includes(`agent:${agent.id}`), selected: linkStart === `agent:${agent.id}` }" :style="{ left: `${nodePoint(`agent:${agent.id}`).x}px`, top: `${nodePoint(`agent:${agent.id}`).y}px` }" @pointerdown.stop="startNodeDrag($event, `agent:${agent.id}`)" @pointermove="moveNode" @pointerup="endNodeDrag" @pointercancel="endNodeDrag" @click="toggleLink(`agent:${agent.id}`)">
        <header><button class="expand-node" :aria-label="`${expanded.has(agent.id) ? '收起' : '展开'} ${agent.name}`" @click.stop="toggleExpanded(agent.id)"><Minus v-if="expanded.has(agent.id)" :size="14" /><Plus v-else :size="14" /></button><span class="map-node-icon"><component :is="deviceIcon(agent.device_category, Cpu)" :size="16" /></span><button class="map-node-title" @click.stop="connectMode ? toggleLink(`agent:${agent.id}`) : emit('open-agent', agent)"><strong>{{ agent.name }}</strong><small>{{ agent.type }} · {{ categoryLabel(agent.device_category) }}{{ agent.device_category && agent.device_category !== 'unknown' ? ' · 人工标注' : ' · 待标注' }}</small></button></header>
        <div class="map-node-counts"><span>{{ agent.interfaces?.length ?? 0 }} 个网卡</span><span>{{ agent.docker_networks?.length ?? 0 }} 个 Docker 网络</span></div>
        <div v-if="expanded.has(agent.id)" class="map-node-children">
          <button v-for="iface in agent.interfaces" :key="iface.key" class="map-child" @click.stop="emit('open-asset', iface)"><Network :size="13" /><span><strong>{{ iface.addresses[0]?.ip ?? iface.mac ?? iface.name }}</strong><small>{{ iface.network_id }}</small></span></button>
          <div v-for="network in agent.docker_networks" :key="network.id" class="docker-child"><div><Box :size="13" /><span><strong>{{ network.name }}</strong><small>{{ network.subnet_cidrs.join(', ') }}</small><small class="docker-scope-note">仅归属此 Agent · 不作为共享 LAN</small></span></div><small v-for="container in network.containers" :key="container.id" class="container-line">{{ container.name }} · {{ container.ipv4_address || 'IP 未知' }}</small></div>
        </div>
      </article>
      <button v-for="asset in assets" :key="asset.key" class="map-node asset-map-node" :class="{ stale: asset.stale, 'root-map-node': rootNodes.includes(`asset:${asset.key}`), conflict: asset.ip_conflict, selected: linkStart === `asset:${asset.key}` }" :style="{ left: `${nodePoint(`asset:${asset.key}`).x}px`, top: `${nodePoint(`asset:${asset.key}`).y}px` }" @pointerdown.stop="startNodeDrag($event, `asset:${asset.key}`)" @pointermove="moveNode" @pointerup="endNodeDrag" @pointercancel="endNodeDrag" @click="connectMode ? toggleLink(`asset:${asset.key}`) : emit('open-asset', asset)"><span class="map-node-icon asset-icon"><component :is="deviceIcon(asset.device_category, Server)" :size="16" /></span><span class="map-node-title"><strong>{{ asset.name }}</strong><small>{{ asset.addresses[0]?.ip || asset.mac || '地址未知' }}</small><small v-if="asset.category_source">{{ categorySourceLabel(asset.category_source) }} · {{ categoryLabel(asset.device_category) }}</small></span><span v-if="asset.ip_conflict" class="map-status">疑似冲突</span></button>
      <article v-for="item in infrastructure" :key="item.id" class="map-node infra-map-node" :class="{ selected: linkStart === `infra:${item.id}` }" :style="{ left: `${positions[`infra:${item.id}`]?.x ?? item.x}px`, top: `${positions[`infra:${item.id}`]?.y ?? item.y}px` }" @pointerdown.stop="startNodeDrag($event, `infra:${item.id}`)" @pointermove="moveNode" @pointerup="endNodeDrag" @pointercancel="endNodeDrag" @click="toggleLink(`infra:${item.id}`)"><span class="map-node-icon infra-icon"><component :is="deviceIcon(item.category, Server)" :size="16" /></span><span class="map-node-title"><strong>{{ item.name }}</strong><small>{{ item.category === 'router' ? '路由器' : '交换机' }} · 手动标注</small></span><button class="remove-link" aria-label="删除设备" :disabled="isHistorical" @click.stop="removeInfrastructure(item.id)">×</button></article>
      <button v-if="agents.length === 0 && assets.length === 0" class="map-empty" @click.stop>等待 Agent 上报局域网快照</button>
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
