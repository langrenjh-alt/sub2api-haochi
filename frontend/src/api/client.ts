/**
 * Axios HTTP Client Configuration
 * Base client with interceptors for authentication, token refresh, and error handling
 */

import axios, { AxiosInstance, AxiosError, InternalAxiosRequestConfig, AxiosResponse } from 'axios'
import type { ApiResponse } from '@/types'
import { getLocale } from '@/i18n'
import {
  ADMIN_UI_REQUEST_HEADER,
  USER_UI_REQUEST_HEADER,
  shouldMarkAdminUIRequest,
  shouldMarkUserUIRequest,
} from './adminUIRequest'
import { getAPIBaseURL } from './url'
export { buildApiUrl, buildGatewayUrl } from './url'

// ==================== Axios Instance Configuration ====================

export const apiClient: AxiosInstance = axios.create({
  baseURL: getAPIBaseURL(),
  withCredentials: true,
  timeout: 30000,
  headers: {
    'Content-Type': 'application/json'
  }
})

// ==================== Token Refresh State ====================

type AuthTrackedRequestConfig = InternalAxiosRequestConfig & {
  _retry?: boolean
  _authAccessToken?: string | null
  _authRefreshToken?: string | null
}

type RefreshedAuthTokens = {
  accessToken: string
  refreshToken: string
  expiresAt: number
}

const tokenRefreshRequests = new Map<string, Promise<RefreshedAuthTokens>>()

function authSessionChangedError(): Error & { code: 'AUTH_SESSION_CHANGED' } {
  const error = new Error('Authentication session changed while the request was in flight') as Error & {
    code: 'AUTH_SESSION_CHANGED'
  }
  error.code = 'AUTH_SESSION_CHANGED'
  return error
}

function authSnapshotMatches(accessToken: string | null, refreshToken: string | null): boolean {
  return (
    localStorage.getItem('auth_token') === accessToken &&
    localStorage.getItem('refresh_token') === refreshToken
  )
}

function clearAuthForSnapshot(accessToken: string | null, refreshToken: string | null): boolean {
  if (!authSnapshotMatches(accessToken, refreshToken)) return false
  localStorage.removeItem('auth_token')
  localStorage.removeItem('refresh_token')
  localStorage.removeItem('auth_user')
  localStorage.removeItem('token_expires_at')
  return true
}

function redirectToLogin() {
  if (!window.location.pathname.includes('/login')) {
    window.location.href = '/login'
  }
}

function getOrStartTokenRefresh(accessToken: string | null, refreshToken: string): Promise<RefreshedAuthTokens> {
  const requestKey = JSON.stringify([accessToken, refreshToken])
  const existingRequest = tokenRefreshRequests.get(requestKey)
  if (existingRequest) return existingRequest

  const request = (async (): Promise<RefreshedAuthTokens> => {
    try {
      const refreshResponse = await axios.post(
        `${getAPIBaseURL()}/auth/refresh`,
        { refresh_token: refreshToken },
        { headers: { 'Content-Type': 'application/json' }, timeout: 30000 }
      )
      const refreshData = refreshResponse.data as ApiResponse<{
        access_token: string
        refresh_token: string
        expires_in: number
      }>
      if (refreshData.code !== 0 || !refreshData.data) {
        throw new Error('Token refresh failed')
      }
      if (!authSnapshotMatches(accessToken, refreshToken)) {
        throw authSessionChangedError()
      }

      const expiresAt = Date.now() + refreshData.data.expires_in * 1000
      localStorage.setItem('auth_token', refreshData.data.access_token)
      localStorage.setItem('refresh_token', refreshData.data.refresh_token)
      localStorage.setItem('token_expires_at', String(expiresAt))
      return {
        accessToken: refreshData.data.access_token,
        refreshToken: refreshData.data.refresh_token,
        expiresAt,
      }
    } catch (cause) {
      if (!authSnapshotMatches(accessToken, refreshToken)) {
        throw authSessionChangedError()
      }
      if ((cause as { code?: unknown })?.code === 'AUTH_SESSION_CHANGED') throw cause

      clearAuthForSnapshot(accessToken, refreshToken)
      sessionStorage.setItem('auth_expired', '1')
      redirectToLogin()
      throw cause
    }
  })()

  const trackedRequest = request.finally(() => {
    if (tokenRefreshRequests.get(requestKey) === trackedRequest) {
      tokenRefreshRequests.delete(requestKey)
    }
  })
  tokenRefreshRequests.set(requestKey, trackedRequest)
  return trackedRequest
}

function extractBearerToken(config: InternalAxiosRequestConfig | undefined): string | null {
  const headers = config?.headers
  const raw = headers && typeof headers.get === 'function'
    ? headers.get('Authorization')
    : (headers as unknown as Record<string, unknown> | undefined)?.Authorization
      ?? (headers as unknown as Record<string, unknown> | undefined)?.authorization
  if (typeof raw !== 'string') return null
  const match = raw.match(/^Bearer\s+(.+)$/i)
  return match?.[1]?.trim() || null
}

// ==================== Request Interceptor ====================

// Get user's timezone
const getUserTimezone = (): string => {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone
  } catch {
    return 'UTC'
  }
}

apiClient.interceptors.request.use(
  (config: InternalAxiosRequestConfig) => {
    // Attach token from localStorage
    const token = localStorage.getItem('auth_token')
    const trackedConfig = config as AuthTrackedRequestConfig
    trackedConfig._authAccessToken = token
    trackedConfig._authRefreshToken = localStorage.getItem('refresh_token')
    if (token && config.headers) {
      config.headers.Authorization = `Bearer ${token}`
    }

    // Attach locale for backend translations
    if (config.headers) {
      config.headers['Accept-Language'] = getLocale()
    }

    // Attach timezone for all GET requests (backend may use it for default date ranges)
    if (config.method === 'get') {
      if (!config.params) {
        config.params = {}
      }
      config.params.timezone = getUserTimezone()
    }

    if (config.headers) {
      const requestURL = String(config.url || '')
      if (shouldMarkAdminUIRequest(requestURL)) {
        config.headers[ADMIN_UI_REQUEST_HEADER] = '1'
      }
      if (shouldMarkUserUIRequest(requestURL)) {
        config.headers[USER_UI_REQUEST_HEADER] = '1'
      }
    }

    return config
  },
  (error) => {
    return Promise.reject(error)
  }
)

// ==================== Response Interceptor ====================

apiClient.interceptors.response.use(
  (response: AxiosResponse) => {
    // Unwrap standard API response format { code, message, data }
    const apiResponse = response.data as ApiResponse<unknown>
    if (apiResponse && typeof apiResponse === 'object' && 'code' in apiResponse) {
      if (apiResponse.code === 0) {
        // Success - return the data portion
        response.data = apiResponse.data
      } else {
        // API error
        const resp = apiResponse as unknown as Record<string, unknown>
        return Promise.reject({
          status: response.status,
          code: apiResponse.code,
          message: apiResponse.message || 'Unknown error',
          reason: resp.reason,
          metadata: resp.metadata,
        })
      }
    }
    return response
  },
  async (error: AxiosError<ApiResponse<unknown>>) => {
    // Request cancellation: keep the original axios cancellation error so callers can ignore it.
    // Otherwise we'd misclassify it as a generic "network error".
    if (error.code === 'ERR_CANCELED' || axios.isCancel(error)) {
      return Promise.reject(error)
    }

    const originalRequest = error.config as AuthTrackedRequestConfig

    // Handle common errors
    if (error.response) {
      const { status, data } = error.response
      const url = String(error.config?.url || '')

      // Validate `data` shape to avoid HTML error pages breaking our error handling.
      const apiData = (typeof data === 'object' && data !== null ? data : {}) as Record<string, any>

      // Ops monitoring disabled: treat as feature-flagged 404, and proactively redirect away
      // from ops pages to avoid broken UI states.
      if (status === 404 && apiData.message === 'Ops monitoring is disabled') {
        try {
          localStorage.setItem('ops_monitoring_enabled_cached', 'false')
        } catch {
          // ignore localStorage failures
        }
        try {
          window.dispatchEvent(new CustomEvent('ops-monitoring-disabled'))
        } catch {
          // ignore event failures
        }

        if (window.location.pathname.startsWith('/admin/ops')) {
          window.location.href = '/admin/settings'
        }

        return Promise.reject({
          status,
          code: 'OPS_DISABLED',
          message: apiData.message || error.message,
          url
        })
      }

      if (status === 423 && apiData.code === 'ADMIN_COMPLIANCE_ACK_REQUIRED') {
        try {
          window.dispatchEvent(new CustomEvent('admin-compliance-required', {
            detail: apiData.metadata || {}
          }))
        } catch {
          // ignore event failures
        }

        return Promise.reject({
          status,
          code: apiData.code,
          message: apiData.message || error.message,
          metadata: apiData.metadata,
        })
      }

      // 401: Try to refresh the token if we have a refresh token
      // This handles TOKEN_EXPIRED, INVALID_TOKEN, TOKEN_REVOKED, etc.
      if (status === 401 && !originalRequest._retry) {
        const currentAccessToken = localStorage.getItem('auth_token')
        const refreshToken = localStorage.getItem('refresh_token')
        const requestAccessToken = originalRequest._authAccessToken ?? extractBearerToken(originalRequest)
        const requestRefreshToken = originalRequest._authRefreshToken
        const isAuthEndpoint =
          url.includes('/auth/login') || url.includes('/auth/register') || url.includes('/auth/refresh')

        if (
          requestAccessToken !== currentAccessToken ||
          (requestRefreshToken !== undefined && requestRefreshToken !== refreshToken)
        ) {
          return Promise.reject({
            status,
            code: 'AUTH_SESSION_CHANGED',
            message: 'Authentication session changed while the request was in flight'
          })
        }

        // If we have a refresh token and this is not an auth endpoint, try to refresh
        if (refreshToken && !isAuthEndpoint) {
          originalRequest._retry = true

          try {
            const refreshed = await getOrStartTokenRefresh(currentAccessToken, refreshToken)
            if (!authSnapshotMatches(refreshed.accessToken, refreshed.refreshToken)) {
              throw authSessionChangedError()
            }
            if (originalRequest.headers) {
              originalRequest.headers.Authorization = `Bearer ${refreshed.accessToken}`
            }
            return apiClient(originalRequest)
          } catch (refreshError) {
            if ((refreshError as { code?: unknown })?.code === 'AUTH_SESSION_CHANGED') {
              return Promise.reject({
                status: 401,
                code: 'AUTH_SESSION_CHANGED',
                message: 'Authentication session changed while the request was in flight'
              })
            }
            return Promise.reject({
              status: 401,
              code: 'TOKEN_REFRESH_FAILED',
              message: 'Session expired. Please log in again.'
            })
          }
        }

        // No refresh token or is auth endpoint - clear auth and redirect
        const hasToken = !!localStorage.getItem('auth_token')
        const headers = error.config?.headers as Record<string, unknown> | undefined
        const authHeader = headers?.Authorization ?? headers?.authorization
        const sentAuth =
          typeof authHeader === 'string'
            ? authHeader.trim() !== ''
            : Array.isArray(authHeader)
              ? authHeader.length > 0
              : !!authHeader

        const clearedCurrentSession = clearAuthForSnapshot(currentAccessToken, refreshToken)
        if (clearedCurrentSession && (hasToken || sentAuth) && !isAuthEndpoint) {
          sessionStorage.setItem('auth_expired', '1')
        }
        // Only redirect if not already on login page
        if (clearedCurrentSession) redirectToLogin()
      }

      // Return structured error
      return Promise.reject({
        status,
        code: apiData.code,
        reason: apiData.reason,
        error: apiData.error,
        message: apiData.message || apiData.detail || error.message,
        metadata: apiData.metadata,
      })
    }

    // Network error
    return Promise.reject({
      status: 0,
      message: 'Network error. Please check your connection.'
    })
  }
)

export default apiClient
