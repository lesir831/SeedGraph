import {
  ClearOutlined,
  DeleteOutlined,
  DisconnectOutlined,
  FileOutlined,
  FilterOutlined,
  LockOutlined,
  MergeCellsOutlined,
  ReloadOutlined,
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
import { useMemo, useState, type Key, type ReactNode } from 'react'
import { api } from '../api/client'
import { normalizePagedResponse } from '../api/transformers'
import type {
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
import { displayError, formatBytes, formatDateTime, formatOptionalBytes, formatRatio, formatRatioRange, formatSpeed } from '../utils/format'
import { countGroupQueryConditions, summarizeGroupQuery } from '../utils/groupQuery'
import { GROUP_SORT_LABELS, loadGroupSorts, saveGroupSorts } from '../utils/groupSortPreferences'
import { useDeletionTasks } from '../deletion/deletion-context'
import { DeleteTasksModal } from '../deletion/DeleteTasksModal'

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

function GroupMetadata({ group }: { group: TorrentGroup }) {
  const paths = group.paths ?? []
  return (
    <div className="group-resource-meta">
      <div className="group-resource-tags">
        <span>路径分类</span>
        {group.categories?.length
          ? group.categories.map((category) => <Tag key={category} color="cyan">{category}</Tag>)
          : <span>未分类</span>}
        {group.stale && <Tag color="warning">快照已过期</Tag>}
      </div>
      {!!group.downloaders?.length && <div className="group-downloader-names">{group.downloaders.join(' · ')}</div>}
      {!!paths.length && (
        <Tooltip title={<div>{paths.map((path) => <div key={path}>{path}</div>)}</div>} placement="topLeft">
          <div className="group-path-line"><span>{paths[0]}</span>{paths.length > 1 && <Tag>+{paths.length - 1} 路径</Tag>}</div>
        </Tooltip>
      )}
    </div>
  )
}

function GroupTransfer({ group }: { group: TorrentGroup }) {
  return (
    <div className="group-transfer-metrics">
      <div><span>上传</span><strong>{formatOptionalBytes(group.runtime?.uploadedBytes)}</strong></div>
      <div><span>下载</span><strong>{formatOptionalBytes(group.runtime?.downloadedBytes)}</strong></div>
      <div className="group-speed-line">↑ {formatSpeed(group.runtime?.uploadSpeed)} · ↓ {formatSpeed(group.runtime?.downloadSpeed)}</div>
    </div>
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

export function TorrentGroupsPage() {
  const { message } = App.useApp()
  const queryClient = useQueryClient()
  const { trackJob } = useDeletionTasks()
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
  const [deleteSelection, setDeleteSelection] = useState<{ group: TorrentGroup; instanceIds: string[] }>()
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
  const downloaderLabels = useMemo(() => new Map(
    (downloaders.data ?? []).map((downloader) => [downloader.id, downloader.name]),
  ), [downloaders.data])
  const advancedFilterCount = countGroupQueryConditions(filters.filter)
  const advancedFilterSummary = summarizeGroupQuery(filters.filter, {
    sites: siteLabels,
    downloaders: downloaderLabels,
  })

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

  const openDeleteModal = async (group: TorrentGroup, instanceId?: string) => {
    if (detailLoadingId || deleteSelection) return
    setDetailLoadingId(group.id)
    try {
      const detail = await queryClient.fetchQuery({
        queryKey: ['torrent-group', group.id],
        queryFn: () => api.getGroup(group.id),
        staleTime: 0,
      })
      const instanceIds = detail.instances
        .filter((instance) => !instanceId || instance.id === instanceId)
        .map((instance) => instance.id)
      if (!instanceIds.length) {
        void message.info('任务已不存在，请刷新列表')
        await invalidateGroups()
        return
      }
      setDeleteSelection({ group: detail, instanceIds })
    } catch (error) {
      void message.error(displayError(error))
    } finally {
      setDetailLoadingId(undefined)
    }
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
          <span>路径分类：{instance.category || '未分类'}</span>
          <Tooltip title={instance.savePath} placement="topLeft"><span>{instance.savePath || '—'}</span></Tooltip>
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
      render: formatRatio,
    },
    {
      title: '累计传输 / 当前速度',
      key: 'transfer',
      width: 230,
      render: (_, instance) => (
        <div className="group-transfer-metrics">
          <div><span>上传</span><strong>{formatOptionalBytes(instance.uploadedBytes)}</strong><span>{formatSpeed(instance.uploadSpeed)}</span></div>
          <div><span>下载</span><strong>{formatOptionalBytes(instance.downloadedBytes)}</strong><span>{formatSpeed(instance.downloadSpeed)}</span></div>
        </div>
      ),
    },
    {
      title: '添加时间',
      dataIndex: 'addedAt',
      width: 170,
      render: (_, instance) => (
        <div className="group-time-cell">
          <span>{formatDateTime(instance.addedAt)}</span>
          <Tooltip title="下载器最后成功同步时间"><small>同步 {formatDateTime(instance.lastSyncAt)}</small></Tooltip>
        </div>
      ),
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
          <Button type="text" danger size="small" aria-label="删除此任务" icon={<DeleteOutlined />} onClick={() => void openDeleteModal(group, instance.id)}>
            删除
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
        aria-label="删除任务组"
        onClick={() => void openDeleteModal(group)}
      >
        删除
      </Button>
    </Space>
  )

  const columns: TableColumnsType<TorrentGroup> = [
    {
      title: '聚合内容',
      key: 'name',
      width: 400,
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
          <GroupMetadata group={group} />
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
      title: <Tooltip title="同组任务的有效分享率范围；单个任务显示其分享率">分享率</Tooltip>,
      key: 'ratio',
      width: 130,
      render: (_, group) => <strong className="group-metric-value">{formatRatioRange(group.runtime?.ratioMin, group.runtime?.ratioMax)}</strong>,
    },
    {
      title: <Tooltip title="同组任务累计上传、下载量及最近同步时的合计速度">累计传输 / 当前速度</Tooltip>,
      key: 'transfer',
      width: 235,
      render: (_, group) => <GroupTransfer group={group} />,
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
              scroll={{ x: 1510 }}
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
        <span>分享率 {formatRatio(instance.ratio)}</span>
        <span>添加 {formatDateTime(instance.addedAt)}</span>
      </div>
      <Descriptions size="small" column={1} className="mobile-instance-details">
        <Descriptions.Item label="路径分类">{instance.category || '未分类'}</Descriptions.Item>
        <Descriptions.Item label="路径"><span className="instance-path">{instance.savePath || '—'}</span></Descriptions.Item>
        <Descriptions.Item label="累计上传">{formatOptionalBytes(instance.uploadedBytes)} · {formatSpeed(instance.uploadSpeed)}</Descriptions.Item>
        <Descriptions.Item label="累计下载">{formatOptionalBytes(instance.downloadedBytes)} · {formatSpeed(instance.downloadSpeed)}</Descriptions.Item>
        <Descriptions.Item label="最后同步">{formatDateTime(instance.lastSyncAt)}</Descriptions.Item>
      </Descriptions>
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
        <Button type="text" danger size="small" aria-label="删除此任务" icon={<DeleteOutlined />} onClick={() => void openDeleteModal(group, instance.id)}>
          删除
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
          <GroupMetadata group={group} />
          <div className="group-mobile-metrics">
            <div><span>大小</span><strong>{formatBytes(group.totalSize)}</strong></div>
            <div><span>实例</span><strong>{group.taskCount}</strong></div>
            <div><span>分享率</span><strong>{formatRatioRange(group.runtime?.ratioMin, group.runtime?.ratioMax)}</strong></div>
            <div className="group-mobile-added"><span>最旧添加时间</span><strong>{formatDateTime(group.oldestAddedAt)}</strong></div>
          </div>
          <GroupTransfer group={group} />
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
          {advancedFilterCount > 0 && (
            <div className="advanced-filter-summary">
              <Tag
                color="blue"
                closable
                onClose={() => setFilters((current) => ({ ...current, filter: undefined, page: 1 }))}
              >
                高级条件 {advancedFilterCount} 条
              </Tag>
              <Tooltip title={advancedFilterSummary} placement="bottomLeft">
                <Typography.Text ellipsis>{advancedFilterSummary}</Typography.Text>
              </Tooltip>
            </div>
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
              scroll={{ x: 1380 }}
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
        filter={filters.filter}
        siteOptions={siteOptions}
        siteOptionsLoading={groupSiteOptions.isLoading}
        siteOptionsError={groupSiteOptions.isError}
        downloaders={downloaders.data ?? []}
        downloadersLoading={downloaders.isLoading}
        onRetrySiteOptions={() => void groupSiteOptions.refetch()}
        onClose={() => setAdvancedSearchOpen(false)}
        onApply={(filter) => setFilters((current) => ({ ...current, filter, page: 1 }))}
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

      {deleteSelection && (
        <DeleteTasksModal
          group={deleteSelection.group}
          initialInstanceIds={deleteSelection.instanceIds}
          onClose={() => setDeleteSelection(undefined)}
          onSubmitted={async (job) => {
            trackJob(job)
            setDeleteSelection(undefined)
            await invalidateGroups()
          }}
        />
      )}
    </div>
  )
}
