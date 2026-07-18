import { LockOutlined, NodeIndexOutlined, SafetyCertificateOutlined, UserOutlined } from '@ant-design/icons'
import { Alert, Button, Card, Form, Input, Space, Typography } from 'antd'
import { useState } from 'react'
import { Navigate, useLocation, useNavigate } from 'react-router-dom'
import { useAuth } from '../auth/auth-context'
import type { LoginInput } from '../api/types'
import { displayError } from '../utils/format'

interface LocationState {
  from?: { pathname?: string }
}

export function LoginPage() {
  const [error, setError] = useState<unknown>()
  const [submitting, setSubmitting] = useState(false)
  const { authenticated, checking, login } = useAuth()
  const location = useLocation()
  const navigate = useNavigate()
  const from = (location.state as LocationState | null)?.from?.pathname || '/'

  const onFinish = async (values: LoginInput) => {
    setError(undefined)
    setSubmitting(true)
    try {
      await login(values)
      void navigate(from, { replace: true })
    } catch (nextError) {
      setError(nextError)
    } finally {
      setSubmitting(false)
    }
  }

  if (!checking && authenticated) return <Navigate to="/" replace />

  return (
    <main className="login-page">
      <section className="login-story" aria-label="产品介绍">
        <div className="login-brand"><NodeIndexOutlined /> SeedGraph</div>
        <div className="story-content">
          <Typography.Title>让散落的种子任务<br />重新连成一张图。</Typography.Title>
          <Typography.Paragraph>
            聚合 qBittorrent 与 Transmission，按保存路径、体积和文件清单识别相同内容，
            在真正删除前看清每一项影响。
          </Typography.Paragraph>
          <Space direction="vertical" size={16} className="story-points">
            <span><i>01</i> 跨下载器聚合与重复识别</span>
            <span><i>02</i> 可预览、可审计的安全操作</span>
            <span><i>03</i> 本地部署，数据留在自己的设备</span>
          </Space>
        </div>
        <div className="story-orbit" aria-hidden="true"><span /><span /><span /></div>
      </section>

      <section className="login-panel">
        <Card className="login-card" bordered={false}>
          <div className="login-icon"><NodeIndexOutlined /></div>
          <Typography.Title level={2}>登录控制台</Typography.Title>
          <Typography.Paragraph type="secondary">使用 SeedGraph 管理员账号继续</Typography.Paragraph>

          {error !== undefined && <Alert type="error" showIcon message="登录失败" description={displayError(error)} />}

          <Form<LoginInput>
            layout="vertical"
            requiredMark={false}
            size="large"
            onFinish={(values) => void onFinish(values)}
          >
            <Form.Item name="username" label="用户名" rules={[{ required: true, message: '请输入用户名' }]}>
              <Input prefix={<UserOutlined />} autoComplete="username" placeholder="admin" autoFocus />
            </Form.Item>
            <Form.Item name="password" label="密码" rules={[{ required: true, message: '请输入密码' }]}>
              <Input.Password prefix={<LockOutlined />} autoComplete="current-password" placeholder="输入管理员密码" />
            </Form.Item>
            <Button type="primary" htmlType="submit" block loading={checking || submitting}>登录</Button>
          </Form>
          <div className="login-security"><SafetyCertificateOutlined /> 会话通过同源 API 安全验证</div>
        </Card>
      </section>
    </main>
  )
}
