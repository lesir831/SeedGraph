import dayjs from 'dayjs'

const BYTE_UNITS = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'] as const

export const formatBytes = (bytes: number, precision = 1): string => {
  if (!Number.isFinite(bytes) || bytes <= 0) return '0 B'
  const unitIndex = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), BYTE_UNITS.length - 1)
  const value = bytes / 1024 ** unitIndex
  const digits = value >= 100 || unitIndex === 0 ? 0 : precision
  return `${value.toFixed(digits)} ${BYTE_UNITS[unitIndex]}`
}

export const formatDateTime = (value?: string): string => {
  if (!value) return '—'
  const date = dayjs(value)
  return date.isValid() ? date.format('YYYY-MM-DD HH:mm:ss') : '—'
}

export const formatDuration = (milliseconds?: number): string => {
  if (milliseconds === undefined || milliseconds < 0 || !Number.isFinite(milliseconds)) return '—'
  if (milliseconds < 1000) return `${Math.round(milliseconds)} ms`
  const seconds = Math.round(milliseconds / 1000)
  if (seconds < 60) return `${seconds} 秒`
  const minutes = Math.floor(seconds / 60)
  const remainingSeconds = seconds % 60
  return remainingSeconds ? `${minutes} 分 ${remainingSeconds} 秒` : `${minutes} 分`
}

export const formatPercent = (ratio: number): string => {
  const normalized = ratio > 1 ? ratio : ratio * 100
  return `${Math.max(0, Math.min(normalized, 100)).toFixed(1)}%`
}

export const displayError = (error: unknown): string =>
  error instanceof Error ? error.message : '发生未知错误，请稍后重试。'

const DELETE_BLOCKER_MESSAGES: Record<string, string> = {
  no_selection: '没有选择要删除的任务实例',
  unknown_instance: '任务实例不存在或已发生变化',
  inactive_instance: '任务实例已不在下载器中',
  downloader_offline: '目标下载器当前离线',
  stale_data: '任务快照已过期，请先同步下载器',
  missing_content_group: '任务缺少内容分组，无法安全判断影响',
  missing_data_group: '任务缺少物理数据分组，无法安全判断影响',
  missing_expected_version: '缺少资源版本，请重新生成预览',
  version_conflict: '资源版本已经变化，请重新生成预览',
  unverified_data_group: '物理数据组尚未验证，禁止删除本地数据',
  file_manifest_missing: '文件路径清单缺失，请同步下载器后重新生成预览',
  conflicting_path_occupant: '待删除文件仍被未选中的其他任务引用',
  storage_snapshot_missing: '缺少存储快照，无法确认文件引用',
  storage_downloader_offline: '可访问同一存储的下载器处于离线状态',
  storage_snapshot_stale: '同一存储的下载器快照已过期',
  invalid_snapshot: '服务端快照不完整，请先同步后重试',
}

export const formatDeleteBlocker = (code: string, fallback: string): string =>
  DELETE_BLOCKER_MESSAGES[code] ?? fallback
