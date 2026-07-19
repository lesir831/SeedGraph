import { DeleteOutlined, LinkOutlined, PlusOutlined, ReloadOutlined, TagsOutlined } from '@ant-design/icons'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  Alert,
  App,
  AutoComplete,
  Button,
  Card,
  Form,
  Input,
  Modal,
  Popconfirm,
  Select,
  Space,
  Table,
  Tag,
  Typography,
  type TableColumnsType,
} from 'antd'
import { useEffect, useState } from 'react'
import { api } from '../api/client'
import type {
  IYUUSite,
  IYUUSiteFilters,
  TrackerMapping,
  TrackerMappingFilters,
  TrackerMatchType,
  TrackerRule,
  TrackerRuleInput,
} from '../api/types'
import { PageHeader } from '../components/PageHeader'
import { PageState } from '../components/PageState'
import { displayError, formatDateTime } from '../utils/format'

const initialValues: TrackerRuleInput = {
  hostPattern: '',
  pathPrefix: '',
  siteName: '',
  displayName: '',
}

const initialTrackerFilters: TrackerMappingFilters = {
  query: '',
  status: 'all',
  matchType: 'all',
  page: 1,
  pageSize: 10,
}

const initialSiteFilters: IYUUSiteFilters = {
  query: '',
  status: 'all',
  page: 1,
  pageSize: 10,
}

const matchTypeMeta: Record<TrackerMatchType, { label: string; color: string }> = {
  exact: { label: '完全匹配', color: 'green' },
  registrable_domain: { label: '二级域名匹配', color: 'blue' },
  keyword: { label: '关键字匹配', color: 'gold' },
  custom: { label: '自定义规则', color: 'purple' },
}

export function TrackerRulesPage() {
  const { message } = App.useApp()
  const queryClient = useQueryClient()
  const [modalOpen, setModalOpen] = useState(false)
  const [trackerFilters, setTrackerFilters] = useState<TrackerMappingFilters>(initialTrackerFilters)
  const [siteFilters, setSiteFilters] = useState<IYUUSiteFilters>(initialSiteFilters)
  const [trackerSearch, setTrackerSearch] = useState('')
  const [siteSearch, setSiteSearch] = useState('')
  const [selectedSiteSlug, setSelectedSiteSlug] = useState<string>()
  const [form] = Form.useForm<TrackerRuleInput>()

  const rules = useQuery({ queryKey: ['tracker-rules'], queryFn: api.getTrackerRules })
  const trackerMappings = useQuery({
    queryKey: ['tracker-mappings', trackerFilters],
    queryFn: () => api.getTrackerMappings(trackerFilters),
  })
  const iyuuCatalog = useQuery({
    queryKey: ['iyuu-sites', siteFilters],
    queryFn: () => api.getIYUUSites(siteFilters),
  })
  const ruleSiteCatalog = useQuery({
    queryKey: ['iyuu-sites', 'tracker-rule-options'],
    queryFn: () => api.getIYUUSites({ status: 'all', page: 1, pageSize: 200 }),
    enabled: modalOpen,
    staleTime: 60_000,
  })

  const ruleSiteOptions = (ruleSiteCatalog.data?.items ?? []).map((site) => ({
    value: site.slug,
    label: `${site.nickname || site.slug} · ${site.slug} · ${site.baseUrl}${site.stale ? ' · 上游已缺失' : ''}`,
  }))
  const selectedIYUUSite = ruleSiteCatalog.data?.items.find((site) => site.slug === selectedSiteSlug)

  useEffect(() => {
    const result = trackerMappings.data
    if (!result || result.items.length > 0 || result.total === 0) return
    const lastPage = Math.max(1, Math.ceil(result.total / trackerFilters.pageSize))
    if (trackerFilters.page > lastPage) {
      setTrackerFilters((current) => current.page > lastPage ? { ...current, page: lastPage } : current)
    }
  }, [trackerMappings.data, trackerFilters.page, trackerFilters.pageSize])

  useEffect(() => {
    const result = iyuuCatalog.data
    if (!result || result.items.length > 0 || result.total === 0) return
    const lastPage = Math.max(1, Math.ceil(result.total / siteFilters.pageSize))
    if (siteFilters.page > lastPage) {
      setSiteFilters((current) => current.page > lastPage ? { ...current, page: lastPage } : current)
    }
  }, [iyuuCatalog.data, siteFilters.page, siteFilters.pageSize])

  const openRuleModal = (tracker?: Pick<TrackerMapping, 'hostIdentity' | 'pathHint'>) => {
    setSelectedSiteSlug(undefined)
    form.setFieldsValue(tracker ? {
      ...initialValues,
      hostPattern: tracker.hostIdentity,
      pathPrefix: tracker.pathHint,
    } : initialValues)
    setModalOpen(true)
  }

  const invalidateMappings = async () => {
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: ['tracker-rules'] }),
      queryClient.invalidateQueries({ queryKey: ['tracker-mappings'] }),
      queryClient.invalidateQueries({ queryKey: ['iyuu-sites'] }),
      queryClient.invalidateQueries({ queryKey: ['torrent-groups'] }),
      queryClient.invalidateQueries({ queryKey: ['torrent-group'] }),
      queryClient.invalidateQueries({ queryKey: ['overview'] }),
    ])
  }

  const createMutation = useMutation({
    mutationFn: api.createTrackerRule,
    onSuccess: async () => {
      void message.success('Tracker 规则已添加，已有任务已重新映射')
      setModalOpen(false)
      form.resetFields()
      setSelectedSiteSlug(undefined)
      await invalidateMappings()
    },
    onError: (error) => void message.error(displayError(error)),
  })

  const deleteMutation = useMutation({
    mutationFn: api.deleteTrackerRule,
    onSuccess: async () => {
      void message.success('规则已删除，已有任务已重新映射')
      await invalidateMappings()
    },
    onError: (error) => void message.error(displayError(error)),
  })

  const iyuuSyncMutation = useMutation({
    mutationFn: api.syncIYUUSites,
    onSuccess: (result) => {
      void message.success(`已同步 ${result.siteCount} 个 IYUU 站点并重新验证 Tracker 映射`)
    },
    onError: (error) => void message.error(displayError(error)),
    onSettled: async () => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['iyuu-sites'] }),
        queryClient.invalidateQueries({ queryKey: ['tracker-mappings'] }),
        queryClient.invalidateQueries({ queryKey: ['torrent-groups'] }),
        queryClient.invalidateQueries({ queryKey: ['overview'] }),
        queryClient.invalidateQueries({ queryKey: ['audit-events'] }),
      ])
    },
  })

  const updateRuleSite = (siteName: string) => {
    const site = ruleSiteCatalog.data?.items.find((candidate) => candidate.slug === siteName)
    const resetCatalogDisplayName = Boolean(selectedSiteSlug && !site)
    setSelectedSiteSlug(site?.slug)
    form.setFieldsValue({
      siteName,
      ...(site
        ? { displayName: site.nickname || site.slug }
        : resetCatalogDisplayName
          ? { displayName: '' }
          : {}),
    })
  }

  const ruleColumns: TableColumnsType<TrackerRule> = [
    {
      title: '规则',
      key: 'rule',
      render: (_, rule) => (
        <div className="primary-cell">
          <Space wrap>
            <strong>{rule.displayName}</strong>
            <Tag color={rule.source === 'custom' ? 'blue' : 'default'}>{rule.source === 'custom' ? '自定义' : '内置'}</Tag>
          </Space>
          <Typography.Text type="secondary">内部站点名：{rule.siteName}</Typography.Text>
        </div>
      ),
    },
    {
      title: '主机匹配',
      dataIndex: 'hostPattern',
      width: 220,
      render: (host: string) => <Typography.Text code copyable={{ text: host }}>{host}</Typography.Text>,
    },
    {
      title: '路径前缀',
      dataIndex: 'pathPrefix',
      width: 180,
      render: (path: string) => path
        ? <Typography.Text code>{path}</Typography.Text>
        : <Typography.Text type="secondary">任意路径</Typography.Text>,
    },
    {
      title: '优先级',
      dataIndex: 'priority',
      width: 100,
      sorter: (left, right) => left.priority - right.priority,
    },
    {
      title: '更新时间',
      dataIndex: 'updatedAt',
      width: 180,
      render: formatDateTime,
    },
    {
      title: '操作',
      key: 'actions',
      width: 100,
      fixed: 'right',
      render: (_, rule) => (
        <Popconfirm
          title="删除这条规则？"
          description="已有任务会立即重新执行三级映射。"
          okText="删除"
          cancelText="取消"
          okButtonProps={{ danger: true }}
          onConfirm={() => deleteMutation.mutate(rule.id)}
        >
          <Button
            type="text"
            danger
            icon={<DeleteOutlined />}
            disabled={rule.source !== 'custom'}
            loading={deleteMutation.isPending && deleteMutation.variables === rule.id}
          >
            删除
          </Button>
        </Popconfirm>
      ),
    },
  ]

  const trackerColumns: TableColumnsType<TrackerMapping> = [
    {
      title: 'Tracker',
      key: 'identity',
      render: (_, tracker) => {
        const identity = `${tracker.hostIdentity}${tracker.pathHint}`
        return (
          <div className="primary-cell tracker-identity-cell">
            <Typography.Text code copyable={{ text: identity }}>{tracker.hostIdentity}</Typography.Text>
            <Typography.Text type="secondary">{tracker.pathHint || '任意路径'}</Typography.Text>
          </div>
        )
      },
    },
    {
      title: '映射站点',
      key: 'site',
      width: 220,
      render: (_, tracker) => tracker.mapped ? (
        <div className="primary-cell">
          <Space wrap size={4}>
            <Tag color="success">已映射</Tag>
            <strong>{tracker.displayName || tracker.siteName}</strong>
          </Space>
          {tracker.siteName && <Typography.Text type="secondary">{tracker.siteName}</Typography.Text>}
        </div>
      ) : <Tag color="warning">未映射</Tag>,
    },
    {
      title: '映射类型',
      dataIndex: 'matchType',
      width: 160,
      render: (matchType?: TrackerMatchType) => matchType
        ? <Tag color={matchTypeMeta[matchType].color}>{matchTypeMeta[matchType].label}</Tag>
        : <Typography.Text type="secondary">—</Typography.Text>,
    },
    {
      title: '影响范围',
      key: 'usage',
      width: 200,
      render: (_, tracker) => (
        <Space wrap size={4}>
          <Tag color="blue">{tracker.instanceCount} 个实例</Tag>
          <Tag>{tracker.groupCount} 个聚合组</Tag>
        </Space>
      ),
    },
    {
      title: '最近发现',
      dataIndex: 'lastSeenAt',
      width: 180,
      render: formatDateTime,
    },
    {
      title: '操作',
      key: 'actions',
      width: 130,
      fixed: 'right',
      render: (_, tracker) => tracker.mapped
        ? <Typography.Text type="secondary">已完成</Typography.Text>
        : <Button type="link" icon={<LinkOutlined />} onClick={() => openRuleModal(tracker)}>创建映射</Button>,
    },
  ]

  const iyuuColumns: TableColumnsType<IYUUSite> = [
    {
      title: '站点',
      key: 'site',
      render: (_, site) => (
        <div className="primary-cell">
          <strong>{site.nickname || site.slug}</strong>
          <Typography.Text type="secondary">{site.slug} · IYUU #{site.remoteId}</Typography.Text>
        </div>
      ),
    },
    {
      title: '站点域名',
      dataIndex: 'baseUrl',
      width: 260,
      render: (host: string) => <Typography.Text code copyable={{ text: host }}>{host}</Typography.Text>,
    },
    {
      title: '映射状态',
      key: 'mapping',
      width: 160,
      render: (_, site) => site.mapped
        ? <Tag color="success">已映射 {site.mappingCount} 个 Tracker</Tag>
        : <Tag color="default">未映射</Tag>,
    },
    {
      title: '属性',
      key: 'attributes',
      width: 180,
      render: (_, site) => (
        <Space wrap size={4}>
          <Tag>{site.isHttps === 1 ? 'HTTPS' : site.isHttps === 2 ? 'HTTP / HTTPS' : 'HTTP'}</Tag>
          {site.cookieRequired && <Tag color="gold">需要 Cookie</Tag>}
          {site.stale && <Tag color="warning">上游已缺失</Tag>}
        </Space>
      ),
    },
  ]

  return (
    <div className="page-stack">
      <PageHeader
        title="Tracker 规则"
        description="通过完全域名、可注册域和域名关键字三级验证，将脱敏 Tracker 映射到 IYUU 站点。"
        extra={
          <>
            <Button
              icon={<ReloadOutlined spin={rules.isFetching || trackerMappings.isFetching || iyuuCatalog.isFetching} />}
              onClick={() => void Promise.all([rules.refetch(), trackerMappings.refetch(), iyuuCatalog.refetch()])}
            >
              刷新
            </Button>
            <Button
              icon={<ReloadOutlined spin={iyuuSyncMutation.isPending || iyuuCatalog.data?.running} />}
              loading={iyuuSyncMutation.isPending}
              onClick={() => iyuuSyncMutation.mutate()}
            >
              同步 IYUU 目录
            </Button>
            <Button type="primary" icon={<PlusOutlined />} onClick={() => openRuleModal()}>添加规则</Button>
          </>
        }
      />

      <Alert
        type="info"
        showIcon
        message="三级自动映射，显式规则优先"
        description="依次验证完整域名、可注册二级域名和域名关键字；同一级只有唯一站点命中时才映射，歧义项保持未映射。关键字是最后一级启发式结果，可用自定义规则覆盖；完整 announce URL 和 passkey 不会保存。"
      />

      <Card
        title={<Space size={8}><span>Tracker 映射列表</span><Tag>{trackerMappings.data?.total ?? 0}</Tag></Space>}
        extra={<Typography.Text type="secondary">仅展示脱敏主机与静态路径</Typography.Text>}
        className="table-card"
      >
        <Space wrap style={{ marginBottom: 16 }}>
          <Input.Search
            allowClear
            value={trackerSearch}
            placeholder="搜索站点名称或 Tracker"
            style={{ width: 320 }}
            onChange={(event) => {
              const value = event.target.value
              setTrackerSearch(value)
              if (!value) setTrackerFilters((current) => ({ ...current, query: '', page: 1 }))
            }}
            onSearch={(query) => setTrackerFilters((current) => ({ ...current, query: query.trim(), page: 1 }))}
          />
          <Select
            aria-label="映射状态"
            value={trackerFilters.status}
            style={{ width: 140 }}
            options={[
              { value: 'all', label: '全部状态' },
              { value: 'mapped', label: '已映射' },
              { value: 'unmapped', label: '未映射' },
            ]}
            onChange={(status) => setTrackerFilters((current) => ({ ...current, status, page: 1 }))}
          />
          <Select
            aria-label="映射类型"
            value={trackerFilters.matchType}
            style={{ width: 180 }}
            options={[
              { value: 'all', label: '全部映射类型' },
              { value: 'exact', label: '完全匹配' },
              { value: 'registrable_domain', label: '二级域名匹配' },
              { value: 'keyword', label: '关键字匹配' },
              { value: 'custom', label: '自定义规则' },
            ]}
            onChange={(matchType) => setTrackerFilters((current) => ({ ...current, matchType, page: 1 }))}
          />
        </Space>
        <PageState
          loading={trackerMappings.isLoading}
          error={trackerMappings.error}
          onRetry={() => void trackerMappings.refetch()}
          empty={trackerMappings.data?.total === 0}
          emptyDescription="没有符合当前筛选条件的 Tracker。"
        >
          <Table<TrackerMapping>
            rowKey={(tracker) => `${tracker.hostIdentity}\u0000${tracker.pathHint}`}
            size="small"
            columns={trackerColumns}
            dataSource={trackerMappings.data?.items}
            loading={trackerMappings.isFetching}
            pagination={{
              current: trackerFilters.page,
              pageSize: trackerFilters.pageSize,
              total: trackerMappings.data?.total ?? 0,
              showSizeChanger: true,
              showTotal: (total) => `共 ${total} 个 Tracker`,
              onChange: (page, pageSize) => setTrackerFilters((current) => ({
                ...current,
                page: pageSize === current.pageSize ? page : 1,
                pageSize,
              })),
            }}
            scroll={{ x: 1080 }}
          />
        </PageState>
      </Card>

      <Card
        title={<Space size={8}><span>IYUU 站点目录</span><Tag>{iyuuCatalog.data?.total ?? 0}</Tag></Space>}
        extra={iyuuCatalog.data?.state.lastSuccessAt
          ? `上次成功：${formatDateTime(iyuuCatalog.data.state.lastSuccessAt)}`
          : '尚未成功同步'}
        className="table-card"
      >
        {iyuuCatalog.data?.state.lastError && (
          <Alert
            type="warning"
            showIcon
            message="最近一次 IYUU 同步失败；已保留上次成功目录"
            description={iyuuCatalog.data.state.lastError}
            style={{ marginBottom: 16 }}
          />
        )}
        <Space wrap style={{ marginBottom: 16 }}>
          <Input.Search
            allowClear
            value={siteSearch}
            placeholder="搜索站点名称或站点域名"
            style={{ width: 320 }}
            onChange={(event) => {
              const value = event.target.value
              setSiteSearch(value)
              if (!value) setSiteFilters((current) => ({ ...current, query: '', page: 1 }))
            }}
            onSearch={(query) => setSiteFilters((current) => ({ ...current, query: query.trim(), page: 1 }))}
          />
          <Select
            aria-label="站点映射状态"
            value={siteFilters.status}
            style={{ width: 140 }}
            options={[
              { value: 'all', label: '全部站点' },
              { value: 'mapped', label: '已映射站点' },
              { value: 'unmapped', label: '未映射站点' },
            ]}
            onChange={(status) => setSiteFilters((current) => ({ ...current, status, page: 1 }))}
          />
        </Space>
        <PageState
          loading={iyuuCatalog.isLoading}
          error={iyuuCatalog.error}
          onRetry={() => void iyuuCatalog.refetch()}
          empty={iyuuCatalog.data?.total === 0}
          emptyDescription="没有符合当前筛选条件的 IYUU 站点。"
        >
          <Table<IYUUSite>
            rowKey="remoteId"
            size="small"
            columns={iyuuColumns}
            dataSource={iyuuCatalog.data?.items}
            loading={iyuuCatalog.isFetching}
            pagination={{
              current: siteFilters.page,
              pageSize: siteFilters.pageSize,
              total: iyuuCatalog.data?.total ?? 0,
              showSizeChanger: true,
              showTotal: (total) => `共 ${total} 个站点`,
              onChange: (page, pageSize) => setSiteFilters((current) => ({
                ...current,
                page: pageSize === current.pageSize ? page : 1,
                pageSize,
              })),
            }}
            scroll={{ x: 900 }}
          />
        </PageState>
      </Card>

      <PageState
        loading={rules.isLoading}
        error={rules.error}
        onRetry={() => void rules.refetch()}
        empty={rules.data?.length === 0}
        emptyDescription={
          <Space direction="vertical">
            <span>还没有自定义 Tracker 规则。</span>
            <Button type="primary" icon={<PlusOutlined />} onClick={() => openRuleModal()}>创建第一条规则</Button>
          </Space>
        }
      >
        <Card title="自定义 Tracker 规则" className="table-card">
          <Table<TrackerRule>
            rowKey="id"
            columns={ruleColumns}
            dataSource={[...(rules.data ?? [])].sort((left, right) => right.priority - left.priority)}
            pagination={false}
            scroll={{ x: 880 }}
          />
        </Card>
      </PageState>

      <Modal
        title="添加 Tracker 规则"
        open={modalOpen}
        okText="保存规则"
        cancelText="取消"
        confirmLoading={createMutation.isPending}
        onCancel={() => {
          setModalOpen(false)
          createMutation.reset()
          form.resetFields()
          setSelectedSiteSlug(undefined)
        }}
        onOk={() => void form.validateFields().then((values) => createMutation.mutate(values))}
      >
        <Form form={form} layout="vertical" requiredMark={false} initialValues={initialValues} className="modal-form">
          <Form.Item name="hostPattern" label="Tracker 主机名" rules={[{ required: true, message: '请输入主机名' }, { max: 253 }]} extra="不含协议、端口、查询参数或 passkey。">
            <Input placeholder="tracker.example.com" />
          </Form.Item>
          <Form.Item name="pathPrefix" label="静态路径前缀（可选）" extra="仅用于同一主机下的静态站点区分；敏感长路径会由服务端脱敏。">
            <Input placeholder="/announce" />
          </Form.Item>
          <Form.Item
            name="siteName"
            label="映射站点"
            rules={[
              { required: true, message: '请选择或输入站点' },
              { pattern: /^[a-z0-9][a-z0-9._-]*$/i, message: '新站点内部名请使用字母、数字、点、下划线或短横线' },
            ]}
            extra={selectedIYUUSite
              ? `复用 IYUU 站点：${selectedIYUUSite.nickname || selectedIYUUSite.slug} · ${selectedIYUUSite.baseUrl}`
              : '可搜索并选择 IYUU 站点；若输入的内部站点名不存在，保存时将新建本地站点。'}
          >
            <AutoComplete
              allowClear
              options={ruleSiteOptions}
              optionFilterProp="label"
              notFoundContent={ruleSiteCatalog.isLoading ? '正在加载 IYUU 站点…' : '未找到；继续输入即可新建站点'}
              placeholder="输入名称、域名搜索，或输入新站点内部名"
              onChange={updateRuleSite}
              onSelect={updateRuleSite}
            />
          </Form.Item>
          {ruleSiteCatalog.error && (
            <Alert
              type="warning"
              showIcon
              message="IYUU 站点选项加载失败，仍可输入新站点"
              description={displayError(ruleSiteCatalog.error)}
              style={{ marginBottom: 16 }}
            />
          )}
          <Form.Item
            name="displayName"
            label={selectedIYUUSite ? 'IYUU 显示名称' : '新站点显示名称'}
            rules={[{ required: true, message: '请输入显示名称' }, { max: 80 }]}
            extra={selectedIYUUSite ? '显示名称由 IYUU 目录维护，创建规则时不会覆盖。' : undefined}
          >
            <Input
              prefix={<TagsOutlined />}
              placeholder="例如：M-Team"
              disabled={Boolean(selectedIYUUSite)}
            />
          </Form.Item>
          {createMutation.error && <Alert type="error" showIcon message="保存失败" description={displayError(createMutation.error)} />}
        </Form>
      </Modal>
    </div>
  )
}
