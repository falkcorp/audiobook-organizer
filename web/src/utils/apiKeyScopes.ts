// file: web/src/utils/apiKeyScopes.ts
// version: 0.1.0
// guid: 419c95ab-174a-4847-a490-cb280bf9db09
// last-edited: 2026-11-22

// The scopes the API-key picker offers: HEAD's atom names, the vocabulary of the
// books key route (POST /api/v1/auth/api-keys). That route validates each name
// against the atom table and mints the key's core scopes from it, ambient ones
// (core:self, books:reading, ...) included. It refuses a core name such as
// books:view, so these are deliberately NOT generated/permissions.ts's
// API_KEY_SCOPES (core names, for core's native routes, which the SPA no longer
// calls for keys: see apiKeysRoute.guard.test.ts).
//
// The permission-literal rule (07 section 7.18) allows a permission string only in
// src/generated, and the generated file speaks core's vocabulary only (cmd/permsgen
// pins that), so this one list carries the rule's exception, with its reason.
// components/settings/APIKeysTab.booksRoute.test.tsx pins the picker to this list.

/* eslint-disable no-restricted-syntax -- HEAD's atom names for the books key route; see the file comment. */
export const API_KEY_ATOM_SCOPES: readonly string[] = [
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
  // Remote fingerprint worker API only; give a worker key this scope alone.
  'fingerprint.worker',
];
/* eslint-enable no-restricted-syntax */
