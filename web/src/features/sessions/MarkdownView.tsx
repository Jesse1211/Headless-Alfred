import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import type { ComponentPropsWithoutRef } from 'react'
import './MarkdownView.css'

interface Props {
  text: string
  className?: string
}

// MarkdownView renders a markdown string into the dark-mode styles
// shared by SummarySidebar and RecapSidebar. Code blocks use simple
// inline styling (no syntax highlighter — the content here is short
// summaries / recaps, not code-heavy).
export function MarkdownView({ text, className }: Props) {
  return (
    <div className={`markdown-view ${className ?? ''}`}>
      <ReactMarkdown
        remarkPlugins={[remarkGfm]}
        components={{
          code(props) {
            const { className, children, ...rest } =
              props as ComponentPropsWithoutRef<'code'>
            return <code className={className} {...rest}>{children}</code>
          },
        }}
      >
        {text}
      </ReactMarkdown>
    </div>
  )
}
