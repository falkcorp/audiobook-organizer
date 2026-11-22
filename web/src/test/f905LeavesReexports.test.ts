// file: web/src/test/f905LeavesReexports.test.ts
// version: 0.1.0
// guid: a8c41f07-5e29-4d63-b0a7-3f9d1c6e8b52
// last-edited: 2026-11-22

// F9-05 leaves: the old web paths of useAppStore, settingLocks, safeReturn,
// apiKeyExpiry and apiResponse are re-exports of the packages' code, not
// copies. A copy that crept back would fork the behaviour the packages' tests
// pin.

import { describe, expect, it } from 'vitest';
import * as apiClient from '@mediacore/api-client';
import * as shell from '@mediacore/shell';
import * as apiKeyExpiry from '../utils/apiKeyExpiry';
import * as apiResponse from '../utils/apiResponse';
import * as safeReturn from '../utils/safeReturn';
import * as settingLocks from '../utils/settingLocks';
import * as appStore from '../stores/useAppStore';

describe('moved leaves are re-exported from their package', () => {
  it('api-client runtime', () => {
    expect(settingLocks.describeLock).toBe(apiClient.describeLock);
    expect(apiResponse.unwrapData).toBe(apiClient.unwrapData);
    expect(apiResponse.responseErrorMessage).toBe(apiClient.responseErrorMessage);
    expect(apiResponse.describeRequestError).toBe(apiClient.describeRequestError);
    expect(apiResponse.SESSION_EXPIRED_MESSAGE).toBe(apiClient.SESSION_EXPIRED_MESSAGE);
    expect(apiKeyExpiry.formatKeyExpiry).toBe(apiClient.formatKeyExpiry);
    expect(apiKeyExpiry.customExpiryBounds).toBe(apiClient.customExpiryBounds);
    expect(apiKeyExpiry.daysUntilDate).toBe(apiClient.daysUntilDate);
    expect(apiKeyExpiry.customExpiryDays).toBe(apiClient.customExpiryDays);
    expect(apiKeyExpiry.expiryAfterDays).toBe(apiClient.expiryAfterDays);
    expect(apiKeyExpiry.API_KEY_EXPIRY_OPTIONS).toBe(apiClient.API_KEY_EXPIRY_OPTIONS);
    expect(apiKeyExpiry.API_KEY_MAX_TTL_DAYS).toBe(apiClient.API_KEY_MAX_TTL_DAYS);
    expect(apiKeyExpiry.API_KEY_CUSTOM_EXPIRY).toBe(apiClient.API_KEY_CUSTOM_EXPIRY);
  });

  it('shell store and return-path guard', () => {
    expect(appStore.useAppStore).toBe(shell.useAppStore);
    expect(safeReturn.sanitizeReturn).toBe(shell.sanitizeReturn);
  });

  it('the old paths export nothing else', () => {
    expect(Object.keys(settingLocks).sort()).toEqual(['describeLock']);
    expect(Object.keys(safeReturn).sort()).toEqual(['sanitizeReturn']);
    expect(Object.keys(appStore).sort()).toEqual(['useAppStore']);
    expect(Object.keys(apiResponse).sort()).toEqual([
      'SESSION_EXPIRED_MESSAGE',
      'describeRequestError',
      'responseErrorMessage',
      'unwrapData',
    ]);
  });
});
