// Shared data layer (Phase 5): API client, WS client, metrics live-store and
// wire types — ONE implementation consumed by apps/customer and apps/admin.
// Phase 3 rule: live cards come from the WS stream only; the historical DB is
// never the source for live values.
import { useEffect, useReducer, useSyncExternalStore } from 'react'

// ---------------------------------------------------------------- api client
const BASE = ''

export class ApiError extends Error {
  status: number
  code: string
  constructor(status: number, code: string, message: string) {
    super(message)
    this.status = status
    this.code = code
  }
}

async function handle(res: Response) {
  if (res.status === 204) return null
  let body: any = null
  try {
    body = await res.json()
  } catch {
    /* empty body */
  }
  if (!res.ok) {
    const err = body?.error ?? {}
    throw new ApiError(res.status, err.code ?? 'error', err.message ?? res.statusText)
  }
  return body
}

async function req<T = any>(method: string, path: string, payload?: unknown): Promise<T> {
  const res = await fetch(BASE + path, {
    method,
    credentials: 'include',
    headers: {
      // CSRF companion to SameSite=Lax cookies: custom headers cannot be
      // sent cross-origin without a CORS preflight approval.
      'X-EpicPanel': '1',
      ...(payload !== undefined ? { 'Content-Type': 'application/json' } : {}),
    },
    body: payload !== undefined ? JSON.stringify(payload) : undefined,
  })
  return handle(res) as Promise<T>
}

export const api = {
  get: <T = any>(p: string) => req<T>('GET', p),
  post: <T = any>(p: string, body?: unknown) => req<T>('POST', p, body),
  put: <T = any>(p: string, body?: unknown) => req<T>('PUT', p, body),
  patch: <T = any>(p: string, body?: unknown) => req<T>('PATCH', p, body),
  del: <T = any>(p: string) => req<T>('DELETE', p),
}

// ---------------------------------------------------------------- wire types

export interface User {
  id: string
  email: string
  name: string
  is_platform_admin: boolean
  status: string
  mfa_enabled?: boolean
  created_at?: string
}

export interface Organization {
  id: string
  name: string
  slug: string
  created_by: string
}

export interface Server {
  id: string
  organization_id: string
  name: string
  hostname: string
  os_info: string
  agent_version: string
  status: string
  maintenance_mode?: boolean
  enrolled_at?: string
  last_seen_at?: string
  created_at: string
}

export interface Website {
  id: string
  organization_id: string
  server_id: string
  name: string
  primary_domain: string
  runtime: string
  runtime_version: string
  web_server: string
  backend_port?: number
  docroot_suffix?: string
  status: string
  unix_user: string
  document_root: string
  is_staging: boolean
  backup_schedule?: string
  backup_retention?: number
  created_at: string
}

export interface Database {
  id: string
  organization_id: string
  server_id: string
  website_id?: string
  engine: string
  name: string
  db_user: string
  status: string
}

export interface Domain {
  id: string
  website_id: string
  domain: string
  kind: string
  ssl_mode: string
  ssl_state: string
  ssl_issued_at?: string
  ssl_expires_at?: string
  ssl_error?: string
  dns_verified_at?: string
  dns_points_to_server?: boolean
  docroot_suffix?: string
  created_at?: string
}

export interface Job {
  id: string
  type: string
  status: string
  error?: string
  created_at: string
}

export interface Member {
  user_id: string
  email: string
  name: string
  role: string
  joined_at: string
}

export interface MetricPoint {
  cpu_percent: number
  memory_used_bytes: number
  memory_total_bytes: number
  disk_used_bytes: number
  disk_total_bytes: number
  load1: number
  collected_at: string
}

export interface DiskSample {
  fs: string
  mount: string
  total_bytes: number
  used_bytes: number
  inodes_total: number
  inodes_used: number
}

export interface DiskIOSample {
  read_bps: number
  write_bps: number
  read_iops: number
  write_iops: number
  total_read_bytes: number
  total_written_bytes: number
}

export interface NetSample {
  rx_bps: number
  tx_bps: number
  rx_bytes: number
  tx_bytes: number
}

export interface ServiceSample {
  name: string
  state: string
  sub?: string
}

export interface NodeSample {
  cpu_percent: number
  cpu_cores: number
  load1: number
  load5: number
  load15: number
  memory_total_bytes: number
  memory_used_bytes: number
  memory_available_bytes: number
  swap_total_bytes: number
  swap_used_bytes: number
  disks?: DiskSample[]
  disk_io: DiskIOSample
  net: NetSample
  tcp_established: number
  tcp_total: number
  processes: number
  uptime_s: number
  services?: ServiceSample[]
}

export interface SiteSample {
  website_id: string
  unix_user: string
  cpu_percent: number
  cpu_cores_used: number
  cpu_limit_cores: number
  memory_bytes: number
  memory_limit_bytes: number
  disk_used_mb: number
  bandwidth_bps: number
  processes: number
  io_read_bps: number
  io_write_bps: number
}

export interface ContainerSample {
  id: string
  name?: string
  cpu_percent: number
  memory_bytes: number
  memory_limit_bytes: number
  net_rx_bps: number
  net_tx_bps: number
  block_io_read_bps: number
  block_io_write_bps: number
  pids: number
  uptime_s: number
}

export interface AppSample {
  website_id: string
  kind: 'app' | 'minecraft' | 'discord'
  status: string
  cpu_percent: number
  memory_bytes: number
  net_rx_bps: number
  net_tx_bps: number
  disk_used_mb: number
  uptime_s: number
  restart_count: number
  players: number
  tps: number
  mspt: number
}

export interface SnapshotFrame {
  server_id: string
  session_id?: string
  seq: number
  collected_at: string
  received_at: string
  freshness: { state: 'LIVE' | 'STALE' | 'OFFLINE'; age_ms: number }
  node_state: 'ONLINE' | 'STALE' | 'OFFLINE'
  degraded: boolean
  degraded_reason?: string
  connected: boolean
  sample?: {
    node: NodeSample
    sites?: SiteSample[]
    containers?: ContainerSample[]
    apps?: AppSample[]
  }
  ring?: { at: string; cpu: number; mem_pct: number; load1: number; rx_bps: number; tx_bps: number; degraded?: boolean }[]
  sites?: SiteSample[]
  containers?: ContainerSample[]
  apps?: AppSample[]
}

export interface Alert {
  id: string
  type: string
  severity: string
  resource_type: string
  resource_id: string
  resource_name: string
  message: string
  created_at: string
  resolved_at?: string
}

export interface AuditEntry {
  id: number
  action: string
  actor_email?: string
  resource_type: string
  resource_id: string
  result: string
  created_at: string
  metadata?: Record<string, unknown>
}

export const fmtBytes = (n: number): string => {
  if (n <= 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  const i = Math.min(Math.floor(Math.log(n) / Math.log(1024)), units.length - 1)
  return `${(n / 1024 ** i).toFixed(i > 1 ? 1 : 0)} ${units[i]}`
}

export const timeAgo = (iso?: string): string => {
  if (!iso) return '—'
  const s = Math.floor((Date.now() - new Date(iso).getTime()) / 1000)
  if (s < 60) return 'just now'
  const m = Math.floor(s / 60)
  if (m < 60) return `${m} min ago`
  const h = Math.floor(m / 60)
  if (h < 24) return `${h} hour${h > 1 ? 's' : ''} ago`
  const d = Math.floor(h / 24)
  if (d < 30) return `${d} day${d > 1 ? 's' : ''} ago`
  return new Date(iso).toLocaleDateString()
}

// -------------------------------------------------------------- ws client

export type WsConnectionState = 'connecting' | 'open' | 'closed'

type MessageHandler = (msg: any) => void
type StateHandler = (state: WsConnectionState) => void

const START_BACKOFF_MS = 1_000
const MAX_BACKOFF_MS = 30_000

const handlers = new Set<MessageHandler>()
const stateHandlers = new Set<StateHandler>()

let socket: WebSocket | null = null
let retryTimer: number | null = null
let backoffMs = START_BACKOFF_MS
let currentState: WsConnectionState = 'connecting'

function setState(next: WsConnectionState) {
  if (currentState === next) return
  currentState = next
  stateHandlers.forEach((cb) => {
    try {
      cb(next)
    } catch {}
  })
}

function clearRetry() {
  if (retryTimer !== null) {
    window.clearTimeout(retryTimer)
    retryTimer = null
  }
}

function scheduleRetry() {
  if (retryTimer !== null) return
  const delay = backoffMs
  backoffMs = Math.min(backoffMs * 2, MAX_BACKOFF_MS)
  retryTimer = window.setTimeout(() => {
    retryTimer = null
    connect()
  }, delay)
}

function wsUrl(): string {
  const apiBase = (import.meta as any).env?.VITE_API_URL || `${location.protocol}//${location.hostname}:8080`
  const proto = apiBase.startsWith('https') ? 'wss' : 'ws'
  return `${proto}://${apiBase.replace(/^https?:\/\//, '')}/v1/ws`
}

function connect() {
  if (typeof window === 'undefined') return
  if (socket && (socket.readyState === WebSocket.OPEN || socket.readyState === WebSocket.CONNECTING)) return
  clearRetry()
  setState('connecting')
  try {
    socket = new WebSocket(wsUrl())
  } catch {
    socket = null
    scheduleRetry()
    return
  }
  socket.onopen = () => {
    backoffMs = START_BACKOFF_MS
    setState('open')
  }
  socket.onmessage = (ev: MessageEvent) => {
    if (typeof ev.data !== 'string') return
    let msg: any = null
    try {
      msg = JSON.parse(ev.data)
    } catch {
      return
    }
    if (!msg || typeof msg !== 'object') return
    handlers.forEach((cb) => {
      try {
        cb(msg)
      } catch {}
    })
  }
  socket.onclose = () => {
    socket = null
    setState('closed')
    scheduleRetry()
  }
}

export function subscribe(handler: MessageHandler): () => void {
  handlers.add(handler)
  connect()
  return () => {
    handlers.delete(handler)
  }
}

export function onConnectionChange(cb: StateHandler): () => void {
  stateHandlers.add(cb)
  return () => {
    stateHandlers.delete(cb)
  }
}

export const ws = {
  get connectionState(): WsConnectionState {
    return currentState
  },
  subscribe,
  onConnectionChange,
}

// -------------------------------------------------------------- live store

export type FreshnessState = 'LIVE' | 'STALE' | 'OFFLINE'

export interface Freshness {
  state: FreshnessState
  ageMs: number
}

export interface MetricsSnapshot {
  frames: Record<string, SnapshotFrame>
  connected: boolean
}

// Thresholds mirror agentproto.LiveMaxAge/StaleMaxAge (one source of truth
// server-side; these are the UI display constants).
const LIVE_MAX_MS = 15_000
const STALE_MAX_MS = 120_000

function ageOf(iso?: string): number {
  if (!iso) return Number.POSITIVE_INFINITY
  const t = new Date(iso).getTime()
  return Number.isFinite(t) ? Math.max(0, Date.now() - t) : Number.POSITIVE_INFINITY
}

function stateForAge(ageMs: number): FreshnessState {
  if (ageMs <= LIVE_MAX_MS) return 'LIVE'
  if (ageMs <= STALE_MAX_MS) return 'STALE'
  return 'OFFLINE'
}

export function computeFreshness(frame: SnapshotFrame | null | undefined): Freshness {
  if (!frame || !frame.collected_at) return { state: 'OFFLINE', ageMs: 0 }
  const age = ageOf(frame.collected_at)
  if (frame.connected === false || frame.node_state === 'OFFLINE') {
    const serverAge = frame.freshness?.age_ms ?? 0
    return { state: 'OFFLINE', ageMs: Number.isFinite(age) ? Math.max(age, serverAge) : serverAge }
  }
  return { state: stateForAge(age), ageMs: age }
}

export function formatAge(ageMs: number): string {
  if (!Number.isFinite(ageMs)) return 'unknown'
  if (ageMs < 1_000) return `${Math.max(0, Math.round(ageMs))}ms`
  if (ageMs < 60_000) return `${(ageMs / 1_000).toFixed(1)}s`
  const totalSec = Math.floor(ageMs / 1_000)
  const m = Math.floor(totalSec / 60)
  const s = totalSec % 60
  return `${m}m ${s}s`
}

/* Shared store: ONE WebSocket subscription for the whole app. */

let frames: Record<string, SnapshotFrame> = {}
let connected = false
let snapshot: MetricsSnapshot = { frames: {}, connected: false }

const listeners = new Set<() => void>()

function publish() {
  snapshot = { frames, connected }
  listeners.forEach((cb) => cb())
}

function handleMessage(msg: any) {
  if (!msg || typeof msg !== 'object') return
  if (msg.type === 'metrics' && msg.data && msg.data.server_id) {
    const f = msg.data as SnapshotFrame
    const cur = frames[f.server_id]
    if (cur === f) return
    if (cur && (cur.seq ?? 0) > (f.seq ?? 0)) return
    frames = { ...frames, [f.server_id]: f }
    publish()
    return
  }
  if (msg.type === 'server_state' && msg.data && msg.data.server_id) {
    applyServerState(String(msg.data.server_id), msg.data.connected !== false)
  }
}

function applyServerState(serverId: string, conn: boolean) {
  const cur = frames[serverId]
  if (cur) {
    if (cur.connected === conn) return
    frames = {
      ...frames,
      [serverId]: { ...cur, connected: conn, node_state: conn ? 'ONLINE' : 'OFFLINE' },
    }
    publish()
    return
  }
  const now = new Date().toISOString()
  frames = {
    ...frames,
    [serverId]: {
      server_id: serverId,
      seq: 0,
      collected_at: now,
      received_at: now,
      freshness: { state: conn ? 'LIVE' : 'OFFLINE', age_ms: 0 },
      node_state: conn ? 'ONLINE' : 'OFFLINE',
      degraded: false,
      connected: conn,
    },
  }
  publish()
}

let unsubWs: (() => void) | null = null
let unsubState: (() => void) | null = null
let refCount = 0

function storeSubscribe(cb: () => void): () => void {
  listeners.add(cb)
  refCount += 1
  if (refCount === 1) {
    unsubWs = subscribe(handleMessage)
    unsubState = onConnectionChange((state) => {
      const next = state === 'open'
      if (connected === next) return
      connected = next
      publish()
    })
  }
  return () => {
    listeners.delete(cb)
    refCount = Math.max(0, refCount - 1)
    if (refCount === 0) {
      unsubWs?.()
      unsubState?.()
      unsubWs = null
      unsubState = null
    }
  }
}

function getSnapshot(): MetricsSnapshot {
  return snapshot
}

export function useMetrics(): MetricsSnapshot {
  return useSyncExternalStore(storeSubscribe, getSnapshot, getSnapshot)
}

/* REST seeding + legacy shape handling. */

export function seedFrames(list: SnapshotFrame[]) {
  let changed = false
  for (const f of list ?? []) {
    if (!f || !f.server_id) continue
    const cur = frames[f.server_id]
    if (cur && (cur.seq ?? 0) > (f.seq ?? 0)) continue
    if (cur !== f) changed = true
    frames = { ...frames, [f.server_id]: f }
  }
  if (changed) publish()
}

export function legacyToFrame(serverId: string, p: MetricPoint, freshness?: { state?: FreshnessState; age_ms?: number }): SnapshotFrame {
  const age = ageOf(p.collected_at)
  const state: FreshnessState = freshness?.state ?? stateForAge(age)
  const nodeState: 'ONLINE' | 'STALE' | 'OFFLINE' = state === 'LIVE' ? 'ONLINE' : state
  return {
    server_id: serverId,
    seq: 0,
    collected_at: p.collected_at,
    received_at: p.collected_at,
    freshness: { state, age_ms: Number.isFinite(age) ? freshness?.age_ms ?? Math.round(age) : freshness?.age_ms ?? 0 },
    node_state: nodeState,
    degraded: false,
    connected: state !== 'OFFLINE',
    sample: {
      node: {
        cpu_percent: p.cpu_percent ?? 0,
        cpu_cores: 0,
        load1: p.load1 ?? 0,
        load5: 0,
        load15: 0,
        memory_total_bytes: p.memory_total_bytes ?? 0,
        memory_used_bytes: p.memory_used_bytes ?? 0,
        memory_available_bytes: 0,
        swap_total_bytes: 0,
        swap_used_bytes: 0,
        disks: [
          {
            fs: '/',
            mount: '/',
            total_bytes: p.disk_total_bytes ?? 0,
            used_bytes: p.disk_used_bytes ?? 0,
            inodes_total: 0,
            inodes_used: 0,
          },
        ],
        disk_io: { read_bps: 0, write_bps: 0, read_iops: 0, write_iops: 0, total_read_bytes: 0, total_written_bytes: 0 },
        net: { rx_bps: 0, tx_bps: 0, rx_bytes: 0, tx_bytes: 0 },
        tcp_established: 0,
        tcp_total: 0,
        processes: 0,
        uptime_s: 0,
      },
    },
  }
}

function isSnapshotFrame(v: unknown): v is SnapshotFrame {
  return !!v && typeof v === 'object' && 'server_id' in v
}

export function normalizeSingle(res: { metrics?: unknown } | null | undefined, serverId: string): SnapshotFrame | null {
  const m = res?.metrics
  if (!m || typeof m !== 'object') return null
  if (isSnapshotFrame(m)) return m
  return legacyToFrame(serverId, m as MetricPoint)
}

export function normalizeBatch(res: { metrics?: unknown } | null | undefined): SnapshotFrame[] {
  const arr = res?.metrics
  if (!Array.isArray(arr)) return []
  const out: SnapshotFrame[] = []
  for (const el of arr) {
    if (!el || typeof el !== 'object') continue
    if (isSnapshotFrame(el)) {
      out.push(el)
      continue
    }
    const wrap = el as { server_id?: string; metrics?: unknown; freshness?: { state?: FreshnessState; age_ms?: number } }
    if (wrap.metrics && typeof wrap.metrics === 'object') {
      out.push(legacyToFrame(String(wrap.server_id ?? ''), wrap.metrics as MetricPoint, wrap.freshness))
    }
  }
  return out
}

/* Helpers for reading frames. */

export function primaryDisk(node?: NodeSample | null): DiskSample | null {
  const disks = node?.disks ?? []
  if (disks.length === 0) return null
  return disks.find((d) => d.mount === '/') ?? disks.reduce((best, d) => (d.total_bytes > best.total_bytes ? d : best), disks[0])
}

/* useFreshness: shared 1s tick so pushed frames age client-side. */

const tickSubs = new Set<() => void>()
let tickTimer: number | null = null

function ensureTicker() {
  if (tickTimer !== null || typeof window === 'undefined') return
  tickTimer = window.setInterval(() => {
    tickSubs.forEach((cb) => cb())
  }, 1_000)
}

export function useFreshness(frame: SnapshotFrame | null | undefined): Freshness {
  const [, force] = useReducer((x: number) => x + 1, 0)
  useEffect(() => {
    const cb = () => force()
    tickSubs.add(cb)
    ensureTicker()
    return () => {
      tickSubs.delete(cb)
    }
  }, [force])
  return computeFreshness(frame)
}

// ------------------------------------------------- Phase 4 typed API helpers

export interface Redirect {
  id: string
  website_id: string
  from_domain: string
  to_url: string
  status_code: number
  enabled: boolean
  created_at: string
}

export interface FtpAccount {
  id: string
  website_id: string
  protocol: 'ftp' | 'sftp'
  label: string
  user_name: string
  home_subdir: string
  status: 'pending' | 'active' | 'failed'
  error_message?: string
  created_at: string
}

export interface DnsZone {
  id: string
  website_id: string
  domain: string
  ttl: number
  serial: number
  status: string
  soa_mname?: string
  soa_rname?: string
  soa_refresh?: number
  soa_retry?: number
  soa_expire?: number
  soa_minimum?: number
  updated_at?: string
  created_at?: string
}

export interface DnsRecord {
  id: string
  zone_id: string
  name: string
  type: string
  value: string
  ttl: number
  priority?: number
}

export const domainsApi = {
  /** POST /organizations/{org_id}/websites/{website_id}/domains — 201 with the created domain. */
  addAlias: (orgId: string, websiteId: string, body: { domain: string; docroot_suffix?: string }) =>
    req<any>('POST', `/v1/organizations/${orgId}/websites/${websiteId}/domains`, { kind: 'alias', ...body }),
  /** DELETE .../domains/{domain_id} — aliases only (primary stays). */
  remove: (orgId: string, websiteId: string, domainId: string) =>
    req<null>('DELETE', `/v1/organizations/${orgId}/websites/${websiteId}/domains/${domainId}`),
  /** POST .../domains/{domain_id}/verify-dns — enqueues a DNS verification job. */
  verifyDns: (orgId: string, domainId: string) =>
    req<unknown>('POST', `/v1/organizations/${orgId}/domains/${domainId}/verify-dns`),
}

export const redirectsApi = {
  list: (orgId: string, websiteId: string) =>
    req<{ redirects: Redirect[] }>('GET', `/v1/organizations/${orgId}/websites/${websiteId}/redirects`),
  create: (orgId: string, websiteId: string, body: { from_domain: string; to_url: string; status_code: number }) =>
    req<Redirect>('POST', `/v1/organizations/${orgId}/websites/${websiteId}/redirects`, body),
  setEnabled: (orgId: string, redirectId: string, enabled: boolean) =>
    req<Redirect>('PATCH', `/v1/organizations/${orgId}/redirects/${redirectId}`, { enabled }),
  remove: (orgId: string, redirectId: string) =>
    req<null>('DELETE', `/v1/organizations/${orgId}/redirects/${redirectId}`),
}

export const ftpApi = {
  list: (orgId: string, websiteId: string) =>
    req<{ ftp_accounts: FtpAccount[] }>('GET', `/v1/organizations/${orgId}/websites/${websiteId}/ftp-accounts`),
  create: (orgId: string, websiteId: string, body: { protocol: string; label: string; password?: string; home_subdir?: string }) =>
    req<FtpAccount & { password?: string }>('POST', `/v1/organizations/${orgId}/websites/${websiteId}/ftp-accounts`, body),
  rotatePassword: (orgId: string, accountId: string, password?: string) =>
    req<{ password: string }>('POST', `/v1/organizations/${orgId}/ftp-accounts/${accountId}/password`, password ? { password } : {}),
  reveal: (orgId: string, accountId: string) =>
    req<{ password: string }>('POST', `/v1/organizations/${orgId}/ftp-accounts/${accountId}/reveal`),
  remove: (orgId: string, accountId: string) =>
    req<null>('DELETE', `/v1/organizations/${orgId}/ftp-accounts/${accountId}`),
}

export const dnsApi = {
  getZone: (orgId: string, websiteId: string) =>
    req<{ zone: DnsZone; records: DnsRecord[] }>('GET', `/v1/organizations/${orgId}/websites/${websiteId}/dns-zone`),
  createZone: (orgId: string, websiteId: string, body: { domain: string; ttl?: number }) =>
    req<{ zone: DnsZone; records: DnsRecord[] }>('POST', `/v1/organizations/${orgId}/websites/${websiteId}/dns-zone`, body),
  deleteZone: (orgId: string, zoneId: string) =>
    req<null>('DELETE', `/v1/organizations/${orgId}/dns-zones/${zoneId}`),
  publish: (orgId: string, zoneId: string) =>
    req<{ job_id?: string }>('POST', `/v1/organizations/${orgId}/dns-zones/${zoneId}/publish`),
  addRecord: (orgId: string, zoneId: string, body: { name: string; type: string; value: string; ttl?: number; priority?: number }) =>
    req<DnsRecord>('POST', `/v1/organizations/${orgId}/dns-zones/${zoneId}/records`, body),
  updateRecord: (orgId: string, recordId: string, body: { value: string; ttl?: number; priority?: number }) =>
    req<DnsRecord>('PATCH', `/v1/organizations/${orgId}/dns-records/${recordId}`, body),
  deleteRecord: (orgId: string, recordId: string) =>
    req<null>('DELETE', `/v1/organizations/${orgId}/dns-records/${recordId}`),
}

export const lifecycleApi = {
  suspend: (orgId: string, websiteId: string) =>
    req<{ job_id: string }>('POST', `/v1/organizations/${orgId}/websites/${websiteId}/suspend`),
  resume: (orgId: string, websiteId: string) =>
    req<{ job_id: string }>('POST', `/v1/organizations/${orgId}/websites/${websiteId}/resume`),
}
