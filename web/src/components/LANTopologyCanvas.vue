<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { Background, Handle, Position, VueFlow, setupVueFlow, type Edge, type Node } from '@xyflow/vue'
import '@xyflow/vue/style.css'
import { Box, Clock3, Cpu, HardDrive, Laptop, Minus, Monitor, Network, Plus, Router, Server, Smartphone, Tablet, Tv, ZoomIn, ZoomOut } from '@lucide/vue'

type Point = { x: number; y: number }
type FlowData = { kind: 'center' | 'network' | 'agent' | 'asset' | 'infra'; entity?: Center | Topology['groups'][number] | Agent | LANDevice | Infra; group?: Topology['groups'][number] | null }
type FlowNode = Node<FlowData, 'topology'>
type FlowEdge = Edge<Record<string, never>>
type LANDevice = { key: string; network_id?: string; name: string; device_category: string; category_source?: string; addresses: { ip: string }[]; mac: string; stale: boolean; ip_conflict: boolean }
type DockerNetwork = { id: string; name: string; subnet_cidrs: string[]; containers: { id: string; name: string; ipv4_address?: string }[] }
type Agent = { id: string; name: string; type: string; device_category: string; last_seen: string; interfaces: LANDevice[]; docker_networks: DockerNetwork[] }
type Infra = { id: string; name: string; category: string; x: number; y: number }
type Layout = { version: number; mode?: 'hierarchy' | 'nebula'; positions: Record<string, Point>; root_assignments?: Record<string, string>; infrastructure: Infra[]; confirmed_links: { id: string; from: string; to: string }[] }
type Snapshot = { id: number; captured_at: string }
type Center = { id: string; name: string; known: boolean }
type Topology = { agents: Agent[]; groups: { id?: string; network_id: string; network_prefix: string; root_device_key?: string; root_agent_id?: string; center?: Center; agents: Agent[]; assets: LANDevice[] }[]; unassigned: { center?: Center; agents?: Agent[]; assets: LANDevice[] }; layout: Layout; settings: { snapshot_interval_minutes: number; retention_days: number }; snapshot?: Snapshot }

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
const minZoom = 0.02
const maxZoom = 5
const flowNodes = ref<FlowNode[]>([])
const flowEdges = ref<FlowEdge[]>([])
const { fitView: flowFitView, zoomTo: flowZoomTo } = setupVueFlow<FlowNode, FlowEdge>({ minZoom, maxZoom })
const connectMode = ref(false)
const linkStart = ref('')
const infraDialog = ref(false)
const infraName = ref('')
const infraCategory = ref('router')
const interval = ref(5)
const retention = ref(7)
const settingsDirty = ref(false)
const positions = ref<Record<string, Point>>({})
const layoutMode = ref<'hierarchy' | 'nebula'>('hierarchy')
const topologyLayoutVersion = 4
const topologyNodeWidth = 164
// Match the signed coordinate range accepted by the layout API.
const layoutCoordinateLimit = 1_000_000
let fittedSnapshot: number | null | undefined
const infrastructure = ref<Infra[]>([])
const links = ref<Layout['confirmed_links']>([])
const isHistorical = computed(() => Boolean(props.topology.snapshot))

watch(() => ({ layout: props.topology.layout, groups: props.topology.groups, loading: props.loading }), ({ layout, loading }) => {
  infrastructure.value = [...(layout?.infrastructure ?? [])]
  links.value = [...(layout?.confirmed_links ?? [])]
  layoutMode.value = layout?.mode === 'nebula' ? 'nebula' : 'hierarchy'
  const obsoleteLayout = (layout?.version ?? 0) < topologyLayoutVersion
  const savedPositions = obsoleteLayout ? {} : (layout?.positions ?? {})
  const nextPositions: Record<string, Point> = layout?.mode === 'nebula'
    ? Object.fromEntries(Object.entries(savedPositions).map(([id, point]) => [id.startsWith('nebula:') ? id : ('nebula:' + id), point]))
    : { ...savedPositions }
  const assignments = rootAssignments()
  const oldAssignments = layout?.root_assignments ?? {}
  let rootChanged = false
  for (const [key, nextRoot] of Object.entries(assignments)) {
    const oldRoot = oldAssignments[key] ?? ''
    if (oldRoot === nextRoot) continue
    rootChanged = true
    for (const id of [oldRoot, nextRoot]) {
      if (id) delete nextPositions[positionKey(id)]
    }
  }
  positions.value = nextPositions
  if (!loading && !isHistorical.value && (obsoleteLayout || rootChanged)) {
    emit('save-layout', {
      version: topologyLayoutVersion,
      mode: layoutMode.value,
      positions: nextPositions,
      root_assignments: assignments,
      infrastructure: infrastructure.value,
      confirmed_links: links.value,
    })
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
const agents = computed(() => {
  // Live responses expose the complete Agent records at the top level, while
  // older snapshots only retain the lightweight records inside each group.
  // Merge all sources so either payload still produces visible nodes.
  const byID = new Map<string, Agent>()
  const add = (agent: Agent) => {
    if (!agent?.id) return
    const previous = byID.get(agent.id)
    byID.set(agent.id, {
      ...previous,
      ...agent,
      interfaces: (agent.interfaces?.length ? agent.interfaces : previous?.interfaces) ?? [],
      docker_networks: (agent.docker_networks?.length ? agent.docker_networks : previous?.docker_networks) ?? [],
    })
  }
  ;(props.topology.agents ?? []).forEach(add)
  ;(props.topology.groups ?? []).forEach((group) => (group.agents ?? []).forEach(add))
  ;(props.topology.unassigned?.agents ?? []).forEach(add)
  return [...byID.values()]
    .filter((agent) => !isExpired(agent.last_seen))
    .map((agent) => ({
      ...agent,
      interfaces: (agent.interfaces ?? []).filter(visibleAsset),
    }))
})
const agentNodes = computed(() => {
  const assigned = groups.value.flatMap((group) => group.agents.map((agent) => ({ agent, id: agentNodeID(group, agent.id) })))
  const assignedIDs = new Set(assigned.map((entry) => entry.agent.id))
  return [...assigned, ...agents.value.filter((agent) => !assignedIDs.has(agent.id)).map((agent) => ({ agent, id: agentNodeID(null, agent.id) }))]
})
const visibleNodeKey = computed(() => [...visibleNodes.value].sort().join('|'))
const groups = computed(() => {
  const visibleAgents = new Map(agents.value.map((agent) => [agent.id, agent]))
  return (props.topology.groups ?? [])
    .map((group) => ({
      ...group,
      // Scope Agent membership to each observed LAN so a multi-homed Agent
      // appears in both groups without creating a cross-network visual bridge.
      agents: (group.agents ?? []).flatMap((agent) => visibleAgents.has(agent.id) ? [visibleAgents.get(agent.id)!] : []),
      assets: (group.assets ?? []).filter(visibleAsset),
    }))
    .filter((group) => group.agents.length > 0 || group.assets.length > 0)
})
const assets = computed(() => {
  const all = [...groups.value.flatMap((group) => group.assets ?? []), ...(props.topology.unassigned?.assets ?? [])]
  const byKey = new Map(all.map((asset) => [asset.key, asset]))
  return [...byKey.values()].filter(visibleAsset)
})
const center = computed(() => props.topology.groups?.find((group) => group.center?.known)?.center ?? props.topology.unassigned?.center)
const roots = computed(() => {
  const byGroup = new Map<string, string>()
  for (const group of groups.value) {
    if (group.root_agent_id && group.agents.some((agent) => agent.id === group.root_agent_id)) {
      byGroup.set(groupKey(group), agentNodeID(group, group.root_agent_id))
    } else if (group.root_device_key && group.assets.some((asset) => asset.key === group.root_device_key)) {
      byGroup.set(groupKey(group), 'asset:' + group.root_device_key)
    }
  }
  return byGroup
})
const rootNodes = computed(() => [...new Set(roots.value.values())])
const regularNodes = computed(() => {
  const rootIDs = new Set(rootNodes.value)
  return [
    ...agentNodes.value.map((entry) => entry.id),
    ...assets.value.map((asset) => 'asset:' + asset.key),
  ].filter((id) => !rootIDs.has(id))
})
const visibleNodes = computed(() => new Set([
  ...rootNodes.value, ...regularNodes.value,
  ...groups.value.map(networkNodeID), 'unassigned', ...infrastructure.value.map((item) => 'infra:' + item.id),
  ...(center.value ? ['center'] : []),
]))
const visibleLinks = computed(() => links.value.flatMap((link) => {
  const from = resolveVisibleNode(link.from)
  const to = resolveVisibleNode(link.to)
  return from && to && from !== to ? [{ ...link, from, to }] : []
}))

function nodeHeight(id: string): number {
  const agentID = id.startsWith('agent:') ? id.slice(id.lastIndexOf(':') + 1) : ''
  if (!agentID || !expanded.value.has(id)) return 60
  const agent = [...groups.value.flatMap((group) => group.agents), ...agents.value].find((item) => item.id === agentID)
  if (!agent) return 60
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
    const members = [...group.agents.map((agent) => agentNodeID(group, agent.id)), ...group.assets.map((asset) => `asset:${asset.key}`)]
      .filter((id, index, all) => all.indexOf(id) === index && !assigned.has(id) && visibleNodes.value.has(id))
    members.forEach((id) => assigned.add(id))
    return { group, id: networkNodeID(group), members }
  })
  const assignedAgentIDs = new Set(groups.value.flatMap((group) => group.agents.map((agent) => agent.id)))
  const unassigned = [...agents.value.filter((agent) => !assignedAgentIDs.has(agent.id)).map((agent) => agentNodeID(null, agent.id)), ...assets.value.map((asset) => `asset:${asset.key}`)]
    .filter((id, index, all) => all.indexOf(id) === index && !assigned.has(id) && visibleNodes.value.has(id))
  if (unassigned.length) clusters.push({ group: null, id: 'unassigned', members: unassigned })
  return clusters
})
const automaticPoints = computed(() => {
  const result: Record<string, Point> = {}
  const nodeWidth = topologyNodeWidth
  const zoneWidth = 420
  const zoneGapX = 72
  const zoneGapY = 72
  const zoneStartX = 48
  const zoneStartY = 176
  const memberColumns = 2
  const memberStepX = 180
  const rowGap = 18

  if (layoutMode.value === 'nebula') {
    let centerY = zoneStartY + 250
    layoutClusters.value.forEach((cluster) => {
      const root = cluster.group ? rootNode(cluster.group) : undefined
      const peers = root ? cluster.members.filter((id) => id !== root) : cluster.members
      const radius = Math.max(210, peers.length * 38)
      const centerX = zoneStartX + radius + nodeWidth / 2
      if (root) {
        result[root] = { x: centerX - nodeWidth / 2, y: centerY - nodeHeight(root) / 2 }
      }
      peers.forEach((id, memberIndex) => {
        const angle = -Math.PI / 2 + (Math.PI * 2 * memberIndex) / Math.max(1, peers.length)
        result[id] = { x: centerX + Math.cos(angle) * radius - nodeWidth / 2, y: centerY + Math.sin(angle) * radius - nodeHeight(id) / 2 }
      })
      centerY += radius * 2 + zoneGapY
    })
    if (center.value) result.center = { x: zoneStartX + 210, y: 32 }
    return result
  }

  // Keep LANs as independent islands. A selected gateway occupies the top
  // center of its island; its observed members are laid out beneath it.
  let rowY = zoneStartY
  for (let index = 0; index < layoutClusters.value.length; index += 2) {
    const rowClusters = layoutClusters.value.slice(index, index + 2)
    let rowHeight = 0
    rowClusters.forEach((cluster, column) => {
      const tileX = zoneStartX + column * (zoneWidth + zoneGapX)
      const root = cluster.group ? rootNode(cluster.group) : undefined
      const peers = root ? cluster.members.filter((id) => id !== root) : cluster.members
      const rowHeights: number[] = []
      const rootY = rowY + 72
      if (root) result[root] = { x: tileX + (zoneWidth - nodeWidth) / 2, y: rootY }
      const peerStartY = root ? rootY + nodeHeight(root) + 48 : rootY
      for (let memberIndex = 0; memberIndex < peers.length; memberIndex += memberColumns) {
        const ids = peers.slice(memberIndex, memberIndex + memberColumns)
        const height = Math.max(60, ...ids.map(nodeHeight))
        const y = peerStartY + rowHeights.reduce((sum, value) => sum + value + rowGap, 0)
        const rowWidth = ids.length * nodeWidth + (ids.length - 1) * (memberStepX - nodeWidth)
        const startX = tileX + (zoneWidth - rowWidth) / 2
        ids.forEach((id, memberColumn) => {
          result[id] = { x: startX + memberColumn * memberStepX, y }
        })
        rowHeights.push(height)
      }
      const tileHeight = 72 + (root ? nodeHeight(root) + 48 : 0) + rowHeights.reduce((sum, height) => sum + height, 0) + Math.max(0, rowHeights.length - 1) * rowGap + 24
      rowHeight = Math.max(rowHeight, tileHeight)
    })
    rowY += rowHeight + zoneGapY
  }

  if (center.value) {
    const canvasWidth = layoutClusters.value.length > 1 ? (zoneWidth * 2 + zoneGapX) : zoneWidth
    result.center = { x: zoneStartX + (canvasWidth - nodeWidth) / 2, y: 32 }
  }
  infrastructure.value.forEach((item) => {
    const id = `infra:${item.id}`
    result[id] = positions.value[positionKey(id)] ?? { x: zoneStartX + (itemIndex(item) % 2) * memberStepX, y: rowY + 80 + Math.floor(itemIndex(item) / 2) * 104 }
  })
  return result
})
const networkRegions = computed(() => layoutClusters.value.map((cluster) => {
  const memberPoints = cluster.members.map((id) => ({ point: nodePoint(id), height: nodeHeight(id) }))
  if (!memberPoints.length) return null
  const left = Math.min(...memberPoints.map(({ point }) => point.x)) - 24
  const top = Math.min(...memberPoints.map(({ point }) => point.y)) - 72
  const right = Math.max(...memberPoints.map(({ point }) => point.x + topologyNodeWidth)) + 24
  const bottom = Math.max(...memberPoints.map(({ point, height }) => point.y + height)) + 24
  return {
    id: cluster.id,
    group: cluster.group,
    position: { x: left, y: top },
    width: Math.max(420, right - left),
    height: Math.max(184, bottom - top),
    count: cluster.members.length,
  }
}).filter((region): region is NonNullable<typeof region> => region !== null))
const topologyEdges = computed<FlowEdge[]>(() => {
  const gatewayMembership = layoutClusters.value.flatMap((cluster) => {
    const root = cluster.group ? rootNode(cluster.group) : undefined
    if (!root) return []
    return cluster.members.filter((id) => id !== root).map((id) => ({
      id: `gateway:${cluster.id}:${root}->${id}`,
      source: root,
      target: id,
      sourceHandle: 'bottom',
      targetHandle: 'top',
      type: 'smoothstep',
      class: 'gateway-membership-link',
      selectable: false,
      focusable: false,
      ariaLabel: '同一局域网的网关归属关系，非直接物理链路',
    }))
  })
  const confirmed = visibleLinks.value
    .filter((link) => !link.from.startsWith('network:') && !link.to.startsWith('network:') && link.from !== 'center' && link.to !== 'center')
    .map((link) => ({ id: link.id, source: link.from, target: link.to, sourceHandle: 'right', targetHandle: 'left', type: 'smoothstep', class: 'confirmed-link', selectable: true, ariaLabel: '人工确认的物理连接' }))
  return [...gatewayMembership, ...confirmed]
})
const graphNodes = computed<FlowNode[]>(() => {
  const nodes: FlowNode[] = []
  if (center.value) nodes.push({ id: 'center', type: 'topology', position: nodePoint('center'), width: topologyNodeWidth, height: 60, data: { kind: 'center', entity: center.value }, draggable: !isHistorical.value })
  for (const region of networkRegions.value) nodes.push({ id: region.id, type: 'topology', position: region.position, width: region.width, height: region.height, style: { width: `${region.width}px`, height: `${region.height}px`, background: 'transparent', border: '0', borderRadius: '0', padding: '0', boxShadow: 'none' }, zIndex: -1, selectable: false, focusable: false, data: { kind: 'network', entity: region.group ?? undefined, group: region.group }, draggable: false })
  for (const entry of agentNodes.value) nodes.push({ id: entry.id, type: 'topology', position: nodePoint(entry.id), width: topologyNodeWidth, height: nodeHeight(entry.id), data: { kind: 'agent', entity: entry.agent, group: groups.value.find((group) => entry.id === agentNodeID(group, entry.agent.id)) ?? null }, draggable: !isHistorical.value })
  for (const asset of assets.value) {
    const id = `asset:${asset.key}`
    nodes.push({ id, type: 'topology', position: nodePoint(id), width: topologyNodeWidth, height: 60, data: { kind: 'asset', entity: asset, group: groups.value.find((group) => group.assets.some((item) => item.key === asset.key)) ?? null }, draggable: !isHistorical.value })
  }
  for (const item of infrastructure.value) {
    const id = `infra:${item.id}`
    nodes.push({ id, type: 'topology', position: nodePoint(id), width: topologyNodeWidth, height: 60, data: { kind: 'infra', entity: item }, draggable: !isHistorical.value })
  }
  return nodes
})
watch(graphNodes, (nodes) => { flowNodes.value = nodes }, { immediate: true })
watch(topologyEdges, (edges) => { flowEdges.value = edges }, { immediate: true })

function itemIndex(item: Infra): number { return infrastructure.value.findIndex((candidate) => candidate.id === item.id) }
function agentNodeID(group: Topology['groups'][number] | null, agentID: string): string {
  return `agent:${group ? groupKey(group) : 'unassigned'}:${agentID}`
}
function groupKey(group: Topology['groups'][number]): string { return group.id || group.network_id }
function networkNodeID(group: Topology['groups'][number]): string { return 'network:' + groupKey(group) }
function networkPoint(group: Topology['groups'][number], _index: number): Point { return positions.value[positionKey(networkNodeID(group))] ?? automaticPoints.value[networkNodeID(group)] ?? { x: 430, y: 220 } }
function resolveVisibleNode(id: string): string | undefined {
  if (visibleNodes.value.has(id)) return id
  if (id.startsWith('agent:')) {
    const agentID = id.slice('agent:'.length)
    const matches = groups.value.filter((group) => group.agents.some((agent) => agent.id === agentID))
    if (matches.length === 1) return agentNodeID(matches[0], agentID)
    if (matches.length === 0 && agents.value.some((agent) => agent.id === agentID)) return agentNodeID(null, agentID)
    return
  }
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
function rootAssignments(): Record<string, string> {
  return Object.fromEntries(props.topology.groups.map((group) => {
    const root = group.root_agent_id && group.agents.some((agent) => agent.id === group.root_agent_id)
      ? agentNodeID(group, group.root_agent_id)
      : group.root_device_key && group.assets.some((asset) => asset.key === group.root_device_key)
        ? 'asset:' + group.root_device_key
        : ''
    return [groupKey(group), root]
  }))
}
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
  const agent = group.agents.find((item) => agentNodeID(group, item.id) === node)
  if (agent) return '根设备：' + agent.name
  const asset = group.assets.find((item) => 'asset:' + item.key === node)
  return asset ? '根设备：' + asset.name : '根路由器待确认'
}
function nodePoint(id: string): Point {
  const saved = positions.value[positionKey(id)]
  if (saved && Number.isFinite(saved.x) && Number.isFinite(saved.y)
    && Math.abs(saved.x) <= layoutCoordinateLimit && Math.abs(saved.y) <= layoutCoordinateLimit) {
    return saved
  }
  return automaticPoints.value[id] ?? { x: 48, y: 40 }
}
function toggleExpanded(id: string): void {
  const next = new Set(expanded.value)
  next.has(id) ? next.delete(id) : next.add(id)
  expanded.value = next
}
const zoomSliderValue = computed(() => Math.round(100 * Math.log(scale.value / minZoom) / Math.log(maxZoom / minZoom)))
function zoomFromSlider(value: number): void { void zoomTo(minZoom * (maxZoom / minZoom) ** (value / 100)) }
async function zoomTo(nextScale: number): Promise<void> {
  const bounded = Math.min(maxZoom, Math.max(minZoom, nextScale))
  await flowZoomTo(bounded)
  scale.value = bounded
}
function zoom(delta: number): void { void zoomTo(scale.value * (delta > 0 ? 1.25 : 0.8)) }
async function fitView(all = true): Promise<boolean> {
  if (!flowNodes.value.length) return false
  if (all) return flowFitView({ padding: 0.16, maxZoom: 1.25, minZoom })

  // Start with one meaningful LAN at a readable scale. Users can still pan
  // freely to other islands or choose “fit all” when they want the overview.
  const primary = layoutClusters.value.find((cluster) => cluster.group?.center?.known) ?? layoutClusters.value[0]
  if (!primary) return flowFitView({ padding: 0.16, maxZoom: 1.1, minZoom: 0.08 })
  const ids = new Set([primary.id, ...primary.members])
  if (primary.group?.center?.known && center.value) ids.add("center")
  const nodes = flowNodes.value.filter((node) => ids.has(node.id))
  return flowFitView({ nodes, padding: 0.16, maxZoom: 1.1, minZoom: 0.08 })
}
function onViewportChange(viewport: { zoom: number }): void { scale.value = viewport.zoom }
function onNodeDragStop(_event: MouseEvent | TouchEvent, node: FlowNode): void {
  const x = Math.max(-layoutCoordinateLimit, Math.min(layoutCoordinateLimit, node.position.x))
  const y = Math.max(-layoutCoordinateLimit, Math.min(layoutCoordinateLimit, node.position.y))
  positions.value = { ...positions.value, [positionKey(node.id)]: { x, y } }
  persistLayout()
}
function onNodeClick(id: string): void { if (connectMode.value && !id.startsWith('network:') && id !== 'center') toggleLink(id) }
async function setLayoutMode(mode: 'hierarchy' | 'nebula'): Promise<void> {
  if (layoutMode.value === mode) return
  layoutMode.value = mode
  connectMode.value = false
  linkStart.value = ''
  persistLayout()
  await nextTick()
  fitView(false)
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
  emit('save-layout', { version: topologyLayoutVersion, mode: layoutMode.value, positions: positions.value, root_assignments: rootAssignments(), infrastructure: infrastructure.value, confirmed_links: links.value })
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
async function fitInitialView(): Promise<void> {
  const nodeKey = visibleNodeKey.value
  const snapshot = props.topology.snapshot?.id ?? null
  if (!nodeKey || props.loading || snapshot === fittedSnapshot) return
  await nextTick()
  if (await fitView(false)) fittedSnapshot = snapshot
}
watch(() => [props.loading, visibleNodeKey.value, props.topology.snapshot?.id ?? null] as const, () => { void fitInitialView() }, { flush: 'post', immediate: true })
</script>

<template>
  <div class="topology-toolbar">
    <div class="topology-tool-group">
      <div class="topology-layout-toggle" role="group" aria-label="局域网视图">
        <button type="button" :aria-pressed="layoutMode === 'hierarchy'" title="按网段分区平铺，区域内同级设备对齐" @click="setLayoutMode('hierarchy')">网段分区</button>
        <button type="button" :aria-pressed="layoutMode === 'nebula'" title="在无限画布上按网段环绕排列设备" @click="setLayoutMode('nebula')">星云布局</button>
      </div>
      <button class="icon-button" type="button" aria-label="缩小拓扑" title="缩小" @click="zoom(-1)"><ZoomOut :size="15" /></button>
      <span class="zoom-value">{{ Math.round(scale * 100) }}%</span>
      <input class="topology-zoom-slider" type="range" min="0" max="100" step="1" :value="zoomSliderValue" :aria-valuetext="`${Math.round(scale * 100)}%`" aria-label="画布缩放" @input="zoomFromSlider(Number(($event.target as HTMLInputElement).value))" />
      <button class="icon-button" type="button" aria-label="放大拓扑" title="放大" @click="zoom(1)"><ZoomIn :size="15" /></button><button class="subtle-button" type="button" @click="fitView(true)">适应全部</button>
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
  <div class="topology-legend"><span><i class="legend-line"></i>虚线：同网段的网关归属，不代表直接物理连接</span><span><i class="legend-line confirmed"></i>实线：人工确认的物理连接</span><span>Docker 子网只挂在所属 Agent 下，不作为独立 LAN 设备</span><span><i class="legend-conflict"></i>红色标记：同一 Agent 网卡近期同时看到同 IP 对应多个 MAC（疑似冲突）</span></div>
  <div class="topology-map" :class="{ 'connect-mode': connectMode }">
    <VueFlow
      v-model:nodes="flowNodes" v-model:edges="flowEdges" class="lan-topology-flow"
      :nodes-draggable="!isHistorical" :nodes-connectable="false" :elements-selectable="!isHistorical"
      :pan-on-drag="true" :zoom-on-scroll="true" :zoom-on-pinch="true"
      :min-zoom="minZoom" :max-zoom="maxZoom" :default-edge-options="{ type: 'smoothstep' }"
      aria-label="局域网设备拓扑画布"
      @node-click="onNodeClick($event.node.id)" @node-drag-stop="onNodeDragStop" @viewport-change="onViewportChange"
    >
      <Background :gap="22" :size="1" pattern-color="#d9e1e5" />
      <template #node-topology="{ id, data }">
        <Handle id="top" type="target" :position="Position.Top" class="topology-handle" />
        <Handle id="bottom" type="source" :position="Position.Bottom" class="topology-handle" />
        <Handle id="left" type="target" :position="Position.Left" class="topology-handle" />
        <Handle id="right" type="source" :position="Position.Right" class="topology-handle" />
        <article v-if="data.kind === 'center'" class="map-node center-map-node" :class="{ selected: linkStart === id }" @click.stop="onNodeClick(id)"><span class="map-node-icon center-icon"><Cpu :size="16" /></span><span class="map-node-title"><strong>{{ (data.entity as Center).name }}</strong><small>{{ (data.entity as Center).known ? '当前 Center · 已匹配网段' : '当前 Center · 网段未知' }}</small></span></article>
        <div v-else-if="data.kind === 'network'" class="network-zone" :class="{ unassigned: !data.group }" aria-hidden="true"><strong>{{ data.group?.network_prefix ?? '未分配网段' }}</strong></div>
        <article v-else-if="data.kind === 'agent'" class="map-node agent-map-node" :class="{ 'root-map-node': rootNodes.includes(id), selected: linkStart === id }">
          <header><button class="expand-node nodrag" :aria-label="expanded.has(id) ? '折叠网卡和容器' : '展开网卡和容器'" :aria-expanded="expanded.has(id)" @click.stop="toggleExpanded(id)"><Minus v-if="expanded.has(id)" :size="14" /><Plus v-else :size="14" /></button><span class="map-node-icon"><component :is="deviceIcon((data.entity as Agent).device_category, Cpu)" :size="16" /></span><button class="map-node-title nodrag" @click.stop="connectMode ? toggleLink(id) : emit('open-agent', data.entity as Agent)"><strong>{{ (data.entity as Agent).name }}</strong><small>{{ (data.entity as Agent).type }} · {{ categoryLabel((data.entity as Agent).device_category) }}{{ (data.entity as Agent).device_category && (data.entity as Agent).device_category !== 'unknown' ? ' · 人工标注' : ' · 待标注' }}</small></button></header>
          <div class="map-node-counts"><span>{{ (data.entity as Agent).interfaces?.length ?? 0 }} 个网卡</span><span>{{ (data.entity as Agent).docker_networks?.length ?? 0 }} 个 Docker 网络</span></div>
          <div v-if="expanded.has(id)" class="map-node-children nodrag"><button v-for="iface in (data.entity as Agent).interfaces" :key="iface.key" class="map-child" @click.stop="emit('open-asset', iface)"><Network :size="13" /><span><strong>{{ iface.addresses[0]?.ip ?? iface.mac ?? iface.name }}</strong><small>{{ iface.network_id }}</small></span></button><div v-for="network in (data.entity as Agent).docker_networks" :key="network.id" class="docker-child"><div><Box :size="13" /><span><strong>{{ network.name }}</strong><small>{{ network.subnet_cidrs.join(', ') }}</small><small class="docker-scope-note">仅归属此 Agent · 不作为共享 LAN</small></span></div><small v-for="container in network.containers" :key="container.id" class="container-line">{{ container.name }} · {{ container.ipv4_address || 'IP 未知' }}</small></div></div>
        </article>
        <button v-else-if="data.kind === 'asset'" class="map-node asset-map-node" :class="{ stale: (data.entity as LANDevice).stale, 'root-map-node': rootNodes.includes(id), conflict: (data.entity as LANDevice).ip_conflict, selected: linkStart === id }" @click.stop="connectMode ? toggleLink(id) : emit('open-asset', data.entity as LANDevice)"><span class="map-node-icon asset-icon"><component :is="deviceIcon((data.entity as LANDevice).device_category, Server)" :size="16" /></span><span class="map-node-title"><strong>{{ (data.entity as LANDevice).name }}</strong><small>{{ (data.entity as LANDevice).addresses[0]?.ip || (data.entity as LANDevice).mac || '地址未知' }}</small><small v-if="(data.entity as LANDevice).category_source">{{ categorySourceLabel((data.entity as LANDevice).category_source) }} · {{ categoryLabel((data.entity as LANDevice).device_category) }}</small></span><span v-if="(data.entity as LANDevice).ip_conflict" class="map-status">疑似冲突</span></button>
        <article v-else class="map-node infra-map-node" :class="{ selected: linkStart === id }" @click.stop="toggleLink(id)"><span class="map-node-icon infra-icon"><component :is="deviceIcon((data.entity as Infra).category, Server)" :size="16" /></span><span class="map-node-title"><strong>{{ (data.entity as Infra).name }}</strong><small>{{ (data.entity as Infra).category === 'router' ? '路由器' : '交换机' }} · 手动标注</small></span><button class="remove-link nodrag" aria-label="删除设备" :disabled="isHistorical" @click.stop="removeInfrastructure((data.entity as Infra).id)">×</button></article>
      </template>
    </VueFlow>
    <div v-if="agents.length === 0 && assets.length === 0" class="map-empty">{{ props.loadError ? '拓扑加载失败，暂无可用数据' : props.loading ? '正在加载局域网拓扑…' : '等待 Agent 上报局域网快照' }}</div>
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
