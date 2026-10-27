export interface ProductVersion {
  version: string
  document_count: number
  compatible: boolean
}
export interface Product {
  product_id: string
  name: string | null
  versions: ProductVersion[]
}
export interface Source {
  product_id: string
  document_name: string
  document_version: string
  source_url: string
  source_sha256: string
  page_number: number
  clause_id: string
  chunk_id: string
  text: string
}
export interface Citation {
  id: number
  quote: string
  source: Source
}
export interface AskResult {
  run_id: string
  product_id: string
  version: string
  answer: string
  citations: Citation[]
  abstained: boolean
  reason?: string
  usage: { prompt_tokens: number; completion_tokens: number; total_tokens: number } | null
  usage_scope: string
  timings: { search_ms: number; generation_ms: number; total_ms: number }
}
export interface AskRequest {
  product_id: string
  version: string
  question: string
  top_k: 5
  mode: 'hnsw'
}
