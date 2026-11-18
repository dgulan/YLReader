import { ApiError } from '@/api/client'
export function canReadOffline(error: unknown) {
  return error instanceof TypeError || (error instanceof ApiError && [502, 503, 504].includes(error.status) && !error.code.startsWith('session_refresh_'))
}
