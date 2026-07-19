import {
  ApartmentOutlined,
  CloudServerOutlined,
  DashboardOutlined,
  HistoryOutlined,
  LogoutOutlined,
  MenuOutlined,
  NodeIndexOutlined,
  SafetyCertificateOutlined,
} from '@ant-design/icons'
import { Avatar, Button, Drawer, Dropdown, Grid, Layout, Menu, Space, Typography, type MenuProps } from 'antd'
import { useMemo, useState } from 'react'
import { Outlet, useLocation, useNavigate } from 'react-router-dom'
import { useAuth } from '../auth/auth-context'

const { Header, Sider, Content } = Layout

const navigationItems: NonNullable<MenuProps['items']> = [
  { key: '/', icon: <DashboardOutlined />, label: '总览' },
  { key: '/groups', icon: <ApartmentOutlined />, label: '聚合任务' },
  { key: '/downloaders', icon: <CloudServerOutlined />, label: '下载器' },
  { key: '/tracker-rules', icon: <NodeIndexOutlined />, label: 'Tracker 规则' },
  { key: '/sync-audit', icon: <HistoryOutlined />, label: '同步与审计' },
]

const routeTitles: Record<string, string> = {
  '/': '运行总览',
  '/groups': '聚合任务',
  '/downloaders': '下载器管理',
  '/tracker-rules': 'Tracker 规则',
  '/sync-audit': '同步与审计',
}

function Brand() {
  return (
    <div className="brand">
      <span className="brand-mark"><img src="/seedgraph-icon.png" alt="" /></span>
      <div>
        <strong>SeedGraph</strong>
        <span>种子关系控制台</span>
      </div>
    </div>
  )
}

export function AppShell() {
  const navigate = useNavigate()
  const location = useLocation()
  const screens = Grid.useBreakpoint()
  const desktop = screens.lg ?? false
  const [drawerOpen, setDrawerOpen] = useState(false)
  const { username, logout } = useAuth()

  const selectedKey = useMemo(() => {
    const match = navigationItems.find((item) => item && 'key' in item && item.key === location.pathname)
    return String(match && 'key' in match ? match.key : '/')
  }, [location.pathname])

  const onMenuClick: MenuProps['onClick'] = ({ key }) => {
    void navigate(key)
    setDrawerOpen(false)
  }

  const userMenu: MenuProps['items'] = [
    {
      key: 'logout',
      icon: <LogoutOutlined />,
      danger: true,
      label: '退出登录',
      onClick: () => void logout(),
    },
  ]

  const menu = (
    <Menu
      theme="dark"
      mode="inline"
      selectedKeys={[selectedKey]}
      items={navigationItems}
      onClick={onMenuClick}
    />
  )

  return (
    <Layout className="app-layout">
      {desktop ? (
        <Sider width={248} className="app-sider">
          <Brand />
          <nav aria-label="主导航">{menu}</nav>
          <div className="sider-security"><SafetyCertificateOutlined /> 单用户安全模式</div>
        </Sider>
      ) : (
        <Drawer
          placement="left"
          width={276}
          open={drawerOpen}
          onClose={() => setDrawerOpen(false)}
          styles={{ body: { padding: 0, background: '#0b1325' }, header: { display: 'none' } }}
        >
          <Brand />
          {menu}
        </Drawer>
      )}

      <Layout>
        <Header className="app-header">
          <Space size={12}>
            {!desktop && (
              <Button
                type="text"
                icon={<MenuOutlined />}
                aria-label="打开导航"
                onClick={() => setDrawerOpen(true)}
              />
            )}
            <Typography.Text className="header-title">{routeTitles[location.pathname] ?? 'SeedGraph'}</Typography.Text>
          </Space>
          <Dropdown menu={{ items: userMenu }} placement="bottomRight" trigger={['click']}>
            <Button type="text" className="user-button">
              <Avatar size={30}>{(username || 'A').slice(0, 1).toUpperCase()}</Avatar>
              {desktop && <span>{username || '管理员'}</span>}
            </Button>
          </Dropdown>
        </Header>
        <Content className="app-content">
          <Outlet />
        </Content>
      </Layout>
    </Layout>
  )
}
