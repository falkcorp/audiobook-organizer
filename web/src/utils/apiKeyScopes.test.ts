// file: web/src/utils/apiKeyScopes.test.ts
// version: 0.1.0
// guid: 66279880-d22a-4fde-a7ba-b4b6ed8e84bc
// last-edited: 2026-11-22

// The scope picker's list is HEAD's, entry for entry and in HEAD's order (F4-08 B10sk).

import { describe, expect, it } from 'vitest';
import { API_KEY_ATOM_SCOPES } from './apiKeyScopes';

// A literal copy of HEAD's picker list: web/src/components/settings/APIKeysTab.tsx lines
// 55-68 (`const ALL_SCOPES = [...]`) on origin/main at aeb705ff1d52c165b5eeaf6a022e67e2690f061b.
const HEAD_ALL_SCOPES = [
  'library.view',
  'library.edit_metadata',
  'library.delete',
  'library.organize',
  'scan.trigger',
  'integrations.manage',
  'users.manage',
  'settings.manage',
  'playlists.create',
  'requests.create',
  'requests.approve',
  'fingerprint.worker',
];

describe('API key scope picker list', () => {
  it('is HEAD\'s ALL_SCOPES, order included', () => {
    expect([...API_KEY_ATOM_SCOPES]).toEqual(HEAD_ALL_SCOPES);
  });

  it('has twelve entries, none a core permission name', () => {
    expect(API_KEY_ATOM_SCOPES).toHaveLength(12);
    expect(API_KEY_ATOM_SCOPES.some((s) => s.includes(':'))).toBe(false);
  });
});
