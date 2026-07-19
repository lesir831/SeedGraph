import {
  normalizeWirePage,
  toAuditEvent,
  toAuthSession,
  toConnectionTest,
  toDeleteJob,
  toDeletePlan,
  toDownloader,
  toOverview,
  toSyncStatus,
  toTorrentGroup,
  toTrackerMapping,
  toTrackerRule,
  toUnmappedTrackerIdentity,
  type WireAuditEvent,
  type WireAuthSession,
  type WireDeleteJob,
  type WireDeletePlan,
  type WireDownloader,
  type WireOverview,
  type WirePagedResponse,
  type WireSyncStatus,
  type WireTorrentGroup,
  type WireTrackerMapping,
  type WireTrackerRule,
  type WireUnmappedTrackerIdentity,
} from './transformers'
import type {
  AuditEvent,
  AuditFilters,
  AuthSession,
  ConnectionTestResult,
  DeleteJob,
  DeletePlan,
  DeletePlanInput,
  Downloader,
  DownloaderInput,
  GroupFilters,
	IYUUCatalog,
	IYUUSiteFilters,
	IYUUSyncResult,
  LoginInput,
  MergeGroupsInput,
  OverviewData,
  PagedResponse,
  SyncStatus,
  TorrentGroup,
  TrackerMapping,
  TrackerMappingFilters,
  TrackerRule,
  TrackerRuleInput,
  UnmappedTrackerIdentity,
	UndoGroupOperationResult,
} from './types'

const API_ROOT = '/api/v1'
const TOKEN_KEY = 'seedgraph.access-token'
const CSRF_KEY = 'seedgraph.csrf-token'
const AUTH_KEY = 'seedgraph.authenticated'

interface ApiEnvelope<T> {
  data: T
}

interface RequestOptions extends RequestInit {
  query?: Record<string, string | number | boolean | undefined>
}

export class ApiError extends Error {
  constructor(
    message: string,
    readonly status: number,
    readonly code?: string,
    readonly details?: unknown,
  ) {
    super(message)
    this.name = 'ApiError'
  }
}

const isEnvelope = <T,>(value: unknown): value is ApiEnvelope<T> =>
  typeof value === 'object' && value !== null && 'data' in value

const makeUrl = (path: string, query?: RequestOptions['query']) => {
  const url = new URL(`${API_ROOT}${path}`, window.location.origin)
  Object.entries(query ?? {}).forEach(([key, value]) => {
    if (value !== undefined && value !== '') url.searchParams.set(key, String(value))
  })
  return `${url.pathname}${url.search}`
}

const request = async <T,>(path: string, options: RequestOptions = {}): Promise<T> => {
  const token = sessionStorage.getItem(TOKEN_KEY)
  const csrfToken = sessionStorage.getItem(CSRF_KEY)
  const method = (options.method ?? 'GET').toUpperCase()
  const headers = new Headers(options.headers)
  headers.set('Accept', 'application/json')
  if (options.body && !headers.has('Content-Type')) headers.set('Content-Type', 'application/json')
  if (token) headers.set('Authorization', `Bearer ${token}`)
  if (csrfToken && !['GET', 'HEAD', 'OPTIONS'].includes(method)) headers.set('X-CSRF-Token', csrfToken)

  const response = await fetch(makeUrl(path, options.query), {
    ...options,
    credentials: 'include',
    headers,
  })

  const contentType = response.headers.get('content-type') ?? ''
  const payload: unknown = contentType.includes('application/json') ? await response.json() : undefined

  if (!response.ok) {
    const errorBody = (payload ?? {}) as Record<string, unknown>
    const nestedError = typeof errorBody.error === 'object' && errorBody.error !== null
      ? errorBody.error as Record<string, unknown>
      : undefined
    const message =
      (typeof errorBody.message === 'string' && errorBody.message) ||
      (typeof errorBody.error === 'string' && errorBody.error) ||
      (typeof nestedError?.message === 'string' && nestedError.message) ||
      `请求失败 (${response.status})`
    if (response.status === 401) window.dispatchEvent(new Event('seedgraph:unauthorized'))
    throw new ApiError(
      message,
      response.status,
      typeof (nestedError?.code ?? errorBody.code) === 'string'
        ? String(nestedError?.code ?? errorBody.code)
        : undefined,
      nestedError?.details ?? errorBody.details,
    )
  }

  if (response.status === 204 || payload === undefined) return undefined as T
  return isEnvelope<T>(payload) ? payload.data : (payload as T)
}

const json = (value: unknown) => JSON.stringify(value)

export const authStorage = {
  isAuthenticated: () => sessionStorage.getItem(AUTH_KEY) === 'true',
  save(session: AuthSession) {
    sessionStorage.setItem(AUTH_KEY, 'true')
    if (session.accessToken) sessionStorage.setItem(TOKEN_KEY, session.accessToken)
    if (session.csrfToken) sessionStorage.setItem(CSRF_KEY, session.csrfToken)
  },
  clear() {
    sessionStorage.removeItem(AUTH_KEY)
    sessionStorage.removeItem(TOKEN_KEY)
    sessionStorage.removeItem(CSRF_KEY)
  },
}

const getPage = (page: number, pageSize: number) => ({
  limit: pageSize,
  offset: (page - 1) * pageSize,
})

export const api = {
  login: async (input: LoginInput): Promise<AuthSession> => {
    const wire = await request<WireAuthSession>('/auth/login', {
      method: 'POST',
      body: json(input),
    })
    return toAuthSession(wire)
  },
  logout: () => request<void>('/auth/logout', { method: 'POST' }),
  getSession: async (): Promise<AuthSession> => toAuthSession(await request<WireAuthSession>('/auth/session')),

  getOverview: async (): Promise<OverviewData> => toOverview(await request<WireOverview>('/overview')),

  getGroups: async (filters: GroupFilters): Promise<PagedResponse<TorrentGroup>> => {
    const wire = await request<WirePagedResponse<WireTorrentGroup> | WireTorrentGroup[]>('/torrent-groups', {
      query: {
        q: filters.query,
        status: filters.status === 'all' ? undefined : filters.status,
        downloader_id: filters.downloaderId,
        missing_site: filters.missingSite,
        max_site_count: filters.maxSiteCount,
        stale: filters.stale,
        sort_by: filters.sortBy,
        sort_order: filters.sortOrder,
        ...getPage(filters.page, filters.pageSize),
      },
    })
    return normalizeWirePage(wire, toTorrentGroup, filters.page, filters.pageSize)
  },
  getGroup: async (id: string): Promise<TorrentGroup> =>
    toTorrentGroup(await request<WireTorrentGroup>(`/torrent-groups/${encodeURIComponent(id)}`)),
  mergeGroups: async (input: MergeGroupsInput): Promise<TorrentGroup> => {
    const expectedVersions = Object.fromEntries(input.groups.map((group) => [group.id, group.version]))
    const wire = await request<WireTorrentGroup>('/torrent-groups/merge', {
      method: 'POST',
      body: json({
        group_ids: input.groups.map((group) => group.id),
        expected_versions: expectedVersions,
        display_name: input.displayName,
      }),
    })
    return toTorrentGroup(wire)
  },
  splitGroup: async (group: Pick<TorrentGroup, 'id' | 'version'>, instanceIds: string[]): Promise<TorrentGroup> => {
    const wire = await request<WireTorrentGroup>(`/torrent-groups/${encodeURIComponent(group.id)}/split`, {
      method: 'POST',
      body: json({ expected_version: group.version, instance_ids: instanceIds }),
    })
    return toTorrentGroup(wire)
  },
	moveGroupMember: async (
		source: Pick<TorrentGroup, 'id' | 'version'>,
		instanceId: string,
		target: Pick<TorrentGroup, 'id' | 'version'>,
	): Promise<TorrentGroup> => {
		const wire = await request<WireTorrentGroup>(
			`/torrent-groups/${encodeURIComponent(source.id)}/members/${encodeURIComponent(instanceId)}/move`,
			{
				method: 'POST',
				body: json({
					target_group_id: target.id,
					expected_source_version: source.version,
					expected_target_version: target.version,
				}),
			},
		)
		return toTorrentGroup(wire)
	},
	undoGroupOperation: async (operationId: string): Promise<UndoGroupOperationResult> => {
		const wire = await request<{
			operation_id: string
			operation_type: UndoGroupOperationResult['operationType']
			restored_group_ids: string[] | null
			retired_group_ids: string[] | null
		}>(`/group-operations/${encodeURIComponent(operationId)}/undo`, { method: 'POST' })
		return {
			operationId: wire.operation_id,
			operationType: wire.operation_type,
			restoredGroupIds: wire.restored_group_ids ?? [],
			retiredGroupIds: wire.retired_group_ids ?? [],
		}
	},
  setGroupLock: (group: Pick<TorrentGroup, 'id' | 'version'>, locked: boolean): Promise<void> =>
    request<void>(`/torrent-groups/${encodeURIComponent(group.id)}/lock`, {
      method: 'PATCH',
      body: json({ expected_version: group.version, locked }),
    }),
  restoreAutomaticGrouping: (group: Pick<TorrentGroup, 'id' | 'version'>): Promise<void> =>
    request<void>(`/torrent-groups/${encodeURIComponent(group.id)}/restore-auto`, {
      method: 'POST',
      body: json({ expected_version: group.version }),
    }),

  // Deletion is deliberately two-phase. The server computes physical-data
  // reference counts in a versioned plan; a later job may execute only that plan.
  createDeletePlan: async (input: DeletePlanInput): Promise<DeletePlan> => {
    const wire = await request<WireDeletePlan | { plan: WireDeletePlan }>('/delete-plans', {
      method: 'POST',
      body: json({ instance_ids: input.instanceIds }),
    })
    return toDeletePlan('plan' in wire ? wire.plan : wire, input.groupId)
  },
  createDeleteJob: async (plan: Pick<DeletePlan, 'id'>): Promise<DeleteJob> =>
    toDeleteJob(await request<WireDeleteJob>('/delete-jobs', {
      method: 'POST',
      headers: { 'Idempotency-Key': `delete-plan:${plan.id}` },
      body: json({ plan_id: plan.id }),
    })),
  getDeleteJob: async (id: string): Promise<DeleteJob> =>
    toDeleteJob(await request<WireDeleteJob>(`/delete-jobs/${encodeURIComponent(id)}`)),

  getDownloaders: async (): Promise<Downloader[]> =>
    ((await request<WireDownloader[] | null>('/downloaders')) ?? []).map(toDownloader),
  createDownloader: async (input: DownloaderInput): Promise<Downloader> => {
    const wire = await request<WireDownloader>('/downloaders', {
      method: 'POST',
      body: json({
        name: input.name,
        kind: input.kind,
        base_url: input.baseUrl,
        username: input.username,
        password: input.password,
        storage_id: input.storageId,
        storage_name: input.storageName,
        path_mappings: input.pathMappings.map((mapping) => ({
          source_prefix: mapping.sourcePrefix,
          target_prefix: mapping.targetPrefix,
        })),
        enabled: input.enabled,
      }),
    })
    return toDownloader(wire)
  },
  deleteDownloader: (id: string) =>
    request<void>(`/downloaders/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  testDownloader: async (id: string): Promise<ConnectionTestResult> =>
    toConnectionTest(await request<Record<string, unknown>>(`/downloaders/${encodeURIComponent(id)}/test`, { method: 'POST' })),
  syncDownloader: async (id: string): Promise<SyncStatus> =>
    toSyncStatus(await request<WireSyncStatus>(`/downloaders/${encodeURIComponent(id)}/sync`, { method: 'POST' })),

  getTrackerRules: async (): Promise<TrackerRule[]> =>
    ((await request<WireTrackerRule[] | null>('/tracker-rules')) ?? []).map(toTrackerRule),
  getUnmappedTrackers: async (): Promise<UnmappedTrackerIdentity[]> =>
    ((await request<WireUnmappedTrackerIdentity[] | null>('/tracker-rules/unmapped')) ?? []).map(toUnmappedTrackerIdentity),
  getTrackerMappings: async (filters: TrackerMappingFilters): Promise<PagedResponse<TrackerMapping>> => {
    const wire = await request<WirePagedResponse<WireTrackerMapping> | WireTrackerMapping[]>('/tracker-rules/mappings', {
      query: {
        q: filters.query,
        status: filters.status === 'all' ? undefined : filters.status,
        match_type: filters.matchType === 'all' ? undefined : filters.matchType,
        ...getPage(filters.page, filters.pageSize),
      },
    })
    return normalizeWirePage(wire, toTrackerMapping, filters.page, filters.pageSize)
  },
  createTrackerRule: async (input: TrackerRuleInput): Promise<TrackerRule> => {
    const wire = await request<WireTrackerRule>('/tracker-rules', {
      method: 'POST',
      body: json({
        host_pattern: input.hostPattern,
        path_prefix: input.pathPrefix,
        site_name: input.siteName,
        display_name: input.displayName,
      }),
    })
    return toTrackerRule(wire)
  },
  deleteTrackerRule: (id: string) =>
    request<void>(`/tracker-rules/${encodeURIComponent(id)}`, { method: 'DELETE' }),
	getIYUUSites: async (filters: IYUUSiteFilters): Promise<IYUUCatalog> => {
		const wire = await request<{
			items: Array<{
				remote_id: number
				slug: string
				nickname: string
				base_url: string
				is_https: number
				cookie_required: boolean
				last_seen_at: string
				stale: boolean
				mapped: boolean
				mapping_count: number
			}> | null
			total?: number
			limit?: number
			offset?: number
			running: boolean
			next_allowed_at?: string | null
			state: {
				last_attempt_at?: string | null
				last_success_at?: string | null
				last_error: string
				site_count: number
			}
		}>('/sites', {
			query: {
				q: filters.query,
				status: filters.status === 'all' ? undefined : filters.status,
				...getPage(filters.page, filters.pageSize),
			},
		})
		return {
			items: (wire.items ?? []).map((site) => ({
				remoteId: site.remote_id,
				slug: site.slug,
				nickname: site.nickname,
				baseUrl: site.base_url,
				isHttps: site.is_https,
				cookieRequired: site.cookie_required,
				lastSeenAt: site.last_seen_at,
				stale: site.stale,
				mapped: site.mapped,
				mappingCount: site.mapping_count,
			})),
			total: wire.total ?? wire.items?.length ?? 0,
			page: Math.floor((wire.offset ?? 0) / (wire.limit ?? filters.pageSize)) + 1,
			pageSize: wire.limit ?? filters.pageSize,
			running: wire.running,
			nextAllowedAt: wire.next_allowed_at ?? undefined,
			state: {
				lastAttemptAt: wire.state.last_attempt_at ?? undefined,
				lastSuccessAt: wire.state.last_success_at ?? undefined,
				lastError: wire.state.last_error,
				siteCount: wire.state.site_count,
			},
		}
	},
	syncIYUUSites: async (): Promise<IYUUSyncResult> => {
		const wire = await request<{ site_count: number; fetched_at: string }>('/sites/sync/iyuu', { method: 'POST' })
		return { siteCount: wire.site_count, fetchedAt: wire.fetched_at }
	},

  getSyncStatus: async (): Promise<SyncStatus> =>
    toSyncStatus(await request<WireSyncStatus>('/sync/status')),
  runSync: async (): Promise<SyncStatus> =>
    toSyncStatus(await request<WireSyncStatus>('/sync/run', { method: 'POST' })),
  getAuditEvents: async (filters: AuditFilters): Promise<PagedResponse<AuditEvent>> => {
    const wire = await request<WirePagedResponse<WireAuditEvent> | WireAuditEvent[]>('/audit-events', {
      query: { limit: 200, offset: 0 },
    })
    const recent = normalizeWirePage(wire, toAuditEvent, 1, 200).items
    const start = (filters.page - 1) * filters.pageSize
    return {
      items: recent.slice(start, start + filters.pageSize),
      total: recent.length,
      page: filters.page,
      pageSize: filters.pageSize,
    }
  },
}
