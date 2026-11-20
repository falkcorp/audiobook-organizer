// file: web/src/utils/secretStatus.ts
// version: 1.0.0
// guid: 8d3f5a07-2b64-4e91-a0c7-1f9e6b4d2a53
// last-edited: 2026-11-19

import type { SecretStatus } from '../services/configView';

/**
 * What a secret's field says about the stored value. The server never returns
 * a secret, not even masked, so this is the whole of what the page can know:
 * whether one is set, where it came from, and a fingerprint to tell two keys
 * apart. `notSet` is the copy for a field with nothing behind it (what the key
 * is for).
 */
export function describeSecretStatus(status: SecretStatus | undefined, notSet: string): string {
  if (!status || !status.set) return notSet;
  if (status.unreadable) {
    return 'A saved key exists but cannot be read (the server lost its encryption key). Enter it again.';
  }
  const print = status.fingerprint ? ` (fingerprint ${status.fingerprint})` : '';
  if (status.source === 'env') {
    return `Set by the server's environment${print}. A key saved here would be ignored.`;
  }
  return `A key is saved${print}. Type a new one to replace it.`;
}

/** True when a secret is set from the UI, so a Clear action means something. */
export function canClearSecret(status: SecretStatus | undefined): boolean {
  return !!status && (status.set || !!status.unreadable) && status.source !== 'env';
}
