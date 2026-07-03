// file: web/tests/e2e/utils/test-helpers.ts
// version: 2.8.0
// guid: a1b2c3d4-e5f6-7890-abcd-e1f2a3b4c5d6
// last-edited: 2026-07-03

import { Page } from '@playwright/test';

// Re-export setup mode helpers for convenience
export {
  resetToFactoryDefaults,
  setupPhase1ApiDriven,
  setupPhase2Interactive,
} from './setup-modes';

export interface MockMetadataSource {
  id: string;
  name: string;
  enabled: boolean;
  priority: number;
  requires_auth: boolean;
  credentials: Record<string, string>;
}

export interface MockConfig {
  root_dir: string;
  database_path: string;
  database_type: string;
  enable_sqlite: boolean;
  playlist_dir: string;
  organization_strategy: string;
  scan_on_startup: boolean;
  auto_organize: boolean;
  folder_naming_pattern: string;
  file_naming_pattern: string;
  create_backups: boolean;
  enable_disk_quota: boolean;
  disk_quota_percent: number;
  enable_user_quotas: boolean;
  default_user_quota_gb: number;
  auto_fetch_metadata: boolean;
  metadata_sources: MockMetadataSource[];
  language: string;
  enable_ai_parsing: boolean;
  openai_api_key: string;
  concurrent_scans: number;
  memory_limit_type: string;
  cache_size: number;
  memory_limit_percent: number;
  memory_limit_mb: number;
  purge_soft_deleted_after_days: number;
  purge_soft_deleted_delete_files: boolean;
  log_level: string;
  log_format: string;
  enable_json_logging: boolean;
  api_keys: Record<string, never>;
  supported_extensions: string[];
  exclude_patterns?: string[];
  ai_backend?: {
    embedding_mode?: string;
    llm_mode?: string;
    local_base_url?: string;
    local_embedding_model?: string;
    local_llm_model?: string;
  };
}

interface MockImportPath {
  id: number;
  path: string;
  book_count: number;
}

interface MockBackup {
  filename: string;
  size: number;
  created_at: string;
  auto?: boolean;
  trigger?: string;
}

interface MockBlockedHash {
  hash: string;
  reason: string;
  created_at: string;
}

interface MockFilesystemItem {
  name: string;
  path: string;
  is_dir: boolean;
  size?: number;
  mod_time?: number;
  excluded: boolean;
}

interface MockFilesystemEntry {
  path: string;
  items: MockFilesystemItem[];
  disk_info?: {
    exists: boolean;
    readable: boolean;
    writable: boolean;
    total_bytes?: number;
    free_bytes?: number;
    library_bytes?: number;
  };
}

interface MockOperationLog {
  id: string;
  level: string;
  message: string;
  created_at: string;
  details?: string;
}

interface MockOperation {
  id: string;
  type: string;
  status: string;
  progress: number;
  total: number;
  message?: string;
  created_at: string;
  folder_path?: string;
  error_message?: string;
}

interface MockActiveOperation {
  id: string;
  type: string;
  status: string;
  progress: number;
  total: number;
  message: string;
  folder_path?: string;
}

interface MockITunesValidation {
  total_tracks: number;
  audiobook_tracks: number;
  files_found: number;
  files_missing: number;
  missing_paths?: string[];
  duplicate_count: number;
  estimated_import_time: string;
}

interface MockITunesImportStatus {
  operation_id: string;
  status: string;
  progress: number;
  message: string;
  total_books?: number;
  processed?: number;
  imported?: number;
  skipped?: number;
  failed?: number;
  errors?: string[];
}

interface MockITunesState {
  validation?: MockITunesValidation;
  importStatus?: MockITunesImportStatus;
}

export interface MockAuthUser {
  id: string;
  username: string;
  email: string;
  roles: string[];
  status: string;
  created_at: string;
}

interface MockAuthSession {
  id: string;
  user_id: string;
  expires_at: string;
  ip_address?: string;
  user_agent?: string;
  revoked?: boolean;
  created_at: string;
  current?: boolean;
}

export interface MockAuthState {
  auth_enabled?: boolean;
  has_users?: boolean;
  requires_auth?: boolean;
  bootstrap_ready?: boolean;
  login_username?: string;
  login_password?: string;
  currentUser?: MockAuthUser | null;
  sessions?: MockAuthSession[];
}

interface ResolvedMockAuthState {
  auth_enabled: boolean;
  has_users: boolean;
  requires_auth: boolean;
  bootstrap_ready: boolean;
  login_username: string;
  login_password: string;
  currentUser: MockAuthUser | null;
  sessions: MockAuthSession[];
}

interface MockFailures {
  getBooks?: number | 'timeout';
  searchBooks?: number | 'timeout';
  getConfig?: number;
  updateConfig?: number;
  openaiTest?: number;
  createBackup?: number;
  listBackups?: number;
  restoreBackup?: number;
  deleteBackup?: number;
  blockedHashes?: number;
  filesystem?: number;
  importFile?: number | 'timeout';
  operationsActive?: number;
  operationLogs?: number;
}

export interface MockAuthorDedupGroup {
  canonical: { id: number; name: string };
  variants: Array<{ id: number; name: string }>;
  book_count: number;
  suggested_name?: string;
  split_names?: string[];
  is_production_company?: boolean;
}

export interface MockSeriesDupGroup {
  canonical: { id: number; name: string };
  variants: Array<{ id: number; name: string }>;
  book_count: number;
  books?: Array<{ id: string; title: string; series_name?: string }>;
}

export interface MockApiOptions {
  books?: TestBook[];
  config?: Partial<MockConfig>;
  importPaths?: MockImportPath[];
  backups?: MockBackup[];
  blockedHashes?: MockBlockedHash[];
  filesystem?: Record<string, MockFilesystemEntry>;
  homeDirectory?: string;
  operations?: {
    active?: MockActiveOperation[];
    history?: MockOperation[];
    logs?: Record<string, MockOperationLog[]>;
  };
  itunes?: MockITunesState;
  auth?: MockAuthState;
  failures?: MockFailures;
  systemStatus?: Record<string, unknown>;
  authorDedup?: { groups: MockAuthorDedupGroup[]; needs_refresh?: boolean };
  seriesDedup?: { groups: MockSeriesDupGroup[]; total_series?: number };
  aiBackendsStatus?: {
    embedding_mode?: string;
    llm_mode?: string;
    local_base_url?: string;
    local_reachable?: boolean;
    embedding_model?: { name: string; pulled: boolean };
    llm_model?: { name: string; pulled: boolean };
    fallback_reason?: string;
  };
}

export interface TestBook {
  id: string;
  title: string;
  author_name?: string;
  series_name?: string | null;
  series_position?: number | null;
  library_state: string;
  marked_for_deletion: boolean;
  language?: string;
  file_path: string;
  file_hash: string;
  original_file_hash: string;
  organized_file_hash?: string | null;
  created_at: string;
  updated_at: string;
  duration?: number;
  file_size?: number;
  publisher?: string;
  description?: string;
  audiobook_release_year?: number;
  print_year?: number;
  quality?: string;
  bitrate?: number;
  codec?: string;
  sample_rate?: number;
  format?: string;
  version_group_id?: string;
  version_notes?: string;
  is_primary_version?: boolean;
  fetch_metadata_error?: boolean;
  fetch_metadata_delay_ms?: number;
  organize_error?: string;
  force_update_required?: boolean;
}

/**
 * Mock EventSource to prevent SSE connections during tests
 */
export async function mockEventSource(page: Page) {
  await page.addInitScript(() => {
    class MockEventSource {
      static instances: MockEventSource[] = [];
      url: string;
      onopen: ((event: Event) => void) | null = null;
      onerror: ((event: Event) => void) | null = null;
      onmessage: ((event: MessageEvent) => void) | null = null;
      constructor(url: string) {
        this.url = url;
        MockEventSource.instances.push(this);
        window.setTimeout(() => {
          if (this.onopen) {
            this.onopen(new Event('open'));
          }
        }, 0);
      }
      addEventListener() {}
      removeEventListener() {}
      close() {}
      emitOpen() {
        if (this.onopen) {
          this.onopen(new Event('open'));
        }
      }
      emitError() {
        if (this.onerror) {
          this.onerror(new Event('error'));
        }
      }
      emitMessage(data: Record<string, unknown>) {
        if (this.onmessage) {
          const event = new MessageEvent('message', {
            data: JSON.stringify(data),
          });
          this.onmessage(event);
        }
      }
    }
    (window as unknown as { EventSource: typeof EventSource }).EventSource =
      MockEventSource as unknown as typeof EventSource;
    (window as unknown as { __mockEventSource: typeof MockEventSource })
      .__mockEventSource = MockEventSource;
  });
}

/**
 * Skip welcome wizard
 */
export async function skipWelcomeWizard(page: Page) {
  await page.addInitScript(() => {
    localStorage.setItem('welcome_wizard_completed', 'true');
  });
}

/**
 * Setup mock API routes using page.route() for reliable interception
 * This approach works across page navigations, unlike addInitScript()
 * Use this INSTEAD of setupMockApi for Phase 2 tests
 *
 * @param page - Playwright test page object
 * @param options - Mock API options
 */
export async function setupMockApiRoutes(
  page: Page,
  options: MockApiOptions = {}
) {
  // Store mock state in page context so it persists
  const mockState = {
    books: options.books || [],
    config: buildConfig(options.config),
    systemStatus: buildSystemStatus(options.systemStatus),
    importPaths: options.importPaths || [],
    backups: options.backups || [],
    blockedHashes: options.blockedHashes || [],
    filesystem: JSON.parse(JSON.stringify(options.filesystem || {})),
    homeDirectory: options.homeDirectory || '/',
    operations: options.operations || {},
    itunes: options.itunes || {},
    auth: buildAuthState(options.auth),
    failures: options.failures || {},
    authorDedup: options.authorDedup || { groups: [] },
    seriesDedup: options.seriesDedup || { groups: [], total_series: 0 },
    aiBackendsStatus: {
      embedding_mode: 'disabled',
      llm_mode: 'disabled',
      local_base_url: '',
      local_reachable: false,
      ...options.aiBackendsStatus,
    },
  };

  // Use addInitScript to set localStorage and basic state
  // This is lightweight and runs before page navigation
  await page.addInitScript(() => {
    localStorage.setItem('welcome_wizard_completed', 'true');
  });

  // Set up route-based interception for ALL API calls
  // This persists across page navigations
  await page.route('**/api/v1/**', async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    const pathname = url.pathname;
    const method = request.method();
    console.log(`[Mock API] ${method} ${pathname}`);

    const jsonResponse = (body: unknown, status = 200) => ({
      status,
      contentType: 'application/json',
      body: JSON.stringify(body),
    });

    // Failure injection helpers
    const maybeTimeout = (failure: number | string | undefined) => {
      if (failure === 'timeout') {
        return route.fulfill(jsonResponse({ error: 'Request timeout' }, 504));
      }
      return null;
    };
    const maybeFailStatus = (failure: number | string | undefined, message: string) => {
      if (typeof failure === 'number') {
        return route.fulfill(jsonResponse({ error: message }, failure));
      }
      return null;
    };

    // Health check
    if (pathname === '/api/v1/health') {
      return route.fulfill(jsonResponse({ status: 'ok' }));
    }

    // Auth endpoints
    if (pathname === '/api/v1/auth/status' && method === 'GET') {
      return route.fulfill(
        jsonResponse({
          has_users: mockState.auth.has_users,
          auth_enabled: mockState.auth.auth_enabled,
          requires_auth: mockState.auth.requires_auth,
          bootstrap_ready: mockState.auth.bootstrap_ready,
        })
      );
    }

    if (pathname === '/api/v1/auth/setup' && method === 'POST') {
      if (mockState.auth.has_users) {
        return route.fulfill(
          jsonResponse({ error: 'initial setup already completed' }, 409)
        );
      }

      const body =
        (request.postDataJSON() as {
          username?: string;
          email?: string;
          password?: string;
        }) || {};
      const username = (body.username || '').trim();
      const password = body.password || '';
      if (!username || password.length < 8) {
        return route.fulfill(
          jsonResponse(
            { error: 'username and password (min 8 chars) are required' },
            400
          )
        );
      }

      const now = new Date().toISOString();
      mockState.auth.currentUser = {
        id: 'user-admin',
        username,
        email: (body.email || `${username}@local`).trim(),
        roles: ['admin'],
        status: 'active',
        created_at: now,
      };
      mockState.auth.login_username = username;
      mockState.auth.login_password = password;
      mockState.auth.has_users = true;
      mockState.auth.requires_auth = true;
      mockState.auth.bootstrap_ready = false;

      return route.fulfill(
        jsonResponse({
          message: 'admin user created',
          user: mockState.auth.currentUser,
        }, 201)
      );
    }

    if (pathname === '/api/v1/auth/login' && method === 'POST') {
      const body =
        (request.postDataJSON() as { username?: string; password?: string }) ||
        {};
      const username = (body.username || '').trim();
      const password = body.password || '';
      if (!username || !password) {
        return route.fulfill(
          jsonResponse({ error: 'username and password are required' }, 400)
        );
      }
      if (
        username !== mockState.auth.login_username ||
        password !== mockState.auth.login_password
      ) {
        return route.fulfill(
          jsonResponse({ error: 'invalid credentials' }, 401)
        );
      }

      if (!mockState.auth.currentUser) {
        mockState.auth.currentUser = {
          id: 'user-admin',
          username,
          email: `${username}@local`,
          roles: ['admin'],
          status: 'active',
          created_at: new Date().toISOString(),
        };
      }

      const sessionId = `session-${Date.now()}`;
      const session = {
        id: sessionId,
        user_id: mockState.auth.currentUser.id,
        expires_at: new Date(Date.now() + 24 * 60 * 60 * 1000).toISOString(),
        ip_address: '127.0.0.1',
        user_agent: 'playwright',
        revoked: false,
        created_at: new Date().toISOString(),
        current: true,
      };
      mockState.auth.sessions = [
        session,
        ...mockState.auth.sessions.map((existing) => ({
          ...existing,
          current: false,
        })),
      ];
      mockState.auth.has_users = true;
      mockState.auth.requires_auth = true;
      mockState.auth.bootstrap_ready = false;

      return route.fulfill(
        jsonResponse({
          user: mockState.auth.currentUser,
          session,
        })
      );
    }

    if (pathname === '/api/v1/auth/me' && method === 'GET') {
      if (!mockState.auth.requires_auth || mockState.auth.currentUser) {
        return route.fulfill(jsonResponse({ user: mockState.auth.currentUser }));
      }
      return route.fulfill(jsonResponse({ error: 'not authenticated' }, 401));
    }

    if (pathname === '/api/v1/auth/logout' && method === 'POST') {
      mockState.auth.currentUser = null;
      mockState.auth.sessions = [];
      return route.fulfill(jsonResponse({ message: 'logged out' }));
    }

    if (pathname === '/api/v1/auth/sessions' && method === 'GET') {
      if (!mockState.auth.currentUser) {
        return route.fulfill(jsonResponse({ error: 'not authenticated' }, 401));
      }
      return route.fulfill(
        jsonResponse({
          sessions: mockState.auth.sessions,
        })
      );
    }

    if (pathname.match(/\/api\/v1\/auth\/sessions\/[^/]+$/) && method === 'DELETE') {
      if (!mockState.auth.currentUser) {
        return route.fulfill(jsonResponse({ error: 'not authenticated' }, 401));
      }
      const sessionId = pathname.split('/').pop() || '';
      mockState.auth.sessions = mockState.auth.sessions.filter(
        (session) => session.id !== sessionId
      );
      return route.fulfill(jsonResponse({}, 204));
    }

    // System reset
    if (pathname === '/api/v1/system/reset' && method === 'POST') {
      return route.fulfill(jsonResponse({ message: 'Reset complete' }));
    }

    // System status
    if (pathname === '/api/v1/system/status') {
      const librarySize = mockState.books.reduce(
        (sum, book) => sum + (book.file_size || 0),
        0
      );
      const totalBooks = mockState.books.length;
      const diskTotal =
        (mockState.systemStatus as { disk_total_bytes?: number })
          .disk_total_bytes || 500 * 1024 * 1024 * 1024;

      return route.fulfill(
        jsonResponse({
          ...mockState.systemStatus,
          status: 'ok',
          library: {
            book_count: totalBooks,
            folder_count: 1,
            total_size: librarySize,
          },
          import_paths: {
            folder_count: mockState.importPaths.length,
            book_count: mockState.books.filter((b: Record<string, unknown>) => b.library_state === 'import').length,
          },
          operations: { recent: (mockState.operations as { history?: unknown[] }).history || [] },
          library_book_count: totalBooks,
          import_book_count: mockState.books.filter((b: Record<string, unknown>) => b.library_state === 'import').length,
          total_book_count: totalBooks,
          library_size_bytes: librarySize,
          total_size_bytes: librarySize,
          disk_used_bytes: librarySize,
          disk_free_bytes: Math.max(0, diskTotal - librarySize),
        })
      );
    }

    // Config endpoint
    if (pathname === '/api/v1/config') {
      if (method === 'GET') {
        const t = maybeTimeout(mockState.failures.getConfig); if (t) return t;
        const f = maybeFailStatus(mockState.failures.getConfig, 'Failed to fetch config'); if (f) return f;
        const maskedConfig = { ...mockState.config };
        return route.fulfill(jsonResponse({ config: maskedConfig }));
      }
      if (method === 'PUT') {
        const t = maybeTimeout(mockState.failures.updateConfig); if (t) return t;
        const f = maybeFailStatus(mockState.failures.updateConfig, 'Failed to update config'); if (f) return f;
        try {
          const body = route.request().postDataJSON();
          if (body && typeof body === 'object') {
            Object.assign(mockState.config, body);
          }
        } catch {
          // ignore parse errors
        }
        return route.fulfill(jsonResponse({ config: mockState.config }));
      }
    }

    // Backup endpoints - CRITICAL for backup-restore tests
    if (pathname === '/api/v1/backup/list') {
      const f = maybeFailStatus(mockState.failures.listBackups, 'Failed to list backups'); if (f) return f;
      return route.fulfill(
        jsonResponse({
          backups: mockState.backups,
          count: mockState.backups.length,
        })
      );
    }

    if (pathname === '/api/v1/backup/create' && method === 'POST') {
      const f = maybeFailStatus(mockState.failures.createBackup, 'Failed to create backup'); if (f) return f;
      const filename = `backup-${Date.now()}.db.gz`;
      const created = {
        filename,
        size: 25 * 1024 * 1024,
        created_at: new Date().toISOString(),
        auto: false,
      };
      mockState.backups.unshift(created);
      return route.fulfill(jsonResponse({ backup: created }));
    }

    if (pathname === '/api/v1/backup/restore' && method === 'POST') {
      if (mockState.failures.restoreBackup) {
        return route.fulfill(jsonResponse({ error: 'Backup file is corrupt.' }, mockState.failures.restoreBackup as number));
      }
      return route.fulfill(jsonResponse({ message: 'Restored' }));
    }

    if (pathname.startsWith('/api/v1/backup/') && method === 'DELETE') {
      const f = maybeFailStatus(mockState.failures.deleteBackup, 'Failed to delete backup'); if (f) return f;
      const filename = pathname.split('/').pop() || '';
      mockState.backups = mockState.backups.filter(
        (item) => item.filename !== filename
      );
      return route.fulfill(jsonResponse({ message: 'Deleted' }));
    }

    // Import paths
    if (pathname === '/api/v1/import-paths' && method === 'GET') {
      return route.fulfill(
        jsonResponse({ importPaths: mockState.importPaths })
      );
    }

    if (pathname === '/api/v1/import-paths' && method === 'POST') {
      let postedPath = '/unknown';
      try {
        const body = route.request().postDataJSON();
        if (body?.path) postedPath = body.path;
      } catch { /* ignore */ }
      const nextId =
        mockState.importPaths.reduce((max, item) => Math.max(max, item.id), 0) +
        1;
      const newPath = {
        id: nextId,
        path: postedPath,
        book_count: 0,
      };
      mockState.importPaths.push(newPath);
      return route.fulfill(jsonResponse(newPath, 201));
    }

    // Books endpoints
    if (pathname === '/api/v1/audiobooks' && method === 'GET') {
      const t = maybeTimeout(mockState.failures.getBooks); if (t) return t;
      const f = maybeFailStatus(mockState.failures.getBooks, 'Server error occurred.'); if (f) return f;
      const offset = parseInt(url.searchParams.get('offset') || '0', 10);
      const limit = parseInt(url.searchParams.get('limit') || '12', 10);
      const paginatedBooks = mockState.books.slice(offset, offset + limit);
      return route.fulfill(
        jsonResponse({
          items: paginatedBooks,
          count: mockState.books.length,
          offset,
          limit,
        })
      );
    }

    if (pathname === '/api/v1/audiobooks/count') {
      return route.fulfill(jsonResponse({ count: mockState.books.length }));
    }

    if (pathname === '/api/v1/audiobooks/soft-deleted' && method === 'GET') {
      const deleted = mockState.books.filter((b: Record<string, unknown>) => b.marked_for_deletion);
      return route.fulfill(jsonResponse({
        items: deleted,
        count: deleted.length,
        total: deleted.length,
        offset: 0,
        limit: 100,
      }));
    }

    if (pathname === '/api/v1/audiobooks/search' && method === 'GET') {
      const t = maybeTimeout(mockState.failures.searchBooks); if (t) return t;
      const f = maybeFailStatus(mockState.failures.searchBooks, 'Failed to search books'); if (f) return f;
      const query = (url.searchParams.get('q') || url.searchParams.get('search') || '').toLowerCase();
      const limit = parseInt(url.searchParams.get('limit') || '50', 10);
      const filtered = mockState.books.filter((b: Record<string, unknown>) => {
        const title = String(b.title || '').toLowerCase();
        const author = String(b.author_name || '').toLowerCase();
        const series = String(b.series_name || '').toLowerCase();
        return title.includes(query) || author.includes(query) || series.includes(query);
      });
      return route.fulfill(jsonResponse({
        items: filtered.slice(0, limit),
        total: filtered.length,
      }));
    }

    if (pathname === '/api/v1/audiobooks/duplicates' && method === 'GET') {
      return route.fulfill(jsonResponse({ items: [], count: 0 }));
    }

    // Author dedup endpoints
    if (pathname === '/api/v1/authors/duplicates' && method === 'GET') {
      const dedup = mockState.authorDedup as { groups: MockAuthorDedupGroup[]; needs_refresh?: boolean };
      return route.fulfill(jsonResponse({ groups: dedup.groups, needs_refresh: dedup.needs_refresh || false }));
    }
    if (pathname === '/api/v1/authors/duplicates/refresh' && method === 'POST') {
      return route.fulfill(jsonResponse({ id: 'dedup-refresh-1', type: 'author-dedup-scan', status: 'running', progress: 0, total: 100, message: 'Starting...' }));
    }
    if (pathname === '/api/v1/authors/merge' && method === 'POST') {
      const body = request.postDataJSON();
      return route.fulfill(jsonResponse({ id: 'merge-op-1', type: 'author-merge', status: 'completed', message: `Merged ${(body?.merge_ids?.length || 0)} authors` }));
    }
    if (pathname.match(/\/api\/v1\/authors\/\d+\/rename/) && method === 'PUT') {
      return route.fulfill(jsonResponse({ message: 'Author renamed' }));
    }
    if (pathname.match(/\/api\/v1\/authors\/\d+\/name/) && method === 'PUT') {
      return route.fulfill(jsonResponse({ message: 'Author renamed' }));
    }
    if (pathname.match(/\/api\/v1\/authors\/\d+\/split/) && method === 'POST') {
      return route.fulfill(jsonResponse({ authors: [{ id: 100, name: 'Author A' }, { id: 101, name: 'Author B' }], books_updated: 3 }));
    }
    if (pathname.match(/\/api\/v1\/authors\/\d+\/resolve-production/) && method === 'POST') {
      return route.fulfill(jsonResponse({
        operation: { id: 'resolve-prod-1', type: 'resolve-production-author', status: 'running', progress: 0, total: 100, message: 'Resolving...' },
      }));
    }
    if (pathname.match(/\/api\/v1\/authors\/\d+\/reclassify-as-narrator/) && method === 'POST') {
      return route.fulfill(jsonResponse({ message: 'Reclassified', books_updated: 1 }));
    }
    if (pathname === '/api/v1/authors/duplicates/ai-review' && method === 'POST') {
      return route.fulfill(jsonResponse({ id: 'ai-review-1', type: 'ai-author-review', status: 'running', progress: 0, total: 100, message: 'Starting AI review...' }, 202));
    }
    if (pathname === '/api/v1/authors/duplicates/ai-review/apply' && method === 'POST') {
      return route.fulfill(jsonResponse({ id: 'ai-apply-1', type: 'ai-author-merge-apply', status: 'running', progress: 0, total: 100, message: 'Applying...' }, 202));
    }
    if (pathname.match(/\/api\/v1\/operations\/[^/]+\/result/) && method === 'GET') {
      return route.fulfill(jsonResponse({
        result_data: [
          { group_index: 0, action: 'merge', canonical_name: 'Test Author', reason: 'Same author', confidence: 'high' },
        ],
      }));
    }

    // Series dedup endpoints
    if (pathname === '/api/v1/series/duplicates' && method === 'GET') {
      const dedup = mockState.seriesDedup as { groups: MockSeriesDupGroup[]; total_series?: number };
      // Transform MockSeriesDupGroup to the real SeriesDupGroup shape
      const groups = (dedup.groups || []).map((g) => ({
        name: g.canonical.name,
        count: g.book_count,
        series: [
          { id: g.canonical.id, name: g.canonical.name, created_at: new Date().toISOString(), books: (g.books || []) },
          ...g.variants.map((v) => ({ id: v.id, name: v.name, created_at: new Date().toISOString(), books: [] })),
        ],
        suggested_name: g.canonical.name,
        match_type: 'fuzzy',
      }));
      return route.fulfill(jsonResponse({ groups, count: groups.length, total_series: dedup.total_series || 0 }));
    }
    if (pathname === '/api/v1/series/duplicates/refresh' && method === 'POST') {
      return route.fulfill(jsonResponse({ id: 'series-refresh-1', type: 'series-dedup-scan', status: 'running', progress: 0, total: 100, message: 'Starting...' }));
    }
    if (pathname === '/api/v1/series/deduplicate' && method === 'POST') {
      return route.fulfill(jsonResponse({ id: 'series-merge-1', type: 'series-merge', status: 'completed', message: 'Series merged' }));
    }
    if (pathname.match(/\/api\/v1\/series\/\d+\/name/) && method === 'PUT') {
      return route.fulfill(jsonResponse({ message: 'Series renamed' }));
    }

    // Dedup validation
    if (pathname === '/api/v1/dedup/validate' && method === 'POST') {
      const body = request.postDataJSON();
      return route.fulfill(jsonResponse({ results: [{ source: 'openlibrary', title: body?.query || 'Test', author: 'Test Author' }], query: body?.query || '', type: body?.type || 'author' }));
    }

    // System tasks (scheduler)
    if (pathname === '/api/v1/system/tasks' && method === 'GET') {
      return route.fulfill(jsonResponse({ tasks: [
        { name: 'library_scan', description: 'Scan library folders', category: 'library', enabled: true, interval_minutes: 0, run_on_startup: false },
        { name: 'dedup_refresh', description: 'Refresh author & series dedup cache', category: 'maintenance', enabled: true, interval_minutes: 360, run_on_startup: false },
        { name: 'resolve_production_authors', description: 'Resolve real authors for production companies', category: 'maintenance', enabled: false, interval_minutes: 0, run_on_startup: false },
      ] }));
    }
    if (pathname.match(/\/api\/v1\/system\/tasks\/[^/]+\/run/) && method === 'POST') {
      return route.fulfill(jsonResponse({ id: 'task-run-1', type: 'task', status: 'running', progress: 0, total: 100, message: 'Running...' }));
    }

    // Audiobook versions sub-endpoint
    if (pathname.match(/\/api\/v1\/audiobooks\/[^/]+\/versions$/) && method === 'GET') {
      const bookId = pathname.split('/')[4];
      const book = mockState.books.find((b: Record<string, unknown>) => b.id === bookId);
      if (book && book.version_group_id) {
        const versions = mockState.books.filter(
          (b: Record<string, unknown>) => b.version_group_id === book.version_group_id
        );
        return route.fulfill(jsonResponse({ versions }));
      }
      return route.fulfill(jsonResponse({ versions: [] }));
    }

    if (pathname.match(/\/api\/v1\/audiobooks\/[^/]+\/versions$/) && method === 'POST') {
      const bookId = pathname.split('/')[4];
      const body = request.postDataJSON();
      const otherId = body?.other_id;
      const book = mockState.books.find((b: Record<string, unknown>) => b.id === bookId);
      if (book) {
        const groupId = book.version_group_id || `group-${Date.now()}`;
        // Update both books to share group
        mockState.books = mockState.books.map((b: Record<string, unknown>) => {
          if (b.id === bookId) return { ...b, version_group_id: groupId, is_primary_version: b.is_primary_version ?? true };
          if (b.id === otherId) return { ...b, version_group_id: groupId };
          return b;
        }) as typeof mockState.books;
      }
      return route.fulfill(jsonResponse({ message: 'Linked' }));
    }

    // Unlink version (DELETE)
    if (pathname.match(/\/api\/v1\/audiobooks\/[^/]+\/versions$/) && method === 'DELETE') {
      const bookId = pathname.split('/')[4];
      const body = request.postDataJSON();
      const otherId = body?.other_id;
      const book = mockState.books.find((b: Record<string, unknown>) => b.id === bookId);
      if (book && book.version_group_id) {
        const groupId = book.version_group_id;
        // Remove the other book from the group
        mockState.books = mockState.books.map((b: Record<string, unknown>) => {
          if (b.id === otherId) return { ...b, version_group_id: undefined, is_primary_version: false };
          return b;
        }) as typeof mockState.books;
        // If only one book left in group, remove group from it too
        const remaining = mockState.books.filter((b: Record<string, unknown>) => b.version_group_id === groupId);
        if (remaining.length <= 1) {
          mockState.books = mockState.books.map((b: Record<string, unknown>) => {
            if (b.version_group_id === groupId) return { ...b, version_group_id: undefined, is_primary_version: false };
            return b;
          }) as typeof mockState.books;
        }
      }
      return route.fulfill(jsonResponse({ message: 'Unlinked' }));
    }

    // Audiobook tags sub-endpoint
    if (pathname.match(/\/api\/v1\/audiobooks\/[^/]+\/tags$/) && method === 'GET') {
      const bookId = pathname.split('/')[4];
      const book = mockState.books.find((b: Record<string, unknown>) => b.id === bookId);
      return route.fulfill(jsonResponse({
        tags: {},
        media: [],
        media_info: {
          codec: book?.codec || 'M4B',
          bitrate: book?.bitrate || 128,
          sample_rate: book?.sample_rate || 44100,
          channels: 2,
          bit_depth: 16,
        },
        file_count: 1,
        duration: book?.duration || 0,
      }));
    }

    // Set primary version
    if (pathname.match(/\/api\/v1\/audiobooks\/[^/]+\/set-primary$/) && method === 'PUT') {
      const bookId = pathname.split('/')[4];
      const book = mockState.books.find((b: Record<string, unknown>) => b.id === bookId);
      if (book && book.version_group_id) {
        mockState.books = mockState.books.map((b: Record<string, unknown>) => {
          if (b.version_group_id === book.version_group_id) {
            return { ...b, is_primary_version: b.id === bookId };
          }
          return b;
        }) as typeof mockState.books;
      }
      return route.fulfill(jsonResponse({ message: 'Primary set' }));
    }

    // Fetch metadata for specific book
    if (pathname.match(/\/api\/v1\/audiobooks\/[^/]+\/fetch-metadata$/) && method === 'POST') {
      const bookId = pathname.split('/')[4];
      const book = mockState.books.find((b: Record<string, unknown>) => b.id === bookId);
      if (book?.fetch_metadata_error) {
        const delayMs = (book as Record<string, unknown>).fetch_metadata_delay_ms as number || 0;
        if (delayMs > 0) await new Promise(resolve => setTimeout(resolve, delayMs));
        return route.fulfill(jsonResponse({ error: 'Metadata fetch failed' }, 500));
      }
      const delayMs = (book as Record<string, unknown>)?.fetch_metadata_delay_ms as number || 0;
      if (delayMs > 0) await new Promise(resolve => setTimeout(resolve, delayMs));
      if (book) {
        Object.assign(book, {
          publisher: book.publisher || 'Test Publisher',
          description: book.description || 'Updated metadata',
        });
      }
      return route.fulfill(jsonResponse({ message: 'Metadata fetched', book: book || {}, source: 'mock' }));
    }

    // Parse with AI for specific book
    if (pathname.match(/\/api\/v1\/audiobooks\/[^/]+\/parse-with-ai$/) && method === 'POST') {
      const bookId = pathname.split('/')[4];
      const book = mockState.books.find((b: Record<string, unknown>) => b.id === bookId);
      return route.fulfill(jsonResponse({ message: 'Parsed', book: book || {}, confidence: 'high' }));
    }

    // Restore soft-deleted book
    if (pathname.match(/\/api\/v1\/audiobooks\/[^/]+\/restore$/) && method === 'POST') {
      const bookId = pathname.split('/')[4];
      mockState.books = mockState.books.map((b: Record<string, unknown>) =>
        b.id === bookId ? { ...b, marked_for_deletion: false } : b
      ) as typeof mockState.books;
      return route.fulfill(jsonResponse({ message: 'Restored' }));
    }

    // Generic audiobook GET by ID (must come AFTER sub-path handlers)
    if (pathname.match(/\/api\/v1\/audiobooks\/[^/]+$/) && method === 'GET') {
      const bookId = pathname.split('/').pop() || '';
      const book = mockState.books.find((b) => b.id === bookId);
      if (book) {
        return route.fulfill(jsonResponse(book));
      }
      return route.fulfill(jsonResponse({ error: 'Not found' }, 404));
    }

    // Operations endpoints
    if (pathname === '/api/v1/operations/active' && method === 'GET') {
      const f = maybeFailStatus(mockState.failures.operationsActive, 'Failed to fetch operations'); if (f) return f;
      const active = (mockState.operations as { active?: unknown[] }).active || [];
      return route.fulfill(jsonResponse({ operations: active }));
    }

    if (pathname === '/api/v1/operations/history' && method === 'GET') {
      const history = (mockState.operations as { history?: unknown[] }).history || [];
      const offset = parseInt(url.searchParams.get('offset') || '0', 10);
      const limit = parseInt(url.searchParams.get('limit') || '20', 10);
      return route.fulfill(jsonResponse({
        operations: history.slice(offset, offset + limit),
        total: history.length,
      }));
    }

    if (pathname.match(/\/api\/v1\/operations\/[^/]+\/status/) && method === 'GET') {
      const opId = pathname.split('/')[4];
      const active = (mockState.operations as { active?: Array<Record<string, unknown>> }).active || [];
      const history = (mockState.operations as { history?: Array<Record<string, unknown>> }).history || [];
      const op = [...active, ...history].find((o) => o.id === opId);
      if (op) return route.fulfill(jsonResponse(op));
      return route.fulfill(jsonResponse({
        id: opId,
        status: 'completed',
        progress: 100,
        type: 'scan',
      }));
    }

    if (pathname.match(/\/api\/v1\/operations\/[^/]+\/logs/) && method === 'GET') {
      const f = maybeFailStatus(mockState.failures.operationLogs, 'Failed to fetch operation logs'); if (f) return f;
      const opId = pathname.split('/')[4];
      const logs = (mockState.operations as { logs?: Record<string, unknown[]> }).logs || {};
      return route.fulfill(jsonResponse({ logs: logs[opId] || [], items: logs[opId] || [] }));
    }

    if (pathname === '/api/v1/operations/scan' && method === 'POST') {
      return route.fulfill(jsonResponse({ id: 'op-scan-1', status: 'running', type: 'scan', progress: 0 }, 201));
    }

    if (pathname === '/api/v1/operations/organize' && method === 'POST') {
      let body: Record<string, unknown> = {};
      try { body = request.postDataJSON() || {}; } catch { /* ignore */ }
      const bookIds = Array.isArray(body.book_ids) ? body.book_ids as string[] : [];
      let errorBook: Record<string, unknown> | null = null;
      const rootDir = (mockState.config.root_dir || '/library').replace(/\/+$/, '');
      mockState.books = mockState.books.map((b) => {
        if (bookIds.length === 0 || bookIds.includes(b.id)) {
          if (b.organize_error) {
            errorBook = b as Record<string, unknown>;
            return b;
          }
          const fileName = b.file_path ? b.file_path.split('/').pop() : `${b.id}.m4b`;
          return {
            ...b,
            library_state: 'organized',
            organized_file_hash: `hash-org-${b.id}`,
            file_path: `${rootDir}/${fileName}`,
          };
        }
        return b;
      }) as typeof mockState.books;
      if (errorBook) {
        return route.fulfill(jsonResponse({
          id: 'op-org-1', status: 'error', type: 'organize',
          error_message: (errorBook as Record<string, unknown>).organize_error,
          progress: bookIds.indexOf((errorBook as Record<string, unknown>).id as string),
          total: bookIds.length,
        }, 200));
      }
      return route.fulfill(jsonResponse({ id: 'op-org-1', status: 'completed', type: 'organize', progress: bookIds.length || mockState.books.length, total: bookIds.length || mockState.books.length }, 201));
    }

    if (pathname.match(/\/api\/v1\/operations\/[^/]+$/) && method === 'DELETE') {
      return route.fulfill(jsonResponse({ message: 'Cancelled' }));
    }

    // Dashboard endpoint
    if (pathname === '/api/v1/dashboard') {
      return route.fulfill(jsonResponse({
        library_count: mockState.books.length,
        import_path_count: mockState.importPaths.length,
        recent_operations: [],
        storage: {
          library_size_bytes: mockState.books.reduce((sum: number, b: Record<string, unknown>) => sum + (Number(b.file_size) || 0), 0),
          total_size_bytes: 0,
          disk_free_bytes: 500 * 1024 * 1024 * 1024,
        },
      }));
    }

    // Authors and Series
    if (pathname === '/api/v1/authors' && method === 'GET') {
      const authors = [...new Set(mockState.books.map((b: Record<string, unknown>) => b.author_name).filter(Boolean))];
      return route.fulfill(jsonResponse({ authors: authors.map((name, i) => ({ id: `author-${i}`, name })) }));
    }

    if (pathname === '/api/v1/series' && method === 'GET') {
      const series = [...new Set(mockState.books.map((b: Record<string, unknown>) => b.series_name).filter(Boolean))];
      return route.fulfill(jsonResponse({ series: series.map((name, i) => ({ id: `series-${i}`, name })) }));
    }

    // Filesystem endpoints
    if (pathname === '/api/v1/filesystem/home') {
      return route.fulfill(
        jsonResponse({ path: mockState.homeDirectory, home: mockState.homeDirectory })
      );
    }

    if (pathname === '/api/v1/filesystem/browse') {
      const f = maybeFailStatus(mockState.failures.filesystem, 'Directory does not exist.'); if (f) return f;
      const browsePath = url.searchParams.get('path') || '/';
      const fsData = mockState.filesystem[browsePath] || { path: browsePath, items: [] };
      return route.fulfill(jsonResponse(fsData));
    }

    if (pathname === '/api/v1/filesystem/exclude' && method === 'POST') {
      try {
        const body = route.request().postDataJSON();
        const targetPath = body?.path;
        if (targetPath) {
          // Update excluded flag in all filesystem entries
          for (const entry of Object.values(mockState.filesystem)) {
            const fsEntry = entry as { items?: Array<{ path: string; excluded: boolean }> };
            if (fsEntry.items) {
              for (const item of fsEntry.items) {
                if (item.path === targetPath) {
                  item.excluded = true;
                }
              }
            }
          }
        }
        return route.fulfill(jsonResponse({ excluded: true, path: targetPath }));
      } catch { /* ignore */ }
      return route.fulfill(jsonResponse({ excluded: true, path: '' }));
    }

    if (pathname === '/api/v1/filesystem/exclude' && method === 'DELETE') {
      try {
        const body = route.request().postDataJSON();
        const targetPath = body?.path;
        if (targetPath) {
          for (const entry of Object.values(mockState.filesystem)) {
            const fsEntry = entry as { items?: Array<{ path: string; excluded: boolean }> };
            if (fsEntry.items) {
              for (const item of fsEntry.items) {
                if (item.path === targetPath) {
                  item.excluded = false;
                }
              }
            }
          }
        }
        return route.fulfill(jsonResponse({ excluded: false, path: targetPath }));
      } catch { /* ignore */ }
      return route.fulfill(jsonResponse({ excluded: false, path: '' }));
    }

    // Blocked hashes
    if (pathname === '/api/v1/blocked-hashes' && method === 'GET') {
      const f = maybeFailStatus(mockState.failures.blockedHashes, 'Failed to load blocked hashes'); if (f) return f;
      return route.fulfill(jsonResponse({
        items: mockState.blockedHashes,
        total: mockState.blockedHashes.length,
      }));
    }

    if (pathname === '/api/v1/blocked-hashes' && method === 'POST') {
      try {
        const body = route.request().postDataJSON();
        if (body?.hash) {
          mockState.blockedHashes.push({
            hash: body.hash,
            reason: body.reason || '',
            created_at: new Date().toISOString(),
          });
        }
      } catch { /* ignore */ }
      return route.fulfill(jsonResponse({ message: 'Added' }, 201));
    }

    if (pathname.startsWith('/api/v1/blocked-hashes/') && method === 'DELETE') {
      const hash = decodeURIComponent(pathname.split('/').pop() || '');
      mockState.blockedHashes = mockState.blockedHashes.filter(
        (h: { hash: string }) => h.hash !== hash
      );
      return route.fulfill(jsonResponse({ message: 'Removed' }));
    }

    // Book update/delete
    if (pathname.startsWith('/api/v1/audiobooks/') && method === 'PUT') {
      const bookId = pathname.split('/')[4];
      const book = mockState.books.find((b: Record<string, unknown>) => b.id === bookId);
      if (book) {
        let body: Record<string, unknown> = {};
        try { body = request.postDataJSON() || {}; } catch { /* ignore */ }
        if (book.force_update_required && !body.force_update) {
          return route.fulfill(jsonResponse({ error: 'Conflict' }, 409));
        }
        Object.assign(book, body);
        return route.fulfill(jsonResponse(book));
      }
      return route.fulfill(jsonResponse({ error: 'Not found' }, 404));
    }

    if (pathname.startsWith('/api/v1/audiobooks/') && method === 'DELETE') {
      return route.fulfill(jsonResponse({ message: 'Deleted' }));
    }

    if (pathname.startsWith('/api/v1/audiobooks/') && method === 'POST') {
      // Handle various POST sub-endpoints (fetch-metadata, parse-with-ai, versions, restore)
      return route.fulfill(jsonResponse({ message: 'OK', book: mockState.books[0] || {} }));
    }

    // Batch operations
    if (pathname === '/api/v1/audiobooks/batch' && method === 'POST') {
      return route.fulfill(jsonResponse({ message: 'Batch update complete', updated: 0 }));
    }

    // Metadata endpoints
    if (pathname === '/api/v1/metadata/bulk-fetch' && method === 'POST') {
      return route.fulfill(jsonResponse({ message: 'Bulk fetch started', operation_id: 'op-bulk-1' }));
    }

    if (pathname.startsWith('/api/v1/metadata/')) {
      return route.fulfill(jsonResponse({ message: 'OK' }));
    }

    // AI endpoints
    if (pathname === '/api/v1/ai/test-connection' && method === 'POST') {
      if (mockState.failures.openaiTest) {
        return route.fulfill(jsonResponse({ error: 'Connection failed' }, mockState.failures.openaiTest));
      }
      return route.fulfill(jsonResponse({ success: true, message: 'Connection successful' }));
    }

    // AI backend-mode status/pull-model (TASK-11). Must come before the
    // "/api/v1/ai/" prefix catch-all below, which would otherwise shadow
    // these with a generic { message: 'OK' } response.
    if (pathname === '/api/v1/ai/backends/status' && method === 'GET') {
      return route.fulfill(jsonResponse({ data: mockState.aiBackendsStatus }));
    }

    if (pathname === '/api/v1/ai/backends/pull-model' && method === 'POST') {
      let model = '';
      try {
        const body = route.request().postDataJSON();
        model = (body && body.model) || '';
      } catch {
        // ignore parse errors
      }
      const status = mockState.aiBackendsStatus as Record<string, unknown>;
      if (status.embedding_model && (status.embedding_model as { name: string }).name === model) {
        status.embedding_model = { name: model, pulled: true };
      }
      if (status.llm_model && (status.llm_model as { name: string }).name === model) {
        status.llm_model = { name: model, pulled: true };
      }
      return route.fulfill(jsonResponse({ data: { model, pulled: true } }));
    }

    if (pathname.startsWith('/api/v1/ai/')) {
      return route.fulfill(jsonResponse({ message: 'OK' }));
    }

    // iTunes endpoints
    if (pathname === '/api/v1/itunes/validate' && method === 'POST') {
      const body = request.postDataJSON?.() || {};
      const path = body?.library_path || '';
      if (!path || path.includes('nonexistent') || path.includes('invalid')) {
        return route.fulfill(jsonResponse({ error: 'iTunes library file not found' }, 400));
      }
      const itunesValidation = (mockState.itunes as MockITunesState).validation || {
        total_tracks: 120,
        audiobook_tracks: 12,
        files_found: 12,
        files_missing: 0,
        missing_paths: [],
        duplicate_count: 1,
        estimated_import_time: '12 seconds',
      };
      return route.fulfill(jsonResponse(itunesValidation));
    }

    if (pathname === '/api/v1/itunes/import' && method === 'POST') {
      const opId = `op-itunes-${Date.now()}`;
      return route.fulfill(jsonResponse({
        operation_id: opId,
        status: 'queued',
        message: 'iTunes import queued',
      }));
    }

    if (pathname.startsWith('/api/v1/itunes/import-status/') && method === 'GET') {
      const itunesImportStatus = (mockState.itunes as MockITunesState).importStatus || {
        operation_id: 'op-itunes-default',
        status: 'completed',
        progress: 100,
        message: 'Import completed',
        total_books: 12,
        processed: 12,
        imported: 11,
        skipped: 1,
        failed: 0,
        errors: [],
      };
      const opId = pathname.split('/').pop() || itunesImportStatus.operation_id;
      return route.fulfill(jsonResponse({ ...itunesImportStatus, operation_id: opId }));
    }

    if (pathname === '/api/v1/itunes/write-back' && method === 'POST') {
      return route.fulfill(jsonResponse({
        success: true,
        updated_count: 0,
        message: 'Write-back completed',
      }));
    }

    if (pathname === '/api/v1/itunes/resolve-conflicts' && method === 'POST') {
      return route.fulfill(jsonResponse({ success: true, message: 'Conflicts resolved' }));
    }

    // Version management
    if (pathname.startsWith('/api/v1/version-groups/')) {
      return route.fulfill(jsonResponse({ id: 'vg-1', books: [] }));
    }

    // Works endpoints
    if (pathname.startsWith('/api/v1/works')) {
      return route.fulfill(jsonResponse({ works: [], items: [] }));
    }

    // System logs
    if (pathname === '/api/v1/system/logs') {
      return route.fulfill(jsonResponse({ logs: [] }));
    }

    // Import file
    if (pathname === '/api/v1/import/file' && method === 'POST') {
      const t = maybeTimeout(mockState.failures.importFile); if (t) return t;
      const f = maybeFailStatus(mockState.failures.importFile, 'Failed to import file'); if (f) return f;
      return route.fulfill(jsonResponse({ message: 'File imported' }, 201));
    }

    // Open Library endpoints
    if (pathname === '/api/v1/openlibrary/status' && method === 'GET') {
      return route.fulfill(jsonResponse({
        enabled: true,
        downloads: {},
      }));
    }

    if (pathname === '/api/v1/openlibrary/upload' && method === 'POST') {
      return route.fulfill(jsonResponse({
        message: 'dump file uploaded',
        type: 'editions',
        filename: 'test.gz',
        size: 1024,
      }));
    }

    if (pathname === '/api/v1/openlibrary/download' && method === 'POST') {
      return route.fulfill(jsonResponse({ message: 'download started', types: ['editions'] }, 202));
    }

    if (pathname === '/api/v1/openlibrary/import' && method === 'POST') {
      return route.fulfill(jsonResponse({ message: 'import started', types: ['editions'] }, 202));
    }

    if (pathname === '/api/v1/openlibrary/data' && method === 'DELETE') {
      return route.fulfill(jsonResponse({ message: 'data deleted' }));
    }

    // Default: pass through to real server (don't 404 - let it fail naturally)
    console.log(`[Mock API] WARNING: Unhandled endpoint ${method} ${pathname}`);
    return route.continue();
  });
}

/**
 * Setup common routes for all tests
 */
export async function setupCommonRoutes(page: Page) {
  await page.route('**/api/v1/health', (route) => {
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ status: 'ok' }),
    });
  });

  await page.route('**/api/v1/system/status', (route) => {
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        status: 'ok',
        library: { book_count: 0, folder_count: 1, total_size: 0 },
        import_paths: { book_count: 0, folder_count: 0, total_size: 0 },
        memory: {},
        runtime: {},
        operations: { recent: [] },
      }),
    });
  });
}

/**
 * Wait for toast notification
 */
export async function waitForToast(
  page: Page,
  text: string,
  timeout = 5000
) {
  await page.waitForSelector(`text=${text}`, { timeout });
}

const DEFAULT_METADATA_SOURCES: MockMetadataSource[] = [
  {
    id: 'audible',
    name: 'Audible',
    enabled: true,
    priority: 1,
    requires_auth: false,
    credentials: {},
  },
  {
    id: 'openlibrary',
    name: 'Open Library',
    enabled: true,
    priority: 2,
    requires_auth: true,
    credentials: { apiKey: '' },
  },
];

const DEFAULT_CONFIG: MockConfig = {
  root_dir: '/library',
  database_path: '/data/library.db',
  database_type: 'pebble',
  enable_sqlite: false,
  playlist_dir: '/library/playlists',
  organization_strategy: 'auto',
  scan_on_startup: false,
  auto_organize: true,
  folder_naming_pattern: '{author}/{series}/{title} ({print_year})',
  file_naming_pattern: '{title} - {author} - read by {narrator}',
  create_backups: true,
  enable_disk_quota: false,
  disk_quota_percent: 80,
  enable_user_quotas: false,
  default_user_quota_gb: 100,
  auto_fetch_metadata: true,
  metadata_sources: DEFAULT_METADATA_SOURCES,
  language: 'en',
  enable_ai_parsing: false,
  openai_api_key: '',
  concurrent_scans: 4,
  memory_limit_type: 'items',
  cache_size: 1000,
  memory_limit_percent: 25,
  memory_limit_mb: 512,
  purge_soft_deleted_after_days: 30,
  purge_soft_deleted_delete_files: false,
  log_level: 'info',
  log_format: 'text',
  enable_json_logging: false,
  api_keys: {},
  supported_extensions: ['.m4b', '.mp3', '.m4a'],
  exclude_patterns: [],
};

const DEFAULT_SYSTEM_STATUS = {
  status: 'ok',
  library_book_count: 0,
  import_book_count: 0,
  total_book_count: 0,
  library_size_bytes: 0,
  import_size_bytes: 0,
  total_size_bytes: 0,
  disk_total_bytes: 500 * 1024 * 1024 * 1024,
  disk_used_bytes: 0,
  disk_free_bytes: 500 * 1024 * 1024 * 1024,
  root_directory: '/library',
  library: {
    book_count: 0,
    folder_count: 1,
    total_size: 0,
    path: '/library',
  },
  import_paths: {
    book_count: 0,
    folder_count: 0,
    total_size: 0,
  },
  memory: {
    alloc_bytes: 0,
    total_alloc_bytes: 0,
    sys_bytes: 0,
    num_gc: 0,
    heap_alloc: 0,
    heap_sys: 0,
  },
  runtime: {
    go_version: 'go1.25',
    num_goroutine: 1,
    num_cpu: 4,
    os: 'linux',
    arch: 'amd64',
  },
  operations: { recent: [] },
};

const buildConfig = (overrides?: Partial<MockConfig>): MockConfig => ({
  ...DEFAULT_CONFIG,
  ...overrides,
  metadata_sources:
    overrides?.metadata_sources ?? DEFAULT_CONFIG.metadata_sources,
  supported_extensions:
    overrides?.supported_extensions ?? DEFAULT_CONFIG.supported_extensions,
  exclude_patterns:
    overrides?.exclude_patterns ?? DEFAULT_CONFIG.exclude_patterns,
});

const buildSystemStatus = (
  overrides?: Record<string, unknown>
): Record<string, unknown> => ({
  ...DEFAULT_SYSTEM_STATUS,
  ...(overrides || {}),
});

const buildAuthState = (
  overrides?: MockAuthState
): ResolvedMockAuthState => {
  const hasUsers = overrides?.has_users ?? false;
  const authEnabled = overrides?.auth_enabled ?? true;
  const requiresAuth = overrides?.requires_auth ?? (authEnabled && hasUsers);
  const bootstrapReady = overrides?.bootstrap_ready ?? (authEnabled && !hasUsers);
  return {
    auth_enabled: authEnabled,
    has_users: hasUsers,
    requires_auth: requiresAuth,
    bootstrap_ready: bootstrapReady,
    login_username: overrides?.login_username || 'admin',
    login_password: overrides?.login_password || 'password123',
    currentUser: overrides?.currentUser ?? null,
    sessions: overrides?.sessions || [],
  };
};

/**
 * Generate test audiobooks
 */
export function generateTestBooks(count: number) {
  const authors = [
    'Brandon Sanderson',
    'J.R.R. Tolkien',
    'Terry Pratchett',
    'Isaac Asimov',
    'Ursula K. Le Guin',
  ];
  const series = [
    'The Stormlight Archive',
    'The Lord of the Rings',
    'Discworld',
    'Foundation',
    'Earthsea',
  ];
  const languages = ['en', 'es', 'fr'];

  return Array.from({ length: count }, (_, i) => ({
    id: `book-${i + 1}`,
    title: `Test Book ${i + 1}`,
    author_name: authors[i % authors.length],
    series_name: i % 3 === 0 ? series[i % series.length] : null,
    series_position: i % 3 === 0 ? (i % 5) + 1 : null,
    library_state: i % 4 === 0 ? 'import' : 'organized',
    marked_for_deletion: false,
    language: languages[i % languages.length],
    file_path: `/library/book${i + 1}.m4b`,
    file_hash: `hash-${i + 1}`,
    original_file_hash: `hash-orig-${i + 1}`,
    organized_file_hash: i % 4 !== 0 ? `hash-org-${i + 1}` : null,
    created_at: new Date(2024, 0, i + 1).toISOString(),
    updated_at: new Date(2024, 11, i + 1).toISOString(),
    duration: 3600 + (i * 100),
    file_size: 100000000 + (i * 1000000),
  }));
}

/**
 * Generate test audiobook with full metadata
 */
export function generateTestBook(overrides: Record<string, unknown> = {}) {
  return {
    id: 'test-book-1',
    title: 'The Way of Kings',
    author_name: 'Brandon Sanderson',
    narrator: 'Michael Kramer, Kate Reading',
    series_name: 'The Stormlight Archive',
    series_position: 1,
    publisher: 'Tor Books',
    audiobook_release_year: 2010,
    language: 'en',
    isbn: '9780765326355',
    description: 'Epic fantasy novel',
    genre: 'Fantasy',
    library_state: 'organized',
    marked_for_deletion: false,
    file_path:
      '/library/Brandon Sanderson/The Stormlight Archive/' +
      'The Way of Kings.m4b',
    file_hash: 'hash-twok',
    original_file_hash: 'hash-orig-twok',
    organized_file_hash: 'hash-org-twok',
    created_at: '2024-01-01T12:00:00Z',
    updated_at: '2024-12-01T12:00:00Z',
    duration: 45600,
    file_size: 450000000,
    ...overrides,
  };
}

/**
 * Setup shared mock API responses and state for tests
 */
export async function setupMockApi(
  page: Page,
  options: MockApiOptions = {}
) {
  // New approach: Use the page.route() based mocking for persistence across navigations
  // This replaces the old addInitScript approach
  await skipWelcomeWizard(page);
  await mockEventSource(page);
  await setupMockApiRoutes(page, options);
}

/**
 * @deprecated Use setupPhase2Interactive or setupMockApiRoutes instead
 * This function is kept for backward compatibility but internally delegates to new setup
 */
export async function setupMockApiLegacy(
  page: Page,
  options: MockApiOptions = {}
) {
  const configData = buildConfig(options.config);
  const systemStatusData = buildSystemStatus(options.systemStatus);
  const importPathsData = options.importPaths || [];
  const backupsData = options.backups || [];
  const blockedHashesData = options.blockedHashes || [];
  const filesystemData = options.filesystem || {};
  const homeDirectoryData = options.homeDirectory || '/';
  const operationsData = options.operations || {};
  const itunesData = options.itunes || {};
  const failuresData = options.failures || {};
  const bookData = options.books || [];

  await page.addInitScript(
    ({
      bookData,
      configData,
      importPathsData,
      backupsData,
      blockedHashesData,
      filesystemData,
      homeDirectoryData,
      operationsData,
      itunesData,
      failuresData,
      systemStatusData,
    }: {
      bookData: TestBook[];
      configData: MockConfig;
      importPathsData: MockImportPath[];
      backupsData: MockBackup[];
      blockedHashesData: MockBlockedHash[];
      filesystemData: Record<string, MockFilesystemEntry>;
      homeDirectoryData: string;
      operationsData: {
        active?: MockActiveOperation[];
        history?: MockOperation[];
        logs?: Record<string, MockOperationLog[]>;
      };
      itunesData: MockITunesState;
      failuresData: MockFailures;
      systemStatusData: Record<string, unknown>;
    }) => {
      let libraryBooks = [...bookData];
      let configState = { ...configData };
      let importPaths = [...importPathsData];
      let backups = [...backupsData];
      let blockedHashes = [...blockedHashesData];
      const filesystem = { ...filesystemData };
      let activeOperations = [...(operationsData.active || [])];
      let historyOperations = [...(operationsData.history || [])];
      const operationLogs = { ...(operationsData.logs || {}) };
      const defaultITunesValidation: MockITunesValidation = {
        total_tracks: 120,
        audiobook_tracks: 12,
        files_found: 12,
        files_missing: 0,
        missing_paths: [],
        duplicate_count: 1,
        estimated_import_time: '12 seconds',
      };
      const defaultITunesImportStatus: MockITunesImportStatus = {
        operation_id: 'op-itunes-default',
        status: 'completed',
        progress: 100,
        message: 'Import completed',
        total_books: 12,
        processed: 12,
        imported: 11,
        skipped: 1,
        failed: 0,
        errors: [],
      };
      const itunesValidation = itunesData.validation || defaultITunesValidation;
      let itunesImportStatus =
        itunesData.importStatus || defaultITunesImportStatus;
      let itunesOperationId = itunesImportStatus.operation_id;
      const failures = { ...failuresData };

      const jsonResponse = (body: unknown, status = 200) =>
        new Response(JSON.stringify(body), {
          status,
          headers: { 'Content-Type': 'application/json' },
        });

      const originalFetch = window.fetch.bind(window);
      const parseJsonBody = (init?: RequestInit) => {
        if (!init?.body) return null;
        if (typeof init.body === 'string') {
          try {
            return JSON.parse(init.body);
          } catch {
            return null;
          }
        }
        return null;
      };

      const apiState = {
        searchCalls: 0,
        activeOperations,
        historyOperations,
      };

      const updateApiState = () => {
        apiState.activeOperations = activeOperations;
        apiState.historyOperations = historyOperations;
      };

      const failWithStatus = (
        status: number | undefined,
        message: string
      ) => {
        if (!status) return null;
        return jsonResponse({ error: message }, status);
      };

      const maybeTimeout = (value: number | 'timeout' | undefined) => {
        if (value === 'timeout') {
          return Promise.reject(new Error('timeout'));
        }
        return null;
      };

      const maskSecretValue = (value: string) => {
        if (!value) return '';
        return `***${value.slice(-4)}`;
      };

      const maskedConfig = () => {
        return {
          ...configState,
          openai_api_key: maskSecretValue(configState.openai_api_key || ''),
          api_keys: {},
        };
      };

      const updateExcluded = (path: string, excluded: boolean) => {
        Object.values(filesystem).forEach((entry) => {
          entry.items = entry.items.map((item) =>
            item.path === path ? { ...item, excluded } : item
          );
        });
      };

      const getVersionGroupId = (bookId: string): string | null => {
        const match = libraryBooks.find((book) => book.id === bookId);
        return match?.version_group_id || null;
      };

      const ensureVersionGroup = (bookId: string): string => {
        const existing = getVersionGroupId(bookId);
        if (existing) return existing;
        const newGroup = `group-${Date.now()}-${Math.random()}`;
        libraryBooks = libraryBooks.map((book) =>
          book.id === bookId
            ? { ...book, version_group_id: newGroup, is_primary_version: true }
            : book
        );
        return newGroup;
      };

      const getVersionsForBook = (bookId: string) => {
        const groupId = getVersionGroupId(bookId);
        if (!groupId) return [];
        return libraryBooks.filter((book) => book.version_group_id === groupId);
      };

      const normalizeRoot = (root: string) => {
        return root.endsWith('/') ? root.slice(0, -1) : root;
      };

      const getFileName = (path: string | undefined, id: string) => {
        if (!path) return `${id}.m4b`;
        const parts = path.split('/');
        const name = parts[parts.length - 1];
        return name || `${id}.m4b`;
      };

      const buildOrganizedBook = (book: TestBook): TestBook => {
        const rootDir = normalizeRoot(configState.root_dir || '/library');
        const fileName = getFileName(book.file_path, book.id);
        const organizedHash =
          book.organized_file_hash ||
          (book.file_hash
            ? `organized-${book.file_hash}`
            : `organized-${book.id}`);
        return {
          ...book,
          library_state: 'organized',
          marked_for_deletion: false,
          file_path: `${rootDir}/${fileName}`,
          organized_file_hash: organizedHash,
        };
      };

      (window as unknown as { __apiMock: unknown }).__apiMock = {
        state: apiState,
        setActiveOperations: (ops: MockActiveOperation[]) => {
          activeOperations = ops;
          updateApiState();
        },
        setHistoryOperations: (ops: MockOperation[]) => {
          historyOperations = ops;
          updateApiState();
        },
        setOperationLogs: (id: string, logs: MockOperationLog[]) => {
          operationLogs[id] = logs;
        },
        setConfig: (updates: Partial<MockConfig>) => {
          configState = { ...configState, ...updates };
        },
      };

      window.fetch = (input: RequestInfo | URL, init?: RequestInit) => {
        const url =
          typeof input === 'string'
            ? input
            : input instanceof URL
              ? input.toString()
              : input.url;
        const method = (init?.method || 'GET').toUpperCase();
        const urlObj = new URL(url, window.location.origin);
        const pathname = urlObj.pathname;

        const applySearchFilter = (
          items: typeof libraryBooks,
          query: string
        ) => {
          if (!query) return items;
          const searchLower = query.toLowerCase();
          return items.filter((b) => {
            const title = b.title?.toLowerCase() || '';
            const author = b.author_name?.toLowerCase() || '';
            const series = b.series_name?.toLowerCase() || '';
            return (
              title.includes(searchLower) ||
              author.includes(searchLower) ||
              series.includes(searchLower)
            );
          });
        };

        // Health/system
        if (pathname === '/api/v1/health') {
          return Promise.resolve(jsonResponse({ status: 'ok' }));
        }
        if (pathname === '/api/v1/system/status') {
          const librarySize = libraryBooks.reduce(
            (sum, book) => sum + (book.file_size || 0),
            0
          );
          const importBookCount = libraryBooks.filter(
            (book) =>
              book.library_state === 'import' && !book.marked_for_deletion
          ).length;
          const totalBooks = libraryBooks.length;
          const diskTotal =
            (systemStatusData as { disk_total_bytes?: number })
              .disk_total_bytes || 0;
          const status = {
            ...systemStatusData,
            library: {
              ...(systemStatusData as { library?: Record<string, unknown> })
                .library,
              book_count: totalBooks,
              total_size: librarySize,
            },
            import_paths: {
              ...(systemStatusData as {
                import_paths?: Record<string, unknown>;
              }).import_paths,
              folder_count: importPaths.length,
              book_count: importBookCount,
            },
            operations: { recent: historyOperations },
            library_book_count: totalBooks,
            import_book_count: importBookCount,
            total_book_count: totalBooks,
            library_size_bytes: librarySize,
            total_size_bytes: librarySize,
            disk_used_bytes: librarySize,
            disk_free_bytes: Math.max(0, diskTotal - librarySize),
          };
          return Promise.resolve(jsonResponse(status));
        }
        if (pathname === '/api/v1/config') {
          if (method === 'GET') {
            const failed = failWithStatus(
              failures.getConfig,
              'Failed to fetch config'
            );
            if (failed) {
              return Promise.resolve(failed);
            }
            return Promise.resolve(jsonResponse({ config: maskedConfig() }));
          }
          if (method === 'PUT') {
            const failed = failWithStatus(
              failures.updateConfig,
              'Failed to update config'
            );
            if (failed) {
              return Promise.resolve(failed);
            }
            const updates = parseJsonBody(init) || {};
            if (updates.api_keys) {
              configState.api_keys = { ...configState.api_keys, ...updates.api_keys };
            }
            configState = { ...configState, ...updates };
            return Promise.resolve(jsonResponse({ config: maskedConfig() }));
          }
        }
        if (pathname === '/api/v1/ai/test-connection' && method === 'POST') {
          const failed = failWithStatus(
            failures.openaiTest,
            'Connection failed'
          );
          if (failed) {
            return Promise.resolve(failed);
          }
          return Promise.resolve(
            jsonResponse({
              success: true,
              model: 'gpt-4o-mini',
              message: 'Connection successful',
            })
          );
        }

        if (pathname === '/api/v1/import-paths' && method === 'GET') {
          return Promise.resolve(jsonResponse({ importPaths }));
        }
        if (pathname === '/api/v1/import-paths' && method === 'POST') {
          const payload = parseJsonBody(init) || {};
          const nextId =
            importPaths.reduce((max, item) => Math.max(max, item.id), 0) + 1;
          const newPath = {
            id: nextId,
            path: payload.path || '/unknown',
            book_count: 0,
          };
          importPaths = [...importPaths, newPath];
          return Promise.resolve(jsonResponse(newPath, 201));
        }
        if (
          pathname.startsWith('/api/v1/import-paths/') &&
          method === 'DELETE'
        ) {
          const id = Number(pathname.split('/').pop() || 0);
          importPaths = importPaths.filter((item) => item.id !== id);
          return Promise.resolve(jsonResponse({ message: 'Removed' }));
        }
        if (pathname === '/api/v1/import/file' && method === 'POST') {
          const timeout = maybeTimeout(failures.importFile);
          if (timeout) {
            return timeout;
          }
          const failed = failWithStatus(
            typeof failures.importFile === 'number'
              ? failures.importFile
              : undefined,
            'Failed to import file'
          );
          if (failed) {
            return Promise.resolve(failed);
          }
          const payload = parseJsonBody(init) || {};
          const newBook = {
            id: `import-${Date.now()}`,
            title: payload.file_path?.split('/').pop() || 'Imported Book',
            author_name: 'Unknown',
            series_name: null,
            series_position: null,
            library_state: payload.organize ? 'organized' : 'import',
            marked_for_deletion: false,
            language: 'en',
            file_path: payload.file_path || '/imported/book.m4b',
            file_hash: `hash-${Date.now()}`,
            original_file_hash: `hash-orig-${Date.now()}`,
            organized_file_hash: payload.organize
              ? `hash-org-${Date.now()}`
              : null,
            created_at: new Date().toISOString(),
            updated_at: new Date().toISOString(),
          };
          libraryBooks = [newBook, ...libraryBooks];
          return Promise.resolve(
            jsonResponse({ message: 'Import started', book: newBook })
          );
        }

        if (pathname === '/api/v1/backup/list' && method === 'GET') {
          const failed = failWithStatus(
            failures.listBackups,
            'Failed to list backups'
          );
          if (failed) {
            return Promise.resolve(failed);
          }
          return Promise.resolve(jsonResponse({ backups }));
        }
        if (pathname === '/api/v1/backup/create' && method === 'POST') {
          const failed = failWithStatus(
            failures.createBackup,
            'Failed to create backup'
          );
          if (failed) {
            return Promise.resolve(failed);
          }
          const filename = `backup-${Date.now()}.db.gz`;
          const created = {
            filename,
            size: 25 * 1024 * 1024,
            created_at: new Date().toISOString(),
            auto: false,
          };
          backups = [created, ...backups];
          return Promise.resolve(jsonResponse({ backup: created }));
        }
        if (pathname === '/api/v1/backup/restore' && method === 'POST') {
          const failed = failWithStatus(
            failures.restoreBackup,
            'Backup file is corrupt'
          );
          if (failed) {
            return Promise.resolve(failed);
          }
          return Promise.resolve(jsonResponse({ message: 'Restored' }));
        }
        if (pathname.startsWith('/api/v1/backup/') && method === 'DELETE') {
          const failed = failWithStatus(
            failures.deleteBackup,
            'Failed to delete backup'
          );
          if (failed) {
            return Promise.resolve(failed);
          }
          const filename = pathname.split('/').pop() || '';
          backups = backups.filter((item) => item.filename !== filename);
          return Promise.resolve(jsonResponse({ message: 'Deleted' }));
        }

        if (pathname === '/api/v1/blocked-hashes' && method === 'GET') {
          const failed = failWithStatus(
            failures.blockedHashes,
            'Failed to load blocked hashes'
          );
          if (failed) {
            return Promise.resolve(failed);
          }
          return Promise.resolve(
            jsonResponse({
              items: blockedHashes,
              total: blockedHashes.length,
            })
          );
        }
        if (pathname === '/api/v1/blocked-hashes' && method === 'POST') {
          const payload = parseJsonBody(init) || {};
          const newItem = {
            hash: payload.hash || 'missing',
            reason: payload.reason || 'Manual block',
            created_at: new Date().toISOString(),
          };
          blockedHashes = [newItem, ...blockedHashes];
          return Promise.resolve(jsonResponse(newItem, 201));
        }
        if (
          pathname.startsWith('/api/v1/blocked-hashes/') &&
          method === 'DELETE'
        ) {
          const hash = pathname.split('/').pop() || '';
          blockedHashes = blockedHashes.filter((item) => item.hash !== hash);
          return Promise.resolve(jsonResponse({ message: 'Removed' }));
        }

        if (pathname === '/api/v1/filesystem/home' && method === 'GET') {
          return Promise.resolve(
            jsonResponse({ path: homeDirectoryData })
          );
        }

        if (pathname === '/api/v1/filesystem/browse' && method === 'GET') {
          const failed = failWithStatus(
            failures.filesystem,
            'Failed to browse filesystem'
          );
          if (failed) {
            return Promise.resolve(failed);
          }
          const pathParam = urlObj.searchParams.get('path') || '/';
          const entry = filesystem[pathParam];
          if (entry) {
            return Promise.resolve(
              jsonResponse({
                path: entry.path,
                items: entry.items,
                count: entry.items.length,
                disk_info: entry.disk_info,
              })
            );
          }
          return Promise.resolve(
            jsonResponse({
              path: pathParam,
              items: [],
              count: 0,
              disk_info: {
                exists: true,
                readable: true,
                writable: true,
                total_bytes: 100 * 1024 * 1024 * 1024,
                free_bytes: 50 * 1024 * 1024 * 1024,
                library_bytes: 10 * 1024 * 1024 * 1024,
              },
            })
          );
        }
        if (pathname === '/api/v1/filesystem/exclude' && method === 'POST') {
          const payload = parseJsonBody(init) || {};
          updateExcluded(payload.path, true);
          return Promise.resolve(
            jsonResponse({ excluded: true, path: payload.path })
          );
        }
        if (pathname === '/api/v1/filesystem/exclude' && method === 'DELETE') {
          const payload = parseJsonBody(init) || {};
          updateExcluded(payload.path, false);
          return Promise.resolve(
            jsonResponse({ excluded: false, path: payload.path })
          );
        }

        if (pathname === '/api/v1/itunes/validate' && method === 'POST') {
          const body = parseJsonBody(init);
          const path = body?.library_path || '';
          if (!path || path.includes('nonexistent') || path.includes('invalid')) {
            return Promise.resolve(jsonResponse({ error: 'iTunes library file not found' }, 400));
          }
          return Promise.resolve(jsonResponse(itunesValidation));
        }
        if (pathname === '/api/v1/itunes/import' && method === 'POST') {
          itunesOperationId = `op-itunes-${Date.now()}`;
          itunesImportStatus = {
            ...itunesImportStatus,
            operation_id: itunesOperationId,
          };
          return Promise.resolve(
            jsonResponse({
              operation_id: itunesOperationId,
              status: 'queued',
              message: 'iTunes import queued',
            })
          );
        }
        if (
          pathname.startsWith('/api/v1/itunes/import-status/') &&
          method === 'GET'
        ) {
          const opId = pathname.split('/').pop() || itunesOperationId;
          return Promise.resolve(
            jsonResponse({ ...itunesImportStatus, operation_id: opId })
          );
        }
        if (pathname === '/api/v1/itunes/write-back' && method === 'POST') {
          return Promise.resolve(
            jsonResponse({
              success: true,
              updated_count: itunesImportStatus.imported || 0,
              message: 'Write-back completed',
            })
          );
        }
        if (pathname === '/api/v1/itunes/resolve-conflicts' && method === 'POST') {
          return Promise.resolve(jsonResponse({ success: true, message: 'Conflicts resolved' }));
        }

        if (pathname === '/api/v1/operations/active' && method === 'GET') {
          const failed = failWithStatus(
            failures.operationsActive,
            'Failed to fetch operations'
          );
          if (failed) {
            return Promise.resolve(failed);
          }
          return Promise.resolve(
            jsonResponse({ operations: activeOperations })
          );
        }
        if (pathname === '/api/v1/operations/scan' && method === 'POST') {
          const op = {
            id: `op-scan-${Date.now()}`,
            type: 'scan',
            status: 'running',
            progress: 0,
            total: libraryBooks.length,
            message: 'Scanning',
            folder_path: parseJsonBody(init)?.folder_path,
          };
          activeOperations = [op, ...activeOperations];
          updateApiState();
          return Promise.resolve(jsonResponse(op));
        }
        if (pathname === '/api/v1/operations/organize' && method === 'POST') {
          const payload = parseJsonBody(init) || {};
          const ids = Array.isArray(payload.book_ids) ? payload.book_ids : [];
          if (ids.length > 0) {
            libraryBooks = libraryBooks.map((book) =>
              ids.includes(book.id) ? buildOrganizedBook(book) : book
            );
          }
          const op = {
            id: `op-organize-${Date.now()}`,
            type: 'organize',
            status: 'running',
            progress: 0,
            total: ids.length || libraryBooks.length,
            message: 'Organizing',
            folder_path: payload.folder_path,
          };
          activeOperations = [op, ...activeOperations];
          updateApiState();
          return Promise.resolve(jsonResponse(op));
        }
        if (
          pathname.startsWith('/api/v1/operations/') &&
          pathname.endsWith('/logs') &&
          method === 'GET'
        ) {
          const failed = failWithStatus(
            failures.operationLogs,
            'Failed to fetch operation logs'
          );
          if (failed) {
            return Promise.resolve(failed);
          }
          const parts = pathname.split('/').filter(Boolean);
          const opId = parts[parts.length - 2];
          const logs = operationLogs[opId] || [];
          const tail = parseInt(urlObj.searchParams.get('tail') || '0', 10);
          const items = tail > 0 ? logs.slice(-tail) : logs;
          return Promise.resolve(jsonResponse({ logs: items, items }));
        }
        if (
          pathname.startsWith('/api/v1/operations/') &&
          method === 'DELETE'
        ) {
          const opId = pathname.split('/').pop() || '';
          activeOperations = activeOperations.filter((op) => op.id !== opId);
          historyOperations = [
            {
              id: opId,
              type: 'scan',
              status: 'cancelled',
              progress: 0,
              total: 0,
              message: 'Operation cancelled',
              created_at: new Date().toISOString(),
            },
            ...historyOperations,
          ];
          updateApiState();
          return Promise.resolve(jsonResponse({ message: 'Cancelled' }));
        }

        if (pathname === '/api/v1/authors' && method === 'GET') {
          const authors = Array.from(
            new Set(
              libraryBooks.map((book) => book.author_name).filter(Boolean)
            )
          ).map((name, index) => ({ id: `author-${index}`, name }));
          return Promise.resolve(jsonResponse({ authors }));
        }
        if (pathname === '/api/v1/series' && method === 'GET') {
          const series = Array.from(
            new Set(
              libraryBooks.map((book) => book.series_name).filter(Boolean)
            )
          ).map((name, index) => ({ id: `series-${index}`, name }));
          return Promise.resolve(jsonResponse({ series }));
        }

        if (pathname === '/api/v1/audiobooks/count' && method === 'GET') {
          return Promise.resolve(jsonResponse({ count: libraryBooks.length }));
        }

        if (
          pathname === '/api/v1/audiobooks/soft-deleted' &&
          method === 'GET'
        ) {
          const deleted = libraryBooks.filter(
            (book) => book.marked_for_deletion
          );
          return Promise.resolve(
            jsonResponse({
              items: deleted,
              total: deleted.length,
              count: deleted.length,
            })
          );
        }

        if (pathname === '/api/v1/audiobooks/search' && method === 'GET') {
          const timeout = maybeTimeout(failures.searchBooks);
          if (timeout) {
            return timeout;
          }
          const failed = failWithStatus(
            typeof failures.searchBooks === 'number'
              ? failures.searchBooks
              : undefined,
            'Failed to search books'
          );
          if (failed) {
            return Promise.resolve(failed);
          }
          const query =
            urlObj.searchParams.get('q') ||
            urlObj.searchParams.get('search') ||
            '';
          const limit = parseInt(urlObj.searchParams.get('limit') || '50');
          const filtered = applySearchFilter([...libraryBooks], query);
          const paginated = filtered.slice(0, limit);
          apiState.searchCalls += 1;
          return Promise.resolve(
            jsonResponse({
              items: paginated,
              audiobooks: paginated,
              total: filtered.length,
            })
          );
        }

        if (pathname === '/api/v1/audiobooks' && method === 'GET') {
          const timeout = maybeTimeout(failures.getBooks);
          if (timeout) {
            return timeout;
          }
          const failed = failWithStatus(
            typeof failures.getBooks === 'number'
              ? failures.getBooks
              : undefined,
            'Failed to fetch books'
          );
          if (failed) {
            return Promise.resolve(failed);
          }
          const limit = parseInt(urlObj.searchParams.get('limit') || '24');
          const offset = parseInt(urlObj.searchParams.get('offset') || '0');
          const page = parseInt(urlObj.searchParams.get('page') || '1');

          const effectiveOffset =
            offset > 0 ? offset : Math.max(0, (page - 1) * limit);

          const paginatedBooks = libraryBooks.slice(
            effectiveOffset,
            effectiveOffset + limit
          );

          return Promise.resolve(
            jsonResponse({
              items: paginatedBooks,
              audiobooks: paginatedBooks,
              total: libraryBooks.length,
              page,
              limit,
            })
          );
        }

        if (pathname.startsWith('/api/v1/audiobooks/') && method === 'GET') {
          const parts = pathname.split('/').filter(Boolean);
          const bookId = parts[parts.length - 1];
          if (bookId && bookId !== 'audiobooks') {
            const match = libraryBooks.find((book) => book.id === bookId);
            if (!match) {
              return Promise.resolve(
                jsonResponse({ error: 'Not found' }, 404)
              );
            }
            return Promise.resolve(jsonResponse(match));
          }
        }

        if (
          pathname.startsWith('/api/v1/audiobooks/') &&
          pathname.endsWith('/tags') &&
          method === 'GET'
        ) {
          return Promise.resolve(
            jsonResponse({
              tags: [],
              media: [],
              file_count: 0,
              duration: 0,
            })
          );
        }

        if (
          pathname.startsWith('/api/v1/audiobooks/') &&
          pathname.endsWith('/versions') &&
          method === 'GET'
        ) {
          const parts = pathname.split('/').filter(Boolean);
          const bookId = parts[parts.length - 2];
          return Promise.resolve(
            jsonResponse({ versions: getVersionsForBook(bookId) })
          );
        }
        if (
          pathname.startsWith('/api/v1/audiobooks/') &&
          pathname.endsWith('/versions') &&
          method === 'POST'
        ) {
          const parts = pathname.split('/').filter(Boolean);
          const bookId = parts[parts.length - 2];
          const payload = parseJsonBody(init) || {};
          const otherId = payload.other_id;
          const groupId = ensureVersionGroup(bookId);
          libraryBooks = libraryBooks.map((book) =>
            book.id === otherId
              ? { ...book, version_group_id: groupId }
              : book
          );
          return Promise.resolve(jsonResponse({ message: 'Linked' }));
        }
        if (
          pathname.startsWith('/api/v1/audiobooks/') &&
          pathname.endsWith('/versions') &&
          method === 'DELETE'
        ) {
          const parts = pathname.split('/').filter(Boolean);
          const bookId = parts[parts.length - 2];
          const payload = parseJsonBody(init) || {};
          const otherId = payload.other_id;
          const groupId = getVersionGroupId(bookId);
          libraryBooks = libraryBooks.map((book) =>
            book.id === otherId
              ? { ...book, version_group_id: '', is_primary_version: false }
              : book
          );
          if (groupId) {
            const remaining = libraryBooks.filter(
              (book) => book.version_group_id === groupId
            );
            if (remaining.length <= 1) {
              libraryBooks = libraryBooks.map((book) =>
                book.version_group_id === groupId
                  ? {
                      ...book,
                      version_group_id: '',
                      is_primary_version: false,
                    }
                  : book
              );
            }
          }
          return Promise.resolve(jsonResponse({ message: 'Unlinked' }));
        }
        if (
          pathname.startsWith('/api/v1/audiobooks/') &&
          pathname.endsWith('/set-primary') &&
          method === 'PUT'
        ) {
          const parts = pathname.split('/').filter(Boolean);
          const bookId = parts[parts.length - 2];
          const groupId = ensureVersionGroup(bookId);
          libraryBooks = libraryBooks.map((book) =>
            book.version_group_id === groupId
              ? { ...book, is_primary_version: book.id === bookId }
              : book
          );
          return Promise.resolve(jsonResponse({ message: 'Primary set' }));
        }

        if (
          pathname.startsWith('/api/v1/audiobooks/') &&
          method === 'DELETE'
        ) {
          const parts = pathname.split('/').filter(Boolean);
          const bookId = parts[parts.length - 1];
          libraryBooks = libraryBooks.map((book) =>
            book.id === bookId
              ? { ...book, marked_for_deletion: true }
              : book
          );
          return Promise.resolve(jsonResponse({ message: 'Deleted' }));
        }

        if (
          pathname.startsWith('/api/v1/audiobooks/') &&
          method === 'PUT'
        ) {
          const parts = pathname.split('/').filter(Boolean);
          const bookId = parts[parts.length - 1];
          const payload = parseJsonBody(init) || {};
          const target = libraryBooks.find((book) => book.id === bookId);
          if (target?.force_update_required && !payload.force_update) {
            return Promise.resolve(
              jsonResponse({ error: 'Conflict' }, 409)
            );
          }
          libraryBooks = libraryBooks.map((book) =>
            book.id === bookId ? { ...book, ...payload } : book
          );
          const updated = libraryBooks.find((book) => book.id === bookId);
          return Promise.resolve(jsonResponse(updated || payload));
        }

        if (
          pathname.startsWith('/api/v1/audiobooks/') &&
          pathname.endsWith('/restore') &&
          method === 'POST'
        ) {
          const parts = pathname.split('/').filter(Boolean);
          const bookId = parts[parts.length - 2];
          libraryBooks = libraryBooks.map((book) =>
            book.id === bookId
              ? { ...book, marked_for_deletion: false }
              : book
          );
          return Promise.resolve(jsonResponse({ message: 'Restored' }));
        }

        if (
          pathname.startsWith('/api/v1/audiobooks/') &&
          pathname.endsWith('/fetch-metadata') &&
          method === 'POST'
        ) {
          const parts = pathname.split('/').filter(Boolean);
          const bookId = parts[parts.length - 2];
          const target = libraryBooks.find((book) => book.id === bookId);
          const shouldFail =
            target &&
            (target as { fetch_metadata_error?: boolean }).fetch_metadata_error;
          const delayMs =
            (target as { fetch_metadata_delay_ms?: number })
              ?.fetch_metadata_delay_ms || 0;

          if (shouldFail) {
            const failure = () =>
              jsonResponse({ error: 'Metadata fetch failed' }, 500);
            if (delayMs > 0) {
              return new Promise((resolve) =>
                window.setTimeout(() => resolve(failure()), delayMs)
              );
            }
            return Promise.resolve(failure());
          }
          libraryBooks = libraryBooks.map((book) =>
            book.id === bookId
              ? {
                  ...book,
                  language: book.language || 'en',
                  publisher: book.publisher || 'Test Publisher',
                  description: book.description || 'Updated metadata',
                }
              : book
          );
          const updated = libraryBooks.find((book) => book.id === bookId);
          const success = () =>
            jsonResponse({
              message: 'Metadata fetched',
              book: updated,
              source: 'mock',
            });
          if (delayMs > 0) {
            return new Promise((resolve) =>
              window.setTimeout(() => resolve(success()), delayMs)
            );
          }
          return Promise.resolve(success());
        }

        // Fallback
        return originalFetch(input, init);
      };
    },
    {
      bookData,
      configData,
      importPathsData,
      backupsData,
      blockedHashesData,
      filesystemData,
      homeDirectoryData,
      operationsData,
      itunesData,
      failuresData,
      systemStatusData,
    }
  );
}

/**
 * Setup library page with mock books
 */
export async function setupLibraryWithBooks(
  page: Page,
  books: ReturnType<typeof generateTestBooks>,
  options: Omit<MockApiOptions, 'books'> = {}
) {
  await setupMockApi(page, { ...options, books });
}
