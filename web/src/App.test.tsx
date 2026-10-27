import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import App from './App'
import { APIError, ask, listProducts } from './api'
import { Answer } from './Answer'
import type { AskResult, Product } from './types'

vi.mock('./api', async importOriginal => ({ ...await importOriginal<typeof import('./api')>(), ask: vi.fn(), listProducts: vi.fn() }))

const products: Product[] = [
  { product_id: 'p', name: null, versions: [{ version: '', document_count: 1, compatible: true }] },
  { product_id: 'multi', name: '多版本保单', versions: [{ version: 'v1', document_count: 1, compatible: true }, { version: 'v2', document_count: 1, compatible: true }, { version: 'bad', document_count: 1, compatible: false }] },
]
const result: AskResult = {
  run_id: 'run-1', product_id: 'p', version: '', answer: '等待期为30天。[1]', abstained: false,
  citations: [{ id: 1, quote: '等待期为30天。', source: { product_id: 'p', document_name: '保险条款', document_version: '', source_url: 'https://example.com/policy', source_sha256: 'sha256', page_number: 3, clause_id: '第一条', chunk_id: 'chunk-1', text: '第一条\n等待期为30天。' } }],
  usage: null, usage_scope: 'generation', timings: { search_ms: 10, generation_ms: 20, total_ms: 30 },
}

beforeEach(() => { vi.resetAllMocks(); vi.mocked(listProducts).mockResolvedValue(products); vi.mocked(ask).mockResolvedValue(result) })

async function prepare() {
  const user = userEvent.setup()
  render(<App />)
  await screen.findByRole('option', { name: 'p' })
  await user.selectOptions(screen.getByLabelText('保险产品'), 'p')
  return user
}

describe('chat', () => {
  it('requires product, preserves legacy empty version, renders citations and clears only local messages', async () => {
    const user = await prepare()
    expect(screen.getByLabelText('条款版本')).toHaveValue('""')
    await user.type(screen.getByLabelText('你的问题'), '等待期多久？')
    await user.click(screen.getByRole('button', { name: /发送问题/ }))
    expect(ask).toHaveBeenCalledWith({ product_id: 'p', version: '', question: '等待期多久？', top_k: 5, mode: 'hnsw' }, expect.any(AbortSignal))
    await screen.findByText('等待期为30天。')
    await user.click(screen.getAllByRole('button', { name: '查看引用 1' })[0])
    expect(screen.getByText('保险条款')).toBeVisible()
    expect(screen.getByRole('link', { name: 'https://example.com/policy' })).toHaveAttribute('rel', 'noopener noreferrer')
    await user.click(screen.getByText('查看完整条款片段'))
    expect(screen.getByText('第一条 等待期为30天。')).toBeVisible()
    await user.click(screen.getByText('运行信息'))
    expect(screen.getByText('未提供')).toBeVisible()
    await user.selectOptions(screen.getByLabelText('保险产品'), 'multi')
    expect(screen.getByLabelText('条款版本')).toHaveValue('')
    expect(screen.getByRole('option', { name: /bad/ })).toBeDisabled()
    expect(screen.getByRole('article')).toHaveTextContent('p · 未标注版本')
    await user.click(screen.getByRole('button', { name: '清空对话' }))
    expect(screen.queryByRole('article')).not.toBeInTheDocument()
    expect(ask).toHaveBeenCalledTimes(1)
  })

  it('locks concurrent requests, cancels, and ignores an old response after a new request', async () => {
    let resolveOld!: (r: AskResult) => void
    vi.mocked(ask).mockImplementationOnce(() => new Promise(resolve => { resolveOld = resolve }))
    const user = await prepare()
    await user.type(screen.getByLabelText('你的问题'), '等待期？{Enter}')
    await screen.findByRole('button', { name: '停止等待' })
    expect(screen.getByLabelText('保险产品')).toBeDisabled()
    expect(screen.getByLabelText('你的问题')).toBeDisabled()
    const signal = vi.mocked(ask).mock.calls[0][1]
    await user.click(screen.getByRole('button', { name: '停止等待' }))
    expect(signal.aborted).toBe(true)
    expect(screen.getByLabelText('你的问题')).toHaveValue('等待期？')
    await user.click(screen.getByRole('button', { name: /发送问题/ }))
    await screen.findByText('等待期为30天。')
    await act(async () => { resolveOld({ ...result, answer: '不应显示的旧结果' }) })
    expect(screen.queryByText('不应显示的旧结果')).not.toBeInTheDocument()
    expect(ask).toHaveBeenCalledTimes(2)
    expect(screen.getByText(/已停止等待/)).toBeVisible()
  })

  it('retains the draft on errors, suggests directory reload on stale data and never retries automatically', async () => {
    vi.mocked(ask).mockRejectedValue(new APIError('版本已变化', 'version_required', 409, 'error-run'))
    const user = await prepare()
    await user.type(screen.getByLabelText('你的问题'), '等待期？')
    await user.click(screen.getByRole('button', { name: /发送问题/ }))
    expect(await screen.findByRole('alert')).toHaveTextContent('error-run')
    expect(screen.getByLabelText('你的问题')).toHaveValue('等待期？')
    expect(screen.getByRole('button', { name: '刷新产品目录' })).toBeEnabled()
    expect(ask).toHaveBeenCalledTimes(1)
  })

  it('counts Unicode code points, blocks overlong questions and handles IME Enter', async () => {
    await prepare()
    const input = screen.getByLabelText('你的问题')
    fireEvent.change(input, { target: { value: '😀'.repeat(2000) } })
    expect(screen.getByText('2000 / 2000')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /发送问题/ })).toBeEnabled()
    fireEvent.change(input, { target: { value: '😀'.repeat(2001) } })
    expect(screen.getByRole('button', { name: /发送问题/ })).toBeDisabled()
    fireEvent.change(input, { target: { value: '等待期？' } })
    fireEvent.compositionStart(input)
    fireEvent.keyDown(input, { key: 'Enter', code: 'Enter', isComposing: true })
    fireEvent.compositionEnd(input)
    expect(ask).not.toHaveBeenCalled()
    fireEvent.keyDown(input, { key: 'Enter', shiftKey: true })
    expect(ask).not.toHaveBeenCalled()
    fireEvent.keyDown(input, { key: 'Enter' })
    await waitFor(() => expect(ask).toHaveBeenCalledTimes(1))
  })

  it('handles empty directories, failures and reload', async () => {
    vi.mocked(listProducts).mockRejectedValueOnce(new Error('目录暂不可用')).mockResolvedValueOnce([])
    const user = userEvent.setup()
    render(<App />)
    expect(await screen.findByText('目录暂不可用')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '重新加载' }))
    expect(await screen.findByText(/暂无已完成索引的保单/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /发送问题/ })).toBeDisabled()
  })

  it.each([
    ['insufficient_evidence', '证据不足'], ['needs_clarification', '信息待补充'], ['conflicting_evidence', '资料冲突'],
    ['out_of_scope', '超出范围'], ['formal_decision', '需正式核保或核赔'],
  ])('renders refusal %s and its evidence when present', async (reason, label) => {
    vi.mocked(ask).mockResolvedValue({ ...result, abstained: true, reason })
    const user = await prepare()
    await user.type(screen.getByLabelText('你的问题'), '问题{Enter}')
    expect(await screen.findByText(label)).toBeVisible()
    expect(screen.getAllByRole('button', { name: '查看引用 1' })).toHaveLength(2)
  })

  it('mounts a fresh app without restoring page history', async () => {
    const user = await prepare()
    await user.type(screen.getByLabelText('你的问题'), '问题{Enter}')
    await screen.findByText('等待期为30天。')
    cleanup()
    render(<App />)
    await screen.findByRole('option', { name: 'p' })
    expect(screen.queryByRole('article')).not.toBeInTheDocument()
    expect(screen.getByLabelText('保险产品')).toHaveValue('')
    expect(localStorage.length).toBe(0)
    expect(sessionStorage.length).toBe(0)
  })

  it('aborts a request at the client deadline and retains its draft', async () => {
    vi.mocked(ask).mockImplementation((_request, signal) => new Promise((_resolve, reject) => {
      signal.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError')))
    }))
    await prepare()
    vi.useFakeTimers()
    try {
      fireEvent.change(screen.getByLabelText('你的问题'), { target: { value: '等待期？' } })
      fireEvent.click(screen.getByRole('button', { name: /发送问题/ }))
      await act(async () => { await vi.advanceTimersByTimeAsync(125_000) })
      expect(screen.getByRole('alert')).toHaveTextContent('等待超时')
      expect(screen.getByLabelText('你的问题')).toHaveValue('等待期？')
      expect(screen.getByRole('button', { name: /发送问题/ })).toBeEnabled()
      expect(ask).toHaveBeenCalledTimes(1)
    } finally { vi.useRealTimers() }
  })
})

it('escapes HTML, refuses unsafe source links and binds references to each individual answer', async () => {
  const user = userEvent.setup()
  const unsafe = { ...result, answer: '<img src=x onerror=alert(1)> [1] [99]', citations: [{ ...result.citations[0], quote: '<script>bad</script>', source: { ...result.citations[0].source, source_url: 'javascript:alert(1)', text: '<b>完整原文</b>' } }] }
  render(<><section data-testid="first"><Answer result={unsafe} entryID={1} /></section><section data-testid="second"><Answer result={{ ...result, answer: '第二次回答 [1]' }} entryID={2} /></section></>)
  const first = within(screen.getByTestId('first'))
  await user.click(first.getAllByRole('button', { name: '查看引用 1' })[0])
  expect(first.queryByRole('link')).not.toBeInTheDocument()
  expect(first.getByText('<script>bad</script>')).toBeVisible()
  expect(document.querySelector('img')).toBeNull()
  expect(document.querySelector('script')).toBeNull()
  expect(first.queryByRole('button', { name: '查看引用 99' })).not.toBeInTheDocument()
  expect(within(screen.getByTestId('second')).queryByText('保险条款')).not.toBeInTheDocument()
})
