import dayjs from 'dayjs'
import type {
  GroupQueryCondition,
  GroupQueryField,
  GroupQueryFilter,
  GroupQueryGroup,
  GroupQueryNode,
  GroupQueryOperator,
  GroupQueryValue,
  GroupSizeUnit,
} from '../api/types'

export const GROUP_QUERY_MAX_DEPTH = 3
export const GROUP_QUERY_MAX_CONDITIONS = 30
export const GROUP_QUERY_MAX_ARRAY_ITEMS = 20
export const GROUP_QUERY_MAX_BIND_VALUES = 300

export const GROUP_SIZE_UNIT_BYTES: Record<GroupSizeUnit, number> = {
  B: 1,
  KiB: 1024,
  MiB: 1024 ** 2,
  GiB: 1024 ** 3,
  TiB: 1024 ** 4,
}

export const groupSizeValueToBytes = (
  value: number,
  unit: GroupSizeUnit,
  operator: GroupQueryOperator,
  boundIndex = 0,
): number => {
  const bytes = value * GROUP_SIZE_UNIT_BYTES[unit]
  if (operator === 'lt' || operator === 'gte') return Math.ceil(bytes)
  if (operator === 'lte' || operator === 'gt') return Math.floor(bytes)
  if (operator === 'between') return boundIndex === 0 ? Math.ceil(bytes) : Math.floor(bytes)
  return bytes
}

export const GROUP_QUERY_FIELD_LABELS: Record<GroupQueryField, string> = {
  group_name: '聚合名称',
  instance_name: '实例名称',
  path: '存放路径',
  size: '内容大小',
  instance_count: '实例数量',
  site_count: '站点 / Tracker 数量',
  downloader_count: '下载器数量',
  data_copy_count: '物理数据副本数',
  oldest_added_at: '最旧添加时间',
  updated_at: '更新时间',
  site: '站点',
  downloader: '下载器',
  state: '运行状态',
  locked: '锁定状态',
  grouping_method: '分组方式',
  confidence: '置信度',
  stale: '同步新鲜度',
  has_unmapped_tracker: '未映射 Tracker',
}

export const GROUP_QUERY_OPERATOR_LABELS: Record<GroupQueryOperator, string> = {
  contains: '包含',
  not_contains: '不包含',
  starts_with: '开头是',
  ends_with: '结尾是',
  eq: '等于',
  ne: '不等于',
  lt: '小于',
  lte: '小于等于',
  gt: '大于',
  gte: '大于等于',
  between: '介于（含边界）',
  before: '早于',
  after: '晚于',
  on: '日期为',
  on_or_before: '不晚于',
  on_or_after: '不早于',
  in: '包含任一',
  contains_all: '同时包含全部',
  not_in: '均不包含',
  is_empty: '为空',
  is_not_empty: '不为空',
}

const allFields = new Set<GroupQueryField>(Object.keys(GROUP_QUERY_FIELD_LABELS) as GroupQueryField[])
const stringFields = new Set<GroupQueryField>(['group_name', 'instance_name', 'path'])
const numberFields = new Set<GroupQueryField>([
  'size', 'instance_count', 'site_count', 'downloader_count', 'data_copy_count',
])
const dateFields = new Set<GroupQueryField>(['oldest_added_at', 'updated_at'])
const collectionFields = new Set<GroupQueryField>(['site', 'downloader', 'state'])
export const GROUP_QUERY_INSTANCE_FIELDS = new Set<GroupQueryField>(['instance_name', 'path', 'site', 'downloader', 'state'])

const stringOperators: GroupQueryOperator[] = [
  'contains', 'not_contains', 'starts_with', 'ends_with', 'eq', 'ne', 'is_empty', 'is_not_empty',
]
const numberOperators: GroupQueryOperator[] = ['eq', 'ne', 'lt', 'lte', 'gt', 'gte', 'between']
const dateOperators: GroupQueryOperator[] = [
  'before', 'on_or_before', 'on', 'on_or_after', 'after', 'between', 'is_empty', 'is_not_empty',
]
const collectionOperators: GroupQueryOperator[] = ['in', 'not_in']
const siteOperators: GroupQueryOperator[] = ['in', 'contains_all', 'not_in']
const enumOperators: GroupQueryOperator[] = ['eq', 'ne']

export const groupQueryOperatorsForField = (field: GroupQueryField): GroupQueryOperator[] => {
  if (stringFields.has(field)) return stringOperators
  if (numberFields.has(field)) return numberOperators
  if (dateFields.has(field)) return dateOperators
  if (field === 'site') return siteOperators
  if (collectionFields.has(field)) return collectionOperators
  return enumOperators
}

export const defaultGroupQueryOperator = (field: GroupQueryField): GroupQueryOperator =>
  groupQueryOperatorsForField(field)[0]

const operatorNeedsNoValue = (operator: GroupQueryOperator) => operator === 'is_empty' || operator === 'is_not_empty'

const normalizedValue = (condition: GroupQueryCondition): GroupQueryValue | undefined => {
  if (operatorNeedsNoValue(condition.operator)) return undefined
  const value = condition.value
  if (typeof value === 'string') return value.trim()
  if (typeof value === 'number' || typeof value === 'boolean') return value
  if (Array.isArray(value)) {
    return value.map((item) => typeof item === 'string' ? item.trim() : item) as string[] | number[]
  }
  return undefined
}

const validateCondition = (condition: GroupQueryCondition, path: string, instanceScoped: boolean): string[] => {
  const errors: string[] = []
  if (!allFields.has(condition.field)) return [`${path}使用了不支持的字段`]
  if (instanceScoped && !GROUP_QUERY_INSTANCE_FIELDS.has(condition.field)) {
    errors.push(`${path}位于“同一实例”组内，只能使用实例名称、路径、站点、下载器或运行状态`)
  }
  if (!groupQueryOperatorsForField(condition.field).includes(condition.operator)) {
    errors.push(`${path}的操作符不适用于“${GROUP_QUERY_FIELD_LABELS[condition.field]}”`)
    return errors
  }
  if (operatorNeedsNoValue(condition.operator)) return errors

  const value = condition.value
  if (stringFields.has(condition.field)) {
    if (typeof value !== 'string' || !value.trim()) errors.push(`${path}需要填写文本`)
    else if (value.trim().length > 1024) errors.push(`${path}文本不能超过 1,024 个字符`)
    return errors
  }
  if (numberFields.has(condition.field)) {
    const values = condition.operator === 'between' ? value : [value]
    if (!Array.isArray(values) || values.length !== (condition.operator === 'between' ? 2 : 1)
      || values.some((item) => typeof item !== 'number' || !Number.isFinite(item))) {
      errors.push(`${path}需要填写${condition.operator === 'between' ? '两个有效数值' : '有效数值'}`)
      return errors
    }
    const numericValues = values as number[]
    if (condition.field === 'size' && numericValues.some((item) => !Number.isInteger(item))) {
      errors.push(`${path}换算后不是整数字节；“等于 / 不等于”请使用能精确换算成字节的数值`)
    } else if (numericValues.some((item) => !Number.isSafeInteger(item))) {
      errors.push(`${path}的数值超出安全范围`)
    }
    if (numericValues.some((item) => item < 0)) errors.push(`${path}不能使用负数`)
    if (condition.field !== 'size' && numericValues.some((item) => !Number.isInteger(item))) {
      errors.push(`${path}需要填写整数`)
    }
    if (condition.field !== 'size' && numericValues.some((item) => item > 1_000_000_000)) {
      errors.push(`${path}不能超过 1,000,000,000`)
    }
    if (condition.operator === 'between' && numericValues[0] > numericValues[1]) {
      errors.push(`${path}的起始值不能大于结束值`)
    }
    return errors
  }
  if (dateFields.has(condition.field)) {
    const values = condition.operator === 'between' ? value : [value]
    if (!Array.isArray(values) || values.length !== (condition.operator === 'between' ? 2 : 1)
      || values.some((item) => typeof item !== 'string' || !dayjs(item).isValid())) {
      errors.push(`${path}需要选择${condition.operator === 'between' ? '完整的起止日期' : '日期'}`)
      return errors
    }
    if (condition.operator === 'between' && dayjs(String(values[0])).isAfter(dayjs(String(values[1])))) {
      errors.push(`${path}的开始日期不能晚于结束日期`)
    }
    return errors
  }
  if (collectionFields.has(condition.field)) {
    if (!Array.isArray(value) || !value.length || value.some((item) => typeof item !== 'string' || !item.trim())) {
      errors.push(`${path}至少需要选择或输入一项`)
    } else if (value.length > GROUP_QUERY_MAX_ARRAY_ITEMS) {
      errors.push(`${path}最多支持 ${GROUP_QUERY_MAX_ARRAY_ITEMS} 项`)
    }
    return errors
  }

  if (condition.field === 'locked' || condition.field === 'stale' || condition.field === 'has_unmapped_tracker') {
    if (typeof value !== 'boolean') errors.push(`${path}需要选择“是”或“否”`)
  } else if (condition.field === 'grouping_method') {
    if (value !== 'auto' && value !== 'manual') errors.push(`${path}需要选择分组方式`)
  } else if (condition.field === 'confidence') {
    if (!['tentative', 'verified', 'manual', 'conflict'].includes(String(value))) errors.push(`${path}需要选择置信度`)
  }
  return errors
}

export interface GroupQueryValidationResult {
  valid: boolean
  errors: string[]
  conditionCount: number
}

/** Validates the whole tree fail-closed: no node is silently dropped or truncated. */
export const validateGroupQueryFilter = (filter?: GroupQueryFilter): GroupQueryValidationResult => {
  if (!filter) return { valid: true, errors: [], conditionCount: 0 }
  if (filter.version !== 1 || !filter.root || filter.root.type !== 'group') {
    return { valid: false, errors: ['查询规则版本或根节点无效'], conditionCount: 0 }
  }
  const errors: string[] = []
  let conditionCount = 0
  let bindValueCount = 0

  const visitGroup = (group: GroupQueryGroup, depth: number, path: string, inheritedInstanceScope: boolean) => {
    if (depth > GROUP_QUERY_MAX_DEPTH) {
      errors.push(`${path}超过 ${GROUP_QUERY_MAX_DEPTH} 层嵌套限制`)
      return
    }
    if (group.combinator !== 'and' && group.combinator !== 'or') errors.push(`${path}的条件关系无效`)
    if (group.scope !== undefined && group.scope !== 'instance') errors.push(`${path}的作用域无效`)
    if (group.negated !== undefined && typeof group.negated !== 'boolean') errors.push(`${path}的取反标记无效`)
    if (!Array.isArray(group.children)) {
      errors.push(`${path}缺少条件列表`)
      return
    }
    if (depth > 1 && !group.children.length) errors.push(`${path}不能为空`)
    const instanceScoped = inheritedInstanceScope || group.scope === 'instance'
    group.children.forEach((node, index) => {
      const childPath = `${path}第 ${index + 1} 项`
      if (!node || (node.type !== 'condition' && node.type !== 'group')) {
        errors.push(`${childPath}类型无效`)
      } else if (node.type === 'condition') {
        conditionCount += 1
        if (!operatorNeedsNoValue(node.operator)) bindValueCount += Array.isArray(node.value) ? node.value.length : 1
        errors.push(...validateCondition(node, childPath, instanceScoped))
      } else {
        visitGroup(node, depth + 1, `${childPath}条件组`, instanceScoped)
      }
    })
  }

  visitGroup(filter.root, 1, '根规则', false)
  if (conditionCount > GROUP_QUERY_MAX_CONDITIONS) errors.push(`条件数量不能超过 ${GROUP_QUERY_MAX_CONDITIONS} 条`)
  if (bindValueCount > GROUP_QUERY_MAX_BIND_VALUES) errors.push(`查询值总数不能超过 ${GROUP_QUERY_MAX_BIND_VALUES} 个`)
  return { valid: errors.length === 0, errors, conditionCount }
}

const normalizeNode = (node: GroupQueryNode): GroupQueryNode => {
  if (node.type === 'group') {
    return {
      type: 'group',
      combinator: node.combinator,
      ...(node.scope ? { scope: node.scope } : {}),
      ...(node.negated ? { negated: true } : {}),
      children: node.children.map(normalizeNode),
    }
  }
  return {
    type: 'condition',
    field: node.field,
    operator: node.operator,
    ...(operatorNeedsNoValue(node.operator) ? {} : { value: normalizedValue(node) }),
    ...(node.field === 'size' && node.displayUnit ? { displayUnit: node.displayUnit } : {}),
  }
}

export const normalizeGroupQueryFilter = (filter?: GroupQueryFilter): GroupQueryFilter | undefined => {
  if (!filter) return undefined
  const validation = validateGroupQueryFilter(filter)
  if (!validation.valid || validation.conditionCount === 0) return undefined
  return { version: 1, root: normalizeNode(filter.root) as GroupQueryGroup }
}

const wireNode = (node: GroupQueryNode): GroupQueryNode => node.type === 'group'
  ? {
      type: 'group',
      combinator: node.combinator,
      ...(node.scope ? { scope: node.scope } : {}),
      ...(node.negated ? { negated: true } : {}),
      children: node.children.map(wireNode),
    }
  : {
      type: 'condition',
      field: node.field,
      operator: node.operator,
      ...(node.value === undefined ? {} : { value: node.value }),
    }

/** Encodes only a fully valid AST and strips all UI-only metadata. */
export const groupQueryFilterForWire = (filter?: GroupQueryFilter): GroupQueryFilter | undefined => {
  const normalized = normalizeGroupQueryFilter(filter)
  return normalized ? { version: 1, root: wireNode(normalized.root) as GroupQueryGroup } : undefined
}

export const encodeGroupQueryFilter = (filter?: GroupQueryFilter): string | undefined => {
  const wireFilter = groupQueryFilterForWire(filter)
  return wireFilter ? JSON.stringify(wireFilter) : undefined
}

export const countGroupQueryConditions = (filter?: GroupQueryFilter): number =>
  validateGroupQueryFilter(filter).conditionCount

export interface GroupQuerySummaryLabels {
  sites?: Map<string, string>
  downloaders?: Map<string, string>
}

const readableValue = (condition: GroupQueryCondition, labels: GroupQuerySummaryLabels): string => {
  if (condition.value === undefined) return ''
  if (condition.field === 'size') {
    const unit = condition.displayUnit ?? 'GiB'
    const divisor = GROUP_SIZE_UNIT_BYTES[unit]
    const values = Array.isArray(condition.value) ? condition.value : [condition.value]
    return values.map((value) => typeof value === 'number'
      ? `${Number((value / divisor).toPrecision(6))} ${unit}`
      : String(value)).join(' ～ ')
  }
  if (condition.field === 'site' || condition.field === 'downloader') {
    const map = condition.field === 'site' ? labels.sites : labels.downloaders
    const values = Array.isArray(condition.value) ? condition.value : [condition.value]
    return values.map((value) => map?.get(String(value)) ?? String(value)).join('、')
  }
  if (dateFields.has(condition.field)) {
    const values = Array.isArray(condition.value) ? condition.value : [condition.value]
    return values.map((value) => dayjs(String(value)).format('YYYY-MM-DD')).join(' ～ ')
  }
  if (condition.field === 'locked') return condition.value ? '已锁定' : '未锁定'
  if (condition.field === 'stale') return condition.value ? '需要刷新' : '快照新鲜'
  if (condition.field === 'has_unmapped_tracker') return condition.value ? '有' : '没有'
  if (condition.field === 'grouping_method') return condition.value === 'manual' ? '手动分组' : '自动分组'
  if (condition.field === 'confidence') {
    return ({ tentative: '待确认', verified: '已验证', manual: '人工确认', conflict: '有冲突' } as Record<string, string>)[String(condition.value)] ?? String(condition.value)
  }
  const values = Array.isArray(condition.value) ? condition.value : [condition.value]
  return values.join('、')
}

export const summarizeGroupQuery = (filter?: GroupQueryFilter, labels: GroupQuerySummaryLabels = {}): string => {
  const normalized = normalizeGroupQueryFilter(filter)
  if (!normalized) return '未设置高级条件'
  const visit = (node: GroupQueryNode, nested: boolean, instanceScoped: boolean): string => {
    if (node.type === 'condition') {
      const value = readableValue(node, labels)
      const fieldLabel = GROUP_QUERY_FIELD_LABELS[node.field]
      if (GROUP_QUERY_INSTANCE_FIELDS.has(node.field) && !instanceScoped) {
        const positiveOperator: Partial<Record<GroupQueryOperator, GroupQueryOperator>> = {
          not_contains: 'contains',
          ne: 'eq',
          not_in: 'in',
        }
        if (node.operator === 'is_empty') return `所有实例的${fieldLabel}均为空`
        const positive = positiveOperator[node.operator]
        if (positive) {
          return `组内不存在${fieldLabel} ${GROUP_QUERY_OPERATOR_LABELS[positive]}${value ? ` ${value}` : ''}的实例`
        }
        return `存在实例的${fieldLabel} ${GROUP_QUERY_OPERATOR_LABELS[node.operator]}${value ? ` ${value}` : ''}`
      }
      return `${fieldLabel} ${GROUP_QUERY_OPERATOR_LABELS[node.operator]}${value ? ` ${value}` : ''}`
    }
    const scoped = instanceScoped || node.scope === 'instance'
    const joined = node.children.map((child) => visit(child, true, scoped)).join(node.combinator === 'and' ? ' 并且 ' : ' 或者 ')
    const scopedText = node.scope === 'instance' ? `同一实例满足（${joined}）` : joined
    const negatedText = node.negated ? `非（${scopedText}）` : scopedText
    return nested && node.scope !== 'instance' && node.children.length > 1 ? `（${negatedText}）` : negatedText
  }
  return visit(normalized.root, false, false)
}
