import { afterEach, describe, expect, it, vi } from 'vitest'
import { api } from '../src/api/client'

afterEach(() => {
  vi.restoreAllMocks()
  sessionStorage.clear()
})

describe('torrent group API', () => {
  it('sends the selected server-side sort and pagination parameters', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response(JSON.stringify({
      items: [],
      total: 0,
      limit: 50,
      offset: 50,
    }), {
      status: 200,
      headers: { 'content-type': 'application/json' },
    }))

    await api.getGroups({
      status: 'all',
      sortBy: 'instance_count',
      sortOrder: 'desc',
      page: 2,
      pageSize: 50,
    })

    expect(fetchMock).toHaveBeenCalledOnce()
    const requestInput = fetchMock.mock.calls[0][0]
    if (typeof requestInput !== 'string') throw new TypeError('expected the API client to call fetch with a URL string')
    const requestUrl = new URL(requestInput, window.location.origin)
    expect(requestUrl.searchParams.get('sort_by')).toBe('instance_count')
    expect(requestUrl.searchParams.get('sort_order')).toBe('desc')
    expect(requestUrl.searchParams.get('limit')).toBe('50')
    expect(requestUrl.searchParams.get('offset')).toBe('50')
    expect(requestUrl.searchParams.has('status')).toBe(false)
  })

  it('sends advanced filters and preserves multi-sort priority with repeated parameters', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response(JSON.stringify({
      items: [],
      total: 0,
      limit: 20,
      offset: 0,
    }), {
      status: 200,
      headers: { 'content-type': 'application/json' },
    }))

    await api.getGroups({
      status: 'all',
      nameContains: 'Ubuntu',
      requiredSites: ['site:site-a', 'site:site-b'],
      excludedSites: ['tracker:tracker.site-c.example'],
      sizeLT: 1_073_741_824,
      oldestAddedGte: '2026-07-01T00:00:00+08:00',
      oldestAddedLt: '2026-08-01T00:00:00+08:00',
      sorts: [
        { field: 'instance_count', order: 'desc' },
        { field: 'oldest_added_at', order: 'desc' },
        { field: 'size', order: 'asc' },
      ],
      // Multi-sort takes precedence while these legacy fields remain accepted.
      sortBy: 'name',
      sortOrder: 'asc',
      page: 1,
      pageSize: 20,
    })

    const requestInput = fetchMock.mock.calls[0][0]
    if (typeof requestInput !== 'string') throw new TypeError('expected the API client to call fetch with a URL string')
    const requestUrl = new URL(requestInput, window.location.origin)
    expect(requestUrl.searchParams.get('name_contains')).toBe('Ubuntu')
    expect(requestUrl.searchParams.getAll('site_all')).toEqual(['site:site-a', 'site:site-b'])
    expect(requestUrl.searchParams.getAll('site_none')).toEqual(['tracker:tracker.site-c.example'])
    expect(requestUrl.searchParams.get('size_lt')).toBe('1073741824')
    expect(requestUrl.searchParams.get('oldest_added_gte')).toBe('2026-07-01T00:00:00+08:00')
    expect(requestUrl.searchParams.get('oldest_added_lt')).toBe('2026-08-01T00:00:00+08:00')
    expect(requestUrl.searchParams.getAll('sort')).toEqual([
      'instance_count:desc',
      'oldest_added_at:desc',
      'size:asc',
    ])
    expect(requestUrl.searchParams.has('sort_by')).toBe(false)
    expect(requestUrl.searchParams.has('sort_order')).toBe(false)
  })

  it('loads stable group site filter options', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response(JSON.stringify({
      data: [
        { key: 'site:site-a', label: '站点 A', mapped: true },
        { key: 'tracker:tracker.unknown.test', label: 'Unknown · tracker.unknown.test', mapped: false },
      ],
    }), {
      status: 200,
      headers: { 'content-type': 'application/json' },
    }))

    await expect(api.getGroupSiteOptions()).resolves.toEqual([
      { key: 'site:site-a', label: '站点 A', mapped: true },
      { key: 'tracker:tracker.unknown.test', label: 'Unknown · tracker.unknown.test', mapped: false },
    ])

    const requestInput = fetchMock.mock.calls[0][0]
    if (typeof requestInput !== 'string') throw new TypeError('expected the API client to call fetch with a URL string')
    expect(new URL(requestInput, window.location.origin).pathname).toBe('/api/v1/torrent-groups/site-options')
  })
})

describe('tracker mapping API', () => {
  it('sends mapping filters and server-side pagination parameters', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response(JSON.stringify({
      items: [],
      total: 0,
      limit: 25,
      offset: 25,
    }), {
      status: 200,
      headers: { 'content-type': 'application/json' },
    }))

    await api.getTrackerMappings({
      query: 'hhanclub',
      status: 'mapped',
      matchType: 'registrable_domain',
      page: 2,
      pageSize: 25,
    })

    const requestInput = fetchMock.mock.calls[0][0]
    if (typeof requestInput !== 'string') throw new TypeError('expected the API client to call fetch with a URL string')
    const requestUrl = new URL(requestInput, window.location.origin)
    expect(requestUrl.pathname).toBe('/api/v1/tracker-rules/mappings')
    expect(requestUrl.searchParams.get('q')).toBe('hhanclub')
    expect(requestUrl.searchParams.get('status')).toBe('mapped')
    expect(requestUrl.searchParams.get('match_type')).toBe('registrable_domain')
    expect(requestUrl.searchParams.get('limit')).toBe('25')
    expect(requestUrl.searchParams.get('offset')).toBe('25')
  })

  it('sends IYUU search, status and pagination parameters', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response(JSON.stringify({
      items: [],
      total: 0,
      limit: 10,
      offset: 20,
      running: false,
      state: { last_error: '', site_count: 0 },
    }), {
      status: 200,
      headers: { 'content-type': 'application/json' },
    }))

    const result = await api.getIYUUSites({ query: 'soulvoice', status: 'unmapped', page: 3, pageSize: 10 })

    const requestInput = fetchMock.mock.calls[0][0]
    if (typeof requestInput !== 'string') throw new TypeError('expected the API client to call fetch with a URL string')
    const requestUrl = new URL(requestInput, window.location.origin)
    expect(requestUrl.pathname).toBe('/api/v1/sites')
    expect(requestUrl.searchParams.get('q')).toBe('soulvoice')
    expect(requestUrl.searchParams.get('status')).toBe('unmapped')
    expect(requestUrl.searchParams.get('limit')).toBe('10')
    expect(requestUrl.searchParams.get('offset')).toBe('20')
    expect(result.page).toBe(3)
  })
})
