import type { DeleteJob } from '../api/types'

export const deletionStatusLabels: Record<DeleteJob['status'], string> = {
  pending: '等待执行',
  executing: '正在删除',
  verifying: '正在校验结果',
  completed: '删除完成',
  failed: '删除失败',
  uncertain: '结果待确认',
}

export function isDeleteJobTerminal(job: DeleteJob) {
  return ['completed', 'failed', 'uncertain'].includes(job.status)
}

export function deleteJobProgress(job: DeleteJob) {
  if (job.status === 'completed') return 100
  // Final verification is one required step; remote calls alone are not success.
  const completed = job.steps.filter((step) => step.status === 'completed').length
  return Math.floor(completed / (job.steps.length + 1) * 100)
}

const storageKey = 'seedgraph.delete-jobs'

export function loadTrackedJobIds(): string[] {
  try {
    const value: unknown = JSON.parse(sessionStorage.getItem(storageKey) ?? '[]')
    return Array.isArray(value)
      ? [...new Set(value.filter((id): id is string => typeof id === 'string' && id.length > 0))]
      : []
  } catch {
    return []
  }
}

export function saveTrackedJobIds(ids: string[]) {
  try {
    sessionStorage.setItem(storageKey, JSON.stringify(ids))
  } catch {
    // Tracking still works in memory when browser storage is unavailable.
  }
}
