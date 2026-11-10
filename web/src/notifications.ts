import { useCallback, useEffect, useRef, useState } from 'react'
import type { Bootstrap, Session } from './types'
import { isActive } from './sessionState'

const baseTitle = 'Beaver'
const supported = typeof Notification !== 'undefined'

export type TaskNotifications = { permission: NotificationPermission | 'unsupported'; enable: () => void }

// 监听所有会话的状态变化：任务结束且页面不在前台时弹出系统通知，并在标签页标题上提示运行/完成状态。
export function useTaskNotifications(data: Bootstrap | null, currentSessionID: string | undefined, refresh: () => Promise<unknown>): TaskNotifications {
  const [permission, setPermission] = useState<NotificationPermission | 'unsupported'>(supported ? Notification.permission : 'unsupported')
  const previous = useRef(new Map<string, Session['status']>())
  const unseenDone = useRef(0)

  const enable = useCallback(() => {
    if (supported) void Notification.requestPermission().then(setPermission)
  }, [])

  const sessions = data?.sessions
  useEffect(() => {
    if (!sessions) return
    const hidden = document.hidden
    for (const item of sessions) {
      const before = previous.current.get(item.id)
      if (before && isActive(before) && !isActive(item.status) && item.status !== 'paused') {
        if (hidden) {
          unseenDone.current += 1
          if (supported && Notification.permission === 'granted') {
            const failed = item.status === 'failed'
            new Notification(failed ? `任务失败 · ${item.title}` : `任务完成 · ${item.title}`, { body: failed ? item.error || '请打开 Beaver 查看详情。' : '点击回到 Beaver 查看结果。', tag: item.id })
          }
        }
      }
      previous.current.set(item.id, item.status)
    }
    const running = sessions.filter((item) => isActive(item.status)).length
    document.title = unseenDone.current > 0 ? `✓ ${unseenDone.current} 个任务已完成 · ${baseTitle}` : running > 0 ? `● ${running} 个任务运行中 · ${baseTitle}` : baseTitle
  }, [sessions])

  useEffect(() => {
    const clear = () => { if (!document.hidden && unseenDone.current > 0) { unseenDone.current = 0; document.title = baseTitle } }
    document.addEventListener('visibilitychange', clear)
    window.addEventListener('focus', clear)
    return () => { document.removeEventListener('visibilitychange', clear); window.removeEventListener('focus', clear) }
  }, [])

  // 非当前会话（如微信或定时任务）没有 SSE，运行期间低频轮询以发现完成状态。
  const backgroundActive = sessions?.some((item) => item.id !== currentSessionID && isActive(item.status)) ?? false
  useEffect(() => {
    if (!backgroundActive) return
    const timer = window.setInterval(() => { void refresh().catch(() => undefined) }, 10_000)
    return () => window.clearInterval(timer)
  }, [backgroundActive, refresh])

  return { permission, enable }
}
