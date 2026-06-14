// Thin fetch wrapper around the existing Image Platform API. Every call is
// recorded in the request log with a copyable curl equivalent. No endpoint is
// invented here — paths map 1:1 to api/openapi.yaml.
import { getConfig } from '../state/config'
import { addEntry } from '../state/requestLog'

export interface ApiResult<T = unknown> {
  ok: boolean
  status: number
  data: T | null
  error: string | null
  raw: unknown
}

export type QueryValue = string | number | boolean | undefined
type Query = Record<string, QueryValue>

export interface RequestSpec {
  method: 'GET' | 'POST' | 'PUT' | 'DELETE'
  path: string
  body?: unknown
  query?: Query
  admin?: boolean
  idempotencyKey?: string
}

function buildQuery(query?: Query): string {
  if (!query) return ''
  const params = new URLSearchParams()
  for (const [key, value] of Object.entries(query)) {
    if (value !== undefined && value !== '') params.set(key, String(value))
  }
  const s = params.toString()
  return s ? `?${s}` : ''
}

function safeJson(text: string): unknown {
  try {
    return JSON.parse(text)
  } catch {
    return text
  }
}

function extractError(body: unknown, status: number): string {
  if (body && typeof body === 'object' && 'message' in body) {
    const b = body as { message?: unknown; code?: unknown }
    const code = typeof b.code === 'string' ? b.code : ''
    const message = typeof b.message === 'string' ? b.message : ''
    return [code, message].filter(Boolean).join(': ') || `HTTP ${status}`
  }
  return `HTTP ${status}`
}

function shellQuote(value: string): string {
  return `'${value.replace(/'/g, `'\\''`)}'`
}

function buildCurl(
  method: string,
  url: string,
  headers: Record<string, string>,
  body?: unknown,
): string {
  const parts = ['curl', '-X', method, shellQuote(url)]
  for (const [name, value] of Object.entries(headers)) {
    parts.push('-H', shellQuote(`${name}: ${value}`))
  }
  if (body !== undefined) {
    parts.push('-d', shellQuote(JSON.stringify(body)))
  }
  return parts.join(' ')
}

export async function apiRequest<T = unknown>(spec: RequestSpec): Promise<ApiResult<T>> {
  const cfg = getConfig()
  const base = cfg.baseUrl.replace(/\/+$/, '')
  const url = base + spec.path + buildQuery(spec.query)
  const token = spec.admin ? cfg.adminToken : cfg.token

  const headers: Record<string, string> = { Accept: 'application/json' }
  if (spec.body !== undefined) headers['Content-Type'] = 'application/json'
  if (token) headers['Authorization'] = `Bearer ${token}`
  if (spec.idempotencyKey) headers['Idempotency-Key'] = spec.idempotencyKey

  const curl = buildCurl(spec.method, url, headers, spec.body)
  const started = performance.now()

  try {
    const res = await fetch(url, {
      method: spec.method,
      headers,
      body: spec.body !== undefined ? JSON.stringify(spec.body) : undefined,
    })
    const text = await res.text()
    const responseBody = text ? safeJson(text) : null
    const durationMs = Math.round(performance.now() - started)

    addEntry({
      id: crypto.randomUUID(),
      ts: new Date().toISOString(),
      method: spec.method,
      url,
      status: res.status,
      durationMs,
      requestBody: spec.body,
      responseBody,
      curl,
    })

    return {
      ok: res.ok,
      status: res.status,
      data: res.ok ? (responseBody as T) : null,
      error: res.ok ? null : extractError(responseBody, res.status),
      raw: responseBody,
    }
  } catch (err) {
    const durationMs = Math.round(performance.now() - started)
    const message = err instanceof Error ? err.message : String(err)
    addEntry({
      id: crypto.randomUUID(),
      ts: new Date().toISOString(),
      method: spec.method,
      url,
      status: null,
      durationMs,
      requestBody: spec.body,
      responseBody: null,
      error: message,
      curl,
    })
    return { ok: false, status: 0, data: null, error: message, raw: null }
  }
}
