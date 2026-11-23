import { useEffect, useRef, useState } from 'react'
import type { Dispatch, SetStateAction } from 'react'
import { api } from '../api'
import { chatGPTCodexProvider } from '../types/model'
import type { ChatGPTAccount, ChatGPTLogin, ChatGPTModel, ModelSettings } from '../types'

type Props = {
  model: ModelSettings
  setModel: Dispatch<SetStateAction<ModelSettings>>
  setReady: (ready: boolean) => void
  onAccountsChanged: () => Promise<unknown>
}

export function ChatGPTSubscriptionFields({ model, setModel, setReady, onAccountsChanged }: Props) {
  const [accounts, setAccounts] = useState<ChatGPTAccount[]>([])
  const [models, setModels] = useState<ChatGPTModel[]>([])
  const [login, setLogin] = useState<ChatGPTLogin>()
  const [busy, setBusy] = useState(false)
  const [message, setMessage] = useState('')
  const { provider } = model
  const codexLogin = provider === chatGPTCodexProvider
  const pending = useRef('')
  const mounted = useRef(false)
  const account = accounts.find((item) => item.id === model.chatgptAccountId)
  const authorizedID = account?.planAuthorized ? account.id : ''

  useEffect(() => {
    mounted.current = true
    void api.chatGPTAccounts(provider).then(setAccounts).catch((error: Error) => setMessage(error.message))
    return () => {
      mounted.current = false
      if (pending.current) void api.cancelChatGPTLogin(pending.current, provider).catch(() => {})
    }
  }, [provider])
  useEffect(() => {
    setReady(Boolean(authorizedID))
    setModels([])
    if (!authorizedID) return
    let active = true
    void api.chatGPTModels(authorizedID, provider).then((items) => {
      if (!active) return
      setModels(items)
      if (items.length === 0) setMessage('此账号没有可选模型，请检查 ChatGPT 工作区。')
    }).catch((error: Error) => { if (active) setMessage(error.message) })
    return () => { active = false }
  }, [account, authorizedID, provider, setReady])

  useEffect(() => {
    if (!login) return
    let active = true
    let timer: number
    const poll = async () => {
      try {
        const result = await api.chatGPTLogin(login.id, provider)
        if (!active) return
        if (result.status === 'pending') {
          timer = window.setTimeout(() => { void poll() }, 1000)
          return
        }
        if (result.account) {
          const connected = result.account
          setAccounts((items) => [...items.filter((item) => item.id !== connected.id), connected])
          setModel((current) => current.provider !== provider ? current : {
            ...current, chatgptAccountId: connected.id, model: current.chatgptAccountId === connected.id ? current.model : '',
          })
          setMessage(connected.planAuthorized ? 'ChatGPT 订阅已授权。' : '请重新授权并允许使用 ChatGPT 订阅用量。')
          void onAccountsChanged().catch(() => {})
        } else {
          setMessage(result.message || 'ChatGPT 授权未完成。')
        }
      } catch (error) {
        if (active) setMessage((error as Error).message)
      }
      if (active) {
        pending.current = ''
        setLogin(undefined)
      }
    }
    timer = window.setTimeout(() => { void poll() }, 1000)
    return () => { active = false; window.clearTimeout(timer) }
  }, [login, onAccountsChanged, provider, setModel])

  const connect = async (id?: string) => {
    const tab = window.open('about:blank', '_blank')
    if (tab) tab.opener = null
    setBusy(true)
    setMessage('')
    try {
      const flow = await api.startChatGPTLogin(provider, id)
      if (!mounted.current) {
        tab?.close()
        await api.cancelChatGPTLogin(flow.id, provider)
        return
      }
      pending.current = flow.id
      setLogin(flow)
      if (tab && flow.authorizeUrl) tab.location.href = flow.authorizeUrl
    } catch (error) {
      tab?.close()
      setMessage((error as Error).message)
    } finally { setBusy(false) }
  }

  const signOut = async (id: string) => {
    setBusy(true)
    try {
      const result = await api.signOutChatGPT(id, provider)
      setAccounts((items) => items.map((item) => item.id === id ? { ...item, signedIn: false, planAuthorized: false } : item))
      setMessage(codexLogin || result.revoked ? '已断开此账号。' : '本地授权已清除。请到 ChatGPT 的登录连接中确认撤销。')
      void onAccountsChanged().catch(() => {})
    } catch (error) {
      setMessage((error as Error).message)
    } finally { setBusy(false) }
  }

  return <>
    <div className="wide">{codexLogin ? '使用自己的 ChatGPT 账号登录 Codex。模型请求计入所选账号的用量。' : '模型请求计入所选账号的套餐用量。'} <a href="https://chatgpt.com/settings/usage" target="_blank" rel="noreferrer">管理用量</a></div>
    <label className="wide">ChatGPT 账号
      <select value={model.chatgptAccountId || ''} disabled={busy || Boolean(login)} onChange={(event) => setModel({ ...model, chatgptAccountId: event.target.value, model: '' })}>
        <option value="">选择账号</option>
        {model.chatgptAccountId && !account && <option value={model.chatgptAccountId}>找不到这份授权</option>}
        {accounts.map((item) => {
          const plan = item.plan === 'ent26' ? 'Enterprise' : item.plan
          const sharedLabel = accounts.some((other) => other.id !== item.id && other.email === item.email && other.plan === item.plan)
          const label = plan && !sharedLabel ? plan : [plan, item.id.slice(0, 8)].filter(Boolean).join(' · ')
          return <option key={item.id} value={item.id}>{item.email || 'ChatGPT'} · {label}{!item.planAuthorized && ' · 待授权'}</option>
        })}
      </select>
    </label>
    <div className="form-actions wide">
      <button type="button" className="ghost-button" disabled={busy || Boolean(login)} onClick={() => { void connect() }}>{codexLogin ? (accounts.length ? '添加账号' : '登录 ChatGPT') : 'Continue with ChatGPT'}</button>
      {account && <button type="button" className="ghost-button" disabled={busy || Boolean(login)} onClick={() => { void connect(account.id) }}>重新授权</button>}
      {account?.signedIn && <button type="button" className="ghost-button" disabled={busy || Boolean(login)} onClick={() => { void signOut(account.id) }}>断开账号</button>}
    </div>
    {login && <div className="wide" role="status">
      <a href={login.authorizeUrl} target="_blank" rel="noreferrer">在 ChatGPT 中完成授权</a>
      <button type="button" className="ghost-button" onClick={() => {
        pending.current = ''
        setLogin(undefined)
        void api.cancelChatGPTLogin(login.id, provider).catch((error: Error) => setMessage(error.message))
      }}>取消授权</button>
    </div>}
    {message && <p className="wide" role="status">{message}</p>}
    <label className="wide">模型名称
      <select value={model.model} disabled={!authorizedID} onChange={(event) => setModel({ ...model, model: event.target.value })}>
        <option value="">{authorizedID && models.length === 0 ? '等待可用模型' : '选择模型'}</option>
        {model.model && !models.some((item) => item.slug === model.model) && <option value={model.model}>{model.model}</option>}
        {models.map((item) => <option key={item.slug} value={item.slug}>{item.display_name || item.slug}</option>)}
      </select>
    </label>
  </>
}
