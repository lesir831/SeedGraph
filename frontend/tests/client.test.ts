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
