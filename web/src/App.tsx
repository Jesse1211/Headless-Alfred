import { useAuth } from './features/auth/useAuth'
import LoginPage from './features/auth/LoginPage'
import { WorkspacePage } from './features/sessions/WorkspacePage'

export default function App() {
  const { token, isAuthenticated, login, logout } = useAuth()
  if (!isAuthenticated) {
    return <LoginPage onLogin={login} />
  }
  return <WorkspacePage token={token} onLogout={logout} />
}
