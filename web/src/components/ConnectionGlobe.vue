<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import * as THREE from 'three'
import landTopology from 'world-atlas/land-110m.json'
import { feature } from 'topojson-client'
import type { GeometryCollection, Topology } from 'topojson-specification'

export type GlobeRoute = {
  key: string
  from: [number, number]
  to: [number, number]
  label: string
  detail: string
  upload: number
  download: number
  rate: number
  flows: number
  trusted: boolean
}

const props = defineProps<{ routes: GlobeRoute[]; missing: number }>()
const host = ref<HTMLDivElement | null>(null)
const message = ref('正在加载地球…')
const paused = ref(false)
const reducedMotion = ref(false)
const accessibleRoutes = ref<GlobeRoute[]>([])
const hoveredRoute = ref<GlobeRoute | null>(null)
const routeTooltip = ref({ left: 0, top: 0 })
let renderer: THREE.WebGLRenderer | undefined
let scene: THREE.Scene | undefined
let camera: THREE.PerspectiveCamera | undefined
let globe: THREE.Group | undefined
let routeLayer: THREE.Group | undefined
let animationFrame = 0
let resizeObserver: ResizeObserver | undefined
let motionQuery: MediaQueryList | undefined
let dragging = false
let previousX = 0
let previousY = 0
let pointerCleanup: (() => void) | undefined
let routeUpdateTimer = 0
let lastRouteUpdate = 0
const raycaster = new THREE.Raycaster()
raycaster.params.Line = { threshold: 0.028 }

function coordinates([longitude, latitude]: [number, number], radius: number): THREE.Vector3 {
  const lon = THREE.MathUtils.degToRad(longitude)
  const lat = THREE.MathUtils.degToRad(latitude)
  return new THREE.Vector3(
    -radius * Math.cos(lat) * Math.cos(lon),
    radius * Math.sin(lat),
    radius * Math.cos(lat) * Math.sin(lon),
  )
}

function inRing(point: [number, number], ring: number[][]): boolean {
  let inside = false
  for (let i = 0, j = ring.length - 1; i < ring.length; j = i++) {
    const xi = ring[i][0], yi = ring[i][1], xj = ring[j][0], yj = ring[j][1]
    const crosses = (yi > point[1]) !== (yj > point[1]) && point[0] < (xj - xi) * (point[1] - yi) / ((yj - yi) || 1e-12) + xi
    if (crosses) inside = !inside
  }
  return inside
}

function isLand(point: [number, number], polygons: number[][][][]): boolean {
  return polygons.some((polygon) => inRing(point, polygon[0]) && !polygon.slice(1).some((hole) => inRing(point, hole)))
}

function buildLandDots(): THREE.Points {
  const topology = landTopology as unknown as Topology<{ land: GeometryCollection }>
  const collection = feature(topology, topology.objects.land) as any
  const polygons = collection.features.flatMap((item: any) => {
    if (item.geometry?.type === 'Polygon') return [item.geometry.coordinates]
    if (item.geometry?.type === 'MultiPolygon') return item.geometry.coordinates
    return []
  }) as number[][][][]
  const positions: number[] = []
  for (let latitude = -84; latitude <= 84; latitude += 3) {
    for (let longitude = -180; longitude < 180; longitude += 3) {
      if (isLand([longitude, latitude], polygons)) positions.push(...coordinates([longitude, latitude], 1.506).toArray())
    }
  }
  const pointsGeometry = new THREE.BufferGeometry()
  pointsGeometry.setAttribute('position', new THREE.Float32BufferAttribute(positions, 3))
  return new THREE.Points(pointsGeometry, new THREE.PointsMaterial({ color: 0x7ab898, size: 0.012, sizeAttenuation: true }))
}

function clearRoutes(): void {
  if (!routeLayer) return
  for (const child of [...routeLayer.children]) {
    routeLayer.remove(child)
    if (child instanceof THREE.Mesh || child instanceof THREE.Line) {
      child.geometry.dispose()
      const materials = Array.isArray(child.material) ? child.material : [child.material]
      materials.forEach((material) => material.dispose())
    }
  }
}

function drawRoutes(): void {
  if (!routeLayer) return
  clearRoutes()
  const routes = props.routes.slice(0, 100)
  accessibleRoutes.value = routes
  hoveredRoute.value = null
  for (const route of routes) {
    const start = coordinates(route.from, 1.515)
    const end = coordinates(route.to, 1.515)
    const middle = start.clone().add(end).normalize().multiplyScalar(1.9)
    const curve = new THREE.QuadraticBezierCurve3(start, middle, end)
    const geometry = new THREE.BufferGeometry().setFromPoints(curve.getPoints(36))
    const material = new THREE.LineBasicMaterial({ color: route.trusted ? 0x8fca9c : 0x67a7ed, transparent: true, opacity: 0.7 })
    const line = new THREE.Line(geometry, material)
    line.userData.route = route
    line.userData.midpoint = curve.getPoint(0.5)
    routeLayer.add(line)
    const dotGeometry = new THREE.SphereGeometry(0.025, 8, 8)
    const dotMaterial = new THREE.MeshBasicMaterial({ color: route.trusted ? 0x9de2ac : 0x9ac7ff })
    const dot = new THREE.Mesh(dotGeometry, dotMaterial)
    dot.position.copy(end)
    routeLayer.add(dot)
  }
  message.value = routes.length ? '' : '当前没有同时具备 Agent 与远端地理坐标的活动连接'
}

function scheduleRouteUpdate(): void {
  if (routeUpdateTimer || !renderer) return
  const delay = Math.max(0, 1000 - (Date.now() - lastRouteUpdate))
  routeUpdateTimer = window.setTimeout(() => {
    routeUpdateTimer = 0
    lastRouteUpdate = Date.now()
    drawRoutes()
  }, delay)
}

function render(): void {
  if (!renderer || !scene || !camera || !globe) return
  if (!paused.value && !reducedMotion.value && !dragging) globe.rotation.y += 0.0007
  renderer.render(scene, camera)
  animationFrame = window.requestAnimationFrame(render)
}

function resize(): void {
  if (!host.value || !renderer || !camera) return
  const width = Math.max(1, host.value.clientWidth)
  const height = Math.max(1, host.value.clientHeight)
  renderer.setSize(width, height, false)
  camera.aspect = width / height
  camera.updateProjectionMatrix()
}

function handlePointerDown(event: PointerEvent): void {
  if (!host.value || event.button !== 0) return
  dragging = true
  previousX = event.clientX
  previousY = event.clientY
  host.value.setPointerCapture(event.pointerId)
}
function handlePointerMove(event: PointerEvent): void {
  if (dragging && globe) {
    globe.rotation.y += (event.clientX - previousX) * 0.008
    globe.rotation.x = THREE.MathUtils.clamp(globe.rotation.x + (event.clientY - previousY) * 0.006, -1.2, 1.2)
    previousX = event.clientX
    previousY = event.clientY
    hoveredRoute.value = null
    return
  }
  if (!host.value || !camera || !routeLayer) return
  const bounds = host.value.getBoundingClientRect()
  const pointer = new THREE.Vector2((event.clientX - bounds.left) / bounds.width * 2 - 1, -((event.clientY - bounds.top) / bounds.height) * 2 + 1)
  raycaster.setFromCamera(pointer, camera)
  const intersection = raycaster.intersectObjects(routeLayer.children.filter((item) => item instanceof THREE.Line), false)[0]
  const route = intersection?.object.userData.route as GlobeRoute | undefined
  if (!route || !intersection) {
    hoveredRoute.value = null
    return
  }
  hoveredRoute.value = route
  const midpoint = (intersection.object.userData.midpoint as THREE.Vector3).clone().applyMatrix4(globe!.matrixWorld).project(camera)
  routeTooltip.value = {
    left: (midpoint.x + 1) * 0.5 * bounds.width,
    top: (-midpoint.y + 1) * 0.5 * bounds.height,
  }
}
function handlePointerUp(): void { dragging = false }
function handlePointerLeave(): void { if (!dragging) hoveredRoute.value = null }
function handleWheel(event: WheelEvent): void {
  if (!camera) return
  camera.position.z = THREE.MathUtils.clamp(camera.position.z + Math.sign(event.deltaY) * 0.22, 2.2, 5)
}
function resetView(): void {
  if (!globe || !camera) return
  globe.rotation.set(0.15, 0, 0)
  camera.position.set(0, 0, 3.7)
}

onMounted(() => {
  if (!host.value) return
  try {
    renderer = new THREE.WebGLRenderer({ antialias: true, alpha: true, powerPreference: 'low-power' })
    renderer.setPixelRatio(Math.min(window.devicePixelRatio || 1, 1.5))
    renderer.setClearColor(0x000000, 0)
    renderer.domElement.setAttribute('aria-hidden', 'true')
    host.value.appendChild(renderer.domElement)
    scene = new THREE.Scene()
    camera = new THREE.PerspectiveCamera(36, 1, 0.1, 20)
    camera.position.z = 3.7
    globe = new THREE.Group()
    routeLayer = new THREE.Group()
    globe.add(new THREE.Mesh(new THREE.SphereGeometry(1.5, 48, 48), new THREE.MeshBasicMaterial({ color: 0x0e1a29, wireframe: true, transparent: true, opacity: 0.34 })))
    globe.add(buildLandDots())
    globe.add(routeLayer)
    scene.add(globe)
    scene.add(new THREE.AmbientLight(0xffffff, 1))
    resize()
    resizeObserver = new ResizeObserver(resize)
    resizeObserver.observe(host.value)
    motionQuery = window.matchMedia('(prefers-reduced-motion: reduce)')
    reducedMotion.value = motionQuery.matches
    motionQuery.addEventListener('change', onMotionChange)
    host.value.addEventListener('pointerdown', handlePointerDown)
    host.value.addEventListener('pointermove', handlePointerMove)
    host.value.addEventListener('pointerup', handlePointerUp)
    host.value.addEventListener('pointercancel', handlePointerUp)
    host.value.addEventListener('pointerleave', handlePointerLeave)
    host.value.addEventListener('wheel', handleWheel, { passive: true })
    pointerCleanup = () => {
      host.value?.removeEventListener('pointerdown', handlePointerDown)
      host.value?.removeEventListener('pointermove', handlePointerMove)
      host.value?.removeEventListener('pointerup', handlePointerUp)
      host.value?.removeEventListener('pointercancel', handlePointerUp)
      host.value?.removeEventListener('pointerleave', handlePointerLeave)
      host.value?.removeEventListener('wheel', handleWheel)
    }
    drawRoutes()
    render()
  } catch {
    message.value = '此浏览器环境无法启动 WebGL；实时连接和排名仍可正常查看。'
  }
})
function onMotionChange(event: MediaQueryListEvent): void { reducedMotion.value = event.matches }
watch(() => props.routes, scheduleRouteUpdate, { deep: true })
onBeforeUnmount(() => {
  cancelAnimationFrame(animationFrame)
  if (routeUpdateTimer) window.clearTimeout(routeUpdateTimer)
  resizeObserver?.disconnect()
  motionQuery?.removeEventListener('change', onMotionChange)
  pointerCleanup?.()
  clearRoutes()
  if (globe) {
    globe.traverse((object) => {
      if (object instanceof THREE.Points || object instanceof THREE.Mesh) {
        object.geometry.dispose()
        const materials = Array.isArray(object.material) ? object.material : [object.material]
        materials.forEach((material) => material.dispose())
      }
    })
  }
  renderer?.dispose()
  renderer?.domElement.remove()
})
</script>

  <template>
  <section class="globe-card" aria-labelledby="globe-title">
    <header class="section-heading"><div><h2 id="globe-title">实时连接地球</h2><p>按 Agent 与远端 IP 聚合 · 仅显示有完整坐标的活动路线</p></div><div class="globe-toolbar"><div class="globe-filters" role="group" aria-label="实时地球筛选"><slot name="filters" /></div><div class="globe-actions"><button class="subtle-button" type="button" @click="paused = !paused">{{ paused ? '恢复旋转' : '暂停旋转' }}</button><button class="subtle-button" type="button" @click="resetView">恢复视角</button></div></div></header>
    <div ref="host" class="globe-stage" role="img" :aria-label="`实时连接地球，已绘制 ${accessibleRoutes.length} 条路线`"><span v-if="message" class="globe-message">{{ message }}</span><div v-if="hoveredRoute" class="globe-tooltip" role="status" :style="{ left: `${routeTooltip.left}px`, top: `${routeTooltip.top}px` }"><strong>{{ hoveredRoute.label }}</strong><span>{{ hoveredRoute.detail }}</span><span>{{ hoveredRoute.flows }} 条流 · ↑ {{ hoveredRoute.upload.toLocaleString() }} B/s · ↓ {{ hoveredRoute.download.toLocaleString() }} B/s</span><span v-if="hoveredRoute.trusted">可信 IP</span></div></div>
    <p class="globe-note">未定位连接：{{ missing }} · 路线最多绘制前 100 条 · IP 地理位置为近似位置</p>
  <details class="globe-route-list"><summary>路线文字列表（{{ accessibleRoutes.length }}）</summary><ul><li v-for="route in accessibleRoutes" :key="route.key" tabindex="0" role="group" :aria-label="`${route.label} 到 ${route.detail}，${route.flows} 条流，上行 ${route.upload.toLocaleString()} 字节每秒，下行 ${route.download.toLocaleString()} 字节每秒${route.trusted ? '，可信' : ''}`"><strong>{{ route.label }}</strong> → {{ route.detail }} · {{ route.flows }} 条流 · ↑ {{ route.upload.toLocaleString() }} B/s · ↓ {{ route.download.toLocaleString() }} B/s{{ route.trusted ? ' · 可信' : '' }}</li></ul></details>
  </section>
</template>
