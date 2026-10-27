import { useEffect, useRef, useState, type FormEvent, type KeyboardEvent } from 'react'
import { APIError, ask, listProducts } from './api'
import { Answer, versionLabel } from './Answer'
import type { AskResult, Product } from './types'

interface Entry {
  id: number
  question: string
  product: string
  version: string
  status: 'pending' | 'answered' | 'failed' | 'canceled'
  result?: AskResult
  error?: string
  refreshDirectory?: boolean
  runID?: string
}

export default function App() {
  const [products, setProducts] = useState<Product[]>([])
  const [directoryState, setDirectoryState] = useState<'loading' | 'ready' | 'failed'>('loading')
  const [directoryError, setDirectoryError] = useState('')
  const [productID, setProductID] = useState('')
  const [version, setVersion] = useState<string | undefined>(undefined)
  const [draft, setDraft] = useState('')
  const [entries, setEntries] = useState<Entry[]>([])
  const [pending, setPending] = useState(false)
  const active = useRef<{ id: number; controller: AbortController } | null>(null)
  const directoryController = useRef<AbortController | null>(null)
  const sequence = useRef(0)
  const textarea = useRef<HTMLTextAreaElement>(null)
  const composing = useRef(false)
  const bottom = useRef<HTMLDivElement>(null)
  const product = products.find(p => p.product_id === productID)
  const selectedVersion = product?.versions.find(v => v.version === version)
  const count = Array.from(draft).length
  const canSend = directoryState === 'ready' && selectedVersion?.compatible && draft.trim() !== '' && count <= 2000 && !pending

  async function loadDirectory() {
    directoryController.current?.abort()
    const controller = new AbortController()
    directoryController.current = controller
    setDirectoryState('loading')
    const timer = window.setTimeout(() => controller.abort(), 15_000)
    try {
      const items = await listProducts(controller.signal)
      if (directoryController.current !== controller) return
      setProducts(items)
      // Reload asks the user to choose again; it must never silently change versions.
      setProductID(''); setVersion(undefined)
      setDirectoryState('ready')
    } catch (error) {
      if (directoryController.current !== controller) return
      setDirectoryError(controller.signal.aborted ? '加载产品目录超时，请重试。' : error instanceof Error ? error.message : '加载产品目录失败。')
      setDirectoryState('failed')
    } finally { window.clearTimeout(timer) }
  }

  useEffect(() => {
    void loadDirectory()
    return () => {
      directoryController.current?.abort(); directoryController.current = null
      active.current?.controller.abort(); active.current = null
    }
  }, [])
  useEffect(() => { bottom.current?.scrollIntoView?.({ behavior: 'smooth', block: 'end' }) }, [entries])

  function chooseProduct(id: string) {
    setProductID(id)
    const available = products.find(p => p.product_id === id)?.versions.filter(v => v.compatible) || []
    setVersion(available.length === 1 ? available[0].version : undefined)
  }
  function updateEntry(id: number, patch: Partial<Entry>) {
    setEntries(old => old.map(entry => entry.id === id ? { ...entry, ...patch } : entry))
  }
  async function send(event?: FormEvent) {
    event?.preventDefault()
    if (!canSend || active.current || !product || version === undefined) return
    const id = ++sequence.current
    const controller = new AbortController()
    active.current = { id, controller }
    setPending(true)
    setEntries(old => [...old, { id, question: draft.trim(), product: product.name?.trim() || product.product_id, version, status: 'pending' }])
    const timer = window.setTimeout(() => controller.abort(), 125_000)
    try {
      const result = await ask({ product_id: productID, version, question: draft.trim(), top_k: 5, mode: 'hnsw' }, controller.signal)
      if (active.current?.id !== id) return
      if (controller.signal.aborted) throw new Error('等待超时，请检查后台运行记录后再决定是否重新提问。')
      updateEntry(id, { status: 'answered', result })
      setDraft('')
    } catch (error) {
      if (active.current?.id !== id) return
      updateEntry(id, {
        status: 'failed',
        error: controller.signal.aborted ? '等待超时，后台运行记录可能已经产生。请检查服务后再重新提问。' : error instanceof Error ? error.message : '提问失败，请稍后再试。',
        refreshDirectory: error instanceof APIError && (error.status === 404 || error.status === 409),
        runID: error instanceof APIError ? error.runID : undefined,
      })
    } finally {
      window.clearTimeout(timer)
      if (active.current?.id === id) { active.current = null; setPending(false); window.setTimeout(() => textarea.current?.focus(), 0) }
    }
  }
  function stopWaiting() {
    const request = active.current
    if (!request) return
    active.current = null
    request.controller.abort()
    updateEntry(request.id, { status: 'canceled' })
    setPending(false)
    textarea.current?.focus()
  }
  function onKeyDown(event: KeyboardEvent<HTMLTextAreaElement>) {
    if (event.key === 'Enter' && !event.shiftKey && !event.nativeEvent.isComposing && !composing.current && event.keyCode !== 229) {
      event.preventDefault(); void send()
    }
  }

  return <div className="app-shell">
    <header className="site-header"><div className="brand"><span className="brand-mark" aria-hidden="true">保</span><div><strong>保险 Agent</strong><span>保单问答</span></div></div><span className="local-label">本地工作台</span></header>
    <main>
      <section className="intro"><p className="eyebrow">有据可查，读懂保障</p><h1>你的保单，逐条解答。</h1><p>基于已导入的保险条款回答，每条引用都能展开核对原文。</p></section>
      <section className="product-bar" aria-label="选择保险产品">
        <div className="field"><label htmlFor="product">保险产品</label><select id="product" value={productID} disabled={pending || directoryState !== 'ready'} onChange={e => chooseProduct(e.target.value)}>
          <option value="">请选择产品</option>{products.map(p => <option key={p.product_id} value={p.product_id}>{p.name?.trim() || p.product_id}</option>)}
        </select></div>
        <div className="field"><label htmlFor="version">条款版本</label><select id="version" value={version === undefined ? '' : JSON.stringify(version)} disabled={pending || !product || directoryState !== 'ready'} onChange={e => setVersion(e.target.value === '' ? undefined : JSON.parse(e.target.value))}>
          <option value="">请选择版本</option>{product?.versions.map(v => <option key={v.version} value={JSON.stringify(v.version)} disabled={!v.compatible}>{versionLabel(v.version)} · {v.document_count} 份文档{!v.compatible ? '（索引配置不兼容）' : ''}</option>)}
        </select></div>
        <button type="button" className="quiet-button" disabled={pending || directoryState === 'loading'} onClick={() => void loadDirectory()}>刷新目录</button>
      </section>
      <div className="directory-notice" role="status">
        {directoryState === 'loading' && '正在加载产品目录…'}
        {directoryState === 'failed' && <><span>{directoryError}</span> <button type="button" className="text-button" onClick={() => void loadDirectory()}>重新加载</button></>}
        {directoryState === 'ready' && products.length === 0 && <>暂无已完成索引的保单，请先导入并完成索引。 <button type="button" className="text-button" onClick={() => void loadDirectory()}>重新加载</button></>}
        {directoryState === 'ready' && product && !product.versions.some(v => v.compatible) && '该产品暂无兼容的条款版本，请检查服务的索引配置。'}
      </div>
      <section className="conversation" aria-label="问答记录">
        <div className="conversation-heading"><h2>保单对话</h2><button type="button" className="quiet-button" disabled={pending || entries.length === 0} onClick={() => setEntries([])}>清空对话</button></div>
        {entries.length === 0 && <div className="welcome"><span className="welcome-icon" aria-hidden="true">条款 · 证据 · 回答</span><h3>从一个具体问题开始</h3><p>先选择产品和版本，再问等待期、保险责任或免责条款。</p><div className="example-questions">例如：“等待期是多久？” “哪些情况不予赔付？”</div><p className="independent-note">每次提问独立，请写完整问题。</p></div>}
        <div className="messages">{entries.map(entry => <article key={entry.id} className="exchange" aria-label={`问题：${entry.question}`}>
          <div className="message-meta">{entry.product} · {versionLabel(entry.result?.version ?? entry.version)}</div>
          <div className="question"><span className="message-label">你</span><p className="plain-text">{entry.question}</p></div>
          <div className={`answer ${entry.status === 'failed' ? 'answer-error' : ''}`}><span className="message-label">保单助手</span>
            {entry.status === 'pending' && <p role="status" className="pending"><span className="pulse" />正在查找条款并核对回答…</p>}
            {entry.status === 'answered' && entry.result && <Answer result={entry.result} entryID={entry.id} />}
            {entry.status === 'canceled' && <p role="status">已停止等待。后台运行记录可能已经产生，问题已保留在输入框。</p>}
            {entry.status === 'failed' && <div role="alert"><p>{entry.error}</p>{entry.refreshDirectory && <p>产品或版本可能已变化，请刷新目录并重新选择后提问。 <button type="button" className="text-button" disabled={pending} onClick={() => void loadDirectory()}>刷新产品目录</button></p>}{entry.runID && <p className="muted">运行 ID：{entry.runID}</p>}</div>}
          </div>
        </article>)}<div ref={bottom} /></div>
      </section>
      <form className="composer" onSubmit={e => void send(e)}>
        <label htmlFor="question">你的问题</label>
        <textarea id="question" ref={textarea} value={draft} disabled={pending} rows={3} placeholder="请写完整问题，例如：这份保单的等待期是多久？" aria-describedby="question-help question-count" aria-invalid={count > 2000}
          onChange={e => setDraft(e.target.value)} onKeyDown={onKeyDown} onCompositionStart={() => { composing.current = true }} onCompositionEnd={() => { composing.current = false }} />
        <div className="composer-footer"><span id="question-help">每次提问独立，请写完整问题。<span className="keyboard-help"> Enter 发送 · Shift+Enter 换行</span></span><span id="question-count" className={count > 2000 ? 'over-limit' : ''}>{count} / 2000</span>
          {pending ? <button type="button" className="primary-button" onClick={stopWaiting}>停止等待</button> : <button type="submit" className="primary-button" disabled={!canSend}>发送问题 <span aria-hidden="true">↑</span></button>}
        </div>
        {count > 2000 && <p role="alert" className="over-limit">问题最多 2,000 个字符，请缩短后发送。</p>}
      </form>
      <footer className="page-footer">回答供理解条款参考，正式核保、核赔请联系保险公司。刷新页面将清空对话，后台问答记录仍保留。</footer>
    </main>
  </div>
}
