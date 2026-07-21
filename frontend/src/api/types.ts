export type DownloaderKind = 'qbittorrent' | 'transmission'
export type HealthStatus = 'online' | 'offline' | 'degraded' | 'unknown'
export type OperationStatus = 'success' | 'failed' | 'warning' | 'running' | 'idle'

export interface AuthSession {
  authenticated?: boolean
  accessToken?: string
  csrfToken?: string
  expiresAt?: string
  username?: string
  user?: {
    username: string
  } | null
}

export interface LoginInput {
  username: string
  password: string
}

export interface OverviewData {
  downloaderCount: number
  onlineDownloaderCount: number
  groupCount: number
  duplicateGroupCount: number
  instanceCount: number
  reclaimableBytes: number
  lastSyncAt?: string
  syncStatus: OperationStatus
  recentErrorCount: number
}

export interface TorrentFile {
  path: string
  size: number
}

export interface TorrentInstance {
  id: string
  downloaderId: string
  downloaderName: string
  downloaderKind: DownloaderKind
  hash: string
  name: string
  savePath: string
  totalSize: number
  progress: number
  ratio: number
  state: string
  sites: string[]
  trackerHost?: string
  addedAt?: string
  completedAt?: string
}

export interface GroupSiteSummary {
  key: string
  label: string
  mapped: boolean
}

export interface TorrentGroup {
  id: string
  name: string
  sites: GroupSiteSummary[]
  canonicalPath: string
  totalSize: number
  fileCount: number
  files: TorrentFile[]
  instances: TorrentInstance[]
  trackerHost?: string
  groupingMethod: 'automatic' | 'manual'
  locked?: boolean
  version: number
  taskCount: number
  siteCount: number
  downloaderCount: number
  dataCopyCount: number
  confidence: 'tentative' | 'verified' | 'manual'
  stale: boolean
  oldestAddedAt?: string
  updatedAt: string
	operationId?: string
}

export type GroupSortBy = 'oldest_added_at' | 'instance_count' | 'size' | 'name'
export type SortOrder = 'asc' | 'desc'

export interface GroupSortRule {
  field: GroupSortBy
  order: SortOrder
}

export type GroupQueryCombinator = 'and' | 'or'
export type GroupQueryField =
  | 'group_name'
  | 'instance_name'
  | 'path'
  | 'size'
  | 'instance_count'
  | 'site_count'
  | 'downloader_count'
  | 'data_copy_count'
  | 'oldest_added_at'
  | 'updated_at'
  | 'site'
  | 'downloader'
  | 'state'
  | 'locked'
  | 'grouping_method'
  | 'confidence'
  | 'stale'
  | 'has_unmapped_tracker'
export type GroupQueryOperator =
  | 'contains'
  | 'not_contains'
  | 'starts_with'
  | 'ends_with'
  | 'eq'
  | 'ne'
  | 'lt'
  | 'lte'
  | 'gt'
  | 'gte'
  | 'between'
  | 'before'
  | 'after'
  | 'on'
  | 'on_or_before'
  | 'on_or_after'
  | 'in'
  | 'contains_all'
  | 'not_in'
  | 'is_empty'
  | 'is_not_empty'
export type GroupSizeUnit = 'B' | 'KiB' | 'MiB' | 'GiB' | 'TiB'
export type GroupQueryValue = string | number | boolean | string[] | number[]

export interface GroupQueryCondition {
  type: 'condition'
  field: GroupQueryField
  operator: GroupQueryOperator
  value?: GroupQueryValue
  /** UI-only hint; the API encoder deliberately removes it. */
  displayUnit?: GroupSizeUnit
}

export interface GroupQueryGroup {
  type: 'group'
  combinator: GroupQueryCombinator
  scope?: 'instance'
  negated?: boolean
  children: GroupQueryNode[]
}

export type GroupQueryNode = GroupQueryCondition | GroupQueryGroup

export interface GroupQueryFilter {
  version: 1
  root: GroupQueryGroup
}

export interface GroupFilters {
  query?: string
  filter?: GroupQueryFilter
  status?: string
  downloaderId?: string
  sorts?: GroupSortRule[]
  /** @deprecated Use sorts for ordered multi-column sorting. */
  sortBy?: GroupSortBy
  /** @deprecated Use sorts for ordered multi-column sorting. */
  sortOrder?: SortOrder
  page: number
  pageSize: number
}

export interface PagedResponse<T> {
  items: T[]
  total: number
  page: number
  pageSize: number
}

export interface DeletePlanInput {
  groupId: string
  instanceIds: string[]
}

export interface DeletePlan {
  id: string
  groupId: string
  selectedInstanceIds: string[]
  executable: boolean
  steps: Array<{
    order: number
    instanceId: string
    downloaderId: string
    contentGroupId: string
    dataGroupId: string
    deleteData: boolean
  }>
  blockers: Array<{
    code: string
    message: string
    instanceId?: string
    instanceName?: string
    downloaderId?: string
    downloaderName?: string
    path?: string
  }>
}

export interface MergeGroupsInput {
  displayName: string
  groups: Array<{ id: string; version: number }>
}

export interface UndoGroupOperationResult {
  operationId: string
  operationType: 'merge' | 'split' | 'move'
  restoredGroupIds: string[]
  retiredGroupIds: string[]
}

export interface DeleteJob {
  id: string
  planId: string
  status: 'pending' | 'executing' | 'verifying' | 'completed' | 'failed' | 'uncertain'
  error?: string
  createdAt: string
}

export interface Downloader {
  id: string
  name: string
  kind: DownloaderKind
  baseUrl: string
  enabled: boolean
  health: HealthStatus
  version?: string
  storageId: string
  storageName: string
  pathMappings: PathMapping[]
  lastSyncAt?: string
  lastError?: string
}

export interface DownloaderInput {
  name: string
  kind: DownloaderKind
  baseUrl: string
  username: string
  password: string
  storageId?: string
  storageName: string
  pathMappings: PathMappingInput[]
  enabled: boolean
}

export interface PathMapping {
  id: string
  sourcePrefix: string
  targetPrefix: string
  position: number
}

export interface PathMappingInput {
  sourcePrefix: string
  targetPrefix: string
}

export interface ConnectionTestResult {
  ok: boolean
  latencyMs?: number
  version?: string
  message: string
}

export interface TrackerRule {
  id: string
  hostPattern: string
  pathPrefix: string
  siteId: string
  siteName: string
  displayName: string
  source: 'builtin' | 'custom'
  priority: number
  createdAt: string
  updatedAt: string
}

export interface TrackerRuleInput {
  hostPattern: string
  pathPrefix: string
  siteName: string
  displayName: string
}

export interface UnmappedTrackerIdentity {
  hostIdentity: string
  pathHint: string
  instanceCount: number
  groupCount: number
  lastSeenAt: string
}

export type TrackerMappingStatus = 'all' | 'mapped' | 'unmapped'
export type TrackerMatchType = 'exact' | 'registrable_domain' | 'keyword' | 'custom'

export interface TrackerMapping {
  hostIdentity: string
  pathHint: string
  mapped: boolean
  matchType?: TrackerMatchType
  siteId?: string
  siteName?: string
  displayName?: string
  instanceCount: number
  groupCount: number
  lastSeenAt: string
}

export interface TrackerMappingFilters {
  query?: string
  status: TrackerMappingStatus
  matchType: 'all' | TrackerMatchType
  page: number
  pageSize: number
}

export interface IYUUSiteFilters {
  query?: string
  status: TrackerMappingStatus
  page: number
  pageSize: number
}

export interface IYUUSite {
  remoteId: number
  slug: string
  nickname: string
  baseUrl: string
  isHttps: number
  cookieRequired: boolean
  lastSeenAt: string
  stale: boolean
  mapped: boolean
  mappingCount: number
}

export interface IYUUCatalog extends PagedResponse<IYUUSite> {
  running: boolean
  nextAllowedAt?: string
  state: {
    lastAttemptAt?: string
    lastSuccessAt?: string
    lastError: string
    siteCount: number
  }
}

export interface IYUUSyncResult {
  siteCount: number
  fetchedAt: string
}

export interface SyncStatus {
  status: OperationStatus
  running: boolean
  startedAt?: string
  completedAt?: string
  scannedInstances: number
  updatedGroups: number
  error?: string
}

export interface AuditEvent {
  id: string
  action: string
  resourceType: string
  resourceName: string
  status: Exclude<OperationStatus, 'running' | 'idle'>
  message: string
  actor: string
  occurredAt: string
}

export interface AuditFilters {
  status?: string
  action?: string
  page: number
  pageSize: number
}
