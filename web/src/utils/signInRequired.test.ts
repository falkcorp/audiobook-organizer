// file: web/src/utils/signInRequired.test.ts
// version: 0.1.0
// guid: 1dee76d4-9dfc-4a05-996e-4c4d09e2d03a
// last-edited: 2026-11-22

import { describe, expect, it } from 'vitest';
import { ApiError } from '../services/apiError';
import {
  SIGN_IN_HELP_URL,
  SIGN_IN_REQUIRED_MESSAGE,
  apiErrorFromResponse,
  isAuthOff,
  isSignInRequiredRefusal,
} from './signInRequired';

const refusal = (status: number, code: string) => new ApiError('refused', status, { code });

describe('isAuthOff', () => {
  it('is true only when the server said auth is off and the status has loaded', () => {
    expect(isAuthOff(false, true)).toBe(true);
    expect(isAuthOff(false, false)).toBe(false);
    expect(isAuthOff(true, true)).toBe(false);
    expect(isAuthOff(null, true)).toBe(false);
    expect(isAuthOff(undefined)).toBe(false);
  });
});

describe('isSignInRequiredRefusal', () => {
  it('maps the stage-13 interactive_required 403 with or without a known auth setting', () => {
    expect(isSignInRequiredRefusal(refusal(403, 'interactive_required'), true)).toBe(true);
    expect(isSignInRequiredRefusal(refusal(403, 'interactive_required'), false)).toBe(true);
  });

  it('maps the books credential-guard FORBIDDEN 403 only while auth is off', () => {
    expect(isSignInRequiredRefusal(refusal(403, 'FORBIDDEN'), true)).toBe(true);
    expect(isSignInRequiredRefusal(refusal(403, 'FORBIDDEN'), false)).toBe(false);
  });

  it('leaves every other failure alone', () => {
    expect(isSignInRequiredRefusal(refusal(500, 'interactive_required'), true)).toBe(false);
    expect(isSignInRequiredRefusal(refusal(403, 'permission_denied'), true)).toBe(false);
    expect(isSignInRequiredRefusal(new ApiError('x', 403), true)).toBe(false);
    expect(isSignInRequiredRefusal(new Error('x'), true)).toBe(false);
    expect(isSignInRequiredRefusal(undefined, true)).toBe(false);
  });
});

describe('apiErrorFromResponse', () => {
  it('keeps the status and the parsed body, and prefers the body error text', async () => {
    const resp = new Response(JSON.stringify({ code: 'FORBIDDEN', error: 'API keys cannot change passwords' }), {
      status: 403,
    });
    const err = await apiErrorFromResponse(resp, 'fallback');
    expect(err.status).toBe(403);
    expect((err.data as { code: string }).code).toBe('FORBIDDEN');
    expect(err.message).toBe('API keys cannot change passwords');
  });

  it('uses the fallback for a body that is not JSON', async () => {
    const err = await apiErrorFromResponse(new Response('<html>', { status: 502 }), 'HTTP 502');
    expect(err.message).toBe('HTTP 502');
    expect(err.status).toBe(502);
  });
});

describe('the shared message and link', () => {
  it('names the action and points at a documentation page', () => {
    expect(SIGN_IN_REQUIRED_MESSAGE).toBe('Turn on sign-in to do this');
    expect(SIGN_IN_HELP_URL).toMatch(/^https:\/\/github\.com\/falkcorp\/audiobook-organizer\/blob\/main\/docs\/configuration\.md$/);
  });
});
