// Manual acceptance only: calls the real local API/model and saves run snapshots.
// Start the API against the existing M6 smoke database, then explicitly run this.
import { chromium } from '@playwright/test'
import assert from 'node:assert/strict'
import { mkdir, writeFile } from 'node:fs/promises'

const base = process.env.M7_SMOKE_URL
if (!base || !/^http:\/\/127\.0\.0\.1:\d+$/.test(base)) throw new Error('请显式设置 M7_SMOKE_URL=http://127.0.0.1:<端口>')
const output = new URL('../../var/m7-smoke/', import.meta.url)
await mkdir(output, { recursive: true, mode: 0o700 })
const browser = await chromium.launch()
const results = []
try {
  const page = await browser.newPage({ viewport: { width: 1280, height: 1000 } })
  const errors = []
  page.on('pageerror', error => errors.push(error.message))
  await page.goto(base)
  await page.getByLabel('保险产品', { exact: true }).selectOption('m6-ankang')
  const cases = [
    { product: 'm6-ankang', question: '等待期是多久？等待期内患病如何处理？', abstained: false, page: 3, anchors: ['30'] },
    { product: 'm6-ankang', question: '等待期是多久？还可以用哪些银行卡支付平台保费？', abstained: true, reason: 'insufficient_evidence' },
    { product: 'm6-travel', question: '意外伤害身故保险责任的180天规定是什么？', abstained: false, page: 1, anchors: ['180'] },
  ]
  for (const sample of cases) {
    await page.getByLabel('保险产品', { exact: true }).selectOption(sample.product)
    await page.getByLabel('你的问题').fill(sample.question)
    const responsePromise = page.waitForResponse(response => response.url().endsWith('/api/rag/ask') && response.request().method() === 'POST', { timeout: 130_000 })
    await page.getByRole('button', { name: /发送问题/ }).click()
    const response = await responsePromise
    const result = await response.json()
    const record = { sample, http_status: response.status(), result, passed: false }
    results.push(record)
    await writeFile(new URL('live-results.json', output), JSON.stringify({ date: new Date().toISOString(), results }, null, 2), { mode: 0o600 })
    assert.equal(response.status(), 200, JSON.stringify(result))
    assert.equal(result.abstained, sample.abstained)
    if (sample.reason) assert.equal(result.reason, sample.reason)
    for (const anchor of sample.anchors || []) assert.ok(result.answer.includes(anchor), `回答缺少预期 ${anchor}`)
    if (sample.page) assert.ok(result.citations.some(c => c.source.page_number === sample.page))
    const runResponse = await page.request.get(`${base}/api/rag/runs/${result.run_id}`)
    assert.equal(runResponse.status(), 200)
    record.run = await runResponse.json()
    assert.equal(record.run.result.answer, result.answer)
    assert.equal(record.run.status, result.abstained ? 'abstained' : 'answered')
    for (const citation of result.citations) {
      assert.ok(citation.source.text.includes(citation.quote))
      assert.equal(citation.source.product_id, sample.product)
      assert.equal(citation.source.document_version, result.version)
    }
    await page.getByRole('button', { name: /发送问题/ }).waitFor({ state: 'visible' })
    const article = page.getByRole('article').last()
    if (result.citations.length) {
      await article.getByRole('button', { name: `查看引用 ${result.citations[0].id}`, exact: true }).first().click()
      await article.getByText('查看完整条款片段', { exact: true }).click()
      assert.ok(await article.locator('blockquote').isVisible())
    }
    record.passed = true
    await writeFile(new URL('live-results.json', output), JSON.stringify({ date: new Date().toISOString(), results }, null, 2), { mode: 0o600 })
    console.log(`${sample.product}: ${result.run_id} ${result.abstained ? result.reason : 'answered'} ${result.timings.total_ms}ms`)
  }
  await page.screenshot({ path: new URL('live-desktop.png', output).pathname, fullPage: true })
  await page.setViewportSize({ width: 390, height: 844 })
  assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth))
  await page.screenshot({ path: new URL('live-mobile.png', output).pathname, fullPage: true })
  await page.reload()
  await page.getByRole('heading', { level: 1 }).waitFor()
  assert.equal(await page.getByRole('article').count(), 0)
  assert.deepEqual(errors, [])
  console.log('3/3 真实问答、引用、运行快照、刷新与窄屏检查通过。')
} finally { await browser.close() }
