import type { GroupSortBy, GroupSortRule, SortOrder } from '../api/types'

export const GROUP_SORT_STORAGE_KEY = 'seedgraph.groups.sort.v1'

export const GROUP_SORT_LABELS: Record<GroupSortBy, string> = {
  instance_count: '实例数量',
  oldest_added_at: '最旧添加时间',
  size: '内容大小',
  name: '名称',
}

export const DEFAULT_GROUP_SORTS: GroupSortRule[] = [
  { field: 'oldest_added_at', order: 'asc' },
]

const GROUP_SORT_FIELDS = new Set<GroupSortBy>([
  'oldest_added_at',
  'instance_count',
  'size',
  'name',
])

const SORT_ORDERS = new Set<SortOrder>(['asc', 'desc'])

const defaultGroupSorts = (): GroupSortRule[] => DEFAULT_GROUP_SORTS.map((rule) => ({ ...rule }))

const normalizeGroupSorts = (value: unknown): GroupSortRule[] => {
  if (!Array.isArray(value)) return defaultGroupSorts()

  const seen = new Set<GroupSortBy>()
  const normalized: GroupSortRule[] = []
  for (const candidate of value) {
    if (typeof candidate !== 'object' || candidate === null) continue
    const field = Reflect.get(candidate, 'field') as unknown
    const order = Reflect.get(candidate, 'order') as unknown
    if (
      typeof field !== 'string' ||
      typeof order !== 'string' ||
      !GROUP_SORT_FIELDS.has(field as GroupSortBy) ||
      !SORT_ORDERS.has(order as SortOrder) ||
      seen.has(field as GroupSortBy)
    ) {
      continue
    }
    const normalizedField = field as GroupSortBy
    seen.add(normalizedField)
    normalized.push({ field: normalizedField, order: order as SortOrder })
    if (normalized.length === 4) break
  }

  return normalized.length ? normalized : defaultGroupSorts()
}

export const loadGroupSorts = (): GroupSortRule[] => {
  try {
    const stored = typeof window === 'undefined' ? null : window.localStorage.getItem(GROUP_SORT_STORAGE_KEY)
    return stored ? normalizeGroupSorts(JSON.parse(stored) as unknown) : defaultGroupSorts()
  } catch {
    return defaultGroupSorts()
  }
}

export const saveGroupSorts = (sorts: GroupSortRule[]): void => {
  try {
    if (typeof window !== 'undefined') {
      window.localStorage.setItem(GROUP_SORT_STORAGE_KEY, JSON.stringify(normalizeGroupSorts(sorts)))
    }
  } catch {
    // Storage can be unavailable in private or restricted browser contexts.
  }
}
