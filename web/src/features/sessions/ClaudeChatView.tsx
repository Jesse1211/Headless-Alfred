import { useEffect, useRef, useState } from 'react'
import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { Prism as SyntaxHighlighter } from 'react-syntax-highlighter'
import { oneDark } from 'react-syntax-highlighter/dist/esm/styles/prism'
import type { ClaudeState, ClaudeToolCall, ClaudeTurn } from './types'
import { ToolApprovalCard } from './ToolApprovalCard'
import { AskUserQuestionCard } from './AskUserQuestionCard'
import { isSubmitKey } from '../../lib/keyboard'
import './ClaudeChatView.css'

interface Props {
  state: ClaudeState
  disabled: boolean
  onPrompt: (text: string) => void
  onToolDecision: (toolUseId: string, decision: 'allow' | 'deny', reason?: string) => void
  onQuestionAnswer: (toolUseId: string, formattedAnswer: string) => void
  onInterrupt: () => void
}

// ClaudeChatView is the V1 ChatGPT-style renderer for a Claude conversation.
// Renders assistant text as markdown (via react-markdown + remark-gfm),
// surfaces tool calls inline, and floats pending approval cards at the bottom.
export function ClaudeChatView({
  state, disabled, onPrompt, onToolDecision, onQuestionAnswer, onInterrupt,
}: Props) {
  const [draft, setDraft] = useState('')
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
    onPrompt(text)
    setDraft('')
  }

  return (
    <div className="claude-chat">
      <div className="claude-chat__scroll" ref={scrollRef}>
        {state.turns.length === 0 && !state.inFlight && (
          <div className="claude-chat__empty">
            Start a conversation with Claude. All tool use will ask before running.
          </div>
        )}
        {state.turns.map((t) => (
          <TurnView key={t.id} turn={t} />
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

      <div className="claude-chat__composer">
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
      </div>
    </div>
  )
}

function TurnView({ turn }: { turn: ClaudeTurn }) {
  return (
    <div className="claude-turn">
      <div className="claude-turn__user">
        <div className="claude-turn__label">You</div>
        <div className="claude-turn__user-text">{turn.prompt}</div>
      </div>
      <div className={`claude-turn__assistant ${turn.isError ? 'is-error' : ''}`}>
        <div className="claude-turn__label">Claude</div>
        {turn.text && (
          <div className="claude-turn__text">
            <ReactMarkdown
              remarkPlugins={[remarkGfm]}
              components={{
                // Custom code renderer: fenced blocks get Prism syntax
                // highlighting (oneDark theme matches our background);
                // inline code keeps the simple span styling from CSS.
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
            >{turn.text}</ReactMarkdown>
          </div>
        )}
        {turn.tools.map((tool) => (
          <ToolCallView key={tool.toolUseId} tool={tool} />
        ))}
        {!turn.done && !turn.text && turn.tools.length === 0 && (
          <div className="claude-turn__thinking">…</div>
        )}
        {turn.done && turn.usage && (
          <div className="claude-turn__footer">
            {turn.totalCostUsd != null && (
              <span title="Total cost for this turn">${turn.totalCostUsd.toFixed(4)}</span>
            )}
            <span title="Input → output tokens">
              {turn.usage.inputTokens.toLocaleString()} in → {turn.usage.outputTokens.toLocaleString()} out
            </span>
          </div>
        )}
      </div>
    </div>
  )
}

function ToolCallView({ tool }: { tool: ClaudeToolCall }) {
  const status = toolStatus(tool)
  return (
    <div className={`claude-tool claude-tool--${status} ${tool.isError ? 'is-error' : ''}`}>
      <div className="claude-tool__header">
        <code className="claude-tool__name">{tool.name}</code>
        <span className="claude-tool__status">{status}</span>
      </div>
      {tool.input != null && (
        <pre className="claude-tool__input">{formatJSON(tool.input)}</pre>
      )}
      {tool.result != null && tool.result !== '' && (
        <pre className="claude-tool__result">{tool.result}</pre>
      )}
    </div>
  )
}

function formatJSON(v: unknown): string {
  try { return JSON.stringify(v, null, 2) } catch { return String(v) }
}

// toolStatus picks a single status label out of three independent
// pieces of state: the user's decision (deny|allow|pending) and
// whether a result has come back. Order matters — a denied tool
// never produces a result, and an allowed tool is "running" until
// the result arrives.
function toolStatus(tool: ClaudeToolCall): 'pending' | 'denied' | 'running' | 'done' {
  if (tool.decision === 'deny') return 'denied'
  if (tool.result != null) return 'done'
  if (tool.decision === 'allow') return 'running'
  return 'pending'
}
