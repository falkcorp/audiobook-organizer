// file: core/web/packages/api-client/test/apiResponse.test.ts
// version: 0.1.0
// guid: df377d0b-bad9-412e-b436-b80ccea208ce
// last-edited: 2026-11-22

// Pins apiResponse before F9-05 moves it to the api-client runtime: the moved
// copy must pass this same table.

import { describe, expect, it } from 'vitest';
import { ApiAuthRedirectError } from '../src/errors';
import {
  SESSION_EXPIRED_MESSAGE,
  describeRequestError,
  responseErrorMessage,
  unwrapData,
} from '../src/runtime/apiResponse';

describe('unwrapData', () => {
  it('returns the payload of a { data } envelope', () => {
    expect(unwrapData<number[]>({ data: [1, 2] })).toEqual([1, 2]);
  });
  it('returns a bare body when there is no envelope', () => {
    expect(unwrapData<{ id: string }>({ id: 'b1' })).toEqual({ id: 'b1' });
  });
  it('falls back to the whole body when data is null or undefined', () => {
    expect(unwrapData({ data: null })).toEqual({ data: null });
    expect(unwrapData({ data: undefined })).toEqual({ data: undefined });
  });
  it('keeps falsy payloads that are not null', () => {
    expect(unwrapData({ data: 0 })).toBe(0);
    expect(unwrapData({ data: '' })).toBe('');
    expect(unwrapData({ data: false })).toBe(false);
  });
  it('passes null and undefined bodies through', () => {
    expect(unwrapData(null)).toBeNull();
    expect(unwrapData(undefined)).toBeUndefined();
  });
});

describe('responseErrorMessage', () => {
  it('reads the JSON error field', async () => {
    const resp = new Response(JSON.stringify({ error: 'Batman is not home' }), { status: 404 });
    expect(await responseErrorMessage(resp)).toBe('Batman is not home');
  });
  it('uses HTTP <status> when the error field is empty or not a string', async () => {
    expect(await responseErrorMessage(new Response(JSON.stringify({ error: '' }), { status: 400 }))).toBe('HTTP 400');
    expect(await responseErrorMessage(new Response(JSON.stringify({ error: 7 }), { status: 422 }))).toBe('HTTP 422');
    expect(await responseErrorMessage(new Response('null', { status: 500 }))).toBe('HTTP 500');
  });
  it('never returns raw non-JSON text', async () => {
    const resp = new Response('<html>Gotham stack trace</html>', { status: 502 });
    expect(await responseErrorMessage(resp)).toBe('HTTP 502');
  });
});

describe('describeRequestError', () => {
  it('names the expired session for an auth redirect', () => {
    const err = new ApiAuthRedirectError('/api/v1/audiobooks', 'https://example.test/login');
    expect(describeRequestError(err, 'fallback')).toBe(SESSION_EXPIRED_MESSAGE);
  });
  it('returns the fallback for any other error and never the error text', () => {
    expect(describeRequestError(new Error('connect ECONNREFUSED 192.0.2.1'), 'Could not load')).toBe('Could not load');
  });
  it('pins the session-expired text', () => {
    expect(SESSION_EXPIRED_MESSAGE).toBe('Your session has expired. Sign in again to continue.');
  });
});
