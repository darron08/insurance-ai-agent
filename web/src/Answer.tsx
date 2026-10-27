import { Fragment, useState } from 'react'
import type { AskResult, Citation } from './types'

export const versionLabel = (version: string) => version === '' ? '未标注版本' : version
export const reasonLabels: Record<string, string> = {
  insufficient_evidence: '证据不足', needs_clarification: '信息待补充', conflicting_evidence: '资料冲突',
  out_of_scope: '超出范围', formal_decision: '需正式核保或核赔',
}

function sourceURL(value: string): string | undefined {
  try {
    const url = new URL(value)
    if (url.protocol === 'https:' || url.protocol === 'http:') return url.href
  } catch { /* Display other source values as text only. */ }
}

function Evidence({ citation }: { citation: Citation }) {
  const { source } = citation
  const href = sourceURL(source.source_url)
  return <div className="evidence">
    <div className="evidence-heading">{source.document_name}</div>
    <div className="muted">{versionLabel(source.document_version)} · PDF 第 {source.page_number} 页{source.clause_id ? ` · ${source.clause_id}` : ''}</div>
    <h4>引用原文</h4><blockquote>{citation.quote}</blockquote>
    <details><summary>查看完整条款片段</summary><p className="plain-text">{source.text}</p></details>
    <p className="source-link">来源：{href ? <a href={href} target="_blank" rel="noopener noreferrer">{source.source_url}</a> : (source.source_url || '未提供来源地址')}</p>
    <details><summary>来源详情</summary><dl><dt>SHA-256</dt><dd>{source.source_sha256}</dd><dt>Chunk ID</dt><dd>{source.chunk_id}</dd></dl></details>
  </div>
}

export function Answer({ result, entryID }: { result: AskResult; entryID: number }) {
  const [opened, setOpened] = useState<number | null>(null)
  const toggle = (id: number) => setOpened(current => current === id ? null : id)
  const citations = new Map(result.citations.map(c => [c.id, c]))
  function reference(id: number, label: string, key: string | number) {
    return <button key={key} type="button" className="cite-button" aria-label={`查看引用 ${id}`} aria-expanded={opened === id}
      aria-controls={`evidence-${entryID}-${id}`} onClick={() => toggle(id)}>{label}</button>
  }
  return <>
    {result.abstained && <div className="reason">{reasonLabels[result.reason || ''] || '暂无法回答'}</div>}
    <div className="answer-text plain-text">{result.answer.split(/(\[[0-9]+\])/g).map((part, i) => {
      const match = /^\[([0-9]+)\]$/.exec(part)
      return match && citations.has(Number(match[1])) ? reference(Number(match[1]), part, i) : <Fragment key={i}>{part}</Fragment>
    })}</div>
    {result.citations.length > 0 && <div className="citation-list" aria-label="回答引用">原文依据 {result.citations.map(c => reference(c.id, `[${c.id}] 第 ${c.source.page_number} 页`, c.id))}</div>}
    {result.citations.map(c => <div key={c.id} id={`evidence-${entryID}-${c.id}`} hidden={opened !== c.id}>{opened === c.id && <Evidence citation={c} />}</div>)}
    <details className="run-info"><summary>运行信息</summary><dl>
      <dt>运行 ID</dt><dd>{result.run_id}</dd>
      <dt>实际版本</dt><dd>{versionLabel(result.version)}</dd>
      <dt>总耗时</dt><dd>{(result.timings.total_ms / 1000).toFixed(2)} 秒</dd>
      <dt>生成用量</dt><dd>{result.usage ? `${result.usage.total_tokens} Token（输入 ${result.usage.prompt_tokens}，输出 ${result.usage.completion_tokens}）` : '未提供'}</dd>
    </dl><p className="muted">用量仅含生成模型，不含检索向量调用。</p></details>
  </>
}
