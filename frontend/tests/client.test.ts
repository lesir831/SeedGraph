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
