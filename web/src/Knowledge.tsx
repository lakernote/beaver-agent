import { useEffect, useRef, useState } from 'react'
import { api } from './api'
import type { KnowledgeDoc } from './types'

// 团队知识库：上传 .md/.txt，Agent 通过 knowledge_search 工具检索。
export function Knowledge({ onError }: { onError: (value: string) => void }) {
  const [docs, setDocs] = useState<KnowledgeDoc[]>([])
  const [busy, setBusy] = useState(false)
  const input = useRef<HTMLInputElement>(null)

  const reload = () => api.knowledge().then(setDocs).catch((error) => onError(String(error.message || error)))
  useEffect(() => { void reload() }, [])

  const upload = async (files: FileList | null) => {
    if (!files?.length) return
    setBusy(true)
    try {
      for (const file of Array.from(files)) await api.uploadKnowledge(file.name, await file.text())
    } catch (error) {
      onError(String((error as Error).message || error))
    } finally {
      setBusy(false)
      if (input.current) input.current.value = ''
      await reload()
    }
  }
  const remove = async (name: string) => {
    try { await api.deleteKnowledge(name) } catch (error) { onError(String((error as Error).message || error)) }
    await reload()
  }

  return <section className="section-block">
    <div className="section-heading"><div><h2>团队知识库</h2><p>上传 .md / .txt 文档，全队共享；Agent 回答相关问题时会检索并标注出处。也可在微信发送文件并附文字 /kb。</p></div><span className="tag">{docs.length} 份</span></div>
    <input ref={input} type="file" accept=".md,.txt" multiple hidden onChange={(event) => void upload(event.target.files)} />
    <button className="ghost-button" type="button" disabled={busy} onClick={() => input.current?.click()}>{busy ? '上传中…' : '上传文档'}</button>
    <ul className="knowledge-list">
      {docs.map((doc) => <li key={doc.name}><strong>{doc.name}</strong> <small>{(doc.size / 1024).toFixed(1)} KB</small> <button className="ghost-button" type="button" onClick={() => void remove(doc.name)}>删除</button></li>)}
      {!docs.length && <li><small>还没有文档。</small></li>}
    </ul>
  </section>
}
