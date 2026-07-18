import {
  ApartmentOutlined,
  CheckCircleOutlined,
  CloudServerOutlined,
  DatabaseOutlined,
  ExclamationCircleOutlined,
  ReloadOutlined,
} from '@ant-design/icons'
import { useQuery } from '@tanstack/react-query'
import { Button, Card, Col, Progress, Row, Space, Statistic, Tag, Typography } from 'antd'
import { useNavigate } from 'react-router-dom'
import { api } from '../api/client'
import { PageHeader } from '../components/PageHeader'
import { PageState } from '../components/PageState'
import { OperationBadge } from '../components/StatusTag'
import { formatBytes, formatDateTime } from '../utils/format'

export function OverviewPage() {
  const navigate = useNavigate()
  const overview = useQuery({
    queryKey: ['overview'],
    queryFn: api.getOverview,
    refetchInterval: 30_000,
  })
  const syncStatus = useQuery({
    queryKey: ['sync-status'],
    queryFn: api.getSyncStatus,
    refetchInterval: 30_000,
  })

  return (
    <div className="page-stack">
      <PageHeader
        title="运行总览"
        description="下载器连接、任务聚合和同步状态一览。"
        extra={
          <Button
            icon={<ReloadOutlined spin={overview.isFetching || syncStatus.isFetching} />}
            onClick={() => void Promise.all([overview.refetch(), syncStatus.refetch()])}
          >
            刷新
          </Button>
        }
      />

      <PageState loading={overview.isLoading} error={overview.error} onRetry={() => void overview.refetch()}>
        {overview.data && (
          <>
            <Row gutter={[16, 16]}>
              <Col xs={24} sm={12} xl={6}>
                <Card className="metric-card">
                  <Statistic title="下载器在线" value={overview.data.onlineDownloaderCount} suffix={`/ ${overview.data.downloaderCount}`} prefix={<CloudServerOutlined />} />
                  <Progress
                    percent={overview.data.downloaderCount ? Math.round((overview.data.onlineDownloaderCount / overview.data.downloaderCount) * 100) : 0}
                    showInfo={false}
                    strokeColor="#16a34a"
                    size="small"
                  />
                </Card>
              </Col>
              <Col xs={24} sm={12} xl={6}>
                <Card className="metric-card">
                  <Statistic title="聚合任务组" value={overview.data.groupCount} prefix={<ApartmentOutlined />} />
                  <Typography.Text type="secondary">共 {overview.data.instanceCount} 个下载器实例</Typography.Text>
                </Card>
              </Col>
              <Col xs={24} sm={12} xl={6}>
                <Card className="metric-card metric-card-warn">
                  <Statistic title="额外任务实例" value={overview.data.duplicateGroupCount} prefix={<DatabaseOutlined />} />
                  <Typography.Text type="secondary">重复计量 {formatBytes(overview.data.reclaimableBytes)}，删除以安全计划为准</Typography.Text>
                </Card>
              </Col>
              <Col xs={24} sm={12} xl={6}>
                <Card className="metric-card">
                  <Statistic title="待刷新任务组" value={overview.data.recentErrorCount} prefix={<ExclamationCircleOutlined />} />
                  <Typography.Text type="secondary">下载器快照超过新鲜度阈值的任务组</Typography.Text>
                </Card>
              </Col>
            </Row>

            <Row gutter={[16, 16]}>
              <Col xs={24} lg={15}>
                <Card title="运行健康度" className="overview-health">
                  <div className="health-row">
                    <Space><span className="health-icon success"><CheckCircleOutlined /></span><div><strong>同步服务</strong><small>定时拉取下载器任务并更新聚合关系</small></div></Space>
                    <OperationBadge status={syncStatus.data?.status ?? overview.data.syncStatus} />
                  </div>
                  <div className="health-row">
                    <Space><span className="health-icon"><CloudServerOutlined /></span><div><strong>下载器连接</strong><small>{overview.data.onlineDownloaderCount} 个在线，{overview.data.downloaderCount - overview.data.onlineDownloaderCount} 个需要检查</small></div></Space>
                    <Tag color={overview.data.onlineDownloaderCount === overview.data.downloaderCount ? 'success' : 'warning'}>
                      {overview.data.onlineDownloaderCount === overview.data.downloaderCount ? '全部正常' : '部分异常'}
                    </Tag>
                  </div>
                  <div className="health-row">
                    <Space><span className="health-icon"><ReloadOutlined /></span><div><strong>最近一次同步</strong><small>完成后的数据会自动刷新到各个页面</small></div></Space>
                    <Typography.Text>{formatDateTime(syncStatus.data?.completedAt)}</Typography.Text>
                  </div>
                </Card>
              </Col>
              <Col xs={24} lg={9}>
                <Card title="快速操作" className="quick-actions">
                  <Button block icon={<ApartmentOutlined />} onClick={() => void navigate('/groups')}>查看重复任务</Button>
                  <Button block icon={<CloudServerOutlined />} onClick={() => void navigate('/downloaders')}>管理下载器</Button>
                  <Button block icon={<ReloadOutlined />} onClick={() => void navigate('/sync-audit')}>同步与审计记录</Button>
                </Card>
              </Col>
            </Row>
          </>
        )}
      </PageState>
    </div>
  )
}
