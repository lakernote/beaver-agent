import { lazy, Suspense } from 'react'
import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import remarkMath from 'remark-math'
import type { Session } from '../types'
import { formatBytes } from '../attachments'
import { FileIcon, Logo } from '../ui'
import { markdownComponents } from '../markdownComponents'
import { remarkRepairModelMarkdown } from '../remarkRepairModelMarkdown'
import { Payload } from './Payload'
import { capabilityResultLabel, describeToolCall, SelectedCapabilities } from './CapabilityActivity'

const MathMarkdown = lazy(() => import('../MathMarkdown'))
const hasMath = (value: string) => /\$\$[\s\S]+?\$\$|\$[^$\n]+?\$/.test(value)

export function MessageView({ message, relatedCall }: { message: Session['messages'][number]; relatedCall?: Session['messages'][number]['toolCalls'][number] }) {
	if (message.role === 'tool') return <details className={`tool-result ${relatedCall ? describeToolCall(relatedCall).kind : ''}`}><summary><span>⌁</span>{capabilityResultLabel(relatedCall, message.name || '工具')}</summary><ToolResult value={message.content || ''} result={message.toolResult} /></details>
  if (message.role === 'user') return <div className="user-row"><div className="user-message">{message.attachments?.length > 0 && <MessageAttachments attachments={message.attachments} />}<SelectedCapabilities message={message} />{message.content && <div>{message.content}</div>}</div></div>
  if (message.role !== 'assistant') return null
  return <div className="assistant-row"><Avatar /><div className="assistant-message">{message.toolCalls?.length > 0 && <div className="tool-intent">{message.toolCalls.map((call) => { const item = describeToolCall(call); return <span className={item.kind} key={call.id}><b>{item.label}</b>{item.name}</span> })}</div>}{message.content && <div className="answer-text"><Markdown>{message.content}</Markdown></div>}</div></div>
}

function ToolResult({ value, result }: { value: string; result?: Session['messages'][number]['toolResult'] }) {
  return result ? <TypedToolResult result={result} fallback={value} /> : <Payload value={value} />
}

type TypedResult = NonNullable<Session['messages'][number]['toolResult']>

function TypedToolResult({ result, fallback }: { result: TypedResult; fallback: string }) {
  const blocks = Array.isArray(result.content) ? result.content : []
  const hasStructured = Object.prototype.hasOwnProperty.call(result, 'structuredContent')
  return <div className="typed-tool-result">
    {result.error && <div className="tool-result-error"><strong>{result.error.code || 'tool_error'}</strong><span>{result.error.message}</span>{result.error.hint && <small>{result.error.hint}</small>}</div>}
    {result.truncation && <div className="tool-result-truncation">模型上下文已按 {result.truncation.strategy} 压缩：保留 {result.truncation.retainedTokens} / {result.truncation.originalTokens} Token</div>}
    {hasStructured && <Payload value={JSON.stringify(result.structuredContent)} />}
    {blocks.map((block, index) => <ToolResultBlock block={block} key={`${block.artifactId || block.uri || block.name || block.type}-${index}`} />)}
    {!hasStructured && blocks.length === 0 && !result.error && <Payload value={fallback} />}
  </div>
}

function ToolResultBlock({ block }: { block: NonNullable<TypedResult['content']>[number] }) {
  if (block.type === 'text') return <Payload value={block.text || ''} />
  if (block.type === 'json') return <Payload value={JSON.stringify(block.json)} />
  if (block.artifactId) {
    const source = `/api/v1/attachments/${encodeURIComponent(block.artifactId)}`
    if (block.type === 'image') return <figure className="tool-result-artifact"><img src={source} alt={block.name || '工具返回图片'} loading="lazy" /><figcaption>{block.name || block.mimeType || '图片'}</figcaption></figure>
    if (block.type === 'audio') return <div className="tool-result-artifact"><audio controls preload="none" src={source} /><span>{block.name || block.mimeType || '音频'}</span></div>
    return <a className="tool-result-artifact-link" href={source} target="_blank" rel="noopener noreferrer">{block.name || block.mimeType || '打开工具产物'}</a>
  }
  if (block.uri && safeHttpURL(block.uri)) return <a href={block.uri} target="_blank" rel="noopener noreferrer">{block.name || block.uri}</a>
  return <Payload value={JSON.stringify(block)} />
}

function safeHttpURL(value?: string) {
  if (!value) return ''
  try {
    const parsed = new URL(value)
    return parsed.protocol === 'http:' || parsed.protocol === 'https:' ? parsed.toString() : ''
  } catch {
    return ''
  }
}

function MessageAttachments({ attachments }: { attachments: Session['messages'][number]['attachments'] }) {
  return <div className="message-attachments">{attachments.map((attachment) => {
    const source = `/api/v1/attachments/${encodeURIComponent(attachment.id)}`
    if (attachment.kind === 'image') return <a key={attachment.id} className="message-image" href={source} target="_blank" rel="noreferrer" title={`查看 ${attachment.name}`}><img src={source} alt={attachment.name} loading="lazy" /><span>{attachment.name}</span></a>
    if (attachment.kind === 'audio') return <div key={attachment.id} className="message-audio"><div><strong>{attachment.name}</strong><small>微信语音 · {formatBytes(attachment.size)}</small></div><audio controls preload="metadata" src={source}>浏览器不支持播放音频。</audio></div>
    return <a key={attachment.id} className="message-file" href={source} target="_blank" rel="noreferrer" download={attachment.name}><FileIcon /><span><strong>{attachment.name}</strong><small>{attachment.kind === 'pdf' ? 'PDF' : '文本文件'} · {formatBytes(attachment.size)}</small></span></a>
  })}</div>
}

export function Avatar() { return <div className="avatar"><Logo /></div> }

export function Markdown({ children }: { children: string }) {
  if (hasMath(children)) return <Suspense fallback={<ReactMarkdown remarkPlugins={[remarkGfm, remarkMath, remarkRepairModelMarkdown]} components={markdownComponents}>{children}</ReactMarkdown>}><MathMarkdown>{children}</MathMarkdown></Suspense>
  return <ReactMarkdown remarkPlugins={[remarkGfm, remarkRepairModelMarkdown]} components={markdownComponents}>{children}</ReactMarkdown>
}
