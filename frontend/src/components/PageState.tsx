import type { PropsWithChildren, ReactNode } from 'react'
import { Alert, Button, Empty, Skeleton, Space } from 'antd'
import { ReloadOutlined } from '@ant-design/icons'
import { displayError } from '../utils/format'

interface PageStateProps extends PropsWithChildren {
  loading: boolean
  error?: unknown
  empty?: boolean
  onRetry?: () => void
  emptyTitle?: string
  emptyDescription?: ReactNode
  skeletonRows?: number
}

export function PageState({
  loading,
  error,
  empty = false,
  onRetry,
  emptyTitle = '暂无数据',
  emptyDescription,
  skeletonRows = 5,
  children,
}: PageStateProps) {
  if (loading) {
    return <Skeleton active paragraph={{ rows: skeletonRows }} title={{ width: '36%' }} />
  }

  if (error) {
    return (
      <Alert
        type="error"
        showIcon
        message="数据加载失败"
        description={
          <Space direction="vertical" size={12}>
            <span>{displayError(error)}</span>
            {onRetry && (
              <Button icon={<ReloadOutlined />} onClick={onRetry}>
                重新加载
              </Button>
            )}
          </Space>
        }
      />
    )
  }

  if (empty) {
    return <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={emptyDescription ?? emptyTitle} />
  }

  return children
}
