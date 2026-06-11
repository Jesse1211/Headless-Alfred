# Headless Alfred — Plan 3: Frontend

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the React UI per spec §11 (split-screen, white-screen non-terminal aesthetic). Frontend dev server proxies API/WS to the local Go backend from Plan 2.

**Architecture:** Vite + React 18 + TypeScript. No router library (only two pages: `/login` and `/`; gate by token presence). Plain CSS (no Tailwind/UI framework — kept minimal per "白屏化"). Two `lib/` modules wrap fetch and WebSocket. Two features: `auth/` (login + token) and `terminal/` (the split-screen view + WS hook).

**Tech Stack:** Node 20+, Vite 5, React 18, TypeScript 5, vitest, @testing-library/react.

**Spec sections covered:** §11 (UI behavior), §6 (consumes API + WS as documented).

**Depends on:** Plan 2 running locally on `:8080` for dev.

---

## File Structure

```
web/
├── package.json
├── tsconfig.json
├── tsconfig.node.json
├── vite.config.ts
├── vitest.config.ts
├── index.html
├── public/
│   └── favicon.svg
└── src/
    ├── main.tsx
    ├── App.tsx                  # gate: token? terminal : login
    ├── index.css                # global styles, root layout
    ├── lib/
    │   ├── api.ts               # fetch wrapper, token injection, 401 hook
    │   └── ws.ts                # reconnecting WS with typed messages
    ├── features/
    │   ├── auth/
    │   │   ├── LoginPage.tsx
    │   │   ├── LoginPage.css
    │   │   ├── useAuth.ts       # token in localStorage, login/logout
    │   │   └── useAuth.test.ts
    │   └── terminal/
    │       ├── TerminalPage.tsx
    │       ├── TerminalPage.css
    │       ├── HistoryList.tsx
    │       ├── OutputView.tsx
    │       ├── CommandInput.tsx
    │       ├── useShell.ts      # state machine over WS messages
    │       ├── useShell.test.ts
    │       └── types.ts         # shared types
```

---

## Task 1: Scaffold Vite + React + TS

**Files:**
- Create: `web/package.json`
- Create: `web/tsconfig.json`
- Create: `web/tsconfig.node.json`
- Create: `web/vite.config.ts`
- Create: `web/vitest.config.ts`
- Create: `web/index.html`
- Create: `web/src/main.tsx`
- Create: `web/src/App.tsx`
- Create: `web/src/index.css`

- [ ] **Step 1.1: Create package.json**

Create `web/package.json`:
```json
{
  "name": "headless-alfred-web",
  "private": true,
  "version": "0.0.0",
  "type": "module",
  "scripts": {
    "dev": "vite",
    "build": "tsc -b && vite build",
    "preview": "vite preview",
    "test": "vitest run",
    "test:watch": "vitest"
  },
  "dependencies": {
    "react": "^18.3.0",
    "react-dom": "^18.3.0"
  },
  "devDependencies": {
    "@testing-library/jest-dom": "^6.4.0",
    "@testing-library/react": "^16.0.0",
    "@types/react": "^18.3.0",
    "@types/react-dom": "^18.3.0",
    "@vitejs/plugin-react": "^4.3.0",
    "jsdom": "^25.0.0",
    "typescript": "^5.5.0",
    "vite": "^5.4.0",
    "vitest": "^2.1.0"
  }
}
```

- [ ] **Step 1.2: Install deps**

```bash
cd web && npm install
```

Expected: `node_modules/` populated; `package-lock.json` written.

- [ ] **Step 1.3: Create tsconfig files**

Create `web/tsconfig.json`:
```json
{
  "compilerOptions": {
    "target": "ES2022",
    "useDefineForClassFields": true,
    "lib": ["ES2022", "DOM", "DOM.Iterable"],
    "module": "ESNext",
    "skipLibCheck": true,
    "moduleResolution": "Bundler",
    "allowImportingTsExtensions": false,
    "resolveJsonModule": true,
    "isolatedModules": true,
    "noEmit": true,
    "jsx": "react-jsx",
    "strict": true,
    "noUnusedLocals": true,
    "noUnusedParameters": true,
    "noFallthroughCasesInSwitch": true,
    "types": ["vitest/globals", "@testing-library/jest-dom"]
  },
  "include": ["src"],
  "references": [{ "path": "./tsconfig.node.json" }]
}
```

Create `web/tsconfig.node.json`:
```json
{
  "compilerOptions": {
    "composite": true,
    "skipLibCheck": true,
    "module": "ESNext",
    "moduleResolution": "Bundler",
    "allowSyntheticDefaultImports": true,
    "strict": true
  },
  "include": ["vite.config.ts", "vitest.config.ts"]
}
```

- [ ] **Step 1.4: Create vite.config.ts**

Create `web/vite.config.ts`:
```ts
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// Frontend dev server proxies API + WS to the local backend.
// In production the Go binary serves the built dist/ directly.
export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: {
      '/api': 'http://localhost:8080',
      '/ws': {
        target: 'ws://localhost:8080',
        ws: true,
      },
    },
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
  },
})
```

- [ ] **Step 1.5: Create vitest.config.ts**

Create `web/vitest.config.ts`:
```ts
import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  test: {
    environment: 'jsdom',
    globals: true,
    setupFiles: ['./src/test-setup.ts'],
  },
})
```

Create `web/src/test-setup.ts`:
```ts
import '@testing-library/jest-dom'
```

- [ ] **Step 1.6: Create index.html and entry**

Create `web/index.html`:
```html
<!doctype html>
<html lang="en">
  <head>
    <meta charset="UTF-8" />
    <link rel="icon" type="image/svg+xml" href="/favicon.svg" />
    <meta name="viewport" content="width=device-width, initial-scale=1.0" />
    <title>Headless Alfred</title>
  </head>
  <body>
    <div id="root"></div>
    <script type="module" src="/src/main.tsx"></script>
  </body>
</html>
```

Create `web/public/favicon.svg`:
```xml
<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 32 32"><rect width="32" height="32" rx="6" fill="#1a1a2e"/><text x="16" y="22" font-family="ui-monospace,SFMono-Regular,Menlo,monospace" font-size="18" fill="#fff" text-anchor="middle">A</text></svg>
```

Create `web/src/main.tsx`:
```tsx
import React from 'react'
import ReactDOM from 'react-dom/client'
import App from './App'
import './index.css'

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
)
```

Create `web/src/index.css`:
```css
:root {
  --bg: #f7f7f8;
  --surface: #ffffff;
  --border: #e5e7eb;
  --text: #111827;
  --text-muted: #6b7280;
  --accent: #2563eb;
  --accent-fg: #ffffff;
  --error: #dc2626;
  --error-bg: #fef2f2;
  --warn: #d97706;
  --ok: #16a34a;
  font-family: ui-sans-serif, system-ui, -apple-system, "Segoe UI", Roboto, "Helvetica Neue", sans-serif;
  color: var(--text);
  background: var(--bg);
}

* {
  box-sizing: border-box;
}

html, body, #root {
  height: 100%;
  margin: 0;
}

button {
  font: inherit;
  cursor: pointer;
}

input, textarea {
  font: inherit;
}

code, pre {
  font-family: ui-monospace, SFMono-Regular, "SF Mono", Menlo, monospace;
}
```

- [ ] **Step 1.7: Stub App.tsx**

Create `web/src/App.tsx`:
```tsx
export default function App() {
  return <div style={{ padding: 24 }}>Headless Alfred — wip</div>
}
```

- [ ] **Step 1.8: Verify dev server starts**

```bash
cd web && npm run dev
```

Expected: Vite prints "Local: http://localhost:5173". Open it in a browser; see "Headless Alfred — wip". Kill with Ctrl+C.

- [ ] **Step 1.9: Verify build**

```bash
cd web && npm run build
```

Expected: produces `web/dist/index.html` and assets.

- [ ] **Step 1.10: Commit**

```bash
git add web/
git commit -m "feat(web): scaffold Vite + React + TS"
```

---

## Task 2: lib/api.ts — fetch wrapper

**Files:**
- Create: `web/src/lib/api.ts`

- [ ] **Step 2.1: Implement api.ts**

Create `web/src/lib/api.ts`:
```ts
// Shared fetch wrapper. Reads token from localStorage; on 401 fires a hook
// the auth feature can register to flush local state and route back to login.

let on401: (() => void) | null = null

export function setOn401(fn: () => void) {
  on401 = fn
}

function token(): string {
  return localStorage.getItem('alfred_token') ?? ''
}

export class ApiError extends Error {
  status: number
  code: string
  constructor(status: number, code: string, message: string) {
    super(message)
    this.status = status
    this.code = code
  }
}

async function request(path: string, init: RequestInit = {}, includeAuth = true): Promise<Response> {
  const headers = new Headers(init.headers)
  if (init.body && !headers.has('Content-Type')) {
    headers.set('Content-Type', 'application/json')
  }
  if (includeAuth) {
    const t = token()
    if (t) headers.set('Authorization', `Bearer ${t}`)
  }
  const res = await fetch(path, { ...init, headers })
  if (res.status === 401 && includeAuth) {
    on401?.()
  }
  if (!res.ok) {
    let code = 'error'
    let msg = res.statusText
    try {
      const j = await res.clone().json()
      code = j.code ?? code
      msg = j.message ?? msg
    } catch {
      // ignore
    }
    throw new ApiError(res.status, code, msg)
  }
  return res
}

export async function login(user: string, password: string): Promise<{ token: string }> {
  const res = await request('/api/login', {
    method: 'POST',
    body: JSON.stringify({ user, password }),
  }, /* includeAuth */ false)
  return res.json()
}

export interface CommandSummary {
  id: string
  command: string
  cwd?: string
  started_at: string
  finished_at?: string
  exit_code?: number
  status: 'running' | 'completed' | 'interrupted' | 'stopped'
  output_truncated: boolean
}

export interface CommandFull extends CommandSummary {
  output: string
}

export async function listCommands(opts: { limit?: number; before?: string } = {}): Promise<CommandSummary[]> {
  const qs = new URLSearchParams()
  if (opts.limit != null) qs.set('limit', String(opts.limit))
  if (opts.before) qs.set('before', opts.before)
  const res = await request(`/api/commands${qs.size ? '?' + qs.toString() : ''}`)
  return res.json()
}

export async function getCommand(id: string): Promise<CommandFull> {
  const res = await request(`/api/commands/${encodeURIComponent(id)}`)
  return res.json()
}

export async function stopCommand(id: string): Promise<void> {
  await request(`/api/commands/${encodeURIComponent(id)}/stop`, { method: 'POST' })
}
```

- [ ] **Step 2.2: Type-check**

```bash
cd web && npx tsc -b
```

Expected: no errors.

- [ ] **Step 2.3: Commit**

```bash
git add web/src/lib/api.ts
git commit -m "feat(web/lib): typed fetch wrapper with 401 hook"
```

---

## Task 3: lib/ws.ts — reconnecting WebSocket

**Files:**
- Create: `web/src/lib/ws.ts`

- [ ] **Step 3.1: Implement ws.ts**

Create `web/src/lib/ws.ts`:
```ts
// Server-to-client message variants (matches backend internal/api/ws.go).
export type ServerMsg =
  | { type: 'reattach'; cmdId: string; command: string; startedAt: string; outputSoFar: string }
  | { type: 'idle' }
  | { type: 'started'; cmdId: string; command: string; startedAt: string }
  | { type: 'chunk'; cmdId: string; data: string }
  | { type: 'done'; cmdId: string; exitCode: number; finishedAt: string }
  | { type: 'error'; code: string; message: string }
  | { type: 'pong' }

export type ClientMsg =
  | { type: 'run'; command: string }
  | { type: 'ping' }

export type ConnState = 'connecting' | 'open' | 'reconnecting' | 'closed'

export interface ShellSocketOptions {
  url: string
  getToken: () => string
  onMessage: (m: ServerMsg) => void
  onState: (s: ConnState) => void
}

const DELAYS = [1000, 2000, 4000, 8000, 16000, 30000]

export class ShellSocket {
  private opts: ShellSocketOptions
  private ws: WebSocket | null = null
  private retry = 0
  private stopped = false
  private pingTimer: number | null = null

  constructor(opts: ShellSocketOptions) {
    this.opts = opts
  }

  start(): void {
    this.stopped = false
    this.connect()
  }

  stop(): void {
    this.stopped = true
    if (this.pingTimer != null) {
      window.clearInterval(this.pingTimer)
      this.pingTimer = null
    }
    this.ws?.close()
    this.ws = null
    this.opts.onState('closed')
  }

  send(m: ClientMsg): void {
    if (this.ws?.readyState === WebSocket.OPEN) {
      this.ws.send(JSON.stringify(m))
    }
  }

  private connect(): void {
    if (this.stopped) return
    this.opts.onState(this.retry === 0 ? 'connecting' : 'reconnecting')
    const token = this.opts.getToken()
    const sep = this.opts.url.includes('?') ? '&' : '?'
    const url = `${this.opts.url}${sep}token=${encodeURIComponent(token)}`
    const ws = new WebSocket(url)
    this.ws = ws

    ws.addEventListener('open', () => {
      this.retry = 0
      this.opts.onState('open')
      this.pingTimer = window.setInterval(() => {
        this.send({ type: 'ping' })
      }, 25_000) as unknown as number
    })

    ws.addEventListener('message', (ev) => {
      try {
        const m = JSON.parse(ev.data) as ServerMsg
        this.opts.onMessage(m)
      } catch {
        // ignore malformed
      }
    })

    ws.addEventListener('close', () => {
      if (this.pingTimer != null) {
        window.clearInterval(this.pingTimer)
        this.pingTimer = null
      }
      if (this.stopped) return
      const delay = DELAYS[Math.min(this.retry, DELAYS.length - 1)]
      this.retry += 1
      this.opts.onState('reconnecting')
      window.setTimeout(() => this.connect(), delay)
    })

    ws.addEventListener('error', () => {
      // Close handler will follow.
    })
  }
}
```

- [ ] **Step 3.2: Type-check**

```bash
cd web && npx tsc -b
```

Expected: no errors.

- [ ] **Step 3.3: Commit**

```bash
git add web/src/lib/ws.ts
git commit -m "feat(web/lib): reconnecting WebSocket with typed messages"
```

---

## Task 4: Auth feature — useAuth + LoginPage

**Files:**
- Create: `web/src/features/auth/useAuth.ts`
- Create: `web/src/features/auth/useAuth.test.ts`
- Create: `web/src/features/auth/LoginPage.tsx`
- Create: `web/src/features/auth/LoginPage.css`

- [ ] **Step 4.1: Write useAuth.test.ts**

Create `web/src/features/auth/useAuth.test.ts`:
```ts
import { renderHook, act } from '@testing-library/react'
import { useAuth } from './useAuth'
import { describe, it, expect, beforeEach, vi } from 'vitest'
import * as api from '../../lib/api'

describe('useAuth', () => {
  beforeEach(() => {
    localStorage.clear()
  })

  it('returns no token initially', () => {
    const { result } = renderHook(() => useAuth())
    expect(result.current.token).toBe('')
    expect(result.current.isAuthenticated).toBe(false)
  })

  it('login stores the token and flips isAuthenticated', async () => {
    vi.spyOn(api, 'login').mockResolvedValueOnce({ token: 'TOK' })
    const { result } = renderHook(() => useAuth())
    await act(async () => {
      await result.current.login('admin', 'pw')
    })
    expect(result.current.token).toBe('TOK')
    expect(result.current.isAuthenticated).toBe(true)
    expect(localStorage.getItem('alfred_token')).toBe('TOK')
  })

  it('logout clears the token', () => {
    localStorage.setItem('alfred_token', 'TOK')
    const { result } = renderHook(() => useAuth())
    act(() => {
      result.current.logout()
    })
    expect(result.current.token).toBe('')
    expect(localStorage.getItem('alfred_token')).toBeNull()
  })
})
```

- [ ] **Step 4.2: Implement useAuth.ts**

Create `web/src/features/auth/useAuth.ts`:
```ts
import { useCallback, useEffect, useState } from 'react'
import { login as apiLogin, setOn401 } from '../../lib/api'

const KEY = 'alfred_token'

export function useAuth() {
  const [token, setToken] = useState<string>(() => localStorage.getItem(KEY) ?? '')

  useEffect(() => {
    setOn401(() => {
      localStorage.removeItem(KEY)
      setToken('')
    })
  }, [])

  const login = useCallback(async (user: string, password: string) => {
    const { token } = await apiLogin(user, password)
    localStorage.setItem(KEY, token)
    setToken(token)
  }, [])

  const logout = useCallback(() => {
    localStorage.removeItem(KEY)
    setToken('')
  }, [])

  return { token, isAuthenticated: token.length > 0, login, logout }
}
```

- [ ] **Step 4.3: Run useAuth tests**

```bash
cd web && npx vitest run features/auth/useAuth.test.ts
```

Expected: 3 tests PASS.

- [ ] **Step 4.4: Implement LoginPage**

Create `web/src/features/auth/LoginPage.tsx`:
```tsx
import { FormEvent, useState } from 'react'
import { ApiError } from '../../lib/api'
import './LoginPage.css'

interface Props {
  onLogin: (user: string, password: string) => Promise<void>
}

export default function LoginPage({ onLogin }: Props) {
  const [user, setUser] = useState('')
  const [password, setPassword] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)

  async function submit(e: FormEvent) {
    e.preventDefault()
    setError(null)
    setSubmitting(true)
    try {
      await onLogin(user, password)
    } catch (err) {
      if (err instanceof ApiError) {
        setError(err.status === 429 ? 'Too many attempts, please wait a minute.' : 'Wrong username or password.')
      } else {
        setError('Network error.')
      }
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <div className="login-page">
      <form className="login-card" onSubmit={submit}>
        <h1>Headless Alfred</h1>
        <label>
          <span>Username</span>
          <input
            autoFocus
            value={user}
            onChange={(e) => setUser(e.target.value)}
            disabled={submitting}
            autoComplete="username"
          />
        </label>
        <label>
          <span>Password</span>
          <input
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            disabled={submitting}
            autoComplete="current-password"
          />
        </label>
        {error && <div className="login-error">{error}</div>}
        <button type="submit" disabled={submitting || !user || !password}>
          {submitting ? 'Signing in…' : 'Sign in'}
        </button>
      </form>
    </div>
  )
}
```

Create `web/src/features/auth/LoginPage.css`:
```css
.login-page {
  min-height: 100vh;
  display: grid;
  place-items: center;
  background: var(--bg);
}

.login-card {
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: 12px;
  padding: 32px;
  width: min(360px, 90vw);
  display: flex;
  flex-direction: column;
  gap: 16px;
  box-shadow: 0 1px 3px rgba(0, 0, 0, 0.05);
}

.login-card h1 {
  margin: 0 0 8px;
  font-size: 20px;
  font-weight: 600;
}

.login-card label {
  display: flex;
  flex-direction: column;
  gap: 4px;
  font-size: 14px;
  color: var(--text-muted);
}

.login-card input {
  padding: 10px 12px;
  border: 1px solid var(--border);
  border-radius: 8px;
  background: var(--surface);
  color: var(--text);
  font-size: 14px;
}

.login-card input:focus {
  outline: none;
  border-color: var(--accent);
}

.login-error {
  color: var(--error);
  background: var(--error-bg);
  padding: 8px 12px;
  border-radius: 8px;
  font-size: 13px;
}

.login-card button {
  background: var(--accent);
  color: var(--accent-fg);
  border: none;
  padding: 10px 16px;
  border-radius: 8px;
  font-size: 14px;
  font-weight: 500;
}

.login-card button:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
```

- [ ] **Step 4.5: Commit**

```bash
git add web/src/features/auth/
git commit -m "feat(web/auth): useAuth hook and LoginPage"
```

---

## Task 5: Terminal feature — useShell hook

**Files:**
- Create: `web/src/features/terminal/types.ts`
- Create: `web/src/features/terminal/useShell.ts`
- Create: `web/src/features/terminal/useShell.test.ts`

The hook owns the WS connection state. It exposes:
- `connState`
- `running?: { id, command, output }` (built up from chunks)
- `submit(cmd)` — sends `run`
- `stop(id)` — calls REST stop
- Most history is fetched via REST, not via WS (separation).

- [ ] **Step 5.1: Create types.ts**

Create `web/src/features/terminal/types.ts`:
```ts
export interface RunningCmd {
  id: string
  command: string
  startedAt: string
  output: string
  truncatedLossWarned: boolean
}
```

- [ ] **Step 5.2: Write useShell.test.ts**

Create `web/src/features/terminal/useShell.test.ts`:
```ts
import { renderHook, act, waitFor } from '@testing-library/react'
import { useShell } from './useShell'
import { describe, it, expect, beforeEach, vi } from 'vitest'

const sendMock = vi.fn()

let onMessage: ((m: any) => void) | null = null
let onState: ((s: any) => void) | null = null

vi.mock('../../lib/ws', () => {
  return {
    ShellSocket: vi.fn().mockImplementation((opts: any) => {
      onMessage = opts.onMessage
      onState = opts.onState
      return {
        start: vi.fn(() => onState!('open')),
        stop: vi.fn(),
        send: sendMock,
      }
    }),
  }
})

function b64(s: string): string {
  return Buffer.from(s, 'utf8').toString('base64')
}

describe('useShell', () => {
  beforeEach(() => {
    sendMock.mockClear()
    onMessage = null
    onState = null
  })

  it('starts in connecting then open', async () => {
    const { result } = renderHook(() => useShell('TOK'))
    await waitFor(() => expect(result.current.connState).toBe('open'))
  })

  it('handles idle message: running is null, idle=true', async () => {
    const { result } = renderHook(() => useShell('TOK'))
    await waitFor(() => expect(onMessage).not.toBeNull())
    act(() => onMessage!({ type: 'idle' }))
    expect(result.current.running).toBeNull()
    expect(result.current.idle).toBe(true)
  })

  it('handles started + chunk + done', async () => {
    const { result } = renderHook(() => useShell('TOK'))
    await waitFor(() => expect(onMessage).not.toBeNull())
    act(() => onMessage!({ type: 'started', cmdId: 'X', command: 'ls', startedAt: '2026-06-11T00:00:00Z' }))
    expect(result.current.running?.id).toBe('X')

    act(() => onMessage!({ type: 'chunk', cmdId: 'X', data: b64('hello\n') }))
    expect(result.current.running?.output).toBe('hello\n')

    act(() => onMessage!({ type: 'chunk', cmdId: 'X', data: b64('world\n') }))
    expect(result.current.running?.output).toBe('hello\nworld\n')

    act(() => onMessage!({ type: 'done', cmdId: 'X', exitCode: 0, finishedAt: '2026-06-11T00:00:01Z' }))
    expect(result.current.running).toBeNull()
    expect(result.current.idle).toBe(true)
  })

  it('handles reattach', async () => {
    const { result } = renderHook(() => useShell('TOK'))
    await waitFor(() => expect(onMessage).not.toBeNull())
    act(() =>
      onMessage!({
        type: 'reattach',
        cmdId: 'Y',
        command: 'train',
        startedAt: '2026-06-11T00:00:00Z',
        outputSoFar: b64('epoch 1\n'),
      }),
    )
    expect(result.current.running?.id).toBe('Y')
    expect(result.current.running?.output).toBe('epoch 1\n')
  })

  it('submit calls send', async () => {
    const { result } = renderHook(() => useShell('TOK'))
    await waitFor(() => expect(onMessage).not.toBeNull())
    act(() => onMessage!({ type: 'idle' }))
    act(() => result.current.submit('ls -la'))
    expect(sendMock).toHaveBeenCalledWith({ type: 'run', command: 'ls -la' })
  })

  it('busy error sets lastError', async () => {
    const { result } = renderHook(() => useShell('TOK'))
    await waitFor(() => expect(onMessage).not.toBeNull())
    act(() => onMessage!({ type: 'error', code: 'busy', message: 'shell busy' }))
    expect(result.current.lastError?.code).toBe('busy')
  })
})
```

- [ ] **Step 5.3: Implement useShell.ts**

Create `web/src/features/terminal/useShell.ts`:
```ts
import { useEffect, useMemo, useRef, useState, useCallback } from 'react'
import { ShellSocket, ServerMsg, ConnState } from '../../lib/ws'
import { stopCommand } from '../../lib/api'
import { RunningCmd } from './types'

function b64decode(s: string): string {
  if (typeof atob === 'function') {
    try {
      // Use TextDecoder to handle UTF-8 correctly.
      const bin = atob(s)
      const bytes = new Uint8Array(bin.length)
      for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i)
      return new TextDecoder().decode(bytes)
    } catch {
      return ''
    }
  }
  return Buffer.from(s, 'base64').toString('utf8')
}

export function useShell(token: string) {
  const [connState, setConnState] = useState<ConnState>('connecting')
  const [running, setRunning] = useState<RunningCmd | null>(null)
  const [idle, setIdle] = useState(false)
  const [lastError, setLastError] = useState<{ code: string; message: string } | null>(null)
  const [historyVersion, setHistoryVersion] = useState(0)

  const tokenRef = useRef(token)
  tokenRef.current = token

  const socket = useMemo(
    () =>
      new ShellSocket({
        url: location.protocol === 'https:' ? `wss://${location.host}/ws` : `ws://${location.host}/ws`,
        getToken: () => tokenRef.current,
        onState: setConnState,
        onMessage: (m: ServerMsg) => {
          switch (m.type) {
            case 'idle':
              setRunning(null)
              setIdle(true)
              break
            case 'reattach':
              setRunning({
                id: m.cmdId,
                command: m.command,
                startedAt: m.startedAt,
                output: b64decode(m.outputSoFar),
                truncatedLossWarned: false,
              })
              setIdle(false)
              break
            case 'started':
              setRunning({
                id: m.cmdId,
                command: m.command,
                startedAt: m.startedAt,
                output: '',
                truncatedLossWarned: false,
              })
              setIdle(false)
              break
            case 'chunk':
              setRunning((cur) => {
                if (!cur || cur.id !== m.cmdId) return cur
                return { ...cur, output: cur.output + b64decode(m.data) }
              })
              break
            case 'done':
              setRunning(null)
              setIdle(true)
              setHistoryVersion((v) => v + 1)
              break
            case 'error':
              setLastError({ code: m.code, message: m.message })
              break
            case 'pong':
              break
          }
        },
      }),
    // Create the socket once per mount.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [],
  )

  useEffect(() => {
    socket.start()
    return () => socket.stop()
  }, [socket])

  const submit = useCallback(
    (command: string) => {
      setLastError(null)
      socket.send({ type: 'run', command })
    },
    [socket],
  )

  const stop = useCallback(async (id: string) => {
    try {
      await stopCommand(id)
    } catch {
      // ignore; UI will see error toast separately if it matters
    }
  }, [])

  const clearError = useCallback(() => setLastError(null), [])

  return { connState, running, idle, lastError, clearError, submit, stop, historyVersion }
}
```

- [ ] **Step 5.4: Run useShell tests**

```bash
cd web && npx vitest run features/terminal/useShell.test.ts
```

Expected: 6 tests PASS.

- [ ] **Step 5.5: Commit**

```bash
git add web/src/features/terminal/useShell.ts web/src/features/terminal/useShell.test.ts web/src/features/terminal/types.ts
git commit -m "feat(web/terminal): useShell state machine"
```

---

## Task 6: Terminal layout — HistoryList, OutputView, CommandInput, TerminalPage

**Files:**
- Create: `web/src/features/terminal/HistoryList.tsx`
- Create: `web/src/features/terminal/OutputView.tsx`
- Create: `web/src/features/terminal/CommandInput.tsx`
- Create: `web/src/features/terminal/TerminalPage.tsx`
- Create: `web/src/features/terminal/TerminalPage.css`

- [ ] **Step 6.1: Implement HistoryList.tsx**

Create `web/src/features/terminal/HistoryList.tsx`:
```tsx
import { useEffect, useState } from 'react'
import { listCommands, CommandSummary, ApiError } from '../../lib/api'

interface Props {
  selectedId: string | null
  runningId: string | null
  onSelect: (id: string) => void
  refreshTrigger: number
}

export default function HistoryList({ selectedId, runningId, onSelect, refreshTrigger }: Props) {
  const [items, setItems] = useState<CommandSummary[]>([])
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    let alive = true
    listCommands({ limit: 100 })
      .then((rows) => {
        if (alive) {
          setItems(rows)
          setError(null)
        }
      })
      .catch((e) => {
        if (alive) setError(e instanceof ApiError ? e.message : 'failed to load history')
      })
    return () => {
      alive = false
    }
  }, [refreshTrigger])

  return (
    <aside className="history-list">
      <header className="history-list__header">History</header>
      {error && <div className="history-list__error">{error}</div>}
      <ul>
        {items.map((it) => {
          const isRunning = it.id === runningId
          const isSelected = it.id === selectedId
          const interactive = !runningId
          return (
            <li
              key={it.id}
              className={`history-list__item ${isSelected ? 'is-selected' : ''} ${isRunning ? 'is-running' : ''} ${interactive ? '' : 'is-locked'}`}
              onClick={() => interactive && onSelect(it.id)}
              role="button"
              tabIndex={interactive ? 0 : -1}
            >
              <div className="history-list__cmd">{it.command || '(empty)'}</div>
              <div className="history-list__meta">
                {it.status}
                {it.exit_code != null && it.exit_code !== 0 ? ` · exit ${it.exit_code}` : ''}
              </div>
            </li>
          )
        })}
        {items.length === 0 && !error && (
          <li className="history-list__empty">No commands yet.</li>
        )}
      </ul>
    </aside>
  )
}
```

- [ ] **Step 6.2: Implement OutputView.tsx**

Create `web/src/features/terminal/OutputView.tsx`:
```tsx
import { useEffect, useRef } from 'react'

interface Props {
  command: string | null
  output: string
  isLive: boolean
  exitCode?: number
  truncated?: boolean
}

export default function OutputView({ command, output, isLive, exitCode, truncated }: Props) {
  const preRef = useRef<HTMLPreElement>(null)

  useEffect(() => {
    if (preRef.current) {
      preRef.current.scrollTop = preRef.current.scrollHeight
    }
  }, [output])

  return (
    <section className="output-view">
      {command != null && (
        <div className="output-view__header">
          <code>{command || '(empty)'}</code>
          {isLive && <span className="output-view__live">● live</span>}
          {!isLive && exitCode != null && (
            <span className={`output-view__exit ${exitCode === 0 ? 'is-ok' : 'is-err'}`}>
              exit {exitCode}
            </span>
          )}
          {truncated && <span className="output-view__warn">output truncated</span>}
        </div>
      )}
      <pre ref={preRef} className="output-view__body">{output}</pre>
    </section>
  )
}
```

- [ ] **Step 6.3: Implement CommandInput.tsx**

Create `web/src/features/terminal/CommandInput.tsx`:
```tsx
import { FormEvent, KeyboardEvent, useState } from 'react'

interface Props {
  disabled: boolean
  busy: boolean
  onSubmit: (cmd: string) => void
  onStop: () => void
}

export default function CommandInput({ disabled, busy, onSubmit, onStop }: Props) {
  const [value, setValue] = useState('')

  function submit(e: FormEvent) {
    e.preventDefault()
    const v = value.trim()
    if (!v || disabled) return
    onSubmit(v)
    setValue('')
  }

  function onKey(e: KeyboardEvent<HTMLTextAreaElement>) {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault()
      submit(e as unknown as FormEvent)
    }
  }

  return (
    <form className="command-input" onSubmit={submit}>
      <textarea
        rows={1}
        placeholder={busy ? 'Command is running…' : 'Type a command, Enter to run, Shift+Enter for newline'}
        value={value}
        onChange={(e) => setValue(e.target.value)}
        onKeyDown={onKey}
        disabled={disabled || busy}
      />
      {busy ? (
        <button type="button" className="command-input__stop" onClick={onStop}>Stop</button>
      ) : (
        <button type="submit" disabled={disabled || !value.trim()}>Run</button>
      )}
    </form>
  )
}
```

- [ ] **Step 6.4: Implement TerminalPage.tsx**

Create `web/src/features/terminal/TerminalPage.tsx`:
```tsx
import { useEffect, useState } from 'react'
import { useShell } from './useShell'
import HistoryList from './HistoryList'
import OutputView from './OutputView'
import CommandInput from './CommandInput'
import { getCommand, CommandFull } from '../../lib/api'
import './TerminalPage.css'

interface Props {
  token: string
  onLogout: () => void
}

export default function TerminalPage({ token, onLogout }: Props) {
  const { connState, running, idle, lastError, clearError, submit, stop, historyVersion } = useShell(token)
  const [selectedId, setSelectedId] = useState<string | null>(null)
  const [selected, setSelected] = useState<CommandFull | null>(null)
  const [loadingSelected, setLoadingSelected] = useState(false)

  // When a command starts, deselect history (right pane shows live output).
  useEffect(() => {
    if (running) setSelectedId(null)
  }, [running])

  // Load a history record when one is selected.
  useEffect(() => {
    let alive = true
    if (!selectedId) {
      setSelected(null)
      return
    }
    setLoadingSelected(true)
    getCommand(selectedId)
      .then((r) => {
        if (alive) {
          setSelected(r)
          setLoadingSelected(false)
        }
      })
      .catch(() => {
        if (alive) setLoadingSelected(false)
      })
    return () => {
      alive = false
    }
  }, [selectedId])

  const showingLive = running != null

  return (
    <div className="terminal-page">
      <header className="terminal-page__header">
        <div className="terminal-page__brand">Headless Alfred</div>
        <div className="terminal-page__status">
          <span className={`status-dot status-dot--${connState}`} /> {connState}
        </div>
        <button className="terminal-page__logout" onClick={onLogout}>Sign out</button>
      </header>

      {lastError && (
        <div className="terminal-page__banner is-error">
          {lastError.message || lastError.code}
          <button onClick={clearError}>×</button>
        </div>
      )}

      <div className="terminal-page__split">
        <HistoryList
          selectedId={selectedId}
          runningId={running?.id ?? null}
          onSelect={setSelectedId}
          refreshTrigger={historyVersion}
        />
        <main className="terminal-page__main">
          {showingLive && running && (
            <OutputView command={running.command} output={running.output} isLive />
          )}
          {!showingLive && selected && (
            <OutputView
              command={selected.command}
              output={selected.output}
              isLive={false}
              exitCode={selected.exit_code}
              truncated={selected.output_truncated}
            />
          )}
          {!showingLive && !selected && (
            <div className="terminal-page__empty">
              {loadingSelected ? 'Loading…' : 'Pick a command from the left, or run a new one below.'}
            </div>
          )}
          <CommandInput
            disabled={connState !== 'open'}
            busy={showingLive}
            onSubmit={submit}
            onStop={() => running && stop(running.id)}
          />
        </main>
      </div>
    </div>
  )
}
```

- [ ] **Step 6.5: Implement TerminalPage.css**

Create `web/src/features/terminal/TerminalPage.css`:
```css
.terminal-page {
  display: grid;
  grid-template-rows: auto auto 1fr;
  height: 100vh;
}

.terminal-page__header {
  display: flex;
  align-items: center;
  gap: 16px;
  padding: 12px 20px;
  background: var(--surface);
  border-bottom: 1px solid var(--border);
}

.terminal-page__brand {
  font-weight: 600;
}

.terminal-page__status {
  margin-left: auto;
  font-size: 13px;
  color: var(--text-muted);
  display: flex;
  align-items: center;
  gap: 6px;
}

.status-dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  background: #d1d5db;
}

.status-dot--open { background: var(--ok); }
.status-dot--connecting,
.status-dot--reconnecting { background: var(--warn); }
.status-dot--closed { background: var(--error); }

.terminal-page__logout {
  background: transparent;
  border: 1px solid var(--border);
  border-radius: 6px;
  padding: 6px 10px;
  color: var(--text);
  font-size: 13px;
}

.terminal-page__banner {
  padding: 8px 16px;
  display: flex;
  align-items: center;
  gap: 12px;
  font-size: 13px;
}

.terminal-page__banner.is-error {
  background: var(--error-bg);
  color: var(--error);
}

.terminal-page__banner button {
  margin-left: auto;
  border: none;
  background: transparent;
  font-size: 16px;
  color: inherit;
}

.terminal-page__split {
  display: grid;
  grid-template-columns: 280px 1fr;
  min-height: 0;
}

/* History */
.history-list {
  border-right: 1px solid var(--border);
  background: var(--surface);
  overflow-y: auto;
}

.history-list__header {
  padding: 12px 16px;
  font-size: 12px;
  font-weight: 600;
  color: var(--text-muted);
  text-transform: uppercase;
  letter-spacing: 0.04em;
  border-bottom: 1px solid var(--border);
  position: sticky;
  top: 0;
  background: var(--surface);
}

.history-list ul {
  list-style: none;
  margin: 0;
  padding: 0;
}

.history-list__item {
  padding: 10px 16px;
  border-bottom: 1px solid var(--border);
  cursor: pointer;
}

.history-list__item.is-selected {
  background: rgba(37, 99, 235, 0.08);
}

.history-list__item.is-running {
  background: rgba(22, 163, 74, 0.06);
}

.history-list__item.is-locked {
  cursor: default;
  opacity: 0.7;
}

.history-list__cmd {
  font-family: ui-monospace, monospace;
  font-size: 13px;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.history-list__meta {
  font-size: 11px;
  color: var(--text-muted);
  margin-top: 2px;
}

.history-list__empty {
  padding: 16px;
  color: var(--text-muted);
  font-size: 13px;
}

.history-list__error {
  padding: 8px 16px;
  color: var(--error);
  background: var(--error-bg);
  font-size: 13px;
}

/* Main */
.terminal-page__main {
  display: grid;
  grid-template-rows: 1fr auto;
  min-height: 0;
}

.terminal-page__empty {
  padding: 32px;
  color: var(--text-muted);
  font-size: 14px;
}

.output-view {
  display: grid;
  grid-template-rows: auto 1fr;
  min-height: 0;
}

.output-view__header {
  padding: 12px 20px;
  border-bottom: 1px solid var(--border);
  display: flex;
  align-items: center;
  gap: 12px;
  font-size: 13px;
}

.output-view__header code {
  font-family: ui-monospace, monospace;
  background: rgba(0, 0, 0, 0.04);
  padding: 2px 6px;
  border-radius: 4px;
}

.output-view__live { color: var(--ok); }
.output-view__exit.is-ok { color: var(--ok); }
.output-view__exit.is-err { color: var(--error); }
.output-view__warn { color: var(--warn); }

.output-view__body {
  margin: 0;
  padding: 16px 20px;
  overflow: auto;
  font-family: ui-monospace, monospace;
  font-size: 13px;
  line-height: 1.5;
  background: var(--surface);
  white-space: pre-wrap;
  word-break: break-word;
}

/* Command input */
.command-input {
  display: flex;
  gap: 8px;
  padding: 12px 20px;
  border-top: 1px solid var(--border);
  background: var(--surface);
}

.command-input textarea {
  flex: 1;
  resize: none;
  padding: 10px 12px;
  border: 1px solid var(--border);
  border-radius: 8px;
  font-family: ui-monospace, monospace;
  font-size: 13px;
  min-height: 38px;
  max-height: 200px;
}

.command-input textarea:focus {
  outline: none;
  border-color: var(--accent);
}

.command-input button {
  padding: 0 16px;
  background: var(--accent);
  color: var(--accent-fg);
  border: none;
  border-radius: 8px;
  font-size: 14px;
  font-weight: 500;
}

.command-input__stop {
  background: var(--error) !important;
}

.command-input button:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
```

- [ ] **Step 6.6: Type-check**

```bash
cd web && npx tsc -b
```

Expected: no errors.

- [ ] **Step 6.7: Commit**

```bash
git add web/src/features/terminal/HistoryList.tsx web/src/features/terminal/OutputView.tsx web/src/features/terminal/CommandInput.tsx web/src/features/terminal/TerminalPage.tsx web/src/features/terminal/TerminalPage.css
git commit -m "feat(web/terminal): split-screen layout components"
```

---

## Task 7: Wire App.tsx with route guard

**Files:**
- Modify: `web/src/App.tsx`

- [ ] **Step 7.1: Replace App.tsx**

Replace the stub `web/src/App.tsx` with:
```tsx
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
```

- [ ] **Step 7.2: Run all frontend tests**

```bash
cd web && npm test
```

Expected: useAuth + useShell tests pass; no broken imports.

- [ ] **Step 7.3: Build production bundle**

```bash
cd web && npm run build
```

Expected: produces `web/dist/index.html` and assets. Confirm `index.html` exists.

- [ ] **Step 7.4: Commit**

```bash
git add web/src/App.tsx
git commit -m "feat(web): wire App with login/terminal gate"
```

---

## Task 8: Live dev test against backend

This is a manual verification step. Required because the WS reconnect logic and base64 decoding only fully exercise against the real backend.

- [ ] **Step 8.1: Start backend with smoke env**

In terminal A:
```bash
ALFRED_USER=admin ALFRED_PASSWORD=test ALFRED_TOKEN=devtoken ALFRED_DATA_DIR=/tmp/alfred-dev \
  go run ./cmd/alfred-server
```

- [ ] **Step 8.2: Start frontend dev server**

In terminal B:
```bash
cd web && npm run dev
```

- [ ] **Step 8.3: Walk through the UI in a browser**

Open http://localhost:5173.

Verify in order:
- Login page shows; trying wrong password shows "Wrong username or password."
- Login with admin/test → terminal page appears, status dot is green.
- Type `ls /` → Run → output appears in right pane.
- Type `for i in 1 2 3; do echo $i; sleep 1; done` → Run → see chunks arrive over 3 seconds, Stop button is red.
- Click Stop midway → command ends with non-zero exit; input unlocks; banner clears.
- Type `cd /tmp` → Run → completes. Then type `pwd` → Run → output shows `/tmp`. Proves cd persistence.
- Type `sleep 5` → Run. While running, close the browser tab. Reopen http://localhost:5173. Status dot green. The reattach should show the same command still ticking; when it finishes, status returns to idle.
- Click on a past command in History — output shows in read-only mode (no live dot).
- Click "Sign out" → returns to login page; localStorage cleared.

If any step fails, debug. None of these require code changes if the code matches what's in this plan.

- [ ] **Step 8.4: Mark plan complete** (no commit needed for manual verification)

---

## Self-Review Notes

**Spec coverage:**
- §6 WS messages: all 7 handled in useShell ✓
- §11 UI behavior: split-screen ✓; lock-when-busy ✓; history not selectable while busy ✓; status indicator ✓; 401 → login redirect via setOn401 hook ✓
- §6 HTTP API: api.ts wraps login + list + get + stop ✓

**Known small gaps & deferred:**
- No vitest tests for `HistoryList`, `OutputView`, `CommandInput` — they're presentational, exercised by Task 8 manual walkthrough; cost/benefit not worth unit tests.
- No "rate limited (429)" branch in `useShell.lastError` — covered via the LoginPage's ApiError handler, which is where 429 originates.
- Output is rendered as plain text (no ANSI color stripping). For MVP this is fine; raw escape codes appear as `^[[31m`-style noise. If they bother you, add `strip-ansi` later in `useShell` decode.

What's deferred to Plan 4+:
- Dockerfile that builds both Go binary and React dist into one image
- K8s manifests
- E2E
