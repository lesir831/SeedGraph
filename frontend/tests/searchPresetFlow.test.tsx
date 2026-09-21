import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { App as AntApp } from 'antd'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { api } from '../src/api/client'
import type { GroupSearchPreset } from '../src/api/types'
import { DeletionContext } from '../src/deletion/deletion-context'
import { TorrentGroupsPage } from '../src/pages/TorrentGroupsPage'

const movies: GroupSearchPreset = { id: 'movies', name: '电影', filter: { version: 1, root: {
  type: 'group', combinator: 'and', children: [{ type: 'condition', field: 'path', operator: 'contains', value: '/Movies/' }],
} } }
const fewSites: GroupSearchPreset = { id: 'few', name: '低辅种', filter: { version: 1, root: {
  type: 'group', combinator: 'or', children: [
    { type: 'condition', field: 'site_count', operator: 'lt', value: 2 },
    { type: 'condition', field: 'instance_count', operator: 'eq', value: 1 },
  ],
} } }

beforeEach(() => {
  Object.defineProperty(window, 'matchMedia', { writable: true, value: (query: string) => ({
    matches: query.includes('min-width'), media: query, onchange: null,
    addListener: vi.fn(), removeListener: vi.fn(), addEventListener: vi.fn(), removeEventListener: vi.fn(), dispatchEvent: vi.fn(),
  }) })
  const getComputedStyle = window.getComputedStyle
  vi.spyOn(window, 'getComputedStyle').mockImplementation((element) => getComputedStyle(element))
  vi.spyOn(api, 'getGroups').mockResolvedValue({ items: [], total: 0, page: 1, pageSize: 20 })
  vi.spyOn(api, 'getDownloaders').mockResolvedValue([])
  vi.spyOn(api, 'getGroupSiteOptions').mockResolvedValue([])
})

afterEach(() => { cleanup(); vi.restoreAllMocks() })

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })
  return render(<AntApp><QueryClientProvider client={client}><MemoryRouter><DeletionContext.Provider value={{ trackJob: vi.fn() }}><TorrentGroupsPage /></DeletionContext.Provider></MemoryRouter></QueryClientProvider></AntApp>)
}

describe('saved advanced search', () => {
  it('saves a valid draft on the server and loads it again after remount', async () => {
    const stored: GroupSearchPreset[] = []
    vi.spyOn(api, 'getSearchPresets').mockImplementation(() => Promise.resolve([...stored]))
    const create = vi.spyOn(api, 'createSearchPreset').mockImplementation((input) => {
      const saved = { id: 'saved', ...input }
      stored.push(saved)
      return Promise.resolve(saved)
    })
    const first = renderPage()
    fireEvent.click(screen.getByRole('button', { name: /高级搜索/ }))
    const save = await screen.findByRole('button', { name: '保存为预设' })
    expect(save).toBeDisabled()
    fireEvent.change(screen.getByPlaceholderText('输入名称关键词'), { target: { value: 'Movie' } })
    await waitFor(() => expect(screen.getByRole('button', { name: '保存为预设' })).toBeEnabled())
    fireEvent.click(screen.getByRole('button', { name: '保存为预设' }))
    const modal = (await screen.findByText('保存搜索预设')).closest<HTMLElement>('[role=dialog]')!
    fireEvent.change(within(modal).getByRole('textbox', { name: '预设名称' }), { target: { value: '  我的电影  ' } })
    fireEvent.click(within(modal).getByRole('button', { name: '保存预设' }))
    await waitFor(() => expect(create).toHaveBeenCalled())
    expect(create.mock.calls[0][0]).toEqual({ name: '我的电影', filter: { version: 1, root: { type: 'group', combinator: 'and', children: [{ type: 'condition', field: 'group_name', operator: 'contains', value: 'Movie' }] } } })
    await waitFor(() => expect(modal).not.toBeVisible())
    expect(vi.mocked(api.getGroups).mock.lastCall?.[0].filter).toBeUndefined()
    first.unmount()
    renderPage()
    fireEvent.click(screen.getByRole('button', { name: '搜索预设' }))
    const savedOption = await screen.findByRole('checkbox', { name: /我的电影/ })
    fireEvent.click(savedOption)
    fireEvent.click(screen.getByRole('button', { name: '应用预设' }))
    await waitFor(() => expect(api.getGroups).toHaveBeenLastCalledWith(expect.objectContaining({ filter: stored[0].filter, page: 1 })))
  })

  it('applies checked presets together, preserves text search, and clears filters without deleting presets', async () => {
    vi.spyOn(api, 'getSearchPresets').mockResolvedValue([movies, fewSites])
    renderPage()
    const search = screen.getByPlaceholderText('搜索名称或存放路径')
    fireEvent.change(search, { target: { value: '2026' } })
    fireEvent.keyDown(search, { key: 'Enter', code: 'Enter', keyCode: 13 })
    fireEvent.click(screen.getByRole('button', { name: '搜索预设' }))
    fireEvent.click(await screen.findByRole('checkbox', { name: /电影/ }))
    fireEvent.click(screen.getByRole('checkbox', { name: /低辅种/ }))
    expect(vi.mocked(api.getGroups).mock.lastCall?.[0].filter).toBeUndefined()
    fireEvent.click(screen.getByRole('button', { name: '应用预设' }))
    await waitFor(() => expect(api.getGroups).toHaveBeenLastCalledWith(expect.objectContaining({ query: '2026', page: 1, filter: {
      version: 1, root: { type: 'group', combinator: 'and', children: [...movies.filter.root.children, fewSites.filter.root] },
    } })))
    expect(screen.getByRole('button', { name: '搜索预设' })).toHaveTextContent('(2)')
    fireEvent.click(screen.getByRole('button', { name: /清空筛选/ }))
    await waitFor(() => expect(vi.mocked(api.getGroups).mock.lastCall?.[0].filter).toBeUndefined())
    expect(screen.getByRole('button', { name: '搜索预设' })).not.toHaveTextContent('(2)')
    fireEvent.click(screen.getByRole('button', { name: '搜索预设' }))
    expect(await screen.findByRole('checkbox', { name: /电影/ })).not.toBeChecked()
  })

  it('keeps the draft and shows save errors, and supports deleting a saved preset', async () => {
    vi.spyOn(api, 'getSearchPresets').mockResolvedValue([movies])
    const remove = vi.spyOn(api, 'deleteSearchPreset').mockResolvedValue(undefined)
    vi.spyOn(api, 'createSearchPreset').mockRejectedValue(new Error('该预设名称已存在'))
    renderPage()
    fireEvent.click(screen.getByRole('button', { name: '搜索预设' }))
    fireEvent.click(await screen.findByRole('button', { name: '删除预设 电影' }))
    fireEvent.click(await screen.findByRole('button', { name: /^删\s*除$/ }))
    await waitFor(() => expect(remove).toHaveBeenCalledWith('movies', expect.anything()))
    await waitFor(() => expect(screen.queryByRole('checkbox', { name: /电影/ })).not.toBeInTheDocument())
    fireEvent.click(screen.getByRole('button', { name: '搜索预设' }))
    fireEvent.click(screen.getByRole('button', { name: /高级搜索/ }))
    fireEvent.change(await screen.findByPlaceholderText('输入名称关键词'), { target: { value: 'Movie' } })
    await waitFor(() => expect(screen.getByRole('button', { name: '保存为预设' })).toBeEnabled())
    fireEvent.click(screen.getByRole('button', { name: '保存为预设' }))
    const modal = (await screen.findByText('保存搜索预设')).closest<HTMLElement>('[role=dialog]')!
    fireEvent.change(within(modal).getByRole('textbox', { name: '预设名称' }), { target: { value: '电影' } })
    fireEvent.click(within(modal).getByRole('button', { name: '保存预设' }))
    expect(await within(modal).findByText('该预设名称已存在')).toBeVisible()
    expect(within(modal).getByRole('textbox', { name: '预设名称' })).toHaveValue('电影')
  })
})
