import {
  ApiOutlined,
  CheckCircleOutlined,
  CloudServerOutlined,
  DeleteOutlined,
  MinusCircleOutlined,
  PlusOutlined,
  ReloadOutlined,
  SyncOutlined,
} from '@ant-design/icons'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  Alert,
  App,
  Button,
  Card,
  Col,
  Form,
  Input,
  Modal,
  Popconfirm,
  Row,
  Select,
  Space,
  Switch,
  Tag,
  Typography,
} from 'antd'
import { useState } from 'react'
import { api } from '../api/client'
import type { Downloader, DownloaderInput } from '../api/types'
import { PageHeader } from '../components/PageHeader'
import { PageState } from '../components/PageState'
import { HealthTag } from '../components/StatusTag'
import { displayError, formatDateTime } from '../utils/format'

const initialValues: DownloaderInput = {
  name: '',
  kind: 'qbittorrent',
  baseUrl: '',
  username: '',
  password: '',
  storageId: undefined,
  storageName: '',
  pathMappings: [],
  enabled: true,
}

export function DownloadersPage() {
  const { message } = App.useApp()
  const queryClient = useQueryClient()
  const [addOpen, setAddOpen] = useState(false)
  const [form] = Form.useForm<DownloaderInput>()

  const downloaders = useQuery({ queryKey: ['downloaders'], queryFn: api.getDownloaders })
  const storageOptions = Array.from(
    new Map((downloaders.data ?? []).map((item) => [item.storageId, item])).values(),
  ).map((item) => ({ value: item.storageId, label: `${item.storageName} · ${item.storageId.slice(0, 8)}` }))

  const createMutation = useMutation({
    mutationFn: api.createDownloader,
    onSuccess: async (downloader) => {
      void message.success(`已添加 ${downloader.name}`)
      setAddOpen(false)
      form.resetFields()
      await queryClient.invalidateQueries({ queryKey: ['downloaders'] })
    },
    onError: (error) => void message.error(displayError(error)),
  })

  const testMutation = useMutation({
    mutationFn: api.testDownloader,
    onSuccess: (result) => {
      if (result.ok) {
        void message.success(`连接成功${result.version ? ` · ${result.version}` : ''}${result.latencyMs ? ` · ${result.latencyMs} ms` : ''}`)
      } else {
        void message.warning(result.message)
      }
    },
    onError: (error) => void message.error(displayError(error)),
  })

  const deleteMutation = useMutation({
    mutationFn: api.deleteDownloader,
    onSuccess: async () => {
      void message.success('下载器配置已删除')
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['downloaders'] }),
        queryClient.invalidateQueries({ queryKey: ['overview'] }),
      ])
    },
    onError: (error) => void message.error(displayError(error)),
  })

  const syncMutation = useMutation({
    mutationFn: api.syncDownloader,
    onSuccess: async () => {
      void message.success('同步任务已启动')
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['downloaders'] }),
        queryClient.invalidateQueries({ queryKey: ['sync-status'] }),
      ])
    },
    onError: (error) => void message.error(displayError(error)),
  })

  const card = (downloader: Downloader) => (
    <Card
      key={downloader.id}
      className="downloader-card"
      title={
        <Space>
          <span className={`client-mark ${downloader.kind}`}>
            {downloader.kind === 'qbittorrent' ? 'qB' : 'Tr'}
          </span>
          <span>{downloader.name}</span>
        </Space>
      }
      extra={<HealthTag status={downloader.health} />}
      actions={[
        <Button
          key="test"
          type="text"
          icon={<ApiOutlined />}
          loading={testMutation.isPending && testMutation.variables === downloader.id}
          onClick={() => testMutation.mutate(downloader.id)}
        >
          测试连接
        </Button>,
        <Button
          key="sync"
          type="text"
          icon={<SyncOutlined spin={syncMutation.isPending && syncMutation.variables === downloader.id} />}
          loading={syncMutation.isPending && syncMutation.variables === downloader.id}
          onClick={() => syncMutation.mutate(downloader.id)}
        >
          立即同步
        </Button>,
        <Popconfirm
          key="delete"
          title="删除下载器配置？"
          description="这不会直接删除客户端中的任务或本地文件，但 SeedGraph 内的关联记录将被移除。"
          okText="删除配置"
          cancelText="取消"
          okButtonProps={{ danger: true }}
          onConfirm={() => deleteMutation.mutate(downloader.id)}
        >
          <Button
            type="text"
            danger
            icon={<DeleteOutlined />}
            loading={deleteMutation.isPending && deleteMutation.variables === downloader.id}
          >
            删除
          </Button>
        </Popconfirm>,
      ]}
    >
      <Space direction="vertical" size={14} className="card-content-full">
        <div className="endpoint-line">
          <Typography.Text type="secondary">接口地址</Typography.Text>
          <Typography.Text copyable={{ text: downloader.baseUrl }} ellipsis>{downloader.baseUrl}</Typography.Text>
        </div>
        <Row gutter={12}>
          <Col span={12}>
            <div className="small-stat"><strong>{downloader.pathMappings.length}</strong><span>路径映射</span></div>
          </Col>
          <Col span={12}>
            <div className="small-stat"><strong>{downloader.version || '—'}</strong><span>客户端版本</span></div>
          </Col>
        </Row>
        <div className="sync-note">
          <span>最近同步</span>
          <Typography.Text>{formatDateTime(downloader.lastSyncAt)}</Typography.Text>
        </div>
        <div className="sync-note">
          <span>存储标识</span>
          <Typography.Text>{downloader.storageName}</Typography.Text>
        </div>
        {downloader.pathMappings.length > 0 && (
          <div className="mapping-summary">
            {downloader.pathMappings.map((mapping) => (
              <Typography.Text code key={mapping.id}>{mapping.sourcePrefix} → {mapping.targetPrefix}</Typography.Text>
            ))}
          </div>
        )}
        {downloader.lastError && <Alert type="error" showIcon message={downloader.lastError} />}
        {!downloader.enabled && <Tag>已停用</Tag>}
      </Space>
    </Card>
  )

  return (
    <div className="page-stack">
      <PageHeader
        title="下载器管理"
        description="连接 qBittorrent 和 Transmission；凭据只发送给本机 SeedGraph API。"
        extra={
          <>
            <Button icon={<ReloadOutlined spin={downloaders.isFetching} />} onClick={() => void downloaders.refetch()}>刷新</Button>
            <Button type="primary" icon={<PlusOutlined />} onClick={() => setAddOpen(true)}>添加下载器</Button>
          </>
        }
      />

      <PageState
        loading={downloaders.isLoading}
        error={downloaders.error}
        onRetry={() => void downloaders.refetch()}
        empty={downloaders.data?.length === 0}
        emptyDescription={
          <Space direction="vertical">
            <span>尚未添加下载器。</span>
            <Button type="primary" icon={<PlusOutlined />} onClick={() => setAddOpen(true)}>添加第一个下载器</Button>
          </Space>
        }
      >
        <Row gutter={[16, 16]}>
          {(downloaders.data ?? []).map((downloader) => (
            <Col xs={24} md={12} xl={8} key={downloader.id}>{card(downloader)}</Col>
          ))}
        </Row>
      </PageState>

      <Card className="hint-card">
        <Space align="start">
          <CheckCircleOutlined />
          <div>
            <strong>连接前请确认</strong>
            <Typography.Paragraph type="secondary">
              下载器 Web UI 已启用且 SeedGraph 容器可以访问；建议创建权限受限的专用账号，并避免把管理接口直接暴露到公网。
            </Typography.Paragraph>
          </div>
        </Space>
      </Card>

      <Modal
        title="添加下载器"
        open={addOpen}
        okText="保存下载器"
        cancelText="取消"
        confirmLoading={createMutation.isPending}
        onCancel={() => {
          setAddOpen(false)
          createMutation.reset()
        }}
        onOk={() => void form.validateFields().then((values) => createMutation.mutate(values))}
      >
        <Form form={form} layout="vertical" requiredMark={false} initialValues={initialValues} className="modal-form">
          <Form.Item name="name" label="显示名称" rules={[{ required: true, message: '请输入显示名称' }, { max: 80 }]}>
            <Input prefix={<CloudServerOutlined />} placeholder="例如：NAS qBittorrent" />
          </Form.Item>
          <Form.Item name="kind" label="客户端类型" rules={[{ required: true }]}>
            <Select
              options={[
                { value: 'qbittorrent', label: 'qBittorrent' },
                { value: 'transmission', label: 'Transmission' },
              ]}
            />
          </Form.Item>
          <Form.Item
            name="baseUrl"
            label="Web API 地址"
            rules={[{ required: true, message: '请输入接口地址' }, { type: 'url', message: '请输入完整的 http(s) 地址' }]}
            extra="请填写 SeedGraph 服务端可访问的地址，例如 http://qbittorrent:8080"
          >
            <Input placeholder="http://192.168.1.20:8080" />
          </Form.Item>
          <Row gutter={12}>
            <Col span={12}>
              <Form.Item name="username" label="用户名" rules={[{ required: true, message: '请输入用户名' }]}>
                <Input autoComplete="off" />
              </Form.Item>
            </Col>
            <Col span={12}>
              <Form.Item name="password" label="密码" rules={[{ required: true, message: '请输入密码' }]}>
                <Input.Password autoComplete="new-password" />
              </Form.Item>
            </Col>
          </Row>
          <Form.Item
            name="storageId"
            label="复用已有物理存储（可选）"
            extra="如果两个下载器能看到同一份磁盘数据，必须在这里选择同一个存储；仅填写相同名称不会建立共享关系。"
          >
            <Select
              allowClear
              placeholder="新建独立存储"
              options={storageOptions}
              onChange={(storageId?: string) => {
                const existing = (downloaders.data ?? []).find((item) => item.storageId === storageId)
                if (existing) form.setFieldValue('storageName', existing.storageName)
              }}
            />
          </Form.Item>
          <Form.Item
            name="storageName"
            label="物理存储名称"
            rules={[{ required: true, message: '请输入物理存储名称' }]}
            extra="用于管理员识别；新建存储时填写，复用已有存储时会自动带入。"
          >
            <Input placeholder="例如：NAS-volume-1" />
          </Form.Item>
          <Form.List name="pathMappings">
            {(fields, { add, remove }) => (
              <Space direction="vertical" size={8} className="card-content-full">
                <div className="form-list-header">
                  <span>路径映射（可选）</span>
                  <Button type="dashed" size="small" icon={<PlusOutlined />} onClick={() => add({ sourcePrefix: '', targetPrefix: '' })}>
                    添加映射
                  </Button>
                </div>
                {fields.map((field) => (
                  <Space key={field.key} align="start" className="mapping-row">
                    <Form.Item
                      {...field}
                      name={[field.name, 'sourcePrefix']}
                      rules={[{ required: true, message: '请输入下载器路径' }]}
                    >
                      <Input placeholder="下载器路径 /downloads" />
                    </Form.Item>
                    <Form.Item
                      {...field}
                      name={[field.name, 'targetPrefix']}
                      rules={[{ required: true, message: '请输入规范路径' }]}
                    >
                      <Input placeholder="规范路径 /data" />
                    </Form.Item>
                    <Button type="text" danger aria-label="删除路径映射" icon={<MinusCircleOutlined />} onClick={() => remove(field.name)} />
                  </Space>
                ))}
              </Space>
            )}
          </Form.List>
          <Form.Item name="enabled" label="保存后启用" valuePropName="checked">
            <Switch />
          </Form.Item>
          {createMutation.error && <Alert type="error" showIcon message="保存失败" description={displayError(createMutation.error)} />}
        </Form>
      </Modal>
    </div>
  )
}
