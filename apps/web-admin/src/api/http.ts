import { observeServerTime } from '@/lib/serverClock'

export class ApiError extends Error {
  readonly status: number
  readonly code: string

  constructor(message: string, status = 0, code = 'request_failed') {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
  }
}

type UnauthorizedHandler = () => void
let unauthorizedHandler: UnauthorizedHandler | undefined

export function onUnauthorized(handler: UnauthorizedHandler): void {
  unauthorizedHandler = handler
}

function errorMessage(body: unknown, fallback: string): { message: string; code: string } {
  if (body && typeof body === 'object') {
    const value = body as Record<string, unknown>
    // The Go API uses { error: { code, message } }. Flat envelopes remain
    // supported for local adapters and proxy-generated errors.
    const detail = value.error && typeof value.error === 'object' && !Array.isArray(value.error)
      ? value.error as Record<string, unknown>
      : value
    const message = typeof detail.message === 'string' ? detail.message : typeof value.error === 'string' ? value.error : fallback
    const code = typeof detail.code === 'string' ? detail.code : 'request_failed'
    return { message, code }
  }
  return { message: fallback, code: 'request_failed' }
}

export class HttpClient {
  constructor(
    private readonly baseUrl: string,
    private readonly tokenProvider: () => string | null,
  ) {}

  async request<T>(path: string, init: RequestInit & { timeoutMs?: number } = {}): Promise<T> {
    const { timeoutMs = !init.method || init.method === 'GET' ? 30_000 : 120_000, signal, ...options } = init
    const controller = new AbortController()
    let timedOut = false
    const cancel = () => controller.abort(signal?.reason)
    if (signal?.aborted) cancel()
    else signal?.addEventListener('abort', cancel, { once: true })
    const timer = timeoutMs > 0 ? setTimeout(() => { timedOut = true; controller.abort() }, timeoutMs) : undefined
    try {
      const headers = new Headers(init.headers)
      headers.set('Accept', 'application/json')
      if (init.body !== undefined) headers.set('Content-Type', 'application/json')
      const token = this.tokenProvider()
      if (token) headers.set('Authorization', `Bearer ${token}`)
      const response = await fetch(`${this.baseUrl}${path}`, { ...options, headers, signal: controller.signal })

      if (response.status === 401 && token && path !== '/auth/login') {
        if (this.tokenProvider() === token) unauthorizedHandler?.()
        throw new ApiError('登录已过期，请重新登录后继续', 401, 'session_expired')
      }
      if (response.ok) observeServerTime(response.headers.get('X-HL-Server-Time') ?? response.headers.get('Date'))
      if (response.status === 204) return undefined as T

      const contentType = response.headers.get('content-type') ?? ''
      let body: unknown
      try {
        body = contentType.includes('application/json') ? await response.json() : await response.text()
      } catch (error) {
        if (controller.signal.aborted) throw error
        if (!response.ok) body = null
        else throw new ApiError('服务响应未能完整读取，请刷新重试', response.status, 'invalid_response')
      }
      if (!response.ok) {
        const detail = errorMessage(body, `请求失败 (${response.status})`)
        if (response.status === 500 && detail.code === 'internal_error') {
          throw new ApiError('面板服务暂时异常，请稍后重试；若持续失败，请检查控制服务与数据库日志', response.status, detail.code)
        }
        if ([502, 503, 504].includes(response.status) && detail.code === 'request_failed') {
          throw new ApiError('无法连接控制服务，请检查 API 服务和代理配置', response.status, 'service_unavailable')
        }
        throw new ApiError(detail.message, response.status, detail.code)
      }
      return body as T
    } catch (error) {
      if (error instanceof ApiError) throw error
      if (timedOut) throw new ApiError('请求超时，请检查网络后重试；提交操作请先核对结果', 0, 'request_timeout')
      if (signal?.aborted) throw new ApiError('请求已取消', 0, 'request_cancelled')
      throw new ApiError('网络连接失败，请检查面板连接后重试')
    } finally {
      if (timer !== undefined) clearTimeout(timer)
      signal?.removeEventListener('abort', cancel)
    }
  }
}
