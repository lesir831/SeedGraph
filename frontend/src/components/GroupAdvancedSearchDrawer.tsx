import {
  ApartmentOutlined,
  CopyOutlined,
  DeleteOutlined,
  PlusOutlined,
  SearchOutlined,
} from '@ant-design/icons'
import {
  Alert,
  App,
  Button,
  Checkbox,
  DatePicker,
  Drawer,
  Input,
  InputNumber,
  Segmented,
  Select,
  Space,
  Tag,
  Typography,
} from 'antd'
import dayjs from 'dayjs'
import { useEffect, useMemo, useState } from 'react'
import type {
  Downloader,
  GroupQueryCondition,
  GroupQueryField,
  GroupQueryFilter,
  GroupQueryGroup,
  GroupQueryNode,
  GroupQueryOperator,
  GroupQueryValue,
  GroupSiteSummary,
  GroupSizeUnit,
} from '../api/types'
import {
  defaultGroupQueryOperator,
  GROUP_QUERY_FIELD_LABELS,
  GROUP_QUERY_INSTANCE_FIELDS,
  GROUP_QUERY_MAX_ARRAY_ITEMS,
  GROUP_QUERY_MAX_CONDITIONS,
  GROUP_QUERY_MAX_DEPTH,
  GROUP_QUERY_OPERATOR_LABELS,
  GROUP_SIZE_UNIT_BYTES,
  groupQueryOperatorsForField,
  groupSizeValueToBytes,
  normalizeGroupQueryFilter,
  summarizeGroupQuery,
  validateGroupQueryFilter,
} from '../utils/groupQuery'

interface DraftCondition extends Omit<GroupQueryCondition, 'value'> {
  id: string
  value?: GroupQueryValue
}

interface DraftGroup extends Omit<GroupQueryGroup, 'children'> {
  id: string
  children: DraftNode[]
}

type DraftNode = DraftCondition | DraftGroup

interface GroupAdvancedSearchDrawerProps {
  open: boolean
  filter?: GroupQueryFilter
  siteOptions: GroupSiteSummary[]
  siteOptionsLoading?: boolean
  siteOptionsError?: boolean
  downloaders: Downloader[]
  downloadersLoading?: boolean
  onRetrySiteOptions?: () => void
  onClose: () => void
  onApply: (filter?: GroupQueryFilter) => void
}

const fieldOptions = [
  {
    label: '文本',
    options: (['group_name', 'instance_name', 'path'] satisfies GroupQueryField[]).map((value) => ({
      value,
      label: GROUP_QUERY_INSTANCE_FIELDS.has(value) ? `${GROUP_QUERY_FIELD_LABELS[value]}（存在实例）` : GROUP_QUERY_FIELD_LABELS[value],
    })),
  },
  {
    label: '数字与时间',
    options: (['size', 'instance_count', 'site_count', 'downloader_count', 'data_copy_count', 'oldest_added_at', 'updated_at'] satisfies GroupQueryField[])
      .map((value) => ({ value, label: GROUP_QUERY_FIELD_LABELS[value] })),
  },
  {
    label: '关联信息',
    options: (['site', 'downloader', 'state'] satisfies GroupQueryField[])
      .map((value) => ({ value, label: `${GROUP_QUERY_FIELD_LABELS[value]}（存在实例）` })),
  },
  {
    label: '状态',
    options: (['locked', 'grouping_method', 'confidence', 'stale', 'has_unmapped_tracker'] satisfies GroupQueryField[])
      .map((value) => ({ value, label: GROUP_QUERY_FIELD_LABELS[value] })),
  },
]

const instanceFieldOptions = [{
  label: '同一实例字段',
  options: (['instance_name', 'path', 'site', 'downloader', 'state'] satisfies GroupQueryField[])
    .map((value) => ({ value, label: GROUP_QUERY_FIELD_LABELS[value] })),
}]

const stateOptions = [
  { value: 'downloading', label: '下载中' },
  { value: 'seeding', label: '做种中（Transmission）' },
  { value: 'uploading', label: '做种中（qBittorrent）' },
  { value: 'stopped', label: '已停止（Transmission）' },
  { value: 'pausedUP', label: '已暂停上传（qBittorrent）' },
  { value: 'pausedDL', label: '已暂停下载（qBittorrent）' },
  { value: 'stalledUP', label: '上传停滞（qBittorrent）' },
  { value: 'stalledDL', label: '下载停滞（qBittorrent）' },
  { value: 'error', label: '异常' },
]

const sizeUnitOptions = (Object.keys(GROUP_SIZE_UNIT_BYTES) as GroupSizeUnit[])
  .map((value) => ({ value, label: value }))

let idSequence = 0
const nextId = () => `query-node-${++idSequence}`

const emptyCondition = (field: GroupQueryField = 'group_name'): DraftCondition => ({
  id: nextId(),
  type: 'condition',
  field,
  operator: defaultGroupQueryOperator(field),
  value: '',
})

const emptyRoot = (): DraftGroup => ({
  id: nextId(),
  type: 'group',
  combinator: 'and',
  children: [emptyCondition()],
})

const preferredSizeUnit = (bytes: number): GroupSizeUnit => {
  if (bytes >= GROUP_SIZE_UNIT_BYTES.TiB) return 'TiB'
  if (bytes >= GROUP_SIZE_UNIT_BYTES.GiB) return 'GiB'
  if (bytes >= GROUP_SIZE_UNIT_BYTES.MiB) return 'MiB'
  if (bytes >= GROUP_SIZE_UNIT_BYTES.KiB) return 'KiB'
  return 'B'
}

const toDraftNode = (node: GroupQueryNode): DraftNode => {
  if (node.type === 'group') {
    return { ...node, id: nextId(), children: node.children.map(toDraftNode) }
  }
  if (node.field !== 'size') return { ...node, id: nextId() }
  const rawValues = Array.isArray(node.value) ? node.value : [node.value]
  const first = rawValues.find((value): value is number => typeof value === 'number') ?? 0
  const displayUnit = node.displayUnit ?? preferredSizeUnit(first)
  const divisor = GROUP_SIZE_UNIT_BYTES[displayUnit]
  const displayValues = rawValues.map((value) => typeof value === 'number' ? value / divisor : value)
  return {
    ...node,
    id: nextId(),
    displayUnit,
    value: Array.isArray(node.value) ? displayValues as number[] : displayValues[0],
  }
}

const toDraft = (filter?: GroupQueryFilter): DraftGroup => {
  const normalized = normalizeGroupQueryFilter(filter)
  return normalized ? toDraftNode(normalized.root) as DraftGroup : emptyRoot()
}

const countDraftConditions = (node: DraftNode): number => node.type === 'condition'
  ? 1
  : node.children.reduce((total, child) => total + countDraftConditions(child), 0)

const countIncompleteConditions = (node: DraftNode): number => {
  if (node.type === 'condition') {
    if (node.operator === 'is_empty' || node.operator === 'is_not_empty') return 0
    if (typeof node.value === 'string') return node.value.trim() ? 0 : 1
    if (typeof node.value === 'number') return Number.isFinite(node.value) ? 0 : 1
    if (typeof node.value === 'boolean') return 0
    if (Array.isArray(node.value)) {
      if (node.operator === 'between') return node.value.length === 2 && node.value.every((value) => (
        typeof value === 'number' ? Number.isFinite(value) : typeof value === 'string' && Boolean(value.trim())
      )) ? 0 : 1
      return node.value.length && node.value.every((value) => typeof value !== 'string' || Boolean(value.trim())) ? 0 : 1
    }
    return 1
  }
  return node.children.reduce((total, child) => total + countIncompleteConditions(child), 0)
}

const mapNode = (group: DraftGroup, id: string, update: (node: DraftNode) => DraftNode): DraftGroup => ({
  ...group,
  children: group.children.map((child) => {
    if (child.id === id) return update(child)
    return child.type === 'group' ? mapNode(child, id, update) : child
  }),
})

const removeNode = (group: DraftGroup, id: string): DraftGroup => ({
  ...group,
  children: group.children
    .filter((child) => child.id !== id)
    .map((child) => child.type === 'group' ? removeNode(child, id) : child),
})

const cloneDraftNode = (node: DraftNode): DraftNode => node.type === 'condition'
  ? { ...node, id: nextId(), value: Array.isArray(node.value) ? [...node.value] as string[] | number[] : node.value }
  : { ...node, id: nextId(), children: node.children.map(cloneDraftNode) }

const duplicateNode = (group: DraftGroup, id: string): DraftGroup => ({
  ...group,
  children: group.children.flatMap((child) => {
    if (child.id === id) return [child, cloneDraftNode(child)]
    return [child.type === 'group' ? duplicateNode(child, id) : child]
  }),
})

const toFilterNode = (node: DraftNode): GroupQueryNode => {
  if (node.type === 'group') {
    return {
      type: 'group',
      combinator: node.combinator,
      ...(node.scope ? { scope: node.scope } : {}),
      ...(node.negated ? { negated: true } : {}),
      children: node.children.map(toFilterNode),
    }
  }
  let value = node.value
  if (node.field === 'size' && value !== undefined) {
    const displayUnit = node.displayUnit ?? 'GiB'
    value = (Array.isArray(value)
      ? value.map((item, index) => typeof item === 'number'
        ? groupSizeValueToBytes(item, displayUnit, node.operator, index)
        : item)
      : typeof value === 'number'
        ? groupSizeValueToBytes(value, displayUnit, node.operator)
        : value) as GroupQueryValue
  }
  return {
    type: 'condition',
    field: node.field,
    operator: node.operator,
    ...(value === undefined ? {} : { value }),
    ...(node.field === 'size' ? { displayUnit: node.displayUnit ?? 'GiB' } : {}),
  }
}

const toRawFilter = (root: DraftGroup): GroupQueryFilter => ({
  version: 1,
  root: toFilterNode(root) as GroupQueryGroup,
})

const toFilter = (root: DraftGroup): GroupQueryFilter | undefined => normalizeGroupQueryFilter(toRawFilter(root))

interface ValueEditorProps {
  condition: DraftCondition
  siteOptions: GroupSiteSummary[]
  siteOptionsLoading?: boolean
  downloaders: Downloader[]
  downloadersLoading?: boolean
  onChange: (patch: Partial<DraftCondition>) => void
}

function ConditionValueEditor({
  condition,
  siteOptions,
  siteOptionsLoading,
  downloaders,
  downloadersLoading,
  onChange,
}: ValueEditorProps) {
  if (condition.operator === 'is_empty' || condition.operator === 'is_not_empty') {
    return <div className="query-value-empty">无需填写值</div>
  }

  if (condition.field === 'group_name' || condition.field === 'instance_name' || condition.field === 'path') {
    return (
      <Input
        allowClear
        maxLength={1024}
        value={typeof condition.value === 'string' ? condition.value : ''}
        placeholder={condition.field === 'path' ? '输入完整或部分路径' : '输入名称关键词'}
        onChange={(event) => onChange({ value: event.target.value })}
      />
    )
  }

  if (condition.field === 'size'
    || condition.field === 'instance_count'
    || condition.field === 'site_count'
    || condition.field === 'downloader_count'
    || condition.field === 'data_copy_count') {
    const isSize = condition.field === 'size'
    const isBetween = condition.operator === 'between'
    const values = Array.isArray(condition.value) ? condition.value : [condition.value]
    const input = (index: number, placeholder: string) => (
      <InputNumber
        min={0}
        precision={isSize && condition.displayUnit !== 'B' ? 6 : 0}
        max={isSize ? Number.MAX_SAFE_INTEGER / GROUP_SIZE_UNIT_BYTES[condition.displayUnit ?? 'GiB'] : 1_000_000_000}
        value={typeof values[index] === 'number' ? values[index] : null}
        placeholder={placeholder}
        className="query-number-input"
        onChange={(next) => {
          if (isBetween) {
            const updated: number[] = [
              typeof values[0] === 'number' ? values[0] : Number.NaN,
              typeof values[1] === 'number' ? values[1] : Number.NaN,
            ]
            updated[index] = typeof next === 'number' ? next : Number.NaN
            onChange({ value: updated })
          } else {
            onChange({ value: typeof next === 'number' ? next : undefined })
          }
        }}
      />
    )
    const inputs = (
      <div className="query-number-value">
        {input(0, isBetween ? '最小值' : '输入数值')}
        {isBetween && <><span className="query-range-separator">至</span>{input(1, '最大值')}</>}
        {isSize && (
          <Select<GroupSizeUnit>
            className="query-size-unit"
            value={condition.displayUnit ?? 'GiB'}
            options={sizeUnitOptions}
            onChange={(displayUnit) => onChange({ displayUnit })}
          />
        )}
      </div>
    )
    if (!isSize) return inputs
    const unit = condition.displayUnit ?? 'GiB'
    const numericValues = values.filter((value): value is number => typeof value === 'number' && Number.isFinite(value))
    const convertedValues = numericValues.map((value, index) => groupSizeValueToBytes(value, unit, condition.operator, index))
    const hasFractionalExactValue = (condition.operator === 'eq' || condition.operator === 'ne')
      && convertedValues.some((value) => !Number.isInteger(value))
    const boundaryText = numericValues.length === (isBetween ? 2 : 1)
      ? hasFractionalExactValue
        ? '当前数值无法精确换算为整数字节，请调整数值或改用范围操作符。'
        : `实际字节边界：${convertedValues.map((value) => value.toLocaleString('zh-CN')).join(' ～ ')} B`
      : '输入值将按所选操作符精确换算为整数字节边界。'
    return (
      <div className="query-size-value-editor">
        {inputs}
        <Typography.Text type={hasFractionalExactValue ? 'danger' : 'secondary'}>{boundaryText}</Typography.Text>
      </div>
    )
  }

  if (condition.field === 'oldest_added_at' || condition.field === 'updated_at') {
    if (condition.operator === 'between') {
      const values = Array.isArray(condition.value) ? condition.value : []
      const range = values.length === 2 && values.every((value) => dayjs(String(value)).isValid())
        ? [dayjs(String(values[0])), dayjs(String(values[1]))] as [ReturnType<typeof dayjs>, ReturnType<typeof dayjs>]
        : null
      return (
        <div className="query-date-value">
          <DatePicker.RangePicker
            allowClear
            className="full-width-control"
            value={range}
            onChange={(next) => onChange({
              value: next?.[0] && next[1]
                ? [next[0].startOf('day').format(), next[1].startOf('day').format()]
                : undefined,
            })}
          />
          <Typography.Text type="secondary">开始和结束日期均包含整天。</Typography.Text>
        </div>
      )
    }
    const value = typeof condition.value === 'string' && dayjs(condition.value).isValid() ? dayjs(condition.value) : null
    return (
      <div className="query-date-value">
        <DatePicker
          allowClear
          className="full-width-control"
          value={value}
          onChange={(next) => onChange({ value: next?.startOf('day').format() })}
        />
        {condition.operator === 'on' && <Typography.Text type="secondary">匹配所选日期的整天。</Typography.Text>}
      </div>
    )
  }

  if (condition.field === 'site' || condition.field === 'downloader' || condition.field === 'state') {
    const options = condition.field === 'site'
      ? siteOptions.map((site) => ({ value: site.key, label: site.label }))
      : condition.field === 'downloader'
        ? downloaders.map((downloader) => ({ value: downloader.id, label: downloader.name }))
        : stateOptions
    return (
      <Select<string[]>
        mode={condition.field === 'state' ? 'tags' : 'multiple'}
        allowClear
        showSearch
        maxTagCount="responsive"
        maxCount={GROUP_QUERY_MAX_ARRAY_ITEMS}
        optionFilterProp="label"
        value={Array.isArray(condition.value) ? condition.value.map(String) : []}
        loading={condition.field === 'site' ? siteOptionsLoading : condition.field === 'downloader' ? downloadersLoading : false}
        options={options}
        placeholder={condition.field === 'site' ? '选择站点' : condition.field === 'downloader' ? '选择下载器' : '选择运行状态'}
        onChange={(value) => onChange({ value })}
      />
    )
  }

  if (condition.field === 'locked' || condition.field === 'stale' || condition.field === 'has_unmapped_tracker') {
    const options = condition.field === 'locked'
      ? [{ value: 'true', label: '已锁定' }, { value: 'false', label: '未锁定' }]
      : condition.field === 'stale'
        ? [{ value: 'false', label: '快照新鲜' }, { value: 'true', label: '需要刷新' }]
        : [{ value: 'true', label: '有未映射 Tracker' }, { value: 'false', label: '没有未映射 Tracker' }]
    return (
      <Select
        value={typeof condition.value === 'boolean' ? String(condition.value) : undefined}
        options={options}
        placeholder="请选择"
        onChange={(value) => onChange({ value: value === 'true' })}
      />
    )
  }

  if (condition.field === 'confidence') {
    return (
      <Select
        value={typeof condition.value === 'string' ? condition.value : undefined}
        options={[
          { value: 'tentative', label: '待确认' },
          { value: 'verified', label: '已验证' },
          { value: 'manual', label: '人工确认' },
          { value: 'conflict', label: '有冲突' },
        ]}
        placeholder="请选择"
        onChange={(value) => onChange({ value })}
      />
    )
  }

  return (
    <Select
      value={typeof condition.value === 'string' ? condition.value : undefined}
      options={[
        { value: 'auto', label: '自动分组' },
        { value: 'manual', label: '手动分组' },
      ]}
      placeholder="请选择"
      onChange={(value) => onChange({ value })}
    />
  )
}

interface GroupEditorProps extends Omit<ValueEditorProps, 'condition' | 'onChange'> {
  group: DraftGroup
  depth: number
  inheritedInstanceScope: boolean
  totalConditions: number
  onUpdateNode: (id: string, update: (node: DraftNode) => DraftNode) => void
  onRemoveNode: (id: string) => void
  onAddCondition: (groupId: string, defaultField: GroupQueryField) => void
  onAddGroup: (groupId: string, scope: 'instance' | undefined, defaultField: GroupQueryField) => void
  onDuplicateNode: (id: string) => void
}

function QueryGroupEditor({
  group,
  depth,
  inheritedInstanceScope,
  totalConditions,
  siteOptions,
  siteOptionsLoading,
  downloaders,
  downloadersLoading,
  onUpdateNode,
  onRemoveNode,
  onAddCondition,
  onAddGroup,
  onDuplicateNode,
}: GroupEditorProps) {
  const nested = depth > 1
  const instanceScoped = inheritedInstanceScope || group.scope === 'instance'
  const selectableFields = instanceScoped ? instanceFieldOptions : fieldOptions
  return (
    <section className={`query-group ${nested ? 'query-group-nested' : 'query-group-root'}`}>
      <div className="query-group-header">
        <div className="query-group-relation">
          <Typography.Text strong>{nested ? '条件组' : '匹配规则'}</Typography.Text>
          {group.scope === 'instance' && <Tag color="purple">同一实例</Tag>}
          <Typography.Text type="secondary">满足以下</Typography.Text>
          <Segmented
            size="small"
            value={group.combinator}
            options={[{ value: 'and', label: '所有条件' }, { value: 'or', label: '任一条件' }]}
            onChange={(combinator) => onUpdateNode(group.id, (node) => ({ ...node, combinator }) as DraftGroup)}
          />
          <Checkbox
            checked={Boolean(group.negated)}
            onChange={(event) => onUpdateNode(group.id, (node) => ({ ...node, negated: event.target.checked }) as DraftGroup)}
          >
            本组取反 NOT
          </Checkbox>
        </div>
        {nested && (
          <Space size={2}>
            <Button
              type="text"
              icon={<CopyOutlined />}
              aria-label="复制条件组"
              disabled={countDraftConditions(group) === 0
                || totalConditions + countDraftConditions(group) > GROUP_QUERY_MAX_CONDITIONS}
              onClick={() => onDuplicateNode(group.id)}
            />
            <Button type="text" danger icon={<DeleteOutlined />} aria-label="删除条件组" onClick={() => onRemoveNode(group.id)} />
          </Space>
        )}
      </div>

      <div className="query-group-children">
        {group.children.map((node, index) => node.type === 'group' ? (
          <QueryGroupEditor
            key={node.id}
            group={node}
            depth={depth + 1}
            inheritedInstanceScope={instanceScoped}
            totalConditions={totalConditions}
            siteOptions={siteOptions}
            siteOptionsLoading={siteOptionsLoading}
            downloaders={downloaders}
            downloadersLoading={downloadersLoading}
            onUpdateNode={onUpdateNode}
            onRemoveNode={onRemoveNode}
            onAddCondition={onAddCondition}
            onAddGroup={onAddGroup}
            onDuplicateNode={onDuplicateNode}
          />
        ) : (
          <div className="query-condition-row" key={node.id}>
            <div className="query-condition-index">{index + 1}</div>
            <Select<GroupQueryField>
              className="query-field-select"
              value={node.field}
              options={selectableFields}
              onChange={(field) => onUpdateNode(node.id, (current) => ({
                ...current,
                field,
                operator: defaultGroupQueryOperator(field),
                value: undefined,
                displayUnit: field === 'size' ? 'GiB' : undefined,
              }) as DraftCondition)}
            />
            <Select<GroupQueryOperator>
              className="query-operator-select"
              value={node.operator}
              options={groupQueryOperatorsForField(node.field).map((value) => ({
                value,
                label: GROUP_QUERY_OPERATOR_LABELS[value],
              }))}
              onChange={(operator) => onUpdateNode(node.id, (current) => ({
                ...current,
                operator,
                value: undefined,
              }) as DraftCondition)}
            />
            <div className="query-condition-value">
              <ConditionValueEditor
                condition={node}
                siteOptions={siteOptions}
                siteOptionsLoading={siteOptionsLoading}
                downloaders={downloaders}
                downloadersLoading={downloadersLoading}
                onChange={(patch) => onUpdateNode(node.id, (current) => current.type === 'condition'
                  ? { ...current, ...patch }
                  : current)}
              />
            </div>
            <Space size={2} className="query-condition-actions">
              <Button
                type="text"
                icon={<CopyOutlined />}
                aria-label="复制条件"
                disabled={totalConditions >= GROUP_QUERY_MAX_CONDITIONS}
                onClick={() => onDuplicateNode(node.id)}
              />
              <Button type="text" danger icon={<DeleteOutlined />} aria-label="删除条件" onClick={() => onRemoveNode(node.id)} />
            </Space>
          </div>
        ))}
        {!group.children.length && (
          <div className="query-group-empty">这个条件组还没有条件，可以从下方添加。</div>
        )}
      </div>

      <Space wrap className="query-group-actions">
        <Button
          size="small"
          icon={<PlusOutlined />}
          disabled={totalConditions >= GROUP_QUERY_MAX_CONDITIONS}
          onClick={() => onAddCondition(group.id, instanceScoped ? 'instance_name' : 'group_name')}
        >
          添加条件
        </Button>
        <Button
          size="small"
          icon={<ApartmentOutlined />}
          disabled={depth >= GROUP_QUERY_MAX_DEPTH || totalConditions >= GROUP_QUERY_MAX_CONDITIONS}
          onClick={() => onAddGroup(group.id, undefined, instanceScoped ? 'instance_name' : 'group_name')}
        >
          添加条件组
        </Button>
        {!instanceScoped && (
          <Button
            size="small"
            icon={<ApartmentOutlined />}
            disabled={depth >= GROUP_QUERY_MAX_DEPTH || totalConditions >= GROUP_QUERY_MAX_CONDITIONS}
            onClick={() => onAddGroup(group.id, 'instance', 'instance_name')}
          >
            添加同一实例条件组
          </Button>
        )}
      </Space>
    </section>
  )
}

export function GroupAdvancedSearchDrawer({
  open,
  filter,
  siteOptions,
  siteOptionsLoading,
  siteOptionsError,
  downloaders,
  downloadersLoading,
  onRetrySiteOptions,
  onClose,
  onApply,
}: GroupAdvancedSearchDrawerProps) {
  const { message } = App.useApp()
  const [draft, setDraft] = useState<DraftGroup>(() => toDraft(filter))

  useEffect(() => {
    if (open) setDraft(toDraft(filter))
  }, [filter, open])

  const totalConditions = countDraftConditions(draft)
  const incompleteConditions = countIncompleteConditions(draft)
  const rawCandidate = useMemo(() => toRawFilter(draft), [draft])
  const validation = useMemo(() => validateGroupQueryFilter(rawCandidate), [rawCandidate])
  const candidate = useMemo(() => toFilter(draft), [draft])
  const summary = summarizeGroupQuery(candidate, {
    sites: new Map(siteOptions.map((site) => [site.key, site.label])),
    downloaders: new Map(downloaders.map((downloader) => [downloader.id, downloader.name])),
  })

  const updateNode = (id: string, update: (node: DraftNode) => DraftNode) => {
    setDraft((current) => current.id === id ? update(current) as DraftGroup : mapNode(current, id, update))
  }

  const addToGroup = (groupId: string, node: DraftNode) => updateNode(groupId, (current) => current.type === 'group'
    ? { ...current, children: [...current.children, node] }
    : current)

  const apply = () => {
    if (incompleteConditions > 0) {
      void message.warning(`还有 ${incompleteConditions} 条条件没有填写完整`)
      return
    }
    const rootIsTrulyEmpty = draft.children.length === 0
    if (!rootIsTrulyEmpty && (!validation.valid || !candidate)) {
      void message.error(validation.errors[0] ?? '查询条件无效，请检查后重试')
      return
    }
    onApply(rootIsTrulyEmpty ? undefined : candidate)
    onClose()
  }

  const clear = () => {
    onApply(undefined)
    setDraft(emptyRoot())
    onClose()
  }

  return (
    <Drawer
      className="query-builder-drawer"
      rootClassName="query-builder-root"
      title="高级搜索"
      width="min(920px, 100vw)"
      open={open}
      onClose={onClose}
      footer={(
        <div className="drawer-footer-actions">
          <Button onClick={clear}>清空高级条件</Button>
          <Space>
            <Button onClick={onClose}>取消</Button>
            <Button type="primary" icon={<SearchOutlined />} onClick={apply}>应用搜索</Button>
          </Space>
        </div>
      )}
    >
      <div className="query-builder-intro">
        <div>
          <Typography.Title level={5}>像组合 SQL 条件一样搜索，无需编写 SQL</Typography.Title>
          <Typography.Text type="secondary">
            自由添加“并且 / 或者”条件和嵌套条件组。系统只发送结构化规则，不会执行原始 SQL。
          </Typography.Text>
          <Typography.Paragraph type="secondary" className="query-builder-scope-hint">
            普通实例条件表示“组内存在实例”；若路径、站点、下载器等必须命中同一个实例，请使用“添加同一实例条件组”。
          </Typography.Paragraph>
        </div>
        <Space size={6} wrap>
          <Tag color="blue">{totalConditions} / {GROUP_QUERY_MAX_CONDITIONS} 条</Tag>
          <Tag>最多 {GROUP_QUERY_MAX_DEPTH} 层</Tag>
        </Space>
      </div>

      {siteOptionsError && (
        <Alert
          className="site-options-alert"
          type="warning"
          showIcon
          message="完整站点目录加载失败，当前仅展示本页候选。"
          action={onRetrySiteOptions ? <Button size="small" onClick={onRetrySiteOptions}>重试</Button> : undefined}
        />
      )}

      <QueryGroupEditor
        group={draft}
        depth={1}
        inheritedInstanceScope={false}
        totalConditions={totalConditions}
        siteOptions={siteOptions}
        siteOptionsLoading={siteOptionsLoading}
        downloaders={downloaders}
        downloadersLoading={downloadersLoading}
        onUpdateNode={updateNode}
        onRemoveNode={(id) => setDraft((current) => removeNode(current, id))}
        onDuplicateNode={(id) => setDraft((current) => duplicateNode(current, id))}
        onAddCondition={(groupId, defaultField) => addToGroup(groupId, emptyCondition(defaultField))}
        onAddGroup={(groupId, scope, defaultField) => addToGroup(groupId, {
          id: nextId(),
          type: 'group',
          combinator: 'and',
          ...(scope ? { scope } : {}),
          children: [emptyCondition(defaultField)],
        })}
      />

      <div className="query-natural-summary">
        <Typography.Text type="secondary">当前规则</Typography.Text>
        <Typography.Paragraph ellipsis={{ rows: 3, expandable: true, symbol: '展开' }}>
          {summary}
        </Typography.Paragraph>
        {incompleteConditions > 0 && <Typography.Text type="warning">还有 {incompleteConditions} 条条件待填写</Typography.Text>}
        {incompleteConditions === 0 && !validation.valid && (
          <Typography.Text type="danger">{validation.errors[0]}</Typography.Text>
        )}
      </div>
    </Drawer>
  )
}
