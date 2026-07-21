import { beforeEach, describe, expect, it } from 'vitest'
import {
  DEFAULT_GROUP_SORTS,
  GROUP_SORT_STORAGE_KEY,
  loadGroupSorts,
  saveGroupSorts,
} from '../src/utils/groupSortPreferences'

const storedValues = new Map<string, string>()
const localStorageMock: Storage = {
  get length() {
    return storedValues.size
  },
  clear: () => storedValues.clear(),
  getItem: (key) => storedValues.get(key) ?? null,
  key: (index) => [...storedValues.keys()][index] ?? null,
  removeItem: (key) => storedValues.delete(key),
  setItem: (key, value) => storedValues.set(key, value),
}

Object.defineProperty(window, 'localStorage', { configurable: true, value: localStorageMock })

beforeEach(() => {
  window.localStorage.clear()
})

describe('group sort preferences', () => {
  it('uses a fresh copy of the default when no preference exists', () => {
    const loaded = loadGroupSorts()
    expect(loaded).toEqual(DEFAULT_GROUP_SORTS)
    expect(loaded).not.toBe(DEFAULT_GROUP_SORTS)
  })

  it('round-trips an ordered multi-sort preference', () => {
    const sorts = [
      { field: 'instance_count', order: 'desc' },
      { field: 'oldest_added_at', order: 'desc' },
      { field: 'size', order: 'asc' },
    ] as const

    saveGroupSorts([...sorts])

    expect(loadGroupSorts()).toEqual(sorts)
  })

  it('drops unknown and duplicate rules while retaining the first valid priority', () => {
    window.localStorage.setItem(GROUP_SORT_STORAGE_KEY, JSON.stringify([
      { field: 'instance_count', order: 'desc' },
      { field: 'missing', order: 'asc' },
      { field: 'instance_count', order: 'asc' },
      { field: 'oldest_added_at', order: 'sideways' },
      { field: 'size', order: 'asc' },
      { field: 'name', order: 'desc' },
      { field: 'oldest_added_at', order: 'asc' },
      { field: 'size', order: 'desc' },
    ]))

    expect(loadGroupSorts()).toEqual([
      { field: 'instance_count', order: 'desc' },
      { field: 'size', order: 'asc' },
      { field: 'name', order: 'desc' },
      { field: 'oldest_added_at', order: 'asc' },
    ])
  })

  it('falls back for malformed JSON or a value with no valid rules', () => {
    window.localStorage.setItem(GROUP_SORT_STORAGE_KEY, '{not-json')
    expect(loadGroupSorts()).toEqual(DEFAULT_GROUP_SORTS)

    window.localStorage.setItem(GROUP_SORT_STORAGE_KEY, JSON.stringify([{ field: 'missing', order: 'asc' }]))
    expect(loadGroupSorts()).toEqual(DEFAULT_GROUP_SORTS)
  })
})
