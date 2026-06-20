import { useEffect, useRef, useState } from 'react'
import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { Prism as SyntaxHighlighter } from 'react-syntax-highlighter'
import { oneDark } from 'react-syntax-highlighter/dist/esm/styles/prism'
import type { BgTask, ClaudeState, ClaudeToolCall, ClaudeTurn, SubagentEntry } from './types'
import type { TemplateSummary } from '../../lib/api'
import { ToolApprovalCard } from './ToolApprovalCard'
import { AskUserQuestionCard } from './AskUserQuestionCard'
import { isSubmitKey } from '../../lib/keyboard'
import './ClaudeChatView.css'

// Per-session template selection lives in localStorage. Sticky
// across page reloads, keyed by sessionID so two sessions can
// have different defaults. We use a Set in memory (cheap membership
// checks for the checkbox renderer) and serialize to a sorted
// string[] for storage stability.
function templatesStorageKey(sid: string): string {
  return `alfred_templates_${sid}`
}

function readSelectedTemplates(sid: string): Set<string> {
  try {
    const raw = localStorage.getItem(templatesStorageKey(sid))
    if (!raw) return new Set()
    const arr = JSON.parse(raw)
    return Array.isArray(arr) ? new Set(arr.filter((x) => typeof x === 'string')) : new Set()
  } catch {
    return new Set()
  }
}

function writeSelectedTemplates(sid: string, sel: Set<string>): void {
  try {
    localStorage.setItem(templatesStorageKey(sid), JSON.stringify([...sel].sort()))
  } catch {
    /* quota / disabled — fail open */
  }
}

interface Props {
  state: ClaudeState
  disabled: boolean
  // disconnected: true when the WS isn't open (connecting / reconnecting /
  // closed). Renders a red banner just above the composer so the user
  // knows their last action may not have reached the server and any
  // in-flight "Claude is thinking..." timer is now lying.
  disconnected?: boolean
  // sessionID is used to scope the per-session template-selection
  // localStorage key. Required so two open sessions don't share
  // a checkbox state.
  sessionID: string
  // templates is the catalog the composer's checkbox strip renders.
  // Empty array means "no templates available" → strip is hidden.
  templates: TemplateSummary[]
  // onPrompt now takes the selected template IDs as a second
  // argument. Caller forwards them as opts.templates to
  // claude_prompt; backend uses them per-prompt (overriding the
  // legacy session-default).
  onPrompt: (text: string, templates: string[]) => void
  onToolDecision: (toolUseId: string, decision: 'allow' | 'deny', reason?: string) => void
  onQuestionAnswer: (toolUseId: string, formattedAnswer: string) => void
  onInterrupt: () => void
}

// ClaudeChatView is the V1 ChatGPT-style renderer for a Claude conversation.
// Renders assistant text as markdown (via react-markdown + remark-gfm),
// surfaces tool calls inline, and floats pending approval cards at the bottom.
export function ClaudeChatView({
  state, disabled, disconnected, sessionID, templates, onPrompt, onToolDecision, onQuestionAnswer, onInterrupt,
}: Props) {
  const [draft, setDraft] = useState('')
  // Per-session, persisted-across-reloads selection. Read once on
  // mount (lazy state init) so a session switch doesn't ever leak
  // a previous session's choices into the new one. Writes happen
  // inside the toggle handler.
  const [selectedTemplates, setSelectedTemplates] = useState<Set<string>>(() =>
    readSelectedTemplates(sessionID)
  )
  const scrollRef = useRef<HTMLDivElement | null>(null)

  // Auto-scroll to bottom whenever a new event lands.
  useEffect(() => {
    const el = scrollRef.current
    if (!el) return
    el.scrollTop = el.scrollHeight
  }, [state.turns, state.pending.length, state.pendingQuestions.length, state.inFlight])

  function submit() {
    const text = draft.trim()
    if (!text || state.inFlight) return
    // Snapshot the currently-checked templates so this prompt uses
    // them even if the user untoggles in the next 50ms. (Sticky
    // checkboxes — selectedTemplates state survives the submit.)
    onPrompt(text, [...selectedTemplates])
    setDraft('')
  }

  function toggleTemplate(id: string) {
    setSelectedTemplates((prev) => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      writeSelectedTemplates(sessionID, next)
      return next
    })
  }

  // Empty state: no turns yet, nothing in flight, no pending
  // approvals / questions. Center the composer with a hero title
  // above it (ChatGPT-style first-run). As soon as the first turn
  // appears we switch to the normal scroll-above-composer layout
  // and the composer pins to the bottom.
  const isEmpty =
    state.turns.length === 0 &&
    !state.inFlight &&
    state.pending.length === 0 &&
    state.pendingQuestions.length === 0 &&
    !state.lastError

  return (
    <div className={`claude-chat ${isEmpty ? 'claude-chat--empty' : ''}`}>
      {isEmpty ? (
        <div className="claude-chat__hero">
          <h1 className="claude-chat__hero-title">Headless Alfred</h1>
          <p className="claude-chat__hero-subtitle">
            What can I help with? Tools will ask before running.
          </p>
        </div>
      ) : (
        <div className="claude-chat__scroll" ref={scrollRef}>
          {state.turns.map((t) => (
            <TurnView key={t.id} turn={t} bgTasks={state.bgTasks} subagents={state.subagents} />
          ))}
          {state.pending.map((req) => (
            <ToolApprovalCard
              key={req.toolUseId}
              request={req}
              onDecide={(d, reason) => onToolDecision(req.toolUseId, d, reason)}
            />
          ))}
          {state.pendingQuestions.map((req) => (
            <AskUserQuestionCard
              key={req.toolUseId}
              request={req}
              onSubmit={onQuestionAnswer}
              onCancel={(id) => onToolDecision(id, 'deny', 'User cancelled the question.')}
            />
          ))}
          {state.lastError && (
            <div className="claude-chat__error">
              {state.lastError.message || state.lastError.code}
            </div>
          )}
        </div>
      )}

      <div className="claude-chat__composer">
        {disconnected && (
          <div className="claude-chat__disconnected" role="status">
            ⚠ Disconnected from server — reconnecting. The current turn may be stale.
          </div>
        )}
        {draft.startsWith('/') && (
          <div className="claude-chat__slash-hint">
            Slash command — sent to Claude CLI ({(draft.trim().split(/\s+/)[0]) || '/'}).
            Commands like <code>/compact</code> and <code>/clear</code> work; some
            interactive ones may fail in headless mode.
          </div>
        )}
        <div className="claude-chat__composer-row">
        <textarea
          className="claude-chat__input"
          placeholder={state.inFlight ? 'Claude is thinking…' : 'Message Claude…'}
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          onKeyDown={(e) => {
            if (isSubmitKey(e)) {
              e.preventDefault()
              submit()
            }
          }}
          rows={3}
          disabled={disabled}
        />
        <div className="claude-chat__actions">
          {state.inFlight ? (
            <button
              type="button"
              className="claude-chat__btn claude-chat__btn--stop"
              onClick={onInterrupt}
              title="Send SIGINT to the running Claude turn"
            >
              Stop
            </button>
          ) : (
            <button
              type="button"
              className="claude-chat__btn claude-chat__btn--send"
              onClick={submit}
              disabled={disabled || !draft.trim()}
            >
              Send
            </button>
          )}
        </div>
        </div>
        {templates.length > 0 && (
          <div className="claude-chat__templates" role="group" aria-label="Templates to inject this prompt">
            <span className="claude-chat__templates-label">Inject:</span>
            {templates.map((tpl) => {
              const checked = selectedTemplates.has(tpl.id)
              return (
                <label
                  key={tpl.id}
                  className={`claude-chat__template ${checked ? 'is-checked' : ''}`}
                >
                  <input
                    type="checkbox"
                    checked={checked}
                    onChange={() => toggleTemplate(tpl.id)}
                    disabled={state.inFlight}
                  />
                  <span>{tpl.name}</span>
                </label>
              )
            })}
          </div>
        )}
      </div>
    </div>
  )
}

// UserPromptBubble shows the user's prompt with an optional "Show
// full prompt" toggle. When the server injected a template body
// (summary-todo wrapping, recap-daily contents, etc.) the visible
// prompt and the expanded prompt diverge; the toggle exposes the
// expanded one so the user can audit exactly what their tokens paid
// for. Same component handles non-injected turns too — if there's
// nothing extra to reveal, the toggle is hidden.
function UserPromptBubble({ turn }: { turn: ClaudeTurn }) {
  const [expanded, setExpanded] = useState(false)
  const full = turn.expandedPrompt
  const hasExtra = !!full && full.trim() !== turn.prompt.trim()
  return (
    <div className="claude-turn__user">
      <div className="claude-turn__label">You</div>
      <div className="claude-turn__user-text">{turn.prompt}</div>
      {hasExtra && (
        <>
          <button
            type="button"
            className="claude-turn__expand-btn"
            onClick={() => setExpanded((v) => !v)}
            title={expanded ? 'Hide full prompt' : 'Show the full prompt sent to Claude (incl. template body)'}
          >
            {expanded ? '▾ Hide full prompt' : '▸ Show full prompt sent to Claude'}
            <span className="claude-turn__expand-meta">
              {' '}({(full!.length).toLocaleString()} chars)
            </span>
          </button>
          {expanded && (
            <pre className="claude-turn__user-expanded">{full}</pre>
          )}
        </>
      )}
    </div>
  )
}

function TurnView({ turn, bgTasks, subagents }: {
  turn: ClaudeTurn
  bgTasks: Record<string, BgTask>
  subagents: Record<string, SubagentEntry>
}) {
  return (
    <div className="claude-turn">
      <UserPromptBubble turn={turn} />
      <div className={`claude-turn__assistant ${turn.isError ? 'is-error' : ''}`}>
        <div className="claude-turn__label">Claude<TurnPhaseChip turn={turn} /></div>
        {turn.thinking && turn.thinking.map((body, i) => (
          <ThinkingBlockView key={`think-${i}`} body={body} />
        ))}
        {turn.blocks.map((block, i) => {
          if (block.kind === 'text') {
            // Skip empty text blocks (can happen mid-stream when a
            // delta hasn't landed yet for an opened block index).
            if (!block.text) return null
            return (
              <div key={`block-${i}`} className="claude-turn__text">
                <AssistantMarkdown text={block.text} />
              </div>
            )
          }
          // TodoWrite gets a custom rendering — it's the model's
          // own task tracker and reads much better as a checklist
          // than as a folded JSON dump. Falls through to the generic
          // ToolCallView if the input shape doesn't match.
          if (block.tool.name === 'TodoWrite') {
            const todos = parseTodoWriteInput(block.tool.input)
            if (todos) {
              return (
                <TodoWriteCard
                  key={block.tool.toolUseId}
                  todos={todos}
                  turn={turn}
                />
              )
            }
          }
          return (
            <ToolCallView
              key={block.tool.toolUseId}
              tool={block.tool}
              bgTask={block.tool.bgTaskId ? bgTasks[block.tool.bgTaskId] : undefined}
            />
          )
        })}
        {!turn.done && turn.blocks.length === 0 && (
          <div className="claude-turn__thinking">…</div>
        )}
        {!turn.done && <LiveTurnElapsed turn={turn} />}
        {turn.done && (turn.usage || turnDuration(turn) !== null) && (
          <div className="claude-turn__footer">
            {turnDuration(turn) !== null && (
              <span title="Time the turn took, end-to-end">
                {formatElapsed(turnDuration(turn)!)}
              </span>
            )}
            {turn.totalCostUsd != null && (
              <span title="Total cost for this turn">${turn.totalCostUsd.toFixed(4)}</span>
            )}
            {turn.usage && (
              <span title="Input → output tokens">
                {turn.usage.inputTokens.toLocaleString()} in → {turn.usage.outputTokens.toLocaleString()} out
              </span>
            )}
          </div>
        )}
        <TurnStatsLine turn={turn} bgTasks={bgTasks} subagents={subagents} />
      </div>
    </div>
  )
}

export function turnPhase(turn: ClaudeTurn): string {
  switch (turn.outcome) {
    case 'completed': return 'Done'
    case 'errored':   return 'Error'
    case 'aborted':   return 'Interrupted'
  }
  if (turn.done) return 'Done' // pre-outcome snapshot fallback
  if (turn.blocks.length === 0) return 'Initializing'
  // The last block determines the phase: if it's a still-running tool, "Calling X"; otherwise "Thinking".
  const last = turn.blocks[turn.blocks.length - 1]
  if (last.kind === 'tool' && !last.tool.finishedAt) return `Calling ${last.tool.name}`
  return 'Thinking'
}

function TurnPhaseChip({ turn }: { turn: ClaudeTurn }) {
  const phase = turnPhase(turn)
  return <span className={`turn-phase-chip turn-phase-chip--${phase.toLowerCase().replace(/\s+/g, '-')}`}>{phase}</span>
}

export function TurnStatsLine({ turn, bgTasks, subagents }: {
  turn: ClaudeTurn
  bgTasks: Record<string, BgTask>
  subagents: Record<string, SubagentEntry>
}) {
  const toolCount = turn.blocks.filter((b) => b.kind === 'tool').length
  const bgTaskBlocks = turn.blocks.filter(
    (b) => b.kind === 'tool' && b.tool.bgTaskId,
  ) as Array<{ kind: 'tool'; tool: ClaudeToolCall }>
  const bgTaskTotal = bgTaskBlocks.length
  const bgTaskRunning = bgTaskBlocks
    .map((b) => bgTasks[b.tool.bgTaskId!])
    .filter((t): t is BgTask => !!t && t.status === 'in_progress').length

  const agentBlocks = turn.blocks.filter(
    (b) => b.kind === 'tool' && b.tool.name === 'Agent',
  )
  const subagentTotal = agentBlocks.length
  const subagentsList = Object.values(subagents)
  const subagentRunning = subagentsList.filter((s) => !s.finishedAt).length
  const subagentDone = subagentsList.filter((s) => !!s.finishedAt).length

  const parts: string[] = []
  if (toolCount > 0) {
    parts.push(`${toolCount} tool call${toolCount === 1 ? '' : 's'}`)
  }
  if (bgTaskTotal > 0) {
    parts.push(`${bgTaskTotal} background task${bgTaskTotal === 1 ? '' : 's'} (${bgTaskRunning > 0 ? 'running' : 'done'})`)
  }
  if (subagentTotal > 0 || subagentRunning > 0 || subagentDone > 0) {
    const total = Math.max(subagentTotal, subagentRunning + subagentDone)
    parts.push(`${total} subagent${total === 1 ? '' : 's'} (${subagentRunning > 0 ? 'blocking' : 'done'})`)
  }
  if (parts.length === 0) return null
  return <div className="turn-stats">{parts.join(' · ')}</div>
}

// AssistantMarkdown is the markdown renderer shared by the
// assistant's regular text reply and its extended-thinking blocks.
// Same Prism + remark-gfm config in both places so tables / fenced
// code / GFM features look identical wherever they appear.
function AssistantMarkdown({ text }: { text: string }) {
  return (
    <ReactMarkdown
      remarkPlugins={[remarkGfm]}
      components={{
        code(props) {
          const { className, children, ...rest } = props as any
          const match = /language-(\w+)/.exec(className || '')
          const isFenced = !!match
          if (!isFenced) {
            return <code className={className} {...rest}>{children}</code>
          }
          return (
            <SyntaxHighlighter
              language={match![1]}
              style={oneDark}
              PreTag="div"
              customStyle={{
                margin: 0,
                borderRadius: 6,
                background: 'rgba(0, 0, 0, 0.35)',
              }}
              codeTagProps={{ style: { fontSize: 12.5, fontFamily: '"JetBrains Mono", ui-monospace, SFMono-Regular, Menlo, monospace' } }}
            >
              {String(children).replace(/\n$/, '')}
            </SyntaxHighlighter>
          )
        },
      }}
    >{text}</ReactMarkdown>
  )
}

// ThinkingBlockView renders one extended-thinking block as a
// collapsible card. Default collapsed so the user's focus stays on
// the actual reply; expand to read Claude's reasoning, which renders
// as full markdown (tables, code blocks, lists — same renderer as
// the main assistant reply).
// TodoItem mirrors the TodoWrite tool's input schema: each entry has
// a content string and a status from a small enum. activeForm is the
// CLI's "what's happening right now" phrasing for the in_progress
// state; if present we prefer it over content for that single entry
// so the card reads "Updating sessions reducer..." instead of
// "Update sessions reducer" when work is mid-flight.
interface TodoItem {
  content: string
  status: 'pending' | 'in_progress' | 'completed'
  activeForm?: string
}

// parseTodoWriteInput narrows the tool's input JSON into a typed
// list of TodoItems. Returns null when the input doesn't match the
// expected shape (model emitted a weird payload, schema drift,
// etc.) — caller falls back to the generic ToolCallView card.
function parseTodoWriteInput(input: unknown): TodoItem[] | null {
  const obj = input as { todos?: unknown } | null
  if (!obj || !Array.isArray(obj.todos)) return null
  const out: TodoItem[] = []
  for (const raw of obj.todos) {
    const t = raw as Partial<TodoItem> | null
    if (!t || typeof t.content !== 'string') continue
    const status = t.status
    if (status !== 'pending' && status !== 'in_progress' && status !== 'completed') continue
    out.push({
      content: t.content,
      status,
      activeForm: typeof t.activeForm === 'string' ? t.activeForm : undefined,
    })
  }
  return out.length > 0 ? out : null
}

// TodoWriteCard renders a parsed TodoWrite call as a checklist with
// done / in-progress / pending markers, plus a header showing the
// turn's elapsed time and cumulative token usage. Pure presentation
// — no state, no re-fetch.
function TodoWriteCard({ todos, turn }: { todos: TodoItem[]; turn: ClaudeTurn }) {
  const elapsed = turnElapsed(turn)
  const inTok = turn.usage?.inputTokens
  const outTok = turn.usage?.outputTokens
  const done = todos.filter((t) => t.status === 'completed').length
  return (
    <div className="claude-todo">
      <div className="claude-todo__header">
        <span className="claude-todo__title">Tasks ({done}/{todos.length})</span>
        <span className="claude-todo__meta">
          {elapsed}
          {(inTok != null || outTok != null) && (
            <> · {(inTok ?? 0).toLocaleString()} in → {(outTok ?? 0).toLocaleString()} out</>
          )}
        </span>
      </div>
      <ul className="claude-todo__list">
        {todos.map((t, i) => (
          <li key={i} className={`claude-todo__item claude-todo__item--${t.status}`}>
            <span className="claude-todo__marker" aria-hidden>
              {t.status === 'completed' ? '✔' : t.status === 'in_progress' ? '◼' : '◻'}
            </span>
            <span className="claude-todo__text">
              {t.status === 'in_progress' && t.activeForm ? t.activeForm : t.content}
            </span>
          </li>
        ))}
      </ul>
    </div>
  )
}

// turnElapsed returns a human-formatted duration since the turn
// started, or up to when it finished if `done`. Used in the
// TodoWrite card header. Live turns re-render frequently enough
// (every stream event) that "now" stays approximately current
// without a separate timer.
function turnElapsed(turn: ClaudeTurn): string {
  const start = Date.parse(turn.startedAt)
  if (isNaN(start)) return ''
  const ms = Date.now() - start
  const sec = Math.max(0, Math.round(ms / 1000))
  if (sec < 60) return `${sec}s`
  const min = Math.floor(sec / 60)
  const rem = sec % 60
  if (min < 60) return rem > 0 ? `${min}m ${rem}s` : `${min}m`
  const hr = Math.floor(min / 60)
  const minRem = min % 60
  return minRem > 0 ? `${hr}h ${minRem}m` : `${hr}h`
}

function ThinkingBlockView({ body }: { body: string }) {
  const [expanded, setExpanded] = useState(false)
  return (
    <div className={`claude-thinking ${expanded ? 'is-expanded' : ''}`}>
      <button
        type="button"
        className="claude-thinking__row"
        onClick={() => setExpanded((v) => !v)}
        aria-expanded={expanded}
      >
        <span className="claude-thinking__chev">{expanded ? '▾' : '▸'}</span>
        <span className="claude-thinking__label">Thinking</span>
        <span className="claude-thinking__meta">({body.length.toLocaleString()} chars)</span>
      </button>
      {expanded && (
        <div className="claude-thinking__body">
          <AssistantMarkdown text={body} />
        </div>
      )}
    </div>
  )
}

// useElapsed returns the number of seconds between `startedAt` and
// either `finishedAt` (if set) or now (re-rendered every second).
// Returns 0 when startedAt is undefined (e.g. restored from history
// without lifecycle events) — callers should hide the elapsed
// display when that's the case.
function useElapsed(startedAt: string | undefined, finishedAt: string | undefined): number {
  const [tick, setTick] = useState(0)
  useEffect(() => {
    if (!startedAt || finishedAt) return
    const id = window.setInterval(() => setTick((n) => n + 1), 1000)
    return () => window.clearInterval(id)
  }, [startedAt, finishedAt])
  if (!startedAt) return 0
  const end = finishedAt ? Date.parse(finishedAt) : Date.now()
  const start = Date.parse(startedAt)
  return Math.max(0, Math.floor((end - start) / 1000))
  // tick is referenced implicitly via re-render; suppress unused warn
  void tick
}

// LiveTurnElapsed renders a one-second-tick counter while the turn is
// streaming, in the same row position the final duration shows up in
// once the turn is done. Pinned at the bottom of the assistant area so
// the user can see "Claude has been replying for 14s" without watching
// the spinner. Reuses useElapsed (now-driven when finishedAt is
// undefined); the parent gates rendering to !turn.done.
function LiveTurnElapsed({ turn }: { turn: ClaudeTurn }) {
  const secs = useElapsed(turn.startedAt, turn.finishedAt)
  if (!turn.startedAt) return null
  return (
    <div className="claude-turn__footer claude-turn__footer--live" aria-live="polite">
      <span title="Time elapsed since the turn began">
        {formatElapsed(secs)}
      </span>
    </div>
  )
}

// turnDuration returns the turn's end-to-end elapsed seconds, or null
// if either timestamp is missing / invalid. Restored history may not
// have finishedAt for the trailing turn (no successor user-line ts to
// bracket against), in which case the footer hides the duration.
function turnDuration(turn: ClaudeTurn): number | null {
  if (!turn.startedAt || !turn.finishedAt) return null
  const start = Date.parse(turn.startedAt)
  const end = Date.parse(turn.finishedAt)
  if (isNaN(start) || isNaN(end) || end < start) return null
  return Math.max(0, Math.floor((end - start) / 1000))
}

// formatElapsed: seconds → "47s" / "2m14s" / "1h03m"
function formatElapsed(secs: number): string {
  if (secs < 60) return `${secs}s`
  if (secs < 3600) {
    const m = Math.floor(secs / 60)
    const s = secs % 60
    return `${m}m${s.toString().padStart(2, '0')}s`
  }
  const h = Math.floor(secs / 3600)
  const m = Math.floor((secs % 3600) / 60)
  return `${h}h${m.toString().padStart(2, '0')}m`
}

function ToolCallView({ tool, bgTask }: { tool: ClaudeToolCall; bgTask?: BgTask }) {
  const [expanded, setExpanded] = useState(false)
  const status = toolStatus(tool)
  const elapsedSecs = useElapsed(tool.startedAt, tool.finishedAt)
  const showElapsed = tool.startedAt !== undefined
  const elapsedClass =
    elapsedSecs >= 300 ? 'is-stuck' :
    elapsedSecs >= 30  ? 'is-slow'  : ''
  // For Monitor: prefer the bgTask's elapsed (which spans the whole
  // background task lifetime, not just the tool_use_id's brief life)
  // and append a count + ✓ marker when completed.
  const isMonitorWithTask = tool.name === 'Monitor' && bgTask !== undefined
  const bgElapsedSecs = useElapsed(bgTask?.startedAt, bgTask?.finishedAt)
  const preview = toolPreview(tool)
  const hasDetails = tool.input != null || (tool.result != null && tool.result !== '')
  return (
    <div className={`claude-tool claude-tool--${status} ${tool.isError ? 'is-error' : ''} ${expanded ? 'is-expanded' : ''}`}>
      <button
        type="button"
        className="claude-tool__row"
        onClick={() => hasDetails && setExpanded((v) => !v)}
        title={hasDetails ? (expanded ? 'Collapse' : 'Expand') : ''}
        aria-expanded={expanded}
      >
        <span className="claude-tool__chev">{hasDetails ? (expanded ? '▾' : '▸') : '·'}</span>
        <code className="claude-tool__name">{tool.name}</code>
        {preview && <span className="claude-tool__preview">({preview})</span>}
        <span className="claude-tool__status">{status}</span>
        {showElapsed && (
          <span className={`claude-tool__elapsed ${elapsedClass}`}>
            {formatElapsed(elapsedSecs)}
          </span>
        )}
        {isMonitorWithTask && (
          <span className="claude-tool__bg">
            {' · task '}<code>{bgTask!.taskId}</code>
            {' · '}{formatElapsed(bgElapsedSecs)}
            {bgTask!.notificationCount > 0 && (
              <> {' · '} {bgTask!.notificationCount} events</>
            )}
            {bgTask!.status === 'completed' && <span className="claude-tool__check">{' ✓'}</span>}
          </span>
        )}
      </button>
      {expanded && hasDetails && (
        <div className="claude-tool__body">
          {tool.input != null && (
            <pre className="claude-tool__input">{formatJSON(tool.input)}</pre>
          )}
          {tool.result != null && tool.result !== '' && (
            <pre className="claude-tool__result">{tool.result}</pre>
          )}
          {bgTask?.lastEventSummary && (
            <div className="claude-tool__last-event">
              Latest: {bgTask.lastEventSummary}
            </div>
          )}
        </div>
      )}
    </div>
  )
}

// toolPreview returns a short, human-readable summary of the tool's
// principal argument for the collapsed row.
function toolPreview(tool: ClaudeToolCall): string {
  const inp = tool.input as Record<string, unknown> | null
  if (!inp || typeof inp !== 'object') return ''
  const pick = (key: string): string | null => {
    const v = inp[key]
    return typeof v === 'string' ? v : null
  }
  const candidates = ['command', 'file_path', 'path', 'pattern', 'query', 'url']
  for (const k of candidates) {
    const v = pick(k)
    if (v) return v.length > 80 ? v.slice(0, 77) + '…' : v
  }
  for (const v of Object.values(inp)) {
    if (typeof v === 'string' && v) return v.length > 80 ? v.slice(0, 77) + '…' : v
  }
  return ''
}

function formatJSON(v: unknown): string {
  try { return JSON.stringify(v, null, 2) } catch { return String(v) }
}

// toolStatus picks a single status label out of three independent
// pieces of state: the user's decision (deny|allow|pending) and
// whether a result has come back. Order matters — a denied tool
// never produces a result, and an allowed tool is "running" until
// the result arrives.
// Exported for unit tests. The `interrupted` case is the loader's
// runner-death finalize (decision=deny + isError + an "Interrupted:"
// result) — distinct from a user-initiated deny so the UI doesn't
// wrongly imply the user rejected the tool.
export function toolStatus(
  tool: ClaudeToolCall,
): 'pending' | 'denied' | 'interrupted' | 'errored' | 'running' | 'done' {
  switch (tool.outcome) {
    case 'aborted':   return 'interrupted'
    case 'errored':   return 'errored'
    case 'denied':    return 'denied'
    case 'completed': return 'done'
  }
  // Not terminated, or a pre-outcome snapshot: fall back to legacy fields.
  if (tool.decision === 'deny') {
    if (tool.isError && tool.result?.startsWith('Interrupted')) return 'interrupted'
    return 'denied'
  }
  if (tool.result != null) return 'done'
  if (tool.decision === 'allow') return 'running'
  return 'pending'
}
