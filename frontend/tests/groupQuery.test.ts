import { describe, expect, it } from 'vitest'
import type { GroupQueryCondition, GroupQueryFilter, GroupQueryGroup } from '../src/api/types'
import {
  countGroupQueryConditions,
  encodeGroupQueryFilter,
  GROUP_QUERY_MAX_CONDITIONS,
  GROUP_QUERY_MAX_ARRAY_ITEMS,
  groupSizeValueToBytes,
  normalizeGroupQueryFilter,
  summarizeGroupQuery,
  validateGroupQueryFilter,
} from '../src/utils/groupQuery'

const condition = (overrides: Partial<GroupQueryCondition> = {}): GroupQueryCondition => ({
  type: 'condition',
  field: 'group_name',
  operator: 'contains',
  value: 'Ubuntu',
  ...overrides,
})

const filter = (...children: Array<GroupQueryCondition | GroupQueryGroup>): GroupQueryFilter => ({
  version: 1,
  root: { type: 'group', combinator: 'and', children },
})

describe('group query AST', () => {
  it('converts fractional display units to integer bytes', () => {
    expect(groupSizeValueToBytes(512, 'MiB', 'eq')).toBe(536_870_912)
    expect(groupSizeValueToBytes(0.1, 'GiB', 'lt')).toBe(107_374_183)
    expect(groupSizeValueToBytes(0.1, 'GiB', 'lte')).toBe(107_374_182)
    expect(groupSizeValueToBytes(0.1, 'GiB', 'gt')).toBe(107_374_182)
    expect(groupSizeValueToBytes(0.1, 'GiB', 'gte')).toBe(107_374_183)
    expect(groupSizeValueToBytes(0.1, 'GiB', 'between', 0)).toBe(107_374_183)
    expect(groupSizeValueToBytes(0.1, 'GiB', 'between', 1)).toBe(107_374_182)
    expect(Number.isInteger(groupSizeValueToBytes(0.1, 'GiB', 'eq'))).toBe(false)
  })

  it('normalizes a nested AND/OR/NOT tree without dropping nodes', () => {
    const input = filter(
      condition({ value: '  Ubuntu  ' }),
      {
        type: 'group',
        combinator: 'or',
        negated: true,
        children: [
          condition({ field: 'site', operator: 'in', value: ['site:a'] }),
          condition({ field: 'site', operator: 'in', value: ['site:b'] }),
        ],
      },
      {
        type: 'group',
        combinator: 'and',
        scope: 'instance',
        children: [
          condition({ field: 'downloader', operator: 'in', value: ['downloader-a'] }),
          condition({ field: 'state', operator: 'in', value: ['seeding'] }),
        ],
      },
    )

    const normalized = normalizeGroupQueryFilter(input)
    expect(normalized).toBeDefined()
    expect(countGroupQueryConditions(normalized)).toBe(5)
    expect(normalized?.root.children[0]).toMatchObject({ value: 'Ubuntu' })
    expect(normalized?.root.children[1]).toMatchObject({ type: 'group', combinator: 'or', negated: true })
    expect(normalized?.root.children[2]).toMatchObject({ type: 'group', scope: 'instance' })
  })

  it('encodes a safe wire AST and strips display-only units', () => {
    const encoded = encodeGroupQueryFilter(filter(condition({
      field: 'size',
      operator: 'lte',
      value: 1_073_741_824,
      displayUnit: 'GiB',
    })))

    expect(JSON.parse(encoded ?? '{}')).toEqual({
      version: 1,
      root: {
        type: 'group',
        combinator: 'and',
        children: [{ type: 'condition', field: 'size', operator: 'lte', value: 1_073_741_824 }],
      },
    })
  })

  it('fails the whole tree when any condition is missing a value', () => {
    const input = filter(
      condition(),
      condition({ field: 'size', operator: 'between', value: [1024] }),
    )

    const validation = validateGroupQueryFilter(input)
    expect(validation.valid).toBe(false)
    expect(validation.errors.join(' ')).toContain('两个有效数值')
    expect(normalizeGroupQueryFilter(input)).toBeUndefined()
    expect(encodeGroupQueryFilter(input)).toBeUndefined()
  })

  it('rejects count values above the backend limit', () => {
    const input = filter(condition({ field: 'instance_count', operator: 'gte', value: 1_000_000_001 }))
    expect(validateGroupQueryFilter(input).errors.join(' ')).toContain('1,000,000,000')
    expect(encodeGroupQueryFilter(input)).toBeUndefined()
  })

  it('rejects collection values above the backend limit', () => {
    const input = filter(condition({
      field: 'state',
      operator: 'in',
      value: Array.from({ length: GROUP_QUERY_MAX_ARRAY_ITEMS + 1 }, (_, index) => `state-${index}`),
    }))
    expect(validateGroupQueryFilter(input).errors.join(' ')).toContain('最多支持 20 项')
    expect(encodeGroupQueryFilter(input)).toBeUndefined()
  })

  it('fails instead of truncating a query over the 30-condition budget', () => {
    const input = filter(...Array.from({ length: GROUP_QUERY_MAX_CONDITIONS + 1 }, (_, index) => condition({ value: `item-${index}` })))

    const validation = validateGroupQueryFilter(input)
    expect(validation.conditionCount).toBe(31)
    expect(validation.valid).toBe(false)
    expect(normalizeGroupQueryFilter(input)).toBeUndefined()
  })

  it('fails an over-deep tree and rejects non-instance fields in an instance scope', () => {
    const deep: GroupQueryFilter = filter({
      type: 'group',
      combinator: 'and',
      children: [{
        type: 'group',
        combinator: 'and',
        children: [{
          type: 'group',
          combinator: 'and',
          children: [condition()],
        }],
      }],
    })
    expect(validateGroupQueryFilter(deep).errors.join(' ')).toContain('超过 3 层')

    const wrongScope = filter({
      type: 'group',
      combinator: 'and',
      scope: 'instance',
      children: [condition({ field: 'size', operator: 'gt', value: 1 })],
    })
    expect(validateGroupQueryFilter(wrongScope).errors.join(' ')).toContain('同一实例')
  })

  it('produces a readable summary without exposing stable keys or byte values', () => {
    const input = filter(
      condition({ field: 'size', operator: 'lt', value: 536_870_912, displayUnit: 'MiB' }),
      condition({ field: 'site', operator: 'in', value: ['site:a'] }),
    )
    const summary = summarizeGroupQuery(input, { sites: new Map([['site:a', 'A 站']]) })

    expect(summary).toContain('512 MiB')
    expect(summary).toContain('A 站')
    expect(summary).not.toContain('site:a')
    expect(summary).not.toContain('536870912')
  })

  it('supports requiring all selected sites in one condition', () => {
    const input = filter(condition({
      field: 'site', operator: 'contains_all', value: ['site:a', 'site:b'],
    }))
    expect(validateGroupQueryFilter(input).valid).toBe(true)
    expect(summarizeGroupQuery(input, {
      sites: new Map([['site:a', 'A 站'], ['site:b', 'B 站']]),
    })).toContain('同时包含全部 A 站、B 站')
    expect(JSON.parse(encodeGroupQueryFilter(input) ?? '{}')).toMatchObject({
      root: { children: [{ field: 'site', operator: 'contains_all', value: ['site:a', 'site:b'] }] },
    })
  })

  it('describes independent negative instance predicates as NOT EXISTS semantics', () => {
    const independent = summarizeGroupQuery(filter(condition({
      field: 'state', operator: 'not_in', value: ['seeding'],
    })))
    expect(independent).toContain('组内不存在')
    expect(independent).not.toContain('存在实例的运行状态 均不包含')

    const scoped = summarizeGroupQuery(filter({
      type: 'group',
      combinator: 'and',
      scope: 'instance',
      children: [condition({ field: 'state', operator: 'not_in', value: ['seeding'] })],
    }))
    expect(scoped).toContain('同一实例满足')
    expect(scoped).toContain('运行状态 均不包含 seeding')
  })

  it('rejects exact size comparisons that do not resolve to a whole byte', () => {
    const input = filter(condition({ field: 'size', operator: 'eq', value: 1.5, displayUnit: 'B' }))
    expect(validateGroupQueryFilter(input).errors.join(' ')).toContain('整数字节')
    expect(encodeGroupQueryFilter(input)).toBeUndefined()
  })
})
