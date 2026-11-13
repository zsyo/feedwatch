// A tiny fetch wrapper: same-origin cookie session, JSON bodies and the CSRF
// header on every unsafe method. No external HTTP client is needed.

export class ApiError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
  ) {
    super(message)
    this.name = 'ApiError'
  }
}

let csrfToken = ''

export function setCsrfToken(token: string): void {
  csrfToken = token || ''
}

export function getCsrfToken(): string {
  return csrfToken
}

const safeMethods = new Set(['GET', 'HEAD'])

export async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = { Accept: 'application/json' }
  if (body !== undefined) headers['Content-Type'] = 'application/json'
  if (!safeMethods.has(method) && csrfToken) headers['X-CSRF-Token'] = csrfToken

  let response: Response
  try {
    response = await fetch(path, {
      method,
      headers,
      credentials: 'same-origin',
      body: body === undefined ? undefined : JSON.stringify(body),
    })
  } catch (e) {
    throw new ApiError(0, 'network_error', `无法连接服务：${(e as Error).message}`)
  }

  const text = await response.text()
  let data: any = null
  if (text) {
    try {
      data = JSON.parse(text)
    } catch {
      data = null
    }
  }
  if (!response.ok) {
    throw new ApiError(
      response.status,
      data?.error?.code ?? 'request_failed',
      data?.error?.message ?? `${response.status} ${response.statusText}`,
    )
  }
  // /api/auth/login and /api/auth/me both return a fresh per-session token.
  if (data && typeof data.csrf_token === 'string') setCsrfToken(data.csrf_token)
  return (data ?? {}) as T
}

export const api = {
  get: <T>(path: string) => request<T>('GET', path),
  post: <T>(path: string, body?: unknown) => request<T>('POST', path, body),
  patch: <T>(path: string, body?: unknown) => request<T>('PATCH', path, body),
  put: <T>(path: string, body?: unknown) => request<T>('PUT', path, body),
  del: <T>(path: string) => request<T>('DELETE', path),
}

export function errorMessage(e: unknown): string {
  if (e instanceof ApiError) return e.message
  if (e instanceof Error) return e.message
  return '未知错误'
}
