import { describe, expect, it } from 'vitest'
import type { GroupQueryFilter, GroupQueryGroup } from '../src/api/types'
import { combineSearchPresets } from '../src/utils/groupSearchPresets'

const simple: GroupQueryFilter = { version: 1, root: { type: 'group', combinator: 'and', children: [
  { type: 'condition', field: 'path', operator: 'contains', value: '/Movies/' },
] } }

describe('search preset composition', () => {
  it('keeps a single preset intact and clears the filter for no presets', () => {
    expect(combineSearchPresets([])).toBeUndefined()
    expect(combineSearchPresets([simple])).toEqual(simple)
    expect(combineSearchPresets([simple])).not.toBe(simple)
  })

  it('intersects presets without changing OR, negation or same-instance semantics', () => {
    const scoped: GroupQueryFilter = { version: 1, root: { ...simple.root, scope: 'instance', negated: true } }
    const either: GroupQueryFilter = { version: 1, root: { ...simple.root, combinator: 'or', children: [
      { type: 'condition', field: 'site_count', operator: 'lte', value: 2 },
      { type: 'condition', field: 'locked', operator: 'eq', value: false },
    ] } }
    expect(combineSearchPresets([simple, scoped, either])?.root).toEqual({
      type: 'group', combinator: 'and', children: [...simple.root.children, scoped.root, either.root],
    })
  })

  it('rejects combinations exceeding server depth and condition limits instead of dropping rules', () => {
    const nested: GroupQueryGroup = { type: 'group', combinator: 'or', children: [
      { type: 'group', combinator: 'or', children: [simple.root] },
    ] }
    expect(combineSearchPresets([{ version: 1, root: nested }])).toBeDefined()
    expect(() => combineSearchPresets([simple, { version: 1, root: nested }])).toThrow('嵌套限制')
    expect(() => combineSearchPresets(Array.from({ length: 31 }, () => simple))).toThrow('30')
  })
})
