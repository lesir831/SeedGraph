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

  it('sends structured advanced filters in a POST body with sort priority preserved', async () => {
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
      query: 'Ubuntu',
      filter: {
        version: 1,
        root: {
          type: 'group',
          combinator: 'and',
          children: [
            { type: 'condition', field: 'size', operator: 'lt', value: 1_073_741_824, displayUnit: 'GiB' },
            {
              type: 'group',
              combinator: 'or',
              negated: true,
              children: [
                { type: 'condition', field: 'site', operator: 'in', value: ['site:site-a'] },
                { type: 'condition', field: 'site', operator: 'in', value: ['site:site-b'] },
              ],
            },
          ],
        },
      },
      sorts: [
        { field: 'instance_count', order: 'desc' },
        { field: 'oldest_added_at', order: 'desc' },
        { field: 'size', order: 'asc' },
      ],
      page: 1,
      pageSize: 20,
    })

    const [requestInput, requestOptions] = fetchMock.mock.calls[0]
    if (typeof requestInput !== 'string') throw new TypeError('expected the API client to call fetch with a URL string')
    const requestUrl = new URL(requestInput, window.location.origin)
    expect(requestUrl.pathname).toBe('/api/v1/torrent-groups/query')
    expect(requestOptions?.method).toBe('POST')
    if (typeof requestOptions?.body !== 'string') throw new TypeError('expected a JSON request body')
    const body = JSON.parse(requestOptions.body) as {
      q?: string
      limit: number
      offset: number
      timezone: string
      sorts: Array<{ field: string; order: string }>
      filter: { root: { children: Array<Record<string, unknown>> } }
    }
    expect(body.q).toBe('Ubuntu')
    expect(body.limit).toBe(20)
    expect(body.offset).toBe(0)
    expect(typeof body.timezone).toBe('string')
    expect(body.timezone.length).toBeGreaterThan(0)
    expect(body.sorts).toEqual([
      { field: 'instance_count', order: 'desc' },
      { field: 'oldest_added_at', order: 'desc' },
      { field: 'size', order: 'asc' },
    ])
    expect(body.filter.root.children[0]).toEqual({
      type: 'condition', field: 'size', operator: 'lt', value: 1_073_741_824,
    })
    expect(body.filter.root.children[1]).toMatchObject({ type: 'group', combinator: 'or', negated: true })
    expect(JSON.stringify(body)).not.toContain('displayUnit')
  })

  it('fails closed without making a request when an advanced AST is invalid', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch')

    await expect(api.getGroups({
      status: 'all',
      filter: {
        version: 1,
        root: {
          type: 'group',
          combinator: 'and',
          children: [{ type: 'condition', field: 'size', operator: 'between', value: [1024] }],
        },
      },
      page: 1,
      pageSize: 20,
    })).rejects.toThrow('高级搜索条件无效')

    expect(fetchMock).not.toHaveBeenCalled()
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
