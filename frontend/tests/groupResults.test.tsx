import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { App as AntApp } from 'antd'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { api } from '../src/api/client'
import { toTorrentGroup } from '../src/api/transformers'
import { DeletionContext } from '../src/deletion/deletion-context'
import { TorrentGroupsPage } from '../src/pages/TorrentGroupsPage'

const contentPath = '/volume1/001/media/Movies/The.Whisper.Man.2026.2160p.NF.WEB-DL.H.265.HDR.DDP5.1.Atmos-HHWEB'
const summary = toTorrentGroup({
  id: 'movies', name: 'The.Whisper.Man.2026', size_bytes: 1024 ** 3, task_count: 2,
  site_count: 0, downloader_count: 1, data_copy_count: 1, confidence: 'verified', mode: 'auto',
  locked: false, version: 1, stale: true, updated_at: '2026-09-21T00:00:00Z',
  oldest_added_at: '2026-09-01T00:00:00Z', categories: ['Movies'], paths: [contentPath], downloaders: ['NAS qB'],
  runtime: { ratio_min: 2.5, ratio_max: 4, uploaded_bytes: 650 * 1024 ** 2, downloaded_bytes: 200 * 1024 ** 2, upload_speed: 1024, download_speed: 2048 },
})

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

function renderResults(mobile: boolean) {
  Object.defineProperty(window, 'matchMedia', { writable: true, value: (query: string) => ({
    matches: mobile ? query.includes('max-width') : query.includes('min-width'), media: query, onchange: null,
    addListener: vi.fn(), removeListener: vi.fn(), addEventListener: vi.fn(), removeEventListener: vi.fn(), dispatchEvent: vi.fn(),
  }) })
  const getComputedStyle = window.getComputedStyle
  vi.spyOn(window, 'getComputedStyle').mockImplementation((element) => getComputedStyle(element))
  vi.spyOn(api, 'getGroups').mockResolvedValue({ items: [summary], total: 1, page: 1, pageSize: 20 })
  vi.spyOn(api, 'getGroup').mockResolvedValue(summary)
  vi.spyOn(api, 'getDownloaders').mockResolvedValue([])
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(<AntApp><QueryClientProvider client={client}><MemoryRouter><DeletionContext.Provider value={{ trackJob: vi.fn() }}><TorrentGroupsPage /></DeletionContext.Provider></MemoryRouter></QueryClientProvider></AntApp>)
}

describe('search result summaries', () => {
  it.each([false, true])('shows downloader metrics and path categories without loading details (mobile=%s)', async (mobile) => {
    renderResults(mobile)
    expect(await screen.findByText('Movies')).toBeVisible()
    expect(screen.getByText(contentPath)).toBeVisible()
    expect(screen.getByText('NAS qB')).toBeVisible()
    expect(screen.getByText('2.50 – 4.00')).toBeVisible()
    expect(screen.getByText('650 MB')).toBeVisible()
    expect(screen.getByText('200 MB')).toBeVisible()
    expect(screen.getByText(/1.0 KB\/s/)).toBeVisible()
    expect(screen.getByText(/2026-09-01/)).toBeVisible()
    expect(screen.getByText('快照已过期')).toBeVisible()
    expect(api.getGroup).not.toHaveBeenCalled()
    const search = screen.getByPlaceholderText('搜索名称或存放路径')
    fireEvent.change(search, { target: { value: 'Whisper' } })
    fireEvent.keyDown(search, { key: 'Enter', code: 'Enter', keyCode: 13 })
    await waitFor(() => expect(api.getGroups).toHaveBeenLastCalledWith(expect.objectContaining({ query: 'Whisper' })))
    expect(await screen.findByText('Movies')).toBeVisible()
  })
})
