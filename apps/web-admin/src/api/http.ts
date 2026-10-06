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

  async request<T>(path: string, init: RequestInit = {}): Promise<T> {
    const headers = new Headers(init.headers)
    headers.set('Accept', 'application/json')
    if (init.body !== undefined) headers.set('Content-Type', 'application/json')
    const token = this.tokenProvider()
    if (token) headers.set('Authorization', `Bearer ${token}`)

    let response: Response
    try {
      response = await fetch(`${this.baseUrl}${path}`, { ...init, headers })
    } catch (error) {
      throw new ApiError(error instanceof Error ? `网络连接失败：${error.message}` : '网络连接失败')
    }

    if (response.status === 401) unauthorizedHandler?.()
    if (response.status === 204) return undefined as T

    const contentType = response.headers.get('content-type') ?? ''
    const body: unknown = contentType.includes('application/json') ? await response.json() : await response.text()
    if (!response.ok) {
      const detail = errorMessage(body, `请求失败 (${response.status})`)
      if ([502, 503, 504].includes(response.status) && detail.code === 'request_failed') {
        throw new ApiError('无法连接控制服务，请检查 API 服务和代理配置', response.status, 'service_unavailable')
      }
      throw new ApiError(detail.message, response.status, detail.code)
    }
    return body as T
  }
}
