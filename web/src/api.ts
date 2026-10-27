import type { AskRequest, AskResult, Product } from './types'

export class APIError extends Error {
  constructor(message: string, public code: string, public status: number, public runID?: string) {
    super(message)
  }
}

// Only relative same-origin URLs; no model credentials or database configuration.
async function jsonRequest<T>(path: string, signal: AbortSignal, body?: unknown): Promise<T> {
  let response: Response
  try {
    response = await fetch(path, {
      method: body === undefined ? 'GET' : 'POST',
      headers: body === undefined ? { Accept: 'application/json' } : { 'Content-Type': 'application/json', Accept: 'application/json' },
      body: body === undefined ? undefined : JSON.stringify(body), signal,
    })
  } catch (error) {
    if (signal.aborted) throw error
    throw new APIError('连接失败，请检查本地服务是否启动。', 'network_error', 0)
  }
  let data
  try { data = await response.json() } catch {
    throw new APIError('服务返回了无法读取的结果，请稍后再试。', 'invalid_response', response.status)
  }
  if (!response.ok) {
    throw new APIError(data?.error?.message || '请求失败，请稍后再试。', data?.error?.code || 'request_failed', response.status, data?.error?.run_id)
  }
  return data as T
}

export async function listProducts(signal: AbortSignal): Promise<Product[]> {
  const data = await jsonRequest<{ products: Product[] }>('/api/rag/products', signal)
  if (!Array.isArray(data.products)) throw new APIError('产品目录格式异常。', 'invalid_response', 200)
  return data.products
}
export async function ask(request: AskRequest, signal: AbortSignal): Promise<AskResult> {
  const data = await jsonRequest<AskResult>('/api/rag/ask', signal, request)
  if (typeof data.answer !== 'string' || typeof data.version !== 'string' || !Array.isArray(data.citations)) {
    throw new APIError('回答格式异常，请稍后再试。', 'invalid_response', 200)
  }
  return data
}
