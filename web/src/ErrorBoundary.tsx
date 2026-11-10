import { Component, type ErrorInfo, type ReactNode } from 'react'

type State = { error: Error | null }

// 渲染异常或懒加载页面失败（例如升级后旧资源被替换）时，给出可恢复的界面，而不是白屏。
export class ErrorBoundary extends Component<{ children: ReactNode }, State> {
  state: State = { error: null }

  static getDerivedStateFromError(error: Error): State {
    return { error }
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error('Beaver UI error', error, info.componentStack)
  }

  render() {
    if (!this.state.error) return this.props.children
    return <main className="boot error-boundary" role="alert">
      <h1>页面出现了问题</h1>
      <p>{this.state.error.message || '未知错误'}</p>
      <p>会话和任务仍在服务器上运行，不会因此中断。刷新页面即可继续。</p>
      <button type="button" className="primary-button" onClick={() => window.location.reload()}>刷新页面</button>
    </main>
  }
}
