import { DeleteOutlined, LinkOutlined, PlusOutlined, ReloadOutlined, TagsOutlined } from '@ant-design/icons'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  Alert,
  App,
  Button,
  Card,
  Form,
  Input,
  Modal,
  Popconfirm,
  Space,
  Table,
  Tag,
  Typography,
  type TableColumnsType,
} from 'antd'
import { useState } from 'react'
import { api } from '../api/client'
import type { IYUUSite, TrackerRule, TrackerRuleInput, UnmappedTrackerIdentity } from '../api/types'
import { PageHeader } from '../components/PageHeader'
import { PageState } from '../components/PageState'
import { displayError, formatDateTime } from '../utils/format'

const initialValues: TrackerRuleInput = {
  hostPattern: '',
  pathPrefix: '',
  siteName: '',
  displayName: '',
}

export function TrackerRulesPage() {
  const { message } = App.useApp()
  const queryClient = useQueryClient()
  const [modalOpen, setModalOpen] = useState(false)
  const [form] = Form.useForm<TrackerRuleInput>()

  const rules = useQuery({ queryKey: ['tracker-rules'], queryFn: api.getTrackerRules })
  const unmappedTrackers = useQuery({ queryKey: ['tracker-rules', 'unmapped'], queryFn: api.getUnmappedTrackers })
  const iyuuCatalog = useQuery({ queryKey: ['iyuu-sites'], queryFn: api.getIYUUSites })

  const openRuleModal = (tracker?: UnmappedTrackerIdentity) => {
    form.setFieldsValue(tracker ? {
      ...initialValues,
      hostPattern: tracker.hostIdentity,
      pathPrefix: tracker.pathHint,
    } : initialValues)
    setModalOpen(true)
  }

  const createMutation = useMutation({
    mutationFn: api.createTrackerRule,
    onSuccess: async () => {
      void message.success('Tracker 规则已添加，已有任务已重新映射')
      setModalOpen(false)
      form.resetFields()
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['tracker-rules'] }),
        queryClient.invalidateQueries({ queryKey: ['torrent-groups'] }),
        queryClient.invalidateQueries({ queryKey: ['torrent-group'] }),
        queryClient.invalidateQueries({ queryKey: ['overview'] }),
      ])
    },
    onError: (error) => void message.error(displayError(error)),
  })

  const deleteMutation = useMutation({
    mutationFn: api.deleteTrackerRule,
    onSuccess: async () => {
      void message.success('规则已删除，已有任务已重新映射')
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['tracker-rules'] }),
        queryClient.invalidateQueries({ queryKey: ['torrent-groups'] }),
        queryClient.invalidateQueries({ queryKey: ['torrent-group'] }),
        queryClient.invalidateQueries({ queryKey: ['overview'] }),
      ])
    },
    onError: (error) => void message.error(displayError(error)),
  })

  const iyuuSyncMutation = useMutation({
    mutationFn: api.syncIYUUSites,
    onSuccess: (result) => {
      void message.success(`已同步 ${result.siteCount} 个 IYUU 站点`)
    },
    onError: (error) => void message.error(displayError(error)),
    onSettled: async () => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['iyuu-sites'] }),
        queryClient.invalidateQueries({ queryKey: ['audit-events'] }),
      ])
    },
  })

  const columns: TableColumnsType<TrackerRule> = [
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
      render: (path: string) => path ? <Typography.Text code>{path}</Typography.Text> : <Typography.Text type="secondary">任意路径</Typography.Text>,
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
          description="已有任务的分类会在下次同步时重新计算。"
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
      title: '属性',
      key: 'attributes',
      width: 180,
      render: (_, site) => (
        <Space wrap size={4}>
          <Tag>{site.isHttps === 1 ? 'HTTPS' : site.isHttps === 2 ? 'HTTP / HTTPS' : 'HTTP'}</Tag>
          {site.cookieRequired && <Tag color="gold">需要 Cookie</Tag>}
        </Space>
      ),
    },
    {
      title: '目录状态',
      key: 'status',
      width: 150,
      render: (_, site) => site.stale ? <Tag color="warning">上游已缺失</Tag> : <Tag color="success">当前</Tag>,
    },
  ]

  const unmappedColumns: TableColumnsType<UnmappedTrackerIdentity> = [
    {
      title: '脱敏 Tracker 身份',
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
      title: '影响范围',
      key: 'usage',
      width: 210,
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
      render: (_, tracker) => (
        <Button type="link" icon={<LinkOutlined />} onClick={() => openRuleModal(tracker)}>创建映射</Button>
      ),
    },
  ]

  return (
    <div className="page-stack">
      <PageHeader
        title="Tracker 规则"
        description="将脱敏后的 Tracker 主机和静态路径映射为统一站点；完整 announce URL 和 passkey 不会保存。"
        extra={
          <>
            <Button
              icon={<ReloadOutlined spin={rules.isFetching || unmappedTrackers.isFetching} />}
              onClick={() => void Promise.all([rules.refetch(), unmappedTrackers.refetch()])}
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
        message="规则按优先级依次匹配"
        description="先匹配主机名，再用可选的静态路径前缀区分同域名站点。IYUU 目录只提供站点网站元数据，不等同于 Tracker announce 域名，因此不会自动生成权威匹配规则。"
      />

      <Card
        title={<Space size={8}><span>待映射 Tracker</span><Tag color={unmappedTrackers.data?.length ? 'warning' : 'success'}>{unmappedTrackers.data?.length ?? 0}</Tag></Space>}
        extra={<Typography.Text type="secondary">仅显示脱敏主机与静态路径</Typography.Text>}
        className="table-card"
      >
        <PageState
          loading={unmappedTrackers.isLoading}
          error={unmappedTrackers.error}
          onRetry={() => void unmappedTrackers.refetch()}
          empty={unmappedTrackers.data?.length === 0}
          emptyDescription="当前没有缺少映射的 Tracker。"
        >
          <Table<UnmappedTrackerIdentity>
            rowKey={(tracker) => `${tracker.hostIdentity}\u0000${tracker.pathHint}`}
            size="small"
            columns={unmappedColumns}
            dataSource={unmappedTrackers.data}
            pagination={{ pageSize: 10, showSizeChanger: true }}
            scroll={{ x: 820 }}
          />
        </PageState>
      </Card>

      <Card
        title="IYUU 站点目录"
        extra={iyuuCatalog.data?.state.lastSuccessAt
          ? `上次成功：${formatDateTime(iyuuCatalog.data.state.lastSuccessAt)}`
          : '尚未成功同步'}
      >
        {iyuuCatalog.data?.state.lastError && (
          <Alert
            type="warning"
            showIcon
            message="最近一次 IYUU 同步失败；已保留上次成功目录"
            description={iyuuCatalog.data.state.lastError}
          />
        )}
        <PageState
          loading={iyuuCatalog.isLoading}
          error={iyuuCatalog.error}
          onRetry={() => void iyuuCatalog.refetch()}
          empty={iyuuCatalog.data?.items.length === 0}
          emptyDescription="目录尚未同步；可点击右上角“同步 IYUU 目录”。"
        >
          <Table<IYUUSite>
            rowKey="remoteId"
            size="small"
            columns={iyuuColumns}
            dataSource={iyuuCatalog.data?.items}
            pagination={{ pageSize: 10, showSizeChanger: true }}
            scroll={{ x: 820 }}
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
            <span>还没有 Tracker 分类规则。</span>
            <Button type="primary" icon={<PlusOutlined />} onClick={() => openRuleModal()}>创建第一条规则</Button>
          </Space>
        }
      >
        <Card className="table-card">
          <Table<TrackerRule>
            rowKey="id"
            columns={columns}
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
          <Form.Item name="siteName" label="内部站点名" rules={[{ required: true, message: '请输入内部站点名' }, { pattern: /^[a-z0-9][a-z0-9._-]*$/i, message: '请使用字母、数字、点、下划线或短横线' }]}>
            <Input placeholder="mteam" />
          </Form.Item>
          <Form.Item name="displayName" label="显示名称" rules={[{ required: true, message: '请输入显示名称' }, { max: 80 }]}>
            <Input prefix={<TagsOutlined />} placeholder="例如：M-Team" />
          </Form.Item>
          {createMutation.error && <Alert type="error" showIcon message="保存失败" description={displayError(createMutation.error)} />}
        </Form>
      </Modal>
    </div>
  )
}
