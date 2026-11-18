import { expect, it } from 'vitest'
import { ApiError } from '@/api/client'
import { canReadOffline } from './offline'
it('allows only network and gateway outages to fall back to cached chapters', () => {
  for (const status of [502, 503, 504]) expect(canReadOffline(new ApiError(status, `http_${status}`, null))).toBe(true)
  expect(canReadOffline(new TypeError('network'))).toBe(true)
  for (const status of [400, 401, 403, 404, 409, 422, 500]) expect(canReadOffline(new ApiError(status, 'denied', null))).toBe(false)
  expect(canReadOffline(new ApiError(503, 'session_refresh_uncertain', null))).toBe(false)
})
