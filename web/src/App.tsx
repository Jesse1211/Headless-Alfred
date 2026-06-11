import { useAuth } from './features/auth/useAuth'
import LoginPage from './features/auth/LoginPage'
import TerminalPage from './features/terminal/TerminalPage'

export default function App() {
  const { token, isAuthenticated, login, logout } = useAuth()

  if (!isAuthenticated) {
    return <LoginPage onLogin={login} />
  }
  return <TerminalPage token={token} onLogout={logout} />
}
