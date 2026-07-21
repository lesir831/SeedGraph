import {
  ClearOutlined,
  DeleteOutlined,
  DisconnectOutlined,
  FileOutlined,
  FilterOutlined,
  LockOutlined,
  MergeCellsOutlined,
  ReloadOutlined,
  SafetyCertificateOutlined,
  SortAscendingOutlined,
  SwapOutlined,
  UnlockOutlined,
} from '@ant-design/icons'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  Alert,
  App,
  Button,
  Card,
  Checkbox,
  Collapse,
  Descriptions,
  Form,
  Grid,
  Input,
  List,
  Modal,
  Pagination,
  Popconfirm,
  Progress,
  Select,
  Space,
  Table,
  Tag,
  Tooltip,
  Typography,
  type TableColumnsType,
} from 'antd'
import dayjs from 'dayjs'
import { useMemo, useState, type Key, type ReactNode } from 'react'
import { api } from '../api/client'
import { normalizePagedResponse } from '../api/transformers'
import type {
  DeletePlan,
  GroupFilters,
  GroupSiteSummary,
  GroupSortRule,
  TorrentGroup,
  TorrentInstance,
} from '../api/types'
import { GroupAdvancedSearchDrawer } from '../components/GroupAdvancedSearchDrawer'
import { GroupSortDrawer } from '../components/GroupSortDrawer'
import { PageHeader } from '../components/PageHeader'
import { PageState } from '../components/PageState'
import { displayError, formatBytes, formatDateTime, formatDeleteBlocker, formatPercent } from '../utils/format'
import { GROUP_SORT_LABELS, loadGroupSorts, saveGroupSorts } from '../utils/groupSortPreferences'

const initialFilters: GroupFilters = {
  status: 'all',
  page: 1,
  pageSize: 20,
}

const groupSortSummary = (sorts: GroupSortRule[]) => sorts
  .map((rule) => `${GROUP_SORT_LABELS[rule.field]}${rule.order === 'asc' ? '↑' : '↓'}`)
  .join(' → ')

function GroupSiteTags({ sites, limit = 5 }: { sites: GroupSiteSummary[]; limit?: number }) {
  if (!sites.length) return <Typography.Text type="secondary" className="group-site-empty">暂无站点</Typography.Text>
  const visible = sites.slice(0, limit)
  return (
    <Tooltip title={sites.map((site) => site.label).join(' · ')} placement="topLeft">
      <div className="group-site-list">
        {visible.map((site) => (
          <Tag key={site.key} color={site.mapped ? 'blue' : 'default'}>{site.label}</Tag>
        ))}
        {sites.length > limit && <Tag>+{sites.length - limit}</Tag>}
      </div>
    </Tooltip>
  )
}

interface MergeFormValues {
  displayName: string
}

interface MoveSelection {
	sourceGroup: TorrentGroup
	instance: TorrentInstance
}

const stateColor = (state: string) => {
  const normalized = state.toLowerCase()
  if (normalized.includes('seed') || normalized.includes('upload')) return 'success'
  if (normalized.includes('download')) return 'processing'
  if (normalized.includes('error')) return 'error'
  if (normalized.includes('pause') || normalized.includes('stop')) return 'default'
  return 'blue'
}

function GroupDetailsLoader({ groupId, children }: { groupId: string; children: (group: TorrentGroup) => ReactNode }) {
  const detail = useQuery({
    queryKey: ['torrent-group', groupId],
    queryFn: () => api.getGroup(groupId),
  })
  return (
    <PageState loading={detail.isLoading} error={detail.error} onRetry={() => void detail.refetch()} skeletonRows={3}>
      {detail.data ? children(detail.data) : null}
    </PageState>
  )
}

function DeletePlanBlockerDetails({ blockers }: { blockers: DeletePlan['blockers'] }) {
  const conflictingTasks = blockers.filter((blocker) => blocker.code === 'conflicting_path_occupant')
  const otherMessages = Array.from(new Set(
    blockers
      .filter((blocker) => blocker.code !== 'conflicting_path_occupant')
      .map((blocker) => formatDeleteBlocker(blocker.code, blocker.message)),
  ))

  return (
    <Space direction="vertical" size={8} className="delete-blocker-details">
      {otherMessages.length > 0 && <Typography.Text>{otherMessages.join('；')}</Typography.Text>}
      {conflictingTasks.length > 0 && (
        <div className="delete-conflict-section">
          <Typography.Text strong>检测到 {conflictingTasks.length} 个文件冲突任务</Typography.Text>
          <List
            className="delete-conflict-list"
            size="small"
            bordered
            dataSource={conflictingTasks}
            renderItem={(blocker) => (
              <List.Item key={blocker.instanceId ?? `${blocker.downloaderId}-${blocker.path}`}>
                <Space direction="vertical" size={0} className="delete-conflict-item">
                  <Typography.Text strong>{blocker.instanceName ?? blocker.instanceId ?? '未知任务'}</Typography.Text>
                  <Typography.Text type="secondary">
                    下载器：{blocker.downloaderName ?? blocker.downloaderId ?? '未知下载器'}
                  </Typography.Text>
                  <Typography.Text className="delete-conflict-path" copyable={Boolean(blocker.path)}>
                    {blocker.path ?? '路径信息不可用'}
                  </Typography.Text>
                </Space>
              </List.Item>
            )}
          />
        </div>
      )}
    </Space>
  )
}

export function TorrentGroupsPage() {
  const { message } = App.useApp()
  const queryClient = useQueryClient()
  const screens = Grid.useBreakpoint()
  const isMobile = !screens.md
  const [filters, setFilters] = useState<GroupFilters>(() => ({
    ...initialFilters,
    sorts: loadGroupSorts(),
  }))
  const [searchDraft, setSearchDraft] = useState('')
  const [advancedSearchOpen, setAdvancedSearchOpen] = useState(false)
  const [sortDrawerOpen, setSortDrawerOpen] = useState(false)
  const [mobileExpandedGroupIds, setMobileExpandedGroupIds] = useState<string[]>([])
  const [selectedGroupIds, setSelectedGroupIds] = useState<Key[]>([])
  const [selectedGroups, setSelectedGroups] = useState<TorrentGroup[]>([])
  const [mergeOpen, setMergeOpen] = useState(false)
  const [mergeForm] = Form.useForm<MergeFormValues>()
  const [deleteGroup, setDeleteGroup] = useState<TorrentGroup>()
  const [deleteInstanceIds, setDeleteInstanceIds] = useState<string[]>([])
  const [deletePlan, setDeletePlan] = useState<DeletePlan>()
  const [detailLoadingId, setDetailLoadingId] = useState<string>()
	const [moveSelection, setMoveSelection] = useState<MoveSelection>()
	const [moveTargetId, setMoveTargetId] = useState<string>()
	const [lastOperation, setLastOperation] = useState<{ id: string; label: string }>()

  const groups = useQuery({
    queryKey: ['torrent-groups', filters],
    queryFn: () => api.getGroups(filters),
    select: (payload) => normalizePagedResponse(payload, filters.page, filters.pageSize),
  })
  const downloaders = useQuery({ queryKey: ['downloaders'], queryFn: api.getDownloaders })
	const moveTargets = useQuery({
		queryKey: ['torrent-groups', 'move-targets'],
		queryFn: () => api.getGroups({ status: 'all', page: 1, pageSize: 200 }),
		enabled: Boolean(moveSelection),
	})

  const groupSiteOptions = useQuery({
    queryKey: ['torrent-groups', 'site-options'],
    queryFn: api.getGroupSiteOptions,
    enabled: advancedSearchOpen,
    staleTime: 5 * 60 * 1000,
  })
  const pageSiteOptions = useMemo(() => {
    const unique = new Map<string, GroupSiteSummary>()
    for (const site of (groups.data?.items ?? []).flatMap((group) => group.sites)) unique.set(site.key, site)
    return Array.from(unique.values()).sort((left, right) => left.label.localeCompare(right.label, 'zh-CN'))
  }, [groups.data?.items])
  const siteOptions = groupSiteOptions.data ?? pageSiteOptions
  const siteLabels = useMemo(() => new Map(
    [...pageSiteOptions, ...siteOptions].map((site) => [site.key, site.label]),
  ), [pageSiteOptions, siteOptions])
  const formatSiteFilters = (keys: string[], separator = '、') => keys.map((key) => siteLabels.get(key) ?? key).join(separator)

  const advancedFilterCount = [
    filters.nameContains,
    filters.requiredSites?.length,
    filters.excludedSites?.length,
    filters.sizeLT,
    filters.oldestAddedGte || filters.oldestAddedLt,
    filters.maxSiteCount !== undefined,
    filters.missingSite,
    filters.stale !== undefined,
  ].filter(Boolean).length

  const invalidateGroups = async () => {
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: ['torrent-groups'] }),
      queryClient.invalidateQueries({ queryKey: ['torrent-group'] }),
      queryClient.invalidateQueries({ queryKey: ['overview'] }),
      queryClient.invalidateQueries({ queryKey: ['audit-events'] }),
    ])
  }

  const mergeMutation = useMutation({
    mutationFn: api.mergeGroups,
    onSuccess: async (group) => {
      void message.success('手动分组已保存')
		if (group.operationId) setLastOperation({ id: group.operationId, label: '合并分组' })
      setSelectedGroupIds([])
      setSelectedGroups([])
      setMergeOpen(false)
      mergeForm.resetFields()
      await invalidateGroups()
    },
    onError: (error) => void message.error(displayError(error)),
  })

  const splitMutation = useMutation({
    mutationFn: ({ group, instanceIds }: { group: TorrentGroup; instanceIds: string[] }) =>
      api.splitGroup(group, instanceIds),
    onSuccess: async (group) => {
      void message.success('任务已拆分为新的手动分组')
		if (group.operationId) setLastOperation({ id: group.operationId, label: '拆分分组' })
      await invalidateGroups()
    },
    onError: (error) => void message.error(displayError(error)),
  })

	const moveMutation = useMutation({
		mutationFn: ({ source, instanceId, target }: {
			source: TorrentGroup
			instanceId: string
			target: TorrentGroup
		}) => api.moveGroupMember(source, instanceId, target),
		onSuccess: async (group) => {
			void message.success('任务已移动到目标分组')
			if (group.operationId) setLastOperation({ id: group.operationId, label: '移动任务' })
			setMoveSelection(undefined)
			setMoveTargetId(undefined)
			await invalidateGroups()
		},
		onError: (error) => void message.error(displayError(error)),
	})

	const undoMutation = useMutation({
		mutationFn: api.undoGroupOperation,
		onSuccess: async () => {
			void message.success('上一步手工分组操作已撤销')
			setLastOperation(undefined)
			await invalidateGroups()
		},
		onError: (error) => void message.error(displayError(error)),
	})

  const lockMutation = useMutation({
    mutationFn: ({ group, locked }: { group: TorrentGroup; locked: boolean }) => api.setGroupLock(group, locked),
    onSuccess: async (_, variables) => {
      void message.success(variables.locked ? '已锁定分组' : '已解除锁定')
      await invalidateGroups()
    },
    onError: (error) => void message.error(displayError(error)),
  })

  const restoreMutation = useMutation({
    mutationFn: api.restoreAutomaticGrouping,
    onSuccess: async () => {
      void message.success('已恢复自动分组')
      await invalidateGroups()
    },
    onError: (error) => void message.error(displayError(error)),
  })

  const planMutation = useMutation({
    mutationFn: api.createDeletePlan,
    onSuccess: setDeletePlan,
    onError: (error) => void message.error(displayError(error)),
  })

  const jobMutation = useMutation({
    mutationFn: api.createDeleteJob,
    onSuccess: async () => {
      void message.success('删除任务已提交，可在审计页查看进度')
      closeDeleteModal()
      await invalidateGroups()
    },
    onError: (error) => void message.error(displayError(error)),
  })

  const openDeleteModal = async (group: TorrentGroup, instanceId?: string) => {
    setDetailLoadingId(group.id)
    try {
      const detail = group.instances.length
        ? group
        : await queryClient.fetchQuery({
          queryKey: ['torrent-group', group.id],
          queryFn: () => api.getGroup(group.id),
        })
      setDeleteGroup(detail)
      setDeleteInstanceIds(instanceId ? [instanceId] : [])
      setDeletePlan(undefined)
    } catch (error) {
      void message.error(displayError(error))
    } finally {
      setDetailLoadingId(undefined)
    }
  }

  const closeDeleteModal = () => {
    setDeleteGroup(undefined)
    setDeleteInstanceIds([])
    setDeletePlan(undefined)
    planMutation.reset()
    jobMutation.reset()
  }

  const generateDeletePlan = () => {
    if (!deleteGroup || !deleteInstanceIds.length) return
    planMutation.mutate({
      groupId: deleteGroup.id,
      instanceIds: deleteInstanceIds,
    })
  }

  const instanceColumns = (group: TorrentGroup): TableColumnsType<TorrentInstance> => [
    {
      title: '下载器 / 任务',
      key: 'name',
      width: 300,
      render: (_, instance) => (
        <div className="primary-cell instance-name-cell">
          <Tooltip title={instance.downloaderName} placement="topLeft">
            <strong>{instance.downloaderName}</strong>
          </Tooltip>
          <Tooltip title={instance.name} placement="topLeft">
            <span>{instance.name}</span>
          </Tooltip>
        </div>
      ),
    },
    {
      title: '客户端',
      dataIndex: 'downloaderKind',
      width: 130,
      render: (kind: TorrentInstance['downloaderKind']) => (
        <Tag color={kind === 'qbittorrent' ? 'blue' : 'geekblue'}>
          {kind === 'qbittorrent' ? 'qBittorrent' : 'Transmission'}
        </Tag>
      ),
    },
    {
      title: '进度',
      dataIndex: 'progress',
      width: 160,
      render: (progress: number) => (
        <Progress percent={Math.round((progress > 1 ? progress / 100 : progress) * 100)} size="small" />
      ),
    },
    {
      title: '状态',
      dataIndex: 'state',
      width: 110,
      render: (state: string) => <Tag color={stateColor(state)}>{state || 'unknown'}</Tag>,
    },
    {
      title: '分享率',
      dataIndex: 'ratio',
      width: 90,
      render: (ratio: number) => ratio.toFixed(2),
    },
    {
      title: '添加时间',
      dataIndex: 'addedAt',
      width: 170,
      render: formatDateTime,
    },
    {
      title: '操作',
      key: 'actions',
      fixed: 'right',
      width: 230,
      render: (_, instance) => (
        <Space size={4}>
          {group.instances.length > 1 && group.groupingMethod === 'manual' && (
            <Popconfirm
              title="拆出这个实例？"
				description="该任务会进入一个新的手动分组；可立即撤销。"
              okText="拆分"
              cancelText="取消"
              onConfirm={() => splitMutation.mutate({ group, instanceIds: [instance.id] })}
            >
              <Button type="text" size="small" icon={<DisconnectOutlined />}>拆分</Button>
            </Popconfirm>
          )}
			<Button
				type="text"
				size="small"
				icon={<SwapOutlined />}
				onClick={() => {
					setMoveSelection({ sourceGroup: group, instance })
					setMoveTargetId(undefined)
				}}
			>
				移动
			</Button>
          <Button type="text" danger size="small" icon={<DeleteOutlined />} onClick={() => void openDeleteModal(group, instance.id)}>
            预览删除
          </Button>
        </Space>
      ),
    },
  ]

  const renderGroupActions = (group: TorrentGroup) => (
    <Space size={2} wrap>
      <Tooltip title={group.locked ? '允许自动聚合调整该组' : '保持当前分组关系'}>
        <Button
          type="text"
          size="small"
          icon={group.locked ? <UnlockOutlined /> : <LockOutlined />}
          loading={lockMutation.isPending && lockMutation.variables?.group.id === group.id}
          onClick={() => lockMutation.mutate({ group, locked: !group.locked })}
        >
          {group.locked ? '解锁' : '锁定'}
        </Button>
      </Tooltip>
      {group.groupingMethod === 'manual' && (
        <Popconfirm
          title="恢复自动分组？"
          description="当前手动关系将被自动规则重新计算。"
          okText="恢复"
          cancelText="取消"
          onConfirm={() => restoreMutation.mutate(group)}
        >
          <Button type="text" size="small" icon={<ReloadOutlined />}>自动</Button>
        </Popconfirm>
      )}
      <Button
        type="text"
        danger
        size="small"
        icon={<DeleteOutlined />}
        loading={detailLoadingId === group.id}
        onClick={() => void openDeleteModal(group)}
      >
        删除预览
      </Button>
    </Space>
  )

  const columns: TableColumnsType<TorrentGroup> = [
    {
      title: '聚合内容',
      key: 'name',
      width: 460,
      render: (_, group) => (
        <div className="primary-cell group-name-cell">
          <div className="group-title-row">
            <Tooltip title={group.name} placement="topLeft">
              <strong className="group-title-text">{group.name}</strong>
            </Tooltip>
            <div className="group-title-tags">
              {group.groupingMethod === 'manual' && <Tag color="purple">手动</Tag>}
              {group.locked && <Tag icon={<LockOutlined />} color="gold">已锁定</Tag>}
            </div>
          </div>
          <GroupSiteTags sites={group.sites} />
        </div>
      ),
    },
    {
      title: '大小',
      key: 'size',
      width: 120,
      render: (_, group) => <strong className="group-metric-value">{formatBytes(group.totalSize)}</strong>,
    },
    {
      title: '实例',
      dataIndex: 'taskCount',
      width: 88,
      align: 'center',
      render: (value: number) => <strong className="group-instance-count">{value}</strong>,
    },
    {
      title: '最旧添加时间',
      dataIndex: 'oldestAddedAt',
      width: 170,
      render: formatDateTime,
    },
    {
      title: '操作',
      key: 'actions',
      fixed: 'right',
      width: 190,
      render: (_, group) => renderGroupActions(group),
    },
  ]

  const expandedRow = (summary: TorrentGroup) => (
    <div className="expanded-group">
      <GroupDetailsLoader groupId={summary.id}>
        {(group) => (
          <>
            <Typography.Title level={5}>下载器实例</Typography.Title>
            <Table<TorrentInstance>
              rowKey="id"
              size="small"
              pagination={false}
              columns={instanceColumns(group)}
              dataSource={group.instances}
              scroll={{ x: 1080 }}
            />
            <details className="manifest-details">
              <summary><FileOutlined /> 分组依据与物理副本</summary>
              <Descriptions size="small" column={{ xs: 1, sm: 3 }} className="group-detail-descriptions">
                <Descriptions.Item label="内容体积">{formatBytes(group.totalSize)}</Descriptions.Item>
                <Descriptions.Item label="物理数据组">{group.dataCopyCount}</Descriptions.Item>
                <Descriptions.Item label="可信度">{group.confidence}</Descriptions.Item>
              </Descriptions>
              <Typography.Paragraph type="secondary">
                服务端以规范路径、选中文件总大小与文件体积清单指纹归组。文件名或完整 Tracker 地址不会被当作删除安全依据。
              </Typography.Paragraph>
            </details>
          </>
        )}
      </GroupDetailsLoader>
    </div>
  )

  const setGroupSelected = (group: TorrentGroup, selected: boolean) => {
    setSelectedGroupIds((current) => selected
      ? [...current.filter((key) => key !== group.id), group.id]
      : current.filter((key) => key !== group.id))
    setSelectedGroups((current) => selected
      ? [...current.filter((item) => item.id !== group.id), group]
      : current.filter((item) => item.id !== group.id))
  }

  const renderMobileInstance = (group: TorrentGroup, instance: TorrentInstance) => (
    <Card key={instance.id} size="small" className="mobile-instance-card">
      <div className="mobile-instance-heading">
        <div className="primary-cell">
          <strong>{instance.downloaderName}</strong>
          <span title={instance.name}>{instance.name}</span>
        </div>
        <Tag color={instance.downloaderKind === 'qbittorrent' ? 'blue' : 'geekblue'}>
          {instance.downloaderKind === 'qbittorrent' ? 'qBittorrent' : 'Transmission'}
        </Tag>
      </div>
      <Progress percent={Math.round((instance.progress > 1 ? instance.progress / 100 : instance.progress) * 100)} size="small" />
      <div className="mobile-instance-meta">
        <Tag color={stateColor(instance.state)}>{instance.state || 'unknown'}</Tag>
        <span>分享率 {instance.ratio.toFixed(2)}</span>
        <span>{formatDateTime(instance.addedAt)}</span>
      </div>
      <div className="mobile-card-actions">
        {group.instances.length > 1 && group.groupingMethod === 'manual' && (
          <Popconfirm
            title="拆出这个实例？"
            description="该任务会进入一个新的手动分组；可立即撤销。"
            okText="拆分"
            cancelText="取消"
            onConfirm={() => splitMutation.mutate({ group, instanceIds: [instance.id] })}
          >
            <Button type="text" size="small" icon={<DisconnectOutlined />}>拆分</Button>
          </Popconfirm>
        )}
        <Button
          type="text"
          size="small"
          icon={<SwapOutlined />}
          onClick={() => {
            setMoveSelection({ sourceGroup: group, instance })
            setMoveTargetId(undefined)
          }}
        >
          移动
        </Button>
        <Button type="text" danger size="small" icon={<DeleteOutlined />} onClick={() => void openDeleteModal(group, instance.id)}>
          预览删除
        </Button>
      </div>
    </Card>
  )

  const renderMobileGroup = (group: TorrentGroup) => {
    const selected = selectedGroupIds.includes(group.id)
    const expanded = mobileExpandedGroupIds.includes(group.id)
    return (
      <List.Item key={group.id}>
        <Card className="group-mobile-card">
          <div className="group-mobile-heading">
            <Checkbox checked={selected} onChange={(event) => setGroupSelected(group, event.target.checked)} />
            <div className="group-mobile-title">
              <Typography.Text strong title={group.name}>{group.name}</Typography.Text>
              <div className="group-title-tags">
                {group.groupingMethod === 'manual' && <Tag color="purple">手动</Tag>}
                {group.locked && <Tag icon={<LockOutlined />} color="gold">已锁定</Tag>}
              </div>
            </div>
          </div>
          <GroupSiteTags sites={group.sites} limit={4} />
          <div className="group-mobile-metrics">
            <div><span>大小</span><strong>{formatBytes(group.totalSize)}</strong></div>
            <div><span>实例</span><strong>{group.taskCount}</strong></div>
            <div><span>最旧添加</span><strong>{formatDateTime(group.oldestAddedAt)}</strong></div>
          </div>
          <div className="mobile-card-actions">{renderGroupActions(group)}</div>
          <Collapse
            ghost
            className="mobile-group-collapse"
            activeKey={expanded ? [group.id] : []}
            onChange={(keys) => {
              const open = Array.isArray(keys) ? keys.includes(group.id) : keys === group.id
              setMobileExpandedGroupIds((current) => open
                ? [...current.filter((id) => id !== group.id), group.id]
                : current.filter((id) => id !== group.id))
            }}
            items={[{
              key: group.id,
              label: `查看 ${group.taskCount} 个下载器实例`,
              children: expanded ? (
                <GroupDetailsLoader groupId={group.id}>
                  {(detail) => <div className="mobile-instance-list">{detail.instances.map((instance) => renderMobileInstance(detail, instance))}</div>}
                </GroupDetailsLoader>
              ) : null,
            }]}
          />
        </Card>
      </List.Item>
    )
  }

  return (
    <div className="page-stack">
      <PageHeader
        title="聚合任务"
        description="按规范路径、总大小和文件清单识别同一内容；展开任务组可核对每个下载器实例。"
        extra={
          <>
            <Button
              icon={<MergeCellsOutlined />}
              disabled={selectedGroupIds.length < 2}
              onClick={() => setMergeOpen(true)}
            >
              手动合并 {selectedGroupIds.length ? `(${selectedGroupIds.length})` : ''}
            </Button>
            <Button icon={<ReloadOutlined spin={groups.isFetching} />} onClick={() => void invalidateGroups()}>刷新</Button>
          </>
        }
      />

		{lastOperation && (
			<Alert
				type="success"
				showIcon
				message={`${lastOperation.label}已保存`}
				description="撤销会再次核对所有受影响的分组版本和成员关系；若期间已有其他修改，服务端会拒绝回滚。"
				action={(
					<Button
						size="small"
						loading={undoMutation.isPending}
						onClick={() => undoMutation.mutate(lastOperation.id)}
					>
						撤销上一步
					</Button>
				)}
			/>
		)}

      <Card className="filter-card group-control-card">
        <div className="group-toolbar">
          <Input.Search
            allowClear
            value={searchDraft}
            placeholder="搜索名称或存放路径"
            className="wide-search"
            onChange={(event) => {
              const query = event.target.value
              setSearchDraft(query)
              if (!query) setFilters((current) => ({ ...current, query: undefined, page: 1 }))
            }}
            onSearch={(query) => setFilters((current) => ({ ...current, query: query.trim() || undefined, page: 1 }))}
          />
          <Select
            value={filters.status}
            className="filter-select"
            options={[
              { value: 'all', label: '全部运行状态' },
              { value: 'downloading', label: '下载中' },
              { value: 'seeding', label: '做种中（Transmission）' },
              { value: 'uploading', label: '做种中（qBittorrent）' },
              { value: 'stopped', label: '已停止（Transmission）' },
              { value: 'pausedUP', label: '已暂停上传（qBittorrent）' },
              { value: 'pausedDL', label: '已暂停下载（qBittorrent）' },
              { value: 'stalledUP', label: '上传停滞（qBittorrent）' },
              { value: 'stalledDL', label: '下载停滞（qBittorrent）' },
              { value: 'error', label: '异常' },
            ]}
            onChange={(status) => setFilters((current) => ({ ...current, status, page: 1 }))}
          />
          <Select<string>
            allowClear
            placeholder="全部下载器"
            className="filter-select"
            value={filters.downloaderId}
            loading={downloaders.isLoading}
            options={(downloaders.data ?? []).map((item) => ({ value: item.id, label: item.name }))}
            onChange={(downloaderId) => setFilters((current) => ({ ...current, downloaderId, page: 1 }))}
          />
          <Button icon={<FilterOutlined />} type={advancedFilterCount ? 'primary' : 'default'} onClick={() => setAdvancedSearchOpen(true)}>
            高级搜索{advancedFilterCount ? ` (${advancedFilterCount})` : ''}
          </Button>
          <Button icon={<SortAscendingOutlined />} onClick={() => setSortDrawerOpen(true)}>
            多级排序 ({filters.sorts?.length ?? 0})
          </Button>
          <Button
            type="text"
            icon={<ClearOutlined />}
            disabled={!filters.query && filters.status === 'all' && !filters.downloaderId && !advancedFilterCount}
            onClick={() => {
              setSearchDraft('')
              setFilters((current) => ({
                ...initialFilters,
                pageSize: current.pageSize,
                sorts: current.sorts,
              }))
            }}
          >
            清空筛选
          </Button>
        </div>
        <div className="group-filter-summary">
          <span className="sort-summary"><SortAscendingOutlined /> {groupSortSummary(filters.sorts ?? [])}</span>
          {filters.nameContains && (
            <Tag closable onClose={() => setFilters((current) => ({ ...current, nameContains: undefined, page: 1 }))}>
              名称包含：{filters.nameContains}
            </Tag>
          )}
          {filters.requiredSites?.length ? (
            <Tag closable color="blue" onClose={() => setFilters((current) => ({ ...current, requiredSites: undefined, page: 1 }))}>
              同时包含：{formatSiteFilters(filters.requiredSites, ' + ')}
            </Tag>
          ) : null}
          {filters.excludedSites?.length ? (
            <Tag closable color="volcano" onClose={() => setFilters((current) => ({ ...current, excludedSites: undefined, page: 1 }))}>
              排除：{formatSiteFilters(filters.excludedSites)}
            </Tag>
          ) : null}
          {filters.sizeLT ? (
            <Tag closable onClose={() => setFilters((current) => ({ ...current, sizeLT: undefined, page: 1 }))}>
              大小 &lt; {formatBytes(filters.sizeLT)}
            </Tag>
          ) : null}
          {(filters.oldestAddedGte || filters.oldestAddedLt) && (
            <Tag
              closable
              onClose={() => setFilters((current) => ({ ...current, oldestAddedGte: undefined, oldestAddedLt: undefined, page: 1 }))}
            >
              最旧添加：{filters.oldestAddedGte?.slice(0, 10)} ～ {filters.oldestAddedLt ? dayjs(filters.oldestAddedLt).subtract(1, 'day').format('YYYY-MM-DD') : ''}
            </Tag>
          )}
          {filters.maxSiteCount !== undefined && (
            <Tag closable onClose={() => setFilters((current) => ({ ...current, maxSiteCount: undefined, page: 1 }))}>
              站点数 ≤ {filters.maxSiteCount}
            </Tag>
          )}
          {filters.missingSite && (
            <Tag closable onClose={() => setFilters((current) => ({ ...current, missingSite: undefined, page: 1 }))}>
              缺少站点：{filters.missingSite}
            </Tag>
          )}
          {filters.stale !== undefined && (
            <Tag closable onClose={() => setFilters((current) => ({ ...current, stale: undefined, page: 1 }))}>
              {filters.stale ? '需要刷新' : '快照新鲜'}
            </Tag>
          )}
        </div>
      </Card>

      <PageState
        loading={groups.isLoading}
        error={groups.error}
        onRetry={() => void groups.refetch()}
        empty={groups.data?.items.length === 0}
        emptyDescription="当前筛选条件下没有聚合任务。同步下载器后，任务会按内容指纹自动归组。"
      >
        {isMobile ? (
          <div className="group-mobile-view">
            <List<TorrentGroup>
              className="group-mobile-list"
              dataSource={groups.data?.items}
              renderItem={renderMobileGroup}
            />
            <Pagination
              simple
              current={groups.data?.page ?? filters.page}
              pageSize={groups.data?.pageSize ?? filters.pageSize}
              total={groups.data?.total ?? 0}
              onChange={(page, pageSize) => setFilters((current) => ({ ...current, page, pageSize }))}
            />
          </div>
        ) : (
          <Card className="table-card group-table-card">
            <Table<TorrentGroup>
              rowKey="id"
              columns={columns}
              dataSource={groups.data?.items}
              rowSelection={{
                selectedRowKeys: selectedGroupIds,
                onChange: (keys, rows) => {
                  setSelectedGroupIds(keys)
                  setSelectedGroups(rows)
                },
              }}
              expandable={{ expandedRowRender: expandedRow }}
              scroll={{ x: 1080 }}
              pagination={{
                current: groups.data?.page ?? filters.page,
                pageSize: groups.data?.pageSize ?? filters.pageSize,
                total: groups.data?.total ?? 0,
                showSizeChanger: true,
                showTotal: (total) => `共 ${total} 个任务组`,
                onChange: (page, pageSize) => setFilters((current) => ({ ...current, page, pageSize })),
              }}
            />
          </Card>
        )}
      </PageState>

      <GroupAdvancedSearchDrawer
        open={advancedSearchOpen}
        filters={filters}
        siteOptions={siteOptions}
        siteOptionsLoading={groupSiteOptions.isLoading}
        siteOptionsError={groupSiteOptions.isError}
        onRetrySiteOptions={() => void groupSiteOptions.refetch()}
        onClose={() => setAdvancedSearchOpen(false)}
        onApply={(advancedFilters) => setFilters((current) => ({ ...current, ...advancedFilters, page: 1 }))}
      />
      <GroupSortDrawer
        open={sortDrawerOpen}
        sorts={filters.sorts ?? []}
        onClose={() => setSortDrawerOpen(false)}
        onApply={(sorts) => {
          saveGroupSorts(sorts)
          setFilters((current) => ({ ...current, sorts, page: 1 }))
        }}
      />

      <Modal
        title="创建手动分组"
        open={mergeOpen}
        okText="合并分组"
        cancelText="取消"
        confirmLoading={mergeMutation.isPending}
        onCancel={() => setMergeOpen(false)}
        onOk={() => void mergeForm.validateFields().then((values) => mergeMutation.mutate({
          displayName: values.displayName,
          groups: selectedGroups.map((group) => ({ id: group.id, version: group.version })),
        }))}
      >
        <Alert type="info" showIcon message={`将合并 ${selectedGroupIds.length} 个任务组`} description="合并关系会作为手动分组持久化；如需禁止后续调整，可在列表中额外锁定。" />
        <Form form={mergeForm} layout="vertical" requiredMark={false} className="modal-form">
          <Form.Item name="displayName" label="分组名称" rules={[{ required: true, message: '请输入一个便于识别的名称' }, { max: 120 }]}>
            <Input placeholder="例如：Ubuntu 24.04 多站辅种" />
          </Form.Item>
        </Form>
      </Modal>

		<Modal
			title="移动任务到其他分组"
			open={Boolean(moveSelection)}
			okText="确认移动"
			cancelText="取消"
			confirmLoading={moveMutation.isPending}
			okButtonProps={{ disabled: !moveTargetId }}
			onCancel={() => {
				setMoveSelection(undefined)
				setMoveTargetId(undefined)
			}}
			onOk={() => {
				const target = moveTargets.data?.items.find((group) => group.id === moveTargetId)
				if (moveSelection && target) {
					moveMutation.mutate({
						source: moveSelection.sourceGroup,
						instanceId: moveSelection.instance.id,
						target,
					})
				}
			}}
		>
			<Space direction="vertical" size={16} className="modal-stack">
				<Alert
					type="info"
					showIcon
					message={moveSelection?.instance.name ?? '选择任务'}
					description="移动只改变逻辑 ContentGroup，不会合并或搬动物理 DataGroup。"
				/>
				<Select
					showSearch
					allowClear
					optionFilterProp="label"
					placeholder="选择目标分组"
					loading={moveTargets.isLoading}
					value={moveTargetId}
					options={(moveTargets.data?.items ?? [])
						.filter((group) => group.id !== moveSelection?.sourceGroup.id)
						.map((group) => ({ value: group.id, label: `${group.name} · v${group.version}` }))}
					onChange={setMoveTargetId}
				/>
				{moveTargets.error && <Alert type="error" showIcon message="无法加载目标分组" description={displayError(moveTargets.error)} />}
			</Space>
		</Modal>

      <Modal
        width={760}
        title="安全删除预览"
        open={Boolean(deleteGroup)}
        onCancel={closeDeleteModal}
        footer={
          deletePlan ? (
            <Space>
              <Button onClick={() => setDeletePlan(undefined)}>返回修改</Button>
              <Popconfirm
                title="确认执行这份删除计划？"
                description="服务端会再次校验版本和下载器快照；计划中的数据删除步骤不可撤销。"
                okText="确认执行"
                cancelText="取消"
                okButtonProps={{ danger: true }}
                onConfirm={() => jobMutation.mutate(deletePlan)}
              >
                <Button danger type="primary" disabled={!deletePlan.executable} loading={jobMutation.isPending}>执行删除计划</Button>
              </Popconfirm>
            </Space>
          ) : (
            <Space>
              <Button onClick={closeDeleteModal}>取消</Button>
              <Button type="primary" disabled={!deleteInstanceIds.length} loading={planMutation.isPending} onClick={generateDeletePlan}>
                生成影响预览
              </Button>
            </Space>
          )
        }
      >
        {deleteGroup && !deletePlan && (
          <Space direction="vertical" size={16} className="modal-stack">
            <Alert
              type="warning"
              showIcon
              message="先选择要移除的下载器实例"
              description="SeedGraph 会让服务端生成不可变的删除计划。只有确认预览后，才会提交实际删除任务。"
            />
            <Checkbox.Group value={deleteInstanceIds} onChange={(values) => setDeleteInstanceIds(values.map(String))} className="instance-checkboxes">
              {deleteGroup.instances.map((instance) => (
                <Checkbox key={instance.id} value={instance.id}>
                  <span><strong>{instance.downloaderName}</strong> · {instance.name}</span>
                  <small>{instance.savePath} · {formatPercent(instance.progress)}</small>
                </Checkbox>
              ))}
            </Checkbox.Group>
            <Typography.Text type="secondary">
              是否删除物理数据由服务端依据 DataGroup 引用计数决定，客户端不能绕过安全判断。
            </Typography.Text>
          </Space>
        )}

        {deletePlan && (
          <Space direction="vertical" size={16} className="modal-stack">
            <Alert
              type={deletePlan.executable ? 'success' : 'error'}
              showIcon
              message={deletePlan.executable ? '删除计划已通过安全检查' : '当前计划不可执行'}
              description={
                deletePlan.blockers.length
                  ? <DeletePlanBlockerDetails blockers={deletePlan.blockers} />
                  : '提交任务时服务端仍会重新校验版本、下载器在线状态和存储快照。'
              }
            />
            <Descriptions bordered size="small" column={{ xs: 1, sm: 2 }}>
              <Descriptions.Item label="选中实例">{deletePlan.selectedInstanceIds.length} 个</Descriptions.Item>
              <Descriptions.Item label="执行步骤">{deletePlan.steps.length} 步</Descriptions.Item>
              <Descriptions.Item label="删除物理数据">{deletePlan.steps.filter((step) => step.deleteData).length} 步</Descriptions.Item>
              <Descriptions.Item label="计划编号"><Typography.Text copyable>{deletePlan.id}</Typography.Text></Descriptions.Item>
            </Descriptions>
            <div>
              <Typography.Title level={5}><SafetyCertificateOutlined /> 有序执行步骤</Typography.Title>
              <List
                size="small"
                bordered
                dataSource={deletePlan.steps}
                locale={{ emptyText: '没有可执行的删除步骤' }}
                renderItem={(step) => (
                  <List.Item extra={step.deleteData ? <Tag color="error">删除数据</Tag> : <Tag>仅删任务</Tag>}>
                    <Space direction="vertical" size={0}>
                      <span>第 {step.order} 步 · {deleteGroup?.instances.find((item) => item.id === step.instanceId)?.name ?? step.instanceId}</span>
                      <Typography.Text type="secondary">下载器 {step.downloaderId} · DataGroup {step.dataGroupId}</Typography.Text>
                    </Space>
                  </List.Item>
                )}
              />
            </div>
          </Space>
        )}

        {(planMutation.error || jobMutation.error) && (
          <Alert className="modal-error" type="error" showIcon message="操作失败" description={displayError(planMutation.error || jobMutation.error)} />
        )}
      </Modal>
    </div>
  )
}
