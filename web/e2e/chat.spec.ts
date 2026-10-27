import { expect, test } from '@playwright/test'

test('select, ask, inspect citations, switch product, refuse, reload and use a narrow screen', async ({ page }) => {
  const products = [
    { product_id: 'ankang', name: '安康医疗', versions: [{ version: '', document_count: 1, compatible: true }] },
    { product_id: 'travel', name: '旅行保障', versions: [{ version: '2020', document_count: 1, compatible: true }] },
  ]
  await page.route('**/api/rag/products', route => route.fulfill({ json: { products } }))
  const requests: unknown[] = []
  await page.route('**/api/rag/ask', async route => {
    const request = route.request().postDataJSON()
    requests.push(request)
    await route.fulfill({ json: {
      run_id: `run-${requests.length}`, product_id: request.product_id, version: request.version,
      answer: request.product_id === 'ankang' ? '等待期为30天。[1]' : '现有条款不足以完整回答该问题。请提供养老金条款。',
      abstained: request.product_id !== 'ankang', reason: request.product_id === 'ankang' ? undefined : 'insufficient_evidence',
      citations: request.product_id === 'ankang' ? [{ id: 1, quote: '等待期为30天。', source: { document_name: '安康保险条款', document_version: '', source_url: 'https://example.com/policy', source_sha256: 'abc', page_number: 3, clause_id: '第一条', chunk_id: 'c1', text: '第一条\n等待期为30天。' } }] : [],
      usage: null, usage_scope: 'generation', timings: { total_ms: 1500, search_ms: 200, generation_ms: 1200 },
    } })
  })
  await page.goto('/')
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('你的保单，逐条解答。')
  await page.getByLabel('保险产品', { exact: true }).selectOption('ankang')
  await expect(page.getByLabel('条款版本')).toHaveValue('""')
  await page.getByLabel('你的问题').fill('等待期多久？')
  await page.getByLabel('你的问题').press('Enter')
  await expect(page.locator('.answer-text')).toHaveText('等待期为30天。[1]')
  expect(requests[0]).toEqual({ product_id: 'ankang', version: '', question: '等待期多久？', top_k: 5, mode: 'hnsw' })
  await page.getByRole('button', { name: '查看引用 1' }).first().click()
  await expect(page.getByText('安康保险条款', { exact: true })).toBeVisible()
  await page.getByText('查看完整条款片段').click()
  await expect(page.getByText('第一条\n等待期为30天。', { exact: true })).toBeVisible()
  await page.getByLabel('保险产品', { exact: true }).selectOption('travel')
  await page.getByLabel('你的问题').fill('养老金能领多少？')
  await page.getByRole('button', { name: /发送问题/ }).click()
  await expect(page.getByText('证据不足', { exact: true })).toBeVisible()
  await expect(page.getByRole('article')).toHaveCount(2)
  await page.screenshot({ path: '../var/m7-smoke/e2e-desktop.png', fullPage: true })
  await page.setViewportSize({ width: 390, height: 844 })
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
  await page.screenshot({ path: '../var/m7-smoke/e2e-mobile.png', fullPage: true })
  await page.reload()
  await expect(page.getByRole('article')).toHaveCount(0)
  await expect(page.getByLabel('保险产品', { exact: true })).toHaveValue('')
})
