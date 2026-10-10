// file: web/src/services/apiFetchServices.test.ts
// version: 1.0.0
// guid: f593c4bb-dba7-4dab-91c1-5cf4cf6fcdc9
// last-edited: 2026-10-10

// versionApi, playlistApi and fileOpsApi used to call raw fetch, so an expired
// session was read as a successful (HTML) response. They go through apiFetch now.

import { afterEach, describe, expect, it, vi } from 'vitest';
import { loginPageResponse } from '../test/loginRedirect';
import { fetchPendingFileOps } from './fileOpsApi';
import { listPlaylists } from './playlistApi';
import { trashVersion } from './versionApi';

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('services that used to call raw fetch', () => {
  it.each([
    ['fileOpsApi.fetchPendingFileOps', () => fetchPendingFileOps()],
    ['playlistApi.listPlaylists', () => listPlaylists()],
    ['versionApi.trashVersion', () => trashVersion('book-1', 'version-1')],
  ])('%s rejects with ApiAuthRedirectError on a login-page answer', async (_name, call) => {
    vi.stubGlobal('fetch', vi.fn(async () => loginPageResponse()));
    await expect(call()).rejects.toMatchObject({ name: 'ApiAuthRedirectError' });
  });
});
