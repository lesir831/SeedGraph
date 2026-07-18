import { Badge, Tag } from 'antd'
import type { HealthStatus, OperationStatus } from '../api/types'

const HEALTH_MAP: Record<HealthStatus, { color: string; label: string }> = {
  online: { color: 'success', label: '在线' },
  offline: { color: 'error', label: '离线' },
  degraded: { color: 'warning', label: '异常' },
  unknown: { color: 'default', label: '未知' },
}

const OPERATION_MAP: Record<OperationStatus, { status: 'success' | 'error' | 'warning' | 'processing' | 'default'; label: string }> = {
  success: { status: 'success', label: '成功' },
  failed: { status: 'error', label: '失败' },
  warning: { status: 'warning', label: '有警告' },
  running: { status: 'processing', label: '进行中' },
  idle: { status: 'default', label: '空闲' },
}

export function HealthTag({ status }: { status: HealthStatus }) {
  const item = HEALTH_MAP[status] ?? HEALTH_MAP.unknown
  return <Tag color={item.color}>{item.label}</Tag>
}

export function OperationBadge({ status }: { status: OperationStatus }) {
  const item = OPERATION_MAP[status] ?? OPERATION_MAP.idle
  return <Badge status={item.status} text={item.label} />
}
