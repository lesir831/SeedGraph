import { AuditOutlined, PlayCircleOutlined, ReloadOutlined, SyncOutlined } from '@ant-design/icons'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  Alert,
  App,
  Button,
  Card,
  Col,
  Descriptions,
  Progress,
  Row,
  Select,
  Space,
  Table,
  Tag,
  Timeline,
  Typography,
  type TableColumnsType,
} from 'antd'
import { useState } from 'react'
import { api } from '../api/client'
import { normalizePagedResponse } from '../api/transformers'
import type { AuditEvent, AuditFilters } from '../api/types'
import { PageHeader } from '../components/PageHeader'
import { PageState } from '../components/PageState'
import { OperationBadge } from '../components/StatusTag'
import { displayError, formatDateTime } from '../utils/format'
import { useDeletionTasks } from '../deletion/deletion-context'

const initialFilters: AuditFilters = { page: 1, pageSize: 20 }

const actionLabels: Record<string, string> = {
  'sync.run': '全量同步',
  'downloader.sync': '下载器同步',
  'downloader.create': '添加下载器',
  'downloader.delete': '删除下载器',
  'group.merge': '手动合并',
  'group.split': '拆分任务组',
  'group.move': '移动任务',
  'group.undo': '撤销分组操作',
  'group.lock': '锁定任务组',
  'delete.plan': '生成删除预览',
  'delete.started': '提交删除任务',
  'delete.completed': '删除完成',
  'delete.failed': '删除失败',
  'delete.uncertain': '删除结果待确认',
  'iyuu.sync': 'IYUU 目录同步',
  login: '管理员登录',
}

export function SyncAuditPage() {
  const { message } = App.useApp()
  const queryClient = useQueryClient()
  const { trackJob } = useDeletionTasks()
  const [filters, setFilters] = useState<AuditFilters>(initialFilters)

  const syncStatus = useQuery({
    queryKey: ['sync-status'],
    queryFn: api.getSyncStatus,
    refetchInterval: (query) => query.state.data?.running ? 2_000 : 15_000,
  })
  const audit = useQuery({
    queryKey: ['audit-events', filters],
    queryFn: () => api.getAuditEvents(filters),
    select: (payload) => normalizePagedResponse(payload, filters.page, filters.pageSize),
    refetchInterval: 15_000,
  })

  const runMutation = useMutation({
    mutationFn: api.runSync,
    onSuccess: async () => {
      void message.success('全量同步已启动')
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['sync-status'] }),
        queryClient.invalidateQueries({ queryKey: ['audit-events'] }),
      ])
    },
    onError: (error) => void message.error(displayError(error)),
  })

  const inspectDeletion = useMutation({
    mutationFn: api.getDeleteJob,
    onSuccess: trackJob,
    onError: (error) => void message.error(displayError(error)),
  })

  const columns: TableColumnsType<AuditEvent> = [
    {
      title: '时间',
      dataIndex: 'occurredAt',
      width: 180,
      render: formatDateTime,
    },
    {
      title: '操作',
      dataIndex: 'action',
      width: 150,
      render: (action: string) => actionLabels[action] ?? action,
    },
    {
      title: '对象',
      key: 'resource',
      width: 210,
      render: (_, event) => (
        <div className="primary-cell">
          <strong>{event.resourceName || 'SeedGraph'}</strong>
          <span>{event.resourceType}</span>
        </div>
      ),
    },
    {
      title: '结果',
      dataIndex: 'status',
      width: 110,
      render: (status: AuditEvent['status']) => <OperationBadge status={status} />,
    },
    {
      title: '说明',
      dataIndex: 'message',
      ellipsis: true,
      render: (value: string) => value || '—',
    },
    {
      title: '操作者',
      dataIndex: 'actor',
      width: 110,
      render: (actor: string) => <Tag>{actor || 'system'}</Tag>,
    },
  ]

  const status = syncStatus.data

  return (
    <div className="page-stack">
      <PageHeader
        title="同步与审计"
        description="查看数据拉取进度、最近运行结果，以及会改变系统状态的所有操作。"
        extra={
          <>
            <Button
              icon={<ReloadOutlined spin={syncStatus.isFetching || audit.isFetching} />}
              onClick={() => void Promise.all([syncStatus.refetch(), audit.refetch()])}
            >
              刷新
            </Button>
            <Button
              type="primary"
              icon={<PlayCircleOutlined />}
              disabled={status?.running}
              loading={runMutation.isPending}
              onClick={() => runMutation.mutate()}
            >
              {status?.running ? '同步进行中' : '立即全量同步'}
            </Button>
          </>
        }
      />

      <Row gutter={[16, 16]}>
        <Col xs={24} lg={15}>
          <Card title={<Space><SyncOutlined spin={status?.running} />同步状态</Space>} className="sync-card">
            <PageState loading={syncStatus.isLoading} error={syncStatus.error} onRetry={() => void syncStatus.refetch()} skeletonRows={3}>
              {status && (
                <Space direction="vertical" size={18} className="card-content-full">
                  <div className="sync-status-head">
                    <div>
                      <OperationBadge status={status.status} />
                      <Typography.Title level={3}>{status.running ? '正在读取下载器任务' : '同步服务已就绪'}</Typography.Title>
                    </div>
                    {status.running && <Progress type="circle" percent={75} status="active" size={70} format={() => '同步中'} />}
                  </div>
                  <Descriptions size="small" column={{ xs: 1, sm: 2 }}>
                    <Descriptions.Item label="开始时间">{formatDateTime(status.startedAt)}</Descriptions.Item>
                    <Descriptions.Item label="完成时间">{formatDateTime(status.completedAt)}</Descriptions.Item>
                    <Descriptions.Item label="扫描实例">{status.scannedInstances}</Descriptions.Item>
                    <Descriptions.Item label="更新任务组">{status.updatedGroups}</Descriptions.Item>
                  </Descriptions>
                  {status.error && <Alert type="error" showIcon message="最近一次同步未完成" description={status.error} />}
                </Space>
              )}
            </PageState>
          </Card>
        </Col>
        <Col xs={24} lg={9}>
          <Card title="同步流程" className="process-card">
            <Timeline
              items={[
                { color: 'blue', children: <><strong>拉取任务</strong><p>从已启用下载器读取任务与文件清单</p></> },
                { color: 'blue', children: <><strong>规范化内容</strong><p>统一路径、体积和文件清单表示</p></> },
                { color: 'blue', children: <><strong>更新关系图</strong><p>保留手动锁定并重算自动分组</p></> },
                { color: 'green', children: <><strong>写入审计</strong><p>记录结果和异常，供管理员复核</p></> },
              ]}
            />
          </Card>
        </Col>
      </Row>

      <Card
        title={<Space><AuditOutlined />审计记录</Space>}
        className="table-card"
      >
        <Space wrap className="audit-toolbar">
          <Select
            className="audit-filter" aria-label="筛选审计操作" placeholder="全部操作" allowClear showSearch optionFilterProp="label"
            value={filters.action}
            options={Object.entries(actionLabels).map(([value, label]) => ({ value, label }))}
            onChange={(action) => setFilters((current) => ({ ...current, action, page: 1 }))}
          />
          <Select
            className="audit-filter" aria-label="筛选审计结果" placeholder="全部结果" allowClear
            value={filters.status}
            options={[{ value: 'success', label: '成功' }, { value: 'failed', label: '失败' }, { value: 'warning', label: '有警告' }]}
            onChange={(status) => setFilters((current) => ({ ...current, status, page: 1 }))}
          />
          <Button disabled={!filters.action && !filters.status} onClick={() => setFilters((current) => ({ page: 1, pageSize: current.pageSize }))}>重置筛选</Button>
        </Space>
        <PageState
          loading={audit.isLoading}
          error={audit.error}
          onRetry={() => void audit.refetch()}
          empty={audit.data?.items.length === 0}
          emptyDescription="没有符合条件的审计记录。"
        >
          <Table<AuditEvent>
            rowKey="id"
            columns={columns}
            dataSource={audit.data?.items}
            scroll={{ x: 940 }}
            expandable={{
              expandedRowRender: (event) => (
                <Descriptions size="small" column={1} className="audit-details">
                  <Descriptions.Item label="记录编号"><Typography.Text copyable>{event.id}</Typography.Text></Descriptions.Item>
                  <Descriptions.Item label="对象编号"><Typography.Text copyable={Boolean(event.resourceName)}>{event.resourceName || '—'}</Typography.Text></Descriptions.Item>
                  <Descriptions.Item label="完整说明">{event.message || '—'}</Descriptions.Item>
                  <Descriptions.Item label="操作详情"><pre>{JSON.stringify(event.details, null, 2)}</pre></Descriptions.Item>
                  {event.resourceType === 'delete_job' && event.resourceName && (
                    <Descriptions.Item label="执行记录"><Button size="small" loading={inspectDeletion.isPending && inspectDeletion.variables === event.resourceName} onClick={() => inspectDeletion.mutate(event.resourceName)}>查看删除进度</Button></Descriptions.Item>
                  )}
                </Descriptions>
              ),
            }}
            pagination={{
              current: audit.data?.page ?? filters.page,
              pageSize: audit.data?.pageSize ?? filters.pageSize,
              total: audit.data?.total ?? 0,
              showSizeChanger: true,
              showTotal: (total) => `共 ${total} 条记录`,
              onChange: (page, pageSize) => setFilters((current) => ({ ...current, page: pageSize === current.pageSize ? page : 1, pageSize })),
            }}
          />
        </PageState>
      </Card>
    </div>
  )
}
