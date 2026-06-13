import { useEffect, useMemo, useRef } from 'react'
import AnsiToHtml from 'ansi-to-html'
import { CompletedMsg, RunningCmd } from './types'

// One instance is enough — the converter is stateless across toHtml calls.
// escapeXML:true is REQUIRED for safety: it tells the lib to HTML-escape
// the raw text before injecting its own <span> tags for SGR codes.
// Without it, command output containing literal '<script>' would render
// as a real script tag when we dangerouslySetInnerHTML below.
const ansi = new AnsiToHtml({
  escapeXML: true,
  newline: false,
  stream: false,
  // A palette that reads well on the dark surface used by the chat.
  fg: '#e6e6e6',
  bg: 'transparent',
  colors: {
    0: '#000000',
    1: '#e06c75', // red
    2: '#98c379', // green
    3: '#e5c07b', // yellow
    4: '#61afef', // blue
    5: '#c678dd', // magenta
    6: '#56b6c2', // cyan
    7: '#dcdfe4',
    8: '#5c6370',
    9: '#e06c75',
    10: '#98c379',
    11: '#e5c07b',
    12: '#61afef',
    13: '#c678dd',
    14: '#56b6c2',
    15: '#ffffff',
  },
})

function renderAnsi(text: string): string {
  return ansi.toHtml(text)
}

interface Props {
  messages: CompletedMsg[]
  running: RunningCmd | null
}

export default function ChatStream({ messages, running }: Props) {
  const endRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    endRef.current?.scrollIntoView({ behavior: 'smooth', block: 'end' })
  }, [messages.length, running?.id])

  // For streaming chunks, jump to bottom without smooth (avoids fighting the
  // animation while output arrives ~10x/sec).
  useEffect(() => {
    endRef.current?.scrollIntoView({ block: 'end' })
  }, [running?.output])

  const isEmpty = messages.length === 0 && !running

  return (
    <div className="chat-stream">
      <div className="chat-stream__inner">
        {isEmpty && (
          <div className="chat-stream__empty">
            <h1>Headless Alfred</h1>
            <p>Type a command below. Output streams here. Close the tab any time — commands keep running.</p>
          </div>
        )}

        {messages.map((m) => (
          <MessageTurn key={m.id} msg={m} />
        ))}

        {running && (
          <div className="msg-turn msg-turn--live">
            <UserBubble text={running.command} />
            <AssistantBlock
              output={running.output}
              live
            />
          </div>
        )}

        <div ref={endRef} />
      </div>
    </div>
  )
}

function MessageTurn({ msg }: { msg: CompletedMsg }) {
  return (
    <div className="msg-turn">
      <UserBubble text={msg.command} />
      <AssistantBlock
        output={msg.output}
        exitCode={msg.exitCode}
        status={msg.status}
        truncated={msg.truncated}
      />
    </div>
  )
}

function UserBubble({ text }: { text: string }) {
  return (
    <div className="msg msg--user">
      <div className="msg__bubble">{text || '(empty)'}</div>
    </div>
  )
}

interface AssistantProps {
  output: string
  live?: boolean
  exitCode?: number
  status?: string
  truncated?: boolean
}

function normalizeOutput(s: string): string {
  // PTYs emit CRLF line endings; in a <pre> the lone CR causes the next line
  // to overwrite the previous one in some browsers. Normalize and trim a
  // single leading blank line that comes from stty/term setup.
  return s.replace(/\r\n/g, '\n').replace(/\r/g, '').replace(/^\n/, '')
}

function AssistantBlock({ output, live, exitCode, status, truncated }: AssistantProps) {
  const normalized = normalizeOutput(output)
  const hasOutput = normalized.length > 0
  const failed = !live && exitCode != null && exitCode !== 0
  // Memoize: parsing ANSI is O(n) over the full buffer, and chunks
  // arrive ~10x/sec for a live command.
  const html = useMemo(() => (hasOutput ? renderAnsi(normalized) : ''), [normalized, hasOutput])
  return (
    <div className="msg msg--assistant">
      {hasOutput && (
        <pre
          className="msg__output"
          dangerouslySetInnerHTML={{ __html: html }}
        />
      )}
      {!hasOutput && live && <div className="msg__hint">running…</div>}
      {!hasOutput && !live && <div className="msg__hint">(no output)</div>}
      <div className="msg__meta">
        {live && <span className="meta-live">● live</span>}
        {!live && status === 'stopped' && <span className="meta-warn">stopped</span>}
        {!live && status === 'interrupted' && <span className="meta-warn">interrupted</span>}
        {!live && exitCode != null && (
          <span className={failed ? 'meta-err' : 'meta-ok'}>exit {exitCode}</span>
        )}
        {truncated && <span className="meta-warn">output truncated</span>}
      </div>
    </div>
  )
}
