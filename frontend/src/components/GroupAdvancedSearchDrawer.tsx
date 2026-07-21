import { Alert, App, Button, DatePicker, Drawer, Form, Input, InputNumber, Select, Space, Typography } from 'antd'
import dayjs, { type Dayjs } from 'dayjs'
import { useEffect } from 'react'
import type { GroupFilters, GroupSiteSummary } from '../api/types'

const GIB = 1024 ** 3

interface AdvancedSearchValues {
  nameContains?: string
  requiredSites?: string[]
  excludedSites?: string[]
  sizeGiB?: number
  oldestRange?: [Dayjs, Dayjs]
  maxSiteCount?: number
  missingSite?: string
  stale?: boolean
}

interface GroupAdvancedSearchDrawerProps {
  open: boolean
  filters: GroupFilters
  siteOptions: GroupSiteSummary[]
  siteOptionsLoading?: boolean
  siteOptionsError?: boolean
  onRetrySiteOptions?: () => void
  onClose: () => void
  onApply: (filters: Partial<GroupFilters>) => void
}

const normalizedSites = (values?: string[]) => Array.from(new Set(
  (values ?? []).map((value) => value.trim()).filter(Boolean),
))

export function GroupAdvancedSearchDrawer({
  open,
  filters,
  siteOptions,
  siteOptionsLoading,
  siteOptionsError,
  onRetrySiteOptions,
  onClose,
  onApply,
}: GroupAdvancedSearchDrawerProps) {
  const { message } = App.useApp()
  const [form] = Form.useForm<AdvancedSearchValues>()

  useEffect(() => {
    if (!open) return
    const range: [Dayjs, Dayjs] | undefined = filters.oldestAddedGte && filters.oldestAddedLt
      ? [dayjs(filters.oldestAddedGte), dayjs(filters.oldestAddedLt).subtract(1, 'day')]
      : undefined
    form.setFieldsValue({
      nameContains: filters.nameContains,
      requiredSites: filters.requiredSites,
      excludedSites: filters.excludedSites,
      sizeGiB: filters.sizeLT ? filters.sizeLT / GIB : undefined,
      oldestRange: range,
      maxSiteCount: filters.maxSiteCount,
      missingSite: filters.missingSite,
      stale: filters.stale,
    })
  }, [filters, form, open])

  const apply = async () => {
    const values = await form.validateFields()
    const requiredSites = normalizedSites(values.requiredSites)
    const excludedSites = normalizedSites(values.excludedSites)
    const overlap = requiredSites.find((site) => excludedSites.includes(site))
    if (overlap) {
      void message.error(`“${overlap}”不能同时设为必须包含和排除`)
      return
    }

    const [start, end] = values.oldestRange ?? []
    onApply({
      nameContains: values.nameContains?.trim() || undefined,
      requiredSites: requiredSites.length ? requiredSites : undefined,
      excludedSites: excludedSites.length ? excludedSites : undefined,
      sizeLT: values.sizeGiB ? Math.floor(values.sizeGiB * GIB) : undefined,
      oldestAddedGte: start?.startOf('day').format(),
      oldestAddedLt: end?.add(1, 'day').startOf('day').format(),
      maxSiteCount: values.maxSiteCount,
      missingSite: values.missingSite?.trim() || undefined,
      stale: values.stale,
    })
    onClose()
  }

  const clear = () => {
    form.resetFields()
    onApply({
      nameContains: undefined,
      requiredSites: undefined,
      excludedSites: undefined,
      sizeLT: undefined,
      oldestAddedGte: undefined,
      oldestAddedLt: undefined,
      maxSiteCount: undefined,
      missingSite: undefined,
      stale: undefined,
    })
    onClose()
  }

  const options = siteOptions.map((site) => ({ value: site.key, label: site.label }))

  return (
    <Drawer
      title="高级搜索"
      width={520}
      open={open}
      onClose={onClose}
      footer={(
        <div className="drawer-footer-actions">
          <Button onClick={clear}>清空高级条件</Button>
          <Space>
            <Button onClick={onClose}>取消</Button>
            <Button type="primary" onClick={() => void apply()}>应用搜索</Button>
          </Space>
        </div>
      )}
    >
      <Typography.Paragraph type="secondary" className="drawer-intro">
        所有条件使用“并且”关系。必须包含多个站点时，聚合组需要同时拥有这些站点。
      </Typography.Paragraph>
      {siteOptionsError && (
        <Alert
          className="site-options-alert"
          type="warning"
          showIcon
          message="完整站点目录加载失败，当前仅展示本页候选。"
          action={onRetrySiteOptions ? <Button size="small" onClick={onRetrySiteOptions}>重试</Button> : undefined}
        />
      )}
      <Form form={form} layout="vertical" requiredMark={false}>
        <Form.Item name="nameContains" label="名称包含">
          <Input allowClear maxLength={200} placeholder="例如：1080p 或某一季名称" />
        </Form.Item>
        <Form.Item name="requiredSites" label="必须包含站点">
          <Select
            mode="multiple"
            allowClear
            showSearch
            optionFilterProp="label"
            loading={siteOptionsLoading}
            options={options}
            placeholder="选择 A 站、B 站"
          />
        </Form.Item>
        <Form.Item name="excludedSites" label="排除站点">
          <Select
            mode="multiple"
            allowClear
            showSearch
            optionFilterProp="label"
            loading={siteOptionsLoading}
            options={options}
            placeholder="选择不希望出现的站点"
          />
        </Form.Item>
        <Form.Item
          name="sizeGiB"
          label="内容大小严格小于"
          extra="按聚合组中最大实例的内容大小计算；1 GiB = 1,073,741,824 字节。"
        >
          <InputNumber min={0.001} precision={3} addonAfter="GiB" placeholder="例如 1" className="full-width-control" />
        </Form.Item>
        <Form.Item
          name="oldestRange"
          label="最旧添加日期"
          extra="按浏览器本地时区计算，开始和结束日期均包含。"
        >
          <DatePicker.RangePicker allowClear className="full-width-control" />
        </Form.Item>
        <Form.Item name="maxSiteCount" label="站点 / Tracker 数量上限">
          <Select
            allowClear
            placeholder="不限制"
            options={[
              { value: 0, label: '没有站点 / Tracker' },
              { value: 1, label: '最多 1 个' },
              { value: 2, label: '最多 2 个' },
            ]}
          />
        </Form.Item>
        <Form.Item name="missingSite" label="缺少指定站点">
          <Input allowClear placeholder="输入系统站点名称" />
        </Form.Item>
        <Form.Item name="stale" label="同步新鲜度">
          <Select
            allowClear
            placeholder="全部"
            options={[
              { value: false, label: '快照新鲜' },
              { value: true, label: '需要刷新' },
            ]}
          />
        </Form.Item>
      </Form>
    </Drawer>
  )
}
