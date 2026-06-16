// Shared fetch wrapper. Reads token from localStorage; on 401 fires a hook
// the auth feature can register to flush local state and route back to login.

import type { ClaudeTurn } from '../features/sessions/types'

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
  // 'chat' (default; empty/missing on old records) or 'recap'.
  kind?: 'chat' | 'recap'
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

export interface GitCredentials {
  host: string
  username: string
  token: string
}

export async function saveGitCredentials(c: GitCredentials): Promise<void> {
  await request('/api/git-credentials', {
    method: 'POST',
    body: JSON.stringify(c),
  })
}

// saveAnthropicCredentials uploads the raw JSON contents of the user's
// ~/.claude/.credentials.json (the file claude writes after OAuth /login)
// to the server, which installs it at the same path inside the pod.
export async function saveAnthropicCredentials(credentialsJson: string): Promise<void> {
  // Send the body verbatim — the server expects the exact bytes claude
  // wrote, not a re-encoded version. Setting Content-Type so the
  // server's MaxBytesReader can size-bound correctly.
  await request('/api/anthropic-credentials', {
    method: 'POST',
    body: credentialsJson,
    headers: { 'Content-Type': 'application/json' },
  })
}

// getSummary fetches the current text of <DATA_DIR>/summaries/<sid>.md.
// Returns '' for both 404 (file never created) and 200-with-empty-body
// (file exists but empty) — both are the "no summary yet" empty state.
export async function getSummary(sessionID: string): Promise<string> {
  try {
    const res = await request(`/api/sessions/${encodeURIComponent(sessionID)}/summary`)
    return await res.text()
  } catch (e) {
    if (e instanceof ApiError && e.status === 404) return ''
    throw e
  }
}

// getTemplate fetches the raw text of a built-in template by id (e.g.
// 'summary-todo'). Placeholders like <sid> and <summary_path> are
// returned verbatim — substitution happens server-side when the prompt
// is composed.
export async function getTemplate(id: string): Promise<string> {
  const res = await request(`/api/templates/${encodeURIComponent(id)}`)
  return res.text()
}

// getClaudeHistory fetches the reconstructed Claude UI chat history
// for a session from the backend's jsonl-restore endpoint. Returns
// [] when the session has no jsonl yet (user hasn't entered Claude,
// or the file has been moved/deleted) — empty is a valid state, not
// an error.
export async function getClaudeHistory(
  sessionID: string,
  opts: { limit?: number; before?: string } = {},
): Promise<ClaudeTurn[]> {
  const qs = new URLSearchParams()
  if (opts.limit != null) qs.set('limit', String(opts.limit))
  if (opts.before) qs.set('before', opts.before)
  const url =
    `/api/sessions/${encodeURIComponent(sessionID)}/claude-history` +
    (qs.size ? '?' + qs.toString() : '')
  const res = await request(url)
  return res.json()
}

// getSession fetches one session's metadata by id. Used to look up
// the currently selected recap session, which doesn't appear in the
// chat-only sessions list.
export async function getSession(id: string): Promise<Session> {
  const res = await request(`/api/sessions/${encodeURIComponent(id)}`)
  return res.json()
}

// createRecapSession is POST /api/recap-sessions — find-or-create
// the singleton recap session. Returns the session metadata
// (including the new `kind: 'recap'` field).
export async function createRecapSession(): Promise<Session> {
  const res = await request('/api/recap-sessions', { method: 'POST' })
  return res.json()
}

// deleteRecapSession is DELETE /api/recap-sessions/current —
// idempotent kill. 204 even if no recap session exists.
export async function deleteRecapSession(): Promise<void> {
  await request('/api/recap-sessions/current', { method: 'DELETE' })
}

export interface RecapEntry {
  date: string
  isToday: boolean
}

// listRecaps returns the dates that have recap files, newest first.
export async function listRecaps(): Promise<RecapEntry[]> {
  const res = await request('/api/recaps')
  return res.json()
}

// getRecap returns the markdown body of one date's recap.
// Throws ApiError(404, 'not_found', ...) when no such recap exists.
export async function getRecap(date: string): Promise<string> {
  const res = await request(`/api/recaps/${encodeURIComponent(date)}`)
  return res.text()
}

// getNote fetches the notes body for the session. Returns '' for
// 404 (file never created) — the empty state in the UI.
export async function getNote(sessionID: string): Promise<string> {
  try {
    const res = await request(`/api/sessions/${encodeURIComponent(sessionID)}/note`)
    return await res.text()
  } catch (e) {
    if (e instanceof ApiError && e.status === 404) return ''
    throw e
  }
}

// putNote writes (atomically server-side) the body to the session's
// notes file. Capped at 64KB by the server.
export async function putNote(sessionID: string, body: string): Promise<void> {
  await request(`/api/sessions/${encodeURIComponent(sessionID)}/note`, {
    method: 'PUT',
    headers: { 'Content-Type': 'text/markdown' },
    body,
  })
}
