import type { ReactNode } from 'react'
import { Space, Typography } from 'antd'

interface PageHeaderProps {
  title: string
  description: string
  extra?: ReactNode
}

export function PageHeader({ title, description, extra }: PageHeaderProps) {
  return (
    <div className="page-header">
      <div>
        <Typography.Title level={2}>{title}</Typography.Title>
        <Typography.Text type="secondary">{description}</Typography.Text>
      </div>
      {extra && <Space wrap>{extra}</Space>}
    </div>
  )
}
