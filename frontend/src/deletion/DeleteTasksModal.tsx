import { useMutation, useQuery } from '@tanstack/react-query'
import { Alert, Button, Checkbox, List, Modal, Space, Tag, Typography } from 'antd'
import { useState } from 'react'
import { api, ApiError } from '../api/client'
import type { DeleteJob, DeletePlan, TorrentGroup } from '../api/types'
import { displayError, formatDeleteBlocker } from '../utils/format'

interface DeleteTasksModalProps {
  group: TorrentGroup
  initialInstanceIds: string[]
  onClose: () => void
  onSubmitted: (job: DeleteJob) => void | Promise<void>
}

const requiresNewPlan = (error: unknown) => error instanceof ApiError &&
  ['plan_expired', 'plan_changed', 'plan_blocked', 'conflict'].includes(error.code ?? '')

export function DeleteTasksModal({ group, initialInstanceIds, onClose, onSubmitted }: DeleteTasksModalProps) {
  const [instanceIds, setInstanceIds] = useState(initialInstanceIds)
  const [revision, setRevision] = useState(0)
  const preview = useQuery({
    queryKey: ['delete-plan', group.id, instanceIds, revision],
    queryFn: () => api.createDeletePlan({ groupId: group.id, instanceIds }),
    enabled: instanceIds.length > 0,
    staleTime: 0,
    gcTime: 0,
    retry: false,
    refetchOnMount: 'always',
    refetchOnWindowFocus: false,
    refetchOnReconnect: false,
  })
  const submission = useMutation({
    mutationFn: api.createDeleteJob,
    onSuccess: onSubmitted,
    onError: (error) => {
      // Refresh the impact automatically, but leave execution to a new user click.
      // Other failures retain the same plan and idempotency key for a safe retry.
      if (requiresNewPlan(error)) setRevision((current) => current + 1)
    },
  })
  const plan = preview.isError || preview.isFetching ? undefined : preview.data
  const deletesFiles = plan?.steps.some((step) => step.deleteData) ?? false
  const selectInstances = (ids: string[]) => {
    setInstanceIds(group.instances.filter((instance) => ids.includes(instance.id)).map((instance) => instance.id))
    submission.reset()
  }

  return (
    <Modal
      title="删除任务"
      width={560}
      open
      onCancel={() => { if (!submission.isPending) onClose() }}
      closable={!submission.isPending}
      maskClosable={!submission.isPending}
      keyboard={!submission.isPending}
      footer={
        <Space>
          <Button disabled={submission.isPending} onClick={onClose}>取消</Button>
          <Button danger type="primary"
            disabled={!instanceIds.length || !plan?.executable || preview.isFetching}
            loading={submission.isPending}
            onClick={() => { if (plan?.executable && !submission.isPending) submission.mutate(plan) }}
          >{deletesFiles ? '删除任务及文件' : '删除任务'}</Button>
        </Space>
      }
    >
      <Space direction="vertical" size={16} className="modal-stack">
        <Typography.Text strong className="delete-task-group-name">{group.name}</Typography.Text>
        {group.instances.length > 1 && (
          <Checkbox disabled={submission.isPending}
            checked={instanceIds.length === group.instances.length}
            indeterminate={instanceIds.length > 0 && instanceIds.length < group.instances.length}
            onChange={(event) => selectInstances(event.target.checked ? group.instances.map((instance) => instance.id) : [])}
          >全选 · 已选 {instanceIds.length}/{group.instances.length} 个任务</Checkbox>
        )}
        <Checkbox.Group disabled={submission.isPending} value={instanceIds} onChange={(values) => selectInstances(values.map(String))} className="delete-task-options">
          {group.instances.map((instance) => {
            const step = plan?.steps.find((item) => item.instanceId === instance.id)
            return (
              <Checkbox key={instance.id} value={instance.id} className="delete-task-option">
                <span className="delete-task-option-heading">
                  <strong>{instance.downloaderName}</strong>
                  {step && <Tag color={step.deleteData ? 'error' : 'default'}>{step.deleteData ? '删除文件' : '仅删任务'}</Tag>}
                </span>
                <span className="delete-task-option-name">{instance.name}</span>
                <small>{instance.savePath}</small>
              </Checkbox>
            )
          })}
        </Checkbox.Group>
        {!instanceIds.length ? (
          <Typography.Text type="secondary">请选择要删除的任务。</Typography.Text>
        ) : preview.isFetching ? (
          <Typography.Text type="secondary" role="status">正在检查文件影响…</Typography.Text>
        ) : preview.error ? (
          <Alert type="error" showIcon message="无法检查删除影响" description={displayError(preview.error)}
            action={<Button size="small" onClick={() => void preview.refetch()}>重试检查</Button>} />
        ) : plan && !plan.executable ? (
          <Alert type="error" showIcon message="当前任务无法删除" description={<DeletePlanBlockerDetails blockers={plan.blockers} />} />
        ) : plan && (
          <Alert type={deletesFiles ? 'warning' : 'info'} showIcon
            message={deletesFiles ? '将同时永久删除标出的文件，无法恢复。' : '仅移除选中的任务，保留文件。'} />
        )}
        {submission.error && (
          <Alert type={requiresNewPlan(submission.error) ? 'info' : 'error'} showIcon
            message={requiresNewPlan(submission.error) ? '任务状态已变化，请核对更新后的文件影响，再确认删除。' : displayError(submission.error)} />
        )}
      </Space>
    </Modal>
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
