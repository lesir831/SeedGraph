import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { App as AntApp } from 'antd'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { MemoryRouter } from 'react-router-dom'
import { api, ApiError } from '../src/api/client'
import { toDeleteJob, toDeletePlan, toTorrentGroup } from '../src/api/transformers'
import { DeletionContext, useDeletionTasks } from '../src/deletion/deletion-context'
import { DeletionTasksProvider } from '../src/deletion/DeletionTasksProvider'
import { loadTrackedJobIds } from '../src/deletion/progress'
import { TorrentGroupsPage } from '../src/pages/TorrentGroupsPage'

beforeEach(() => {
  Object.defineProperty(window, 'matchMedia', { writable: true, value: (query: string) => ({
    matches: true, media: query, onchange: null,
    addListener: vi.fn(), removeListener: vi.fn(), addEventListener: vi.fn(), removeEventListener: vi.fn(), dispatchEvent: vi.fn(),
  }) })
  const getComputedStyle = window.getComputedStyle
  vi.spyOn(window, 'getComputedStyle').mockImplementation((element) => getComputedStyle(element))
})

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
  sessionStorage.clear()
})

const job = toDeleteJob({
  id: 'job-123', plan_id: 'plan', status: 'running', internal_status: 'executing', created_at: '2026-09-19T00:00:00Z',
  steps: [{ id: 'step', position: 1, instance_id: 'instance', downloader_id: 'downloader', delete_data: true, status: 'pending' }],
})

const group = toTorrentGroup({
  id: 'group', name: 'Ubuntu ISO', size_bytes: 100, task_count: 1, site_count: 0, downloader_count: 1, data_copy_count: 1,
  confidence: 'verified', mode: 'automatic', locked: false, version: 1, stale: false, updated_at: '2026-09-19T00:00:00Z',
  instances: [{ id: 'instance', downloader_id: 'downloader', downloader_name: 'Test NAS', downloader_kind: 'qbittorrent', stable_hash_key: 'hash', name: 'Ubuntu ISO', canonical_path: '/downloads/ubuntu', storage_id: 'disk', wanted_bytes: 100, data_group_id: 'data', assignment_source: 'automatic', status: 'seeding', progress: 1, ratio: 1, updated_at: '2026-09-19T00:00:00Z' }],
})

const plan = toDeletePlan({
  id: 'plan', selected_instance_ids: ['instance'], executable: true,
  steps: [{ order: 1, instance_id: 'instance', downloader_id: 'downloader', content_group_id: 'group', data_group_id: 'data', delete_data: true }],
}, 'group')

function renderFlow(selectedGroup = group) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })
  vi.spyOn(api, 'getGroups').mockResolvedValue({ items: [selectedGroup], total: 1, page: 1, pageSize: 20 })
  vi.spyOn(api, 'getGroup').mockResolvedValue(selectedGroup)
  vi.spyOn(api, 'getDownloaders').mockResolvedValue([])
  vi.spyOn(api, 'createDeletePlan').mockResolvedValue(plan)
  const trackJob = vi.fn()
  render(<AntApp><QueryClientProvider client={client}><MemoryRouter><DeletionContext.Provider value={{ trackJob }}><TorrentGroupsPage /></DeletionContext.Provider></MemoryRouter></QueryClientProvider></AntApp>)
  return { client, trackJob }
}

async function openConfirmation() {
  fireEvent.click(await screen.findByRole('button', { name: '删除任务组' }))
  return screen.findByRole('button', { name: '删除任务及文件' })
}

describe('single-dialog deletion flow', () => {
  it('preselects tasks, checks impact automatically, and submits with one confirmation', async () => {
    const { trackJob } = renderFlow()
    let finish: (value: typeof job) => void = () => undefined
    const submit = vi.spyOn(api, 'createDeleteJob').mockImplementation(() => new Promise((resolve) => { finish = resolve }))
    const confirm = await openConfirmation()
    expect(confirm).toBeEnabled()
    expect(screen.queryByText('生成影响预览')).not.toBeInTheDocument()
    expect(screen.queryByText('返回修改')).not.toBeInTheDocument()
    expect(screen.getByRole('checkbox', { name: /Test NAS/ })).toBeChecked()
    expect(api.createDeletePlan).toHaveBeenCalledWith({ groupId: 'group', instanceIds: ['instance'] })
    fireEvent.click(confirm)
    await waitFor(() => expect(submit).toHaveBeenCalledOnce())
    expect(screen.getByRole('button', { name: /取.*消/ })).toBeDisabled()
    expect(screen.getByRole('checkbox', { name: /Test NAS/ })).toBeDisabled()
    act(() => { finish(job) })
    await waitFor(() => expect(trackJob).toHaveBeenCalledWith(job))
    expect(api.getGroup).toHaveBeenCalledWith('group')
  })

  it('preselects only the clicked instance when deleting from an expanded group', async () => {
    const other = { ...group.instances[0], id: 'second', downloaderName: 'Second NAS' }
    renderFlow({ ...group, instances: [...group.instances, other], taskCount: 2 })
    fireEvent.click(await screen.findByRole('button', { name: 'Expand row' }))
    const entries = await screen.findAllByRole('button', { name: '删除此任务' })
    fireEvent.click(entries[1])
    await waitFor(() => expect(api.createDeletePlan).toHaveBeenCalledWith({ groupId: 'group', instanceIds: ['second'] }))
    expect(screen.getByRole('checkbox', { name: /Second NAS/ })).toBeChecked()
    expect(screen.getByRole('checkbox', { name: /Test NAS/ })).not.toBeChecked()
  })

  it('refreshes an expired preview automatically but waits for a new confirmation', async () => {
    const { trackJob } = renderFlow()
    const submit = vi.spyOn(api, 'createDeleteJob')
      .mockRejectedValueOnce(new ApiError('删除预览已过期', 410, 'plan_expired'))
      .mockResolvedValue(job)
    fireEvent.click(await openConfirmation())
    await waitFor(() => expect(api.createDeletePlan).toHaveBeenCalledTimes(2))
    await screen.findByText('任务状态已变化，请核对更新后的文件影响，再确认删除。')
    const confirm = await screen.findByRole('button', { name: '删除任务及文件' })
    expect(confirm).toBeEnabled()
    expect(submit).toHaveBeenCalledOnce()
    expect(screen.getByRole('checkbox', { name: /Test NAS/ })).toBeChecked()
    fireEvent.click(confirm)
    await waitFor(() => expect(trackJob).toHaveBeenCalledWith(job))
  })

  it('ignores an outdated preview when the selection changes while checking', async () => {
    const other = { ...group.instances[0], id: 'second', downloaderName: 'Second NAS' }
    renderFlow({ ...group, instances: [...group.instances, other], taskCount: 2 })
    let finishOld: (value: typeof plan) => void = () => undefined
    const taskOnlyPlan = { ...plan, id: 'task-only-plan', steps: plan.steps.map((step) => ({ ...step, deleteData: false })) }
    const preview = vi.mocked(api.createDeletePlan).mockImplementation(({ instanceIds }) => instanceIds.length === 2
      ? new Promise((resolve) => { finishOld = resolve })
      : Promise.resolve(taskOnlyPlan))
    const submit = vi.spyOn(api, 'createDeleteJob').mockResolvedValue(job)
    fireEvent.click(await screen.findByRole('button', { name: '删除任务组' }))
    await waitFor(() => expect(preview).toHaveBeenCalledWith({ groupId: 'group', instanceIds: ['instance', 'second'] }))
    expect(screen.getByRole('button', { name: '删除任务' })).toBeDisabled()
    fireEvent.click(screen.getByRole('checkbox', { name: /Second NAS/ }))
    await screen.findByText('仅移除选中的任务，保留文件。')
    act(() => { finishOld({ ...plan, selectedInstanceIds: ['instance', 'second'] }) })
    expect(screen.queryByRole('button', { name: '删除任务及文件' })).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: '删除任务' }))
    await waitFor(() => expect(submit).toHaveBeenCalledWith(taskOnlyPlan, expect.anything()))
  })

  it('keeps a blocked plan non-executable and allows retrying a failed impact check', async () => {
    renderFlow()
    vi.mocked(api.createDeletePlan)
      .mockRejectedValueOnce(new Error('离线'))
      .mockResolvedValue({ ...plan, executable: false, blockers: [{ code: 'downloader_offline', message: '下载器离线' }] })
    const submit = vi.spyOn(api, 'createDeleteJob')
    fireEvent.click(await screen.findByRole('button', { name: '删除任务组' }))
    await screen.findByText('无法检查删除影响')
    expect(screen.getByRole('button', { name: '删除任务' })).toBeDisabled()
    fireEvent.click(screen.getByRole('button', { name: '重试检查' }))
    await screen.findByText('当前任务无法删除')
    expect(screen.getByRole('button', { name: '删除任务及文件' })).toBeDisabled()
    expect(submit).not.toHaveBeenCalled()
  })

  it('retains the submitted plan after a network error so retries stay idempotent', async () => {
    const { trackJob } = renderFlow()
    const submit = vi.spyOn(api, 'createDeleteJob').mockRejectedValueOnce(new Error('网络中断')).mockResolvedValue(job)
    fireEvent.click(await openConfirmation())
    await screen.findByText('网络中断')
    fireEvent.click(screen.getByRole('button', { name: '删除任务及文件' }))
    await waitFor(() => expect(trackJob).toHaveBeenCalledWith(job))
    expect(api.createDeletePlan).toHaveBeenCalledOnce()
    expect(submit.mock.calls.map(([submittedPlan]) => submittedPlan.id)).toEqual(['plan', 'plan'])
  })
})

function RegisterJob() {
  const { trackJob } = useDeletionTasks()
  return <button onClick={() => trackJob(job)}>track</button>
}

describe('deletion task tracking', () => {
  it('restores tracking after remount, refreshes results on completion, and allows dismissal', async () => {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    const getJob = vi.spyOn(api, 'getDeleteJob').mockResolvedValue(job)
    const invalidate = vi.spyOn(client, 'invalidateQueries')
    const first = render(<QueryClientProvider client={client}><DeletionTasksProvider><RegisterJob /></DeletionTasksProvider></QueryClientProvider>)
    fireEvent.click(screen.getByRole('button', { name: 'track' }))
    await screen.findByText('正在删除')
    await waitFor(() => expect(loadTrackedJobIds()).toEqual(['job-123']))
    expect(screen.queryByRole('button', { name: '移除已结束任务' })).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: '收起删除进度' }))
    expect(screen.getByRole('button', { name: '展开删除进度' })).toBeVisible()
    first.unmount()
    client.clear()

    getJob.mockResolvedValue({ ...job, status: 'completed', steps: job.steps.map((step) => ({ ...step, status: 'completed' })) })
    render(<QueryClientProvider client={client}><DeletionTasksProvider><span>another page</span></DeletionTasksProvider></QueryClientProvider>)
    await screen.findByText('删除完成')
    await waitFor(() => expect(invalidate).toHaveBeenCalledWith({ queryKey: ['torrent-groups'] }))
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ['audit-events'] })
    fireEvent.click(screen.getByRole('button', { name: '移除已结束任务' }))
    await waitFor(() => expect(loadTrackedJobIds()).toEqual([]))
    expect(screen.queryByLabelText('删除任务进度')).not.toBeInTheDocument()
  })
})
