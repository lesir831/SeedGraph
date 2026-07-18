import { Navigate, Route, Routes, useLocation } from 'react-router-dom'
import { Spin } from 'antd'
import { AppShell } from './components/AppShell'
import { useAuth } from './auth/auth-context'
import { LoginPage } from './pages/LoginPage'
import { OverviewPage } from './pages/OverviewPage'
import { TorrentGroupsPage } from './pages/TorrentGroupsPage'
import { DownloadersPage } from './pages/DownloadersPage'
import { TrackerRulesPage } from './pages/TrackerRulesPage'
import { SyncAuditPage } from './pages/SyncAuditPage'

function ProtectedLayout() {
  const { authenticated, checking } = useAuth()
  const location = useLocation()

  if (checking) {
    return (
      <div className="full-page-loading">
        <Spin size="large" tip="正在验证会话…">
          <div className="loading-placeholder" />
        </Spin>
      </div>
    )
  }

  if (!authenticated) return <Navigate to="/login" state={{ from: location }} replace />
  return <AppShell />
}

export function App() {
  return (
    <Routes>
      <Route path="/login" element={<LoginPage />} />
      <Route element={<ProtectedLayout />}>
        <Route index element={<OverviewPage />} />
        <Route path="groups" element={<TorrentGroupsPage />} />
        <Route path="downloaders" element={<DownloadersPage />} />
        <Route path="tracker-rules" element={<TrackerRulesPage />} />
        <Route path="sync-audit" element={<SyncAuditPage />} />
      </Route>
      <Route path="*" element={<Navigate to="/" replace />} />
    </Routes>
  )
}
