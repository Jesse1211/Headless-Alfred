// Shared fetch wrapper. Reads token from localStorage; on 401 fires a hook
// the auth feature can register to flush local state and route back to login.

let on401: (() => void) | null = null

export function setOn401(fn: () => void): void {
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
      code = (j as { code?: string }).code ?? code
      msg = (j as { message?: string }).message ?? msg
    } catch {
      // body wasn't JSON; keep statusText as message
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

export interface Session {
  id: string
  name: string
  created_at: string
}

export async function listSessions(): Promise<Session[]> {
  const res = await request('/api/sessions')
  return res.json()
}

export async function createSession(name?: string): Promise<Session> {
  // Send an empty JSON body when no name. Sending no body at all (body:
  // undefined) means fetch omits Content-Length, which some browsers
  // surface to handlers as length=-1 and which our handler treats as
  // "chunked" — usually fine, but {} is unambiguous and tiny.
  const init: RequestInit = {
    method: 'POST',
    body: JSON.stringify(name && name.trim() ? { name } : {}),
  }
  const res = await request('/api/sessions', init)
  return res.json()
}

export async function renameSession(id: string, name: string): Promise<void> {
  await request(`/api/sessions/${encodeURIComponent(id)}`, {
    method: 'PATCH',
    body: JSON.stringify({ name }),
  })
}

export async function deleteSession(id: string): Promise<void> {
  await request(`/api/sessions/${encodeURIComponent(id)}`, { method: 'DELETE' })
}

export async function listCommands(
  sessionID: string,
  opts: { limit?: number; before?: string } = {},
): Promise<CommandSummary[]> {
  const qs = new URLSearchParams()
  if (opts.limit != null) qs.set('limit', String(opts.limit))
  if (opts.before) qs.set('before', opts.before)
  const res = await request(
    `/api/sessions/${encodeURIComponent(sessionID)}/commands${qs.size ? '?' + qs.toString() : ''}`,
  )
  return res.json()
}

export async function getCommand(sessionID: string, id: string): Promise<CommandFull> {
  const res = await request(
    `/api/sessions/${encodeURIComponent(sessionID)}/commands/${encodeURIComponent(id)}`,
  )
  return res.json()
}

export async function stopCommand(sessionID: string, id: string): Promise<void> {
  await request(
    `/api/sessions/${encodeURIComponent(sessionID)}/commands/${encodeURIComponent(id)}/stop`,
    { method: 'POST' },
  )
}
