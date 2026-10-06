export class ApiError extends Error {
  constructor(message: string, readonly status = 0, readonly code = 'request_failed') {
    super(message)
    this.name = 'ApiError'
  }
}

let unauthorizedHandler: (() => void) | undefined
export function onUnauthorized(handler: () => void): void { unauthorizedHandler = handler }

function errorDetail(body: unknown, fallback: string): { message: string; code: string } {
  if (body && typeof body === 'object') {
    const value = body as Record<string, unknown>
    const nested = value.error && typeof value.error === 'object' ? value.error as Record<string, unknown> : value
    return {
      message: typeof nested.message === 'string' ? nested.message : fallback,
      code: typeof nested.code === 'string' ? nested.code : 'request_failed',
    }
  }
  return { message: fallback, code: 'request_failed' }
}

export class HttpClient {
  constructor(private readonly tokenProvider: () => string | null) {}

  async request<T>(path: string, init: RequestInit = {}): Promise<T> {
    const headers = new Headers(init.headers)
    headers.set('Accept', 'application/json')
    if (init.body !== undefined) headers.set('Content-Type', 'application/json')
    const token = this.tokenProvider()
    if (token) headers.set('Authorization', `Bearer ${token}`)
    let response: Response
    try {
      response = await fetch(path, { ...init, headers })
    } catch (cause) {
      throw new ApiError(cause instanceof Error ? `网络连接失败：${cause.message}` : '网络连接失败')
    }
    if (response.status === 401) unauthorizedHandler?.()
    if (response.status === 204) return undefined as T
    const contentType = response.headers.get('content-type') ?? ''
    const body: unknown = contentType.includes('application/json') ? await response.json() : await response.text()
    if (!response.ok) {
      const detail = errorDetail(body, `请求失败 (${response.status})`)
      throw new ApiError(detail.message, response.status, detail.code)
    }
    return body as T
  }
}
