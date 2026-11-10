import { FormEvent, useState } from 'react'
import { api } from './api'
import { Logo } from './ui'

type LoginPageProps = { onLogin: () => Promise<void>; initialError?: string }

export function LoginPage({ onLogin, initialError = '' }: LoginPageProps) {
  const [username, setUsername] = useState('admin')
  const [password, setPassword] = useState('')
  const [error, setError] = useState(initialError)
  const [submitting, setSubmitting] = useState(false)
  const [reveal, setReveal] = useState(false)

  const submit = async (event: FormEvent) => {
    event.preventDefault()
    if (submitting) return
    setError('')
    setSubmitting(true)
    try {
      await api.login(username, password)
      await onLogin()
    } catch (reason) {
      setError((reason as Error).message)
    } finally {
      setSubmitting(false)
    }
  }

  return <main className="login-page">
    <section className="login-panel" aria-labelledby="login-title">
      <div className="login-brand"><span className="login-logo"><Logo /></span><span><strong>Beaver</strong><small>SELF-HOSTED AGENT WORKBENCH</small></span></div>
      <div className="login-copy"><p className="login-kicker">自托管 Agent 工作台</p><h1 id="login-title">登录 Beaver</h1><p>在你自己的服务器或电脑上运行研发、测试和运维任务，并保留完整执行记录。</p></div>
      <form onSubmit={submit} className="login-form">
        <label>用户名<input autoFocus name="username" autoComplete="username" value={username} onChange={(event) => setUsername(event.target.value)} /></label>
        <label>密码<span className="password-field"><input name="password" type={reveal ? 'text' : 'password'} autoComplete="current-password" value={password} onChange={(event) => setPassword(event.target.value)} /><button type="button" className="password-reveal" onClick={() => setReveal(!reveal)} aria-pressed={reveal}>{reveal ? '隐藏' : '显示'}</button></span></label>
        {error && <p className="login-error" role="alert">{error}</p>}
        <button className="login-submit" type="submit" disabled={submitting || !username.trim() || !password}>{submitting ? '正在登录…' : '登录'}</button>
      </form>
      <details className="login-hint"><summary>第一次使用，或忘记密码？</summary><ol><li>用户名是 <code>admin</code>，初始密码在 Beaver 启动时打印在终端日志里（只显示一次）。</li><li>忘记或日志已丢失：停止服务后运行 <code>beaver -reset-password</code> 生成新密码。</li></ol></details>
    </section>
  </main>
}
