import type { GroupQueryFilter } from '../api/types'
import { normalizeGroupQueryFilter, validateGroupQueryFilter } from './groupQuery'

/** Intersect presets while preserving OR, negation and same-instance scopes. */
export function combineSearchPresets(filters: GroupQueryFilter[]): GroupQueryFilter | undefined {
  if (!filters.length) return undefined
  const normalized = filters.map((filter) => {
    const result = normalizeGroupQueryFilter(filter)
    if (!result) throw new Error('预设包含无效条件，请重新配置')
    return result
  })
  if (normalized.length === 1) return normalized[0]
  const combined: GroupQueryFilter = {
    version: 1,
    root: {
      type: 'group', combinator: 'and',
      children: normalized.flatMap(({ root }) => root.combinator === 'and' && !root.negated && !root.scope
        ? root.children : [root]),
    },
  }
  const validation = validateGroupQueryFilter(combined)
  if (!validation.valid) throw new Error(`无法组合这些预设：${validation.errors[0]}`)
  return combined
}
