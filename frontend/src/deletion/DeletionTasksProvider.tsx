import { CloseOutlined, MinusOutlined, ReloadOutlined } from '@ant-design/icons'
import { useQueries, useQueryClient } from '@tanstack/react-query'
import { Alert, Button, Collapse, Progress, Space, Tag, Typography } from 'antd'
import { useCallback, useEffect, useMemo, useRef, useState, type PropsWithChildren } from 'react'
import { api, ApiError } from '../api/client'
import type { DeleteJob } from '../api/types'
import { displayError } from '../utils/format'
import { DeletionContext } from './deletion-context'
import { deleteJobProgress, deletionStatusLabels, isDeleteJobTerminal, loadTrackedJobIds, saveTrackedJobIds } from './progress'

export function DeletionTasksProvider({ children }: PropsWithChildren) {
  const queryClient = useQueryClient()
  const [jobIds, setJobIds] = useState(loadTrackedJobIds)
  const [expanded, setExpanded] = useState(true)
  const observedTerminals = useRef(new Set<string>())
  const queries = useQueries({
    queries: jobIds.map((id) => ({
      queryKey: ['delete-job', id],
      queryFn: () => api.getDeleteJob(id),
      refetchInterval: (query: { state: { data?: DeleteJob; error: Error | null } }) =>
        (query.state.data && isDeleteJobTerminal(query.state.data)) ||
        (query.state.error instanceof ApiError && query.state.error.status === 404) ? false : 2_000,
      retry: false,
      staleTime: 0,
      refetchOnWindowFocus: true,
    })),
  })

  useEffect(() => saveTrackedJobIds(jobIds), [jobIds])

  useEffect(() => {
    for (const query of queries) {
      const job = query.data
      if (!job || !isDeleteJobTerminal(job) || observedTerminals.current.has(job.id)) continue
      observedTerminals.current.add(job.id)
      for (const key of ['torrent-groups', 'torrent-group', 'overview', 'audit-events']) {
        void queryClient.invalidateQueries({ queryKey: [key] })
      }
    }
  }, [queries, queryClient])

  const trackJob = useCallback((job: DeleteJob) => {
    queryClient.setQueryData(['delete-job', job.id], job)
    setJobIds((ids) => ids.includes(job.id) ? ids : [...ids, job.id])
    setExpanded(true)
  }, [queryClient])
  const context = useMemo(() => ({ trackJob }), [trackJob])
  const activeCount = queries.filter((query) => !query.data || !isDeleteJobTerminal(query.data)).length
  const hasError = queries.some((query) => query.error || ['failed', 'uncertain'].includes(query.data?.status ?? ''))
  const percent = jobIds.length
    ? Math.floor(queries.reduce((sum, query) => sum + (query.data ? deleteJobProgress(query.data) : 0), 0) / jobIds.length)
    : 0
  const progressStatus = hasError ? 'exception' : activeCount ? 'normal' : 'success'

  return (
    <DeletionContext.Provider value={context}>
      {children}
      {jobIds.length > 0 && (
        <aside className={`deletion-widget${expanded ? '' : ' deletion-widget-collapsed'}`} aria-label="删除任务进度">
          {expanded ? (
            <>
              <div className="deletion-widget-header">
                <div><strong>删除任务</strong><Typography.Text type="secondary">{activeCount ? `${activeCount} 项进行中` : '执行已结束'}</Typography.Text></div>
                <Progress type="circle" percent={percent} size={48} status={progressStatus} />
                <Button type="text" icon={<MinusOutlined />} aria-label="收起删除进度" onClick={() => setExpanded(false)} />
              </div>
              <div className="deletion-widget-body">
                {queries.map((query, index) => {
                  const job = query.data
                  const missing = query.error instanceof ApiError && query.error.status === 404
                  return (
                    <section className="deletion-job" key={jobIds[index]}>
                      <div className="deletion-job-heading">
                        <Space wrap>
                          <Tag color={job?.status === 'completed' ? 'success' : job?.status === 'failed' ? 'error' : job?.status === 'uncertain' ? 'warning' : 'processing'}>
                            {query.error ? '进度获取失败' : job ? deletionStatusLabels[job.status] : '正在获取进度'}
                          </Tag>
                          <Typography.Text type="secondary">{jobIds[index].slice(0, 8)}</Typography.Text>
                        </Space>
                        {((job && isDeleteJobTerminal(job)) || missing) && (
                          <Button type="text" size="small" icon={<CloseOutlined />} aria-label="移除已结束任务" onClick={() => setJobIds((ids) => ids.filter((id) => id !== jobIds[index]))} />
                        )}
                      </div>
                      {query.error && <Alert type="warning" showIcon message={displayError(query.error)} action={<Button size="small" icon={<ReloadOutlined />} onClick={() => void query.refetch()}>重试查询</Button>} />}
                      {job && (
                        <>
                          <Typography.Text type="secondary">已完成 {job.steps.filter((step) => step.status === 'completed').length}/{job.steps.length} 个删除步骤 · {job.status === 'completed' ? '校验通过' : '含最终结果校验'}</Typography.Text>
                          {(job.status === 'failed' || job.status === 'uncertain') && (
                            <Alert type={job.status === 'uncertain' ? 'warning' : 'error'} showIcon
                              message={job.status === 'uncertain' ? '请先同步并核对下载器中的任务与文件，再决定后续操作。' : '执行已停止，请检查原因并重新生成预览。'}
                              description={job.error} />
                          )}
                          <Collapse ghost size="small" items={[{
                            key: 'steps', label: '查看执行步骤', children: <ol className="deletion-step-list">{job.steps.map((step) => (
                              <li key={step.id}>
                                <span>{step.deleteData ? '删除任务和文件' : '仅删除任务'} · {deletionStatusLabels[step.status]}</span>
                                {step.error && <Typography.Text type="danger">{step.error}</Typography.Text>}
                              </li>
                            ))}</ol>,
                          }]} />
                        </>
                      )}
                    </section>
                  )
                })}
              </div>
            </>
          ) : (
            <button className="deletion-widget-toggle" aria-label="展开删除进度" onClick={() => setExpanded(true)}>
              <Progress type="circle" percent={percent} size={48} status={progressStatus} />
              <span>删除{activeCount ? ` · ${activeCount}` : hasError ? ' · 需处理' : '完成'}</span>
            </button>
          )}
        </aside>
      )}
    </DeletionContext.Provider>
  )
}
