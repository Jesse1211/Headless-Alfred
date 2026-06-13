export interface RunningCmd {
  id: string
  command: string
  startedAt: string
  output: string
  truncatedLossWarned: boolean
}

export interface CompletedMsg {
  id: string
  command: string
  output: string
  startedAt: string
  finishedAt?: string
  exitCode?: number
  status: 'completed' | 'interrupted' | 'stopped' | 'running'
  truncated: boolean
}

export type SessionMode = 'shell' | 'claude'

export interface PerSessionState {
  running: RunningCmd | null
  messages: CompletedMsg[]
  messagesLoaded: boolean
  mode: SessionMode
}

export function emptyPerSessionState(): PerSessionState {
  return { running: null, messages: [], messagesLoaded: false, mode: 'shell' }
}
