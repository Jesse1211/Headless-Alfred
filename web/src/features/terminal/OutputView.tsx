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
