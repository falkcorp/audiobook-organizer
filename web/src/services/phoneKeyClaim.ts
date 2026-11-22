// file: web/src/services/phoneKeyClaim.ts
// version: 0.1.0
// guid: 809798a0-ea99-4074-9fb1-d4787719d73f
// last-edited: 2026-11-22

// The guest's one-time phone-app key claim (F4-08 B10s contract). After core's invite accept has
// opened the session, the accept page calls POST /api/v1/auth/phone-key/claim once: same origin,
// with the session cookie, no body and no query. A books route, so it goes through apiFetch and not
// the core client; every failure is swallowed here, so a refusal never reaches an error reporter.
//
// The answer is the secret itself. This module keeps it nowhere: no storage, no cache, no log; it
// returns the value to the caller, which holds it in component state only.

import { apiFetch } from '../utils/apiFetch';
import type { PhoneAppKey } from './coreIdentity';

export const PHONE_KEY_CLAIM_PATH = '/api/v1/auth/phone-key/claim';

function isPhoneApp(value: unknown): value is PhoneAppKey {
  if (typeof value !== 'object' || value === null) return false;
  const status = (value as { status?: unknown }).status;
  return status === 'issued' || status === 'unavailable';
}

/**
 * Reads phone_app from the object, or from data.phone_app when RespondWithOK wrapped the body.
 * An 'issued' answer without both halves of the key is not shown.
 */
export function parsePhoneApp(payload: unknown): PhoneAppKey | null {
  if (typeof payload !== 'object' || payload === null) return null;
  const direct = (payload as { phone_app?: unknown }).phone_app;
  const wrapped = (payload as { data?: { phone_app?: unknown } | null }).data?.phone_app;
  const found = isPhoneApp(direct) ? direct : isPhoneApp(wrapped) ? wrapped : null;
  if (!found) return null;
  if (found.status === 'issued' && (!found.client_id || !found.client_secret)) return null;
  return found;
}

/**
 * Claims the guest's phone-app key. Resolves to the key (issued, or unavailable with its message),
 * or null for everything else: 404 (a link-only invite, the normal answer), 401, 403, 429, any
 * other status, a body that is not the contract, or a network error. There is no retry. The
 * invitation was already accepted, so no outcome here is an error for the caller.
 */
export async function claimPhoneKey(): Promise<PhoneAppKey | null> {
  try {
    const response = await apiFetch(PHONE_KEY_CLAIM_PATH, {
      method: 'POST',
      credentials: 'same-origin',
      headers: { Accept: 'application/json' },
    });
    if (response.status !== 200) return null;
    return parsePhoneApp(await response.json());
  } catch {
    return null;
  }
}
