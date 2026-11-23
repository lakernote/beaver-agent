import type { Session } from './types'
export function formatTime(value: string) { const date = new Date(value); return date.toLocaleDateString('zh-CN', { month: 'numeric', day: 'numeric' }) + ' ' + date.toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit' }) }
export function statusLabel(value: Session['status']) { return value === 'idle' ? '完成' : value === 'queued' ? '排队中' : value === 'paused' ? '已暂停' : value === 'running' ? '运行中' : value === 'failed' ? '失败' : '已停止' }
export function formatDuration(ms: number) { return ms > 1000 ? `${(ms / 1000).toFixed(1)}s` : `${ms}ms` }
export function formatTokens(value: number) { return value >= 1000 ? `${(value / 1000).toFixed(value >= 10000 ? 0 : 1)}k` : value.toLocaleString() }
export function historyModeLabel(value: string) {
  switch (value) {
    case 'full_history': return '完整历史'
    case 'provider_continuation': return 'Provider 续接'
    case 'responses_full_input': return 'Responses 全量'
    case 'summary_history': return '摘要 + 最近历史'
    case 'codex_tool_callback': return 'Codex 工具回传'
    default: return '等待识别'
  }
}
export function recordLines(value: Record<string, string>) { return Object.entries(value || {}).map(([key, item]) => `${key}=${item}`).join('\n') }
export function parseRecord(value: string) { const result: Record<string, string> = {}; value.split('\n').forEach((line) => { const index = line.indexOf('='); if (index > 0) result[line.slice(0, index).trim()] = line.slice(index + 1) }); return result }
