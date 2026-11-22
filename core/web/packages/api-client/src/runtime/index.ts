// file: core/web/packages/api-client/src/runtime/index.ts
// version: 0.1.0
// guid: 5d2a8e71-0b3c-4f96-a1d7-8c4e6b9f2a35
// last-edited: 2026-11-22

// Pure helpers over the wire: lock descriptions, API key expiry maths and
// response-body readers. No React, no fetch of their own.

export {
  API_KEY_CUSTOM_EXPIRY,
  API_KEY_EXPIRY_OPTIONS,
  API_KEY_MAX_TTL_DAYS,
  customExpiryBounds,
  customExpiryDays,
  daysUntilDate,
  expiryAfterDays,
  formatKeyExpiry,
} from './apiKeyExpiry';
export { describeRequestError, responseErrorMessage, SESSION_EXPIRED_MESSAGE, unwrapData } from './apiResponse';
export { describeLock } from './settingLocks';
