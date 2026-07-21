import {
  ArrowDownOutlined,
  ArrowUpOutlined,
  DeleteOutlined,
  PlusOutlined,
} from '@ant-design/icons'
import { Button, Drawer, Select, Space, Typography } from 'antd'
import { useEffect, useState } from 'react'
import type { GroupSortBy, GroupSortRule, SortOrder } from '../api/types'
import { DEFAULT_GROUP_SORTS, GROUP_SORT_LABELS } from '../utils/groupSortPreferences'

interface GroupSortDrawerProps {
  open: boolean
  sorts: GroupSortRule[]
  onClose: () => void
  onApply: (sorts: GroupSortRule[]) => void
}

const cloneSorts = (sorts: GroupSortRule[]) => sorts.map((rule) => ({ ...rule }))

export function GroupSortDrawer({ open, sorts, onClose, onApply }: GroupSortDrawerProps) {
  const [draft, setDraft] = useState<GroupSortRule[]>(() => cloneSorts(sorts))

  useEffect(() => {
    if (open) setDraft(cloneSorts(sorts))
  }, [open, sorts])

  const update = (index: number, change: Partial<GroupSortRule>) => {
    setDraft((current) => current.map((rule, ruleIndex) => (
      ruleIndex === index ? { ...rule, ...change } : rule
    )))
  }

  const move = (index: number, direction: -1 | 1) => {
    setDraft((current) => {
      const target = index + direction
      if (target < 0 || target >= current.length) return current
      const next = [...current]
      ;[next[index], next[target]] = [next[target], next[index]]
      return next
    })
  }

  const add = () => {
    const used = new Set(draft.map((rule) => rule.field))
    const field = (Object.keys(GROUP_SORT_LABELS) as GroupSortBy[]).find((candidate) => !used.has(candidate))
    if (field) setDraft((current) => [...current, { field, order: field === 'name' ? 'asc' : 'desc' }])
  }

  const apply = () => {
    onApply(draft.length ? draft : cloneSorts(DEFAULT_GROUP_SORTS))
    onClose()
  }

  return (
    <Drawer
      title="多级排序"
      width={540}
      open={open}
      onClose={onClose}
      footer={(
        <div className="drawer-footer-actions">
          <Button onClick={() => setDraft(cloneSorts(DEFAULT_GROUP_SORTS))}>恢复默认</Button>
          <Space>
            <Button onClick={onClose}>取消</Button>
            <Button type="primary" onClick={apply}>应用并记住</Button>
          </Space>
        </div>
      )}
    >
      <Typography.Paragraph type="secondary" className="drawer-intro">
        从上到下依次比较；上一项相同时，才会使用下一项。设置会保存在当前浏览器。
      </Typography.Paragraph>
      <div className="sort-rule-list">
        {draft.map((rule, index) => {
          const usedElsewhere = new Set(draft.filter((_, ruleIndex) => ruleIndex !== index).map((item) => item.field))
          return (
            <div className="sort-rule-row" key={rule.field}>
              <span className="sort-priority">{index + 1}</span>
              <Select<GroupSortBy>
                aria-label={`第 ${index + 1} 排序字段`}
                value={rule.field}
                options={(Object.entries(GROUP_SORT_LABELS) as Array<[GroupSortBy, string]>).map(([value, label]) => ({
                  value,
                  label,
                  disabled: usedElsewhere.has(value),
                }))}
                onChange={(field) => update(index, { field })}
              />
              <Select<SortOrder>
                aria-label={`第 ${index + 1} 排序方向`}
                value={rule.order}
                options={[
                  { value: 'asc', label: '升序' },
                  { value: 'desc', label: '降序' },
                ]}
                onChange={(order) => update(index, { order })}
              />
              <Space size={2}>
                <Button
                  type="text"
                  aria-label="上移"
                  icon={<ArrowUpOutlined />}
                  disabled={index === 0}
                  onClick={() => move(index, -1)}
                />
                <Button
                  type="text"
                  aria-label="下移"
                  icon={<ArrowDownOutlined />}
                  disabled={index === draft.length - 1}
                  onClick={() => move(index, 1)}
                />
                <Button
                  type="text"
                  danger
                  aria-label="删除排序条件"
                  icon={<DeleteOutlined />}
                  onClick={() => setDraft((current) => current.filter((_, ruleIndex) => ruleIndex !== index))}
                />
              </Space>
            </div>
          )
        })}
      </div>
      <Button
        block
        type="dashed"
        icon={<PlusOutlined />}
        disabled={draft.length >= 4}
        onClick={add}
      >
        添加下一排序条件
      </Button>
    </Drawer>
  )
}
