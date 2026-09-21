import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { App as AntApp } from 'antd'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { api } from '../src/api/client'
import { toDeleteJob, toDeletePlan, toTorrentGroup } from '../src/api/transformers'
import type { PagedResponse, TorrentGroup } from '../src/api/types'
import { DeletionTasksProvider } from '../src/deletion/DeletionTasksProvider'
import { useDeletionTasks } from '../src/deletion/deletion-context'
import { TorrentGroupsPage } from '../src/pages/TorrentGroupsPage'

const group = toTorrentGroup({
  id: 'deleted', name: 'Deleted movie', size_bytes: 100, task_count: 1, site_count: 0, downloader_count: 1, data_copy_count: 1,
  confidence: 'verified', mode: 'auto', locked: false, version: 1, stale: false, updated_at: '2026-09-21T00:00:00Z',
  instances: [{ id: 'instance', downloader_id: 'downloader', downloader_name: 'NAS', downloader_kind: 'qbittorrent', stable_hash_key: 'hash', name: 'Deleted movie', canonical_path: '/movies/deleted', storage_id: 'disk', wanted_bytes: 100, data_group_id: 'data', assignment_source: 'auto', status: 'seeding', progress: 1, ratio: 1, updated_at: '2026-09-21T00:00:00Z' }],
})
const survivor = { ...group, id: 'survivor', name: 'Remaining movie' }
const job = toDeleteJob({
  id: 'job-refresh', plan_id: 'plan-refresh', status: 'running', internal_status: 'executing', created_at: '2026-09-21T00:00:00Z',
  steps: [{ id: 'step', position: 1, instance_id: 'instance', downloader_id: 'downloader', delete_data: true, status: 'pending' }],
})
const completed = { ...job, status: 'completed' as const, steps: job.steps.map((step) => ({ ...step, status: 'completed' as const })) }

beforeEach(() => {
  Object.defineProperty(window, 'matchMedia', { writable: true, value: (query: string) => ({
    matches: query.includes('min-width'), media: query, onchange: null,
    addListener: vi.fn(), removeListener: vi.fn(), addEventListener: vi.fn(), removeEventListener: vi.fn(), dispatchEvent: vi.fn(),
  }) })
  const getComputedStyle = window.getComputedStyle
  vi.spyOn(window, 'getComputedStyle').mockImplementation((element) => getComputedStyle(element))
  vi.spyOn(api, 'getDownloaders').mockResolvedValue([])
})

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
  sessionStorage.clear()
})

function TrackJob() {
  const { trackJob } = useDeletionTasks()
  return <button onClick={() => trackJob(job)}>Track deletion</button>
}

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: 15_000 }, mutations: { retry: false } } })
  render(<AntApp><QueryClientProvider client={client}><MemoryRouter><DeletionTasksProvider><TrackJob /><TorrentGroupsPage /></DeletionTasksProvider></MemoryRouter></QueryClientProvider></AntApp>)
  return client
}

describe('list refresh after deletion', () => {
  it('replaces an in-flight initial list request when deletion completes, ignoring its late stale response', async () => {
    let finishOld: (value: PagedResponse<TorrentGroup>) => void = () => undefined
    const oldRequest = new Promise<PagedResponse<TorrentGroup>>((resolve) => { finishOld = resolve })
    const getGroups = vi.spyOn(api, 'getGroups')
      .mockReturnValueOnce(oldRequest)
      .mockResolvedValue({ items: [], total: 0, page: 1, pageSize: 20 })
    vi.spyOn(api, 'getDeleteJob').mockResolvedValue(completed)
    renderPage()
    fireEvent.click(screen.getByRole('button', { name: 'Track deletion' }))
    await screen.findByText('删除完成')
    await waitFor(() => expect(getGroups).toHaveBeenCalledTimes(2))
    await act(async () => { finishOld({ items: [group], total: 1, page: 1, pageSize: 20 }); await oldRequest })
    await screen.findByText('当前筛选条件下没有聚合任务。同步下载器后，任务会按内容指纹自动归组。')
    expect(screen.queryByText('Deleted movie')).not.toBeInTheDocument()
  })

  it('returns to the last available page after the final task on a page is deleted', async () => {
    let deleted = false
    const getGroups = vi.spyOn(api, 'getGroups').mockImplementation((filters) => Promise.resolve({
      items: filters.page === 2 ? deleted ? [] : [group] : [survivor],
      total: deleted ? 20 : 21, page: filters.page, pageSize: 20,
    }))
    vi.spyOn(api, 'getGroup').mockResolvedValue(group)
    vi.spyOn(api, 'createDeletePlan').mockResolvedValue(toDeletePlan({
      id: 'plan-refresh', selected_instance_ids: ['instance'], executable: true,
      steps: [{ order: 1, instance_id: 'instance', downloader_id: 'downloader', content_group_id: 'deleted', data_group_id: 'data', delete_data: true }],
    }, 'deleted'))
    vi.spyOn(api, 'createDeleteJob').mockResolvedValue(job)
    const getJob = vi.spyOn(api, 'getDeleteJob').mockResolvedValue(job)
    const client = renderPage()
    fireEvent.click(await screen.findByTitle('2'))
    await screen.findByText('Deleted movie')
    fireEvent.click(screen.getByRole('button', { name: '删除任务组' }))
    const confirm = await screen.findByRole('button', { name: '删除任务及文件' })
    fireEvent.click(confirm)
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    await waitFor(() => expect(getJob).toHaveBeenCalled())
    deleted = true
    getJob.mockResolvedValue(completed)
    await act(async () => { await client.refetchQueries({ queryKey: ['delete-job', job.id] }) })
    await screen.findByText('删除完成')
    await screen.findByText('Remaining movie')
    expect(screen.queryByText('Deleted movie')).not.toBeInTheDocument()
    expect(getGroups).toHaveBeenLastCalledWith(expect.objectContaining({ page: 1 }))
  })
})
