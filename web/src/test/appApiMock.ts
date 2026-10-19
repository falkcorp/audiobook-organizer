// file: web/src/test/appApiMock.ts
// version: 1.0.0
// guid: 2f6a9c14-5b83-4d70-9e1a-8c3d7b0e5f42
// last-edited: 2026-10-19

import { vi } from 'vitest';

/**
 * Stub of `services/api` shared by the App-level tests. Use it from a hoisted
 * mock factory so the `vi.fn` instances are created per test file:
 *
 *   vi.mock('./services/api', async () => (await import('./test/appApiMock')).createAppApiMock());
 */
export function createAppApiMock() {
  return {
    getAuthStatus: vi.fn().mockResolvedValue({
      has_users: false,
      requires_auth: false,
      bootstrap_ready: false,
    }),
    getMe: vi.fn().mockResolvedValue(null),
    login: vi.fn(),
    logout: vi.fn(),
    setupAdmin: vi.fn(),
    getConfig: vi.fn().mockResolvedValue({
      root_dir: '/tmp/library',
      setup_complete: true,
    }),
    getHomeDirectory: vi.fn().mockResolvedValue('/home/user'),
    getSystemStatus: vi.fn().mockResolvedValue({
      status: 'ok',
      library: { book_count: 0, folder_count: 1, total_size: 0 },
      import_paths: { book_count: 0, folder_count: 0, total_size: 0 },
      memory: {},
      runtime: {},
      operations: { recent: [] },
    }),
    getImportPaths: vi.fn().mockResolvedValue([{ path: '/tmp' }]),
    countBooks: vi.fn().mockResolvedValue(0),
    getOperationLogsTail: vi.fn().mockResolvedValue([]),
    getAuthors: vi.fn().mockResolvedValue([]),
    getSeries: vi.fn().mockResolvedValue([]),
    getNarrators: vi.fn().mockResolvedValue([]),
    getBooks: vi.fn().mockResolvedValue([]),
    searchBooks: vi.fn().mockResolvedValue([]),
    getSoftDeletedBooks: vi.fn().mockResolvedValue({ items: [], count: 0 }),
    getAppVersion: vi.fn().mockResolvedValue('1.0.0-test'),
    getOperationTimeline: vi.fn().mockResolvedValue([]),
    getSystemStorage: vi.fn().mockResolvedValue({
      path: '/',
      total_bytes: 1000000,
      used_bytes: 500000,
      free_bytes: 500000,
      percent_used: 50,
      quota_enabled: false,
      quota_percent: 0,
      user_quotas_enabled: false,
    }),
    openOperationsSSE: vi.fn().mockReturnValue({
      close: vi.fn(),
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    }),
  };
}
