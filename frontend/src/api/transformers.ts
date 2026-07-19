import type {
  AuditEvent,
  AuthSession,
  ConnectionTestResult,
  DeleteJob,
  DeletePlan,
  Downloader,
  OverviewData,
  PagedResponse,
  SyncStatus,
  TorrentGroup,
  TorrentInstance,
  TrackerRule,
  UnmappedTrackerIdentity,
} from './types'

export interface WirePagedResponse<T> {
  items?: T[] | null
  groups?: T[] | null
  events?: T[] | null
  total?: number
  page?: number
  page_size?: number
  limit?: number
  offset?: number
}

export interface WireAuthSession {
  authenticated?: boolean
  access_token?: string
  csrf_token?: string
  expires_at?: string
  username?: string
  subject?: string
  user?: { username?: string } | null
}

export interface WireOverview {
  logical_resources: number
  torrent_tasks: number
  logical_bytes: number
  raw_task_bytes: number
  known_sites: number
  unknown_trackers: number
  online_downloaders: number
  total_downloaders: number
  stale_groups: number
  last_sync_at?: string
}

export interface WireTorrentGroup {
  id: string
  name: string
  size_bytes: number
  task_count: number
  site_count: number
  downloader_count: number
  data_copy_count: number
  confidence: string
  mode: string
  locked: boolean
  version: number
  stale: boolean
  oldest_added_at?: string | null
  updated_at: string
	operation_id?: string
  instances?: WireTorrentInstance[] | null
}

export interface WireTorrentInstance {
  id: string
  downloader_id: string
  downloader_name: string
  downloader_kind: 'qbittorrent' | 'transmission'
  stable_hash_key: string
  name: string
  canonical_path: string
  storage_id: string
  wanted_bytes: number
  data_group_id: string
  assignment_source: string
  status: string
  progress: number
  ratio: number
  added_at?: string | null
  updated_at: string
  last_sync_at?: string | null
  sites?: string[] | null
}

export interface WireDownloader {
  id: string
  name: string
  kind: 'qbittorrent' | 'transmission'
  base_url: string
  storage_id: string
  storage_name: string
  path_mappings?: Array<{
    id: string
    source_prefix: string
    target_prefix: string
    position: number
  }> | null
  enabled: boolean
  online: boolean
  version?: string
  last_success_at?: string | null
  last_error?: string
}

export interface WireTrackerRule {
  id: string
  host_pattern: string
  path_prefix: string
  site_id: string
  site_name: string
  display_name: string
  source: string
  priority: number
  created_at: string
  updated_at: string
}

export interface WireUnmappedTrackerIdentity {
  host_identity: string
  path_hint: string
  instance_count: number
  group_count: number
  last_seen_at: string
}

export interface WireSyncStatus {
  status?: string
  running?: boolean
  started_at?: string
  completed_at?: string
  scanned_instances?: number
  updated_groups?: number
  seen_count?: number
  changed_count?: number
  error?: string
}

export interface WireAuditEvent {
  id: string
  actor?: string
  action: string
  target_type?: string
  target_id?: string
  details?: Record<string, unknown>
  created_at: string
  status?: string
  message?: string
}

export interface WireDeletePlan {
  id: string
  selected_instance_ids?: string[]
  executable: boolean
  steps?: Array<{
    order: number
    instance_id: string
    downloader_id: string
    content_group_id: string
    data_group_id: string
    delete_data: boolean
  }> | null
  blockers?: Array<{
    code: string
    message: string
    instance_id?: string
    downloader_id?: string
  }> | null
}

export interface WireDeleteJob {
  id: string
  plan_id: string
  status: DeleteJob['status']
  error?: string
  created_at: string
}

export interface GroupSummary {
  instanceCount: number
  duplicateCount: number
  downloaderNames: string[]
  reclaimableBytes: number
  averageProgress: number
}

const operationStatus = (value?: string): SyncStatus['status'] => {
  switch (value) {
    case 'success':
    case 'completed':
      return 'success'
    case 'failed':
    case 'error':
      return 'failed'
    case 'warning':
    case 'partial':
      return 'warning'
    case 'running':
      return 'running'
    default:
      return 'idle'
  }
}

export const toAuthSession = (wire: WireAuthSession): AuthSession => ({
  authenticated: wire.authenticated,
  accessToken: wire.access_token,
  csrfToken: wire.csrf_token,
  expiresAt: wire.expires_at,
  username: wire.username ?? wire.subject ?? wire.user?.username,
  user: wire.username || wire.subject || wire.user?.username
    ? { username: wire.username ?? wire.subject ?? wire.user?.username ?? '' }
    : undefined,
})

export const toOverview = (wire: WireOverview): OverviewData => ({
  downloaderCount: wire.total_downloaders,
  onlineDownloaderCount: wire.online_downloaders,
  groupCount: wire.logical_resources,
  duplicateGroupCount: Math.max(0, wire.torrent_tasks - wire.logical_resources),
  instanceCount: wire.torrent_tasks,
  reclaimableBytes: Math.max(0, wire.raw_task_bytes - wire.logical_bytes),
  lastSyncAt: wire.last_sync_at,
  syncStatus: wire.stale_groups > 0 ? 'warning' : 'success',
  recentErrorCount: wire.stale_groups,
})

const toTorrentInstance = (wire: WireTorrentInstance): TorrentInstance => ({
  id: wire.id,
  downloaderId: wire.downloader_id,
  downloaderName: wire.downloader_name,
  downloaderKind: wire.downloader_kind,
  hash: wire.stable_hash_key,
  name: wire.name,
  savePath: wire.canonical_path,
  totalSize: wire.wanted_bytes,
  progress: wire.progress,
  ratio: wire.ratio,
  state: wire.status,
  sites: wire.sites ?? [],
  trackerHost: wire.sites?.join(', '),
  addedAt: wire.added_at ?? undefined,
})

export const toTorrentGroup = (wire: WireTorrentGroup): TorrentGroup => {
  const instances = (wire.instances ?? []).map(toTorrentInstance)
  return {
    id: wire.id,
    name: wire.name,
    canonicalPath: instances[0]?.savePath ?? '',
    totalSize: wire.size_bytes,
    fileCount: 0,
    files: [],
    instances,
    trackerHost: undefined,
    groupingMethod: wire.mode === 'manual' ? 'manual' : 'automatic',
    locked: wire.locked,
    version: wire.version,
    taskCount: wire.task_count,
    siteCount: wire.site_count,
    downloaderCount: wire.downloader_count,
    dataCopyCount: wire.data_copy_count,
    confidence: wire.confidence === 'verified' || wire.confidence === 'manual' ? wire.confidence : 'tentative',
    stale: wire.stale,
    oldestAddedAt: wire.oldest_added_at ?? undefined,
    updatedAt: wire.updated_at,
		operationId: wire.operation_id,
  }
}

export const toDownloader = (wire: WireDownloader): Downloader => ({
  id: wire.id,
  name: wire.name,
  kind: wire.kind,
  baseUrl: wire.base_url,
  enabled: wire.enabled,
  health: !wire.enabled ? 'unknown' : wire.online ? 'online' : wire.last_error ? 'degraded' : 'offline',
  version: wire.version,
  storageId: wire.storage_id,
  storageName: wire.storage_name,
  pathMappings: (wire.path_mappings ?? []).map((mapping) => ({
    id: mapping.id,
    sourcePrefix: mapping.source_prefix,
    targetPrefix: mapping.target_prefix,
    position: mapping.position,
  })),
  lastSyncAt: wire.last_success_at ?? undefined,
  lastError: wire.last_error,
})

export const toTrackerRule = (wire: WireTrackerRule): TrackerRule => ({
  id: wire.id,
  hostPattern: wire.host_pattern,
  pathPrefix: wire.path_prefix,
  siteId: wire.site_id,
  siteName: wire.site_name,
  displayName: wire.display_name,
  source: wire.source === 'custom' ? 'custom' : 'builtin',
  priority: wire.priority,
  createdAt: wire.created_at,
  updatedAt: wire.updated_at,
})

export const toUnmappedTrackerIdentity = (wire: WireUnmappedTrackerIdentity): UnmappedTrackerIdentity => ({
  hostIdentity: wire.host_identity,
  pathHint: wire.path_hint,
  instanceCount: wire.instance_count,
  groupCount: wire.group_count,
  lastSeenAt: wire.last_seen_at,
})

export const toConnectionTest = (wire: Record<string, unknown>): ConnectionTestResult => ({
  ok: wire.ok === true,
  latencyMs: typeof wire.latency_ms === 'number' ? wire.latency_ms : undefined,
  version: typeof wire.version === 'string' ? wire.version : undefined,
  message: typeof wire.message === 'string' ? wire.message : wire.ok === true ? '连接成功' : '连接失败',
})

export const toSyncStatus = (wire: WireSyncStatus): SyncStatus => ({
  status: operationStatus(wire.status ?? (wire.running ? 'running' : undefined)),
  running: wire.running ?? wire.status === 'running',
  startedAt: wire.started_at,
  completedAt: wire.completed_at,
  scannedInstances: wire.scanned_instances ?? wire.seen_count ?? 0,
  updatedGroups: wire.updated_groups ?? wire.changed_count ?? 0,
  error: wire.error,
})

export const toAuditEvent = (wire: WireAuditEvent): AuditEvent => {
  const details = wire.details ?? {}
  const error = typeof details.error === 'string' ? details.error : undefined
  const message =
    wire.message ??
    (typeof details.message === 'string' ? details.message : undefined) ??
    error ??
    ''
  return {
    id: wire.id,
    action: wire.action,
    resourceType: wire.target_type ?? '',
    resourceName: wire.target_id ?? '',
    status: operationStatus(wire.status ?? (error ? 'failed' : 'success')) as AuditEvent['status'],
    message,
    actor: wire.actor ?? 'system',
    occurredAt: wire.created_at,
  }
}

export const toDeletePlan = (wire: WireDeletePlan, groupId: string): DeletePlan => ({
  id: wire.id,
  groupId,
  selectedInstanceIds: wire.selected_instance_ids ?? [],
  executable: wire.executable,
  steps: (wire.steps ?? []).map((step) => ({
    order: step.order,
    instanceId: step.instance_id,
    downloaderId: step.downloader_id,
    contentGroupId: step.content_group_id,
    dataGroupId: step.data_group_id,
    deleteData: step.delete_data,
  })),
  blockers: (wire.blockers ?? []).map((blocker) => ({
    code: blocker.code,
    message: blocker.message,
    instanceId: blocker.instance_id,
    downloaderId: blocker.downloader_id,
  })),
})

export const toDeleteJob = (wire: WireDeleteJob): DeleteJob => ({
  id: wire.id,
  planId: wire.plan_id,
  status: wire.status,
  error: wire.error,
  createdAt: wire.created_at,
})

export const summarizeGroup = (group: TorrentGroup): GroupSummary => {
  const uniqueDownloaders = new Set(group.instances.map((instance) => instance.downloaderName))
  const progressTotal = group.instances.reduce((sum, instance) => sum + instance.progress, 0)
  const instanceCount = group.instances.length || group.taskCount
  return {
    instanceCount,
    duplicateCount: Math.max(0, instanceCount - 1),
    downloaderNames: [...uniqueDownloaders].sort((left, right) => left.localeCompare(right, 'zh-CN')),
    reclaimableBytes: Math.max(0, group.dataCopyCount - 1) * group.totalSize,
    averageProgress: group.instances.length ? progressTotal / group.instances.length : 0,
  }
}

export const normalizePagedResponse = <T,>(
  payload: PagedResponse<T> | T[],
  page = 1,
  pageSize = 20,
): PagedResponse<T> =>
  Array.isArray(payload)
    ? { items: payload, total: payload.length, page, pageSize }
    : payload

export const normalizeWirePage = <T, R>(
  payload: WirePagedResponse<T> | T[],
  mapper: (item: T) => R,
  page = 1,
  pageSize = 20,
): PagedResponse<R> => {
  if (Array.isArray(payload)) {
    return { items: payload.map(mapper), total: payload.length, page, pageSize }
  }
  const items = payload.items ?? payload.groups ?? payload.events ?? []
  return {
    items: items.map(mapper),
    total: payload.total ?? items.length,
    page: payload.page ?? Math.floor((payload.offset ?? 0) / (payload.limit ?? pageSize)) + 1,
    pageSize: payload.page_size ?? payload.limit ?? pageSize,
  }
}

export const filterInstanceOptions = (instances: TorrentInstance[], query: string): TorrentInstance[] => {
  const keyword = query.trim().toLocaleLowerCase('zh-CN')
  if (!keyword) return instances
  return instances.filter((instance) =>
    [instance.name, instance.hash, instance.savePath, instance.downloaderName].some((value) =>
      value.toLocaleLowerCase('zh-CN').includes(keyword),
    ),
  )
}
