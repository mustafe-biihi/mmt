// All calls go to the API gateway (same origin; proxied by Vite in dev, nginx in Docker).

// Kept in localStorage so reopening the browser reuses the session: the server
// allows only one active session per customer.
const TOKEN_KEY = 'mmt.token'

export const session = {
  get: () => localStorage.getItem(TOKEN_KEY),
  set: (token) => localStorage.setItem(TOKEN_KEY, token),
  clear: () => localStorage.removeItem(TOKEN_KEY),
}

export class ApiError extends Error {
  constructor(status, code, message) {
    super(message)
    this.status = status
    this.code = code
  }
}

async function request(method, path, body) {
  const headers = { 'Content-Type': 'application/json' }
  const token = session.get()
  if (token) headers.Authorization = `Bearer ${token}`

  const res = await fetch(path, { method, headers, body: body ? JSON.stringify(body) : undefined })
  if (res.status === 204) return null
  const data = await res.json().catch(() => ({}))
  if (!res.ok) {
    if (res.status === 401) session.clear()
    throw new ApiError(res.status, data.error?.code, data.error?.message || `Request failed (${res.status})`)
  }
  return data
}

export const api = {
  register: (input) => request('POST', '/api/auth/register', input),
  login: (msisdn, pin) => request('POST', '/api/auth/login', { msisdn, pin }),
  logout: () => request('POST', '/api/auth/logout'),
  me: () => request('GET', '/api/me'),
  transactions: (limit = 20) => request('GET', `/api/transactions?limit=${limit}`),
  services: () => request('GET', '/api/services'),
}

// USSD hop in the aggregator form-post format.
export async function ussd({ sessionId, phoneNumber, serviceCode, text }) {
  const res = await fetch('/ussd', {
    method: 'POST',
    headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
    body: new URLSearchParams({ sessionId, phoneNumber, serviceCode, text }),
  })
  const raw = await res.text()
  const end = !raw.startsWith('CON ')
  return { end, message: raw.replace(/^(CON|END) /, '') }
}

// Amounts from the API are actual values (e.g. 1000.5 = 1,000.50).
export function formatMoney(amount, currency = 'USD') {
  const n = Number(amount).toLocaleString('en-US', { minimumFractionDigits: 2, maximumFractionDigits: 2 })
  return `${currency} ${n}`
}
