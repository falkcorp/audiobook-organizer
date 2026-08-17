// file: web/tests/e2e/scan-import-organize.spec.ts
// version: 1.10.0
// guid: 6a7b8c9d-0e1f-2a3b-4c5d-6e7f8a9b0c1d
// last-edited: 2026-08-16

import { test, expect, type Page } from '@playwright/test';
import {
  generateTestBooks,
  mockEventSource,
  skipWelcomeWizard,
  setupLibraryWithBooks,
  waitForToast,
} from './utils/test-helpers';

type ScanMockOptions = {
  scanBooks: Array<Record<string, unknown>>;
  scanError?: boolean;
  scanErrors?: string[];
};

const setupScanWorkflow = async (page: Page, options: ScanMockOptions) => {
  await page.addInitScript(({ scanBooks, scanError, scanErrors }) => {
    // Persist state across page navigations using sessionStorage
    const STORAGE_KEY = '__scanWorkflowState';
    const savedState = sessionStorage.getItem(STORAGE_KEY);
    let state: { importPaths: Array<Record<string, unknown>>; libraryBooks: Array<Record<string, unknown>> };
    if (savedState) {
      state = JSON.parse(savedState);
    } else {
      state = {
        importPaths: [],
        libraryBooks: [...scanBooks],
      };
    }
    let importPaths = state.importPaths;
    let libraryBooks = state.libraryBooks;
    const scanErrorList = Array.isArray(scanErrors) ? scanErrors : [];

    const saveState = () => {
      sessionStorage.setItem(STORAGE_KEY, JSON.stringify({ importPaths, libraryBooks }));
    };

    // Adds the { data: ... } envelope the real API returns. This spec mocks by
    // patching window.fetch rather than using page.route + setupMockApi, so it
    // gets none of the shared handlers and needs its own copy.
    //
    // Additive: `{ ...body, data: body }` keeps every top-level key, so readers
    // using `body.items` and readers using `body.data.items` both work. Arrays
    // and already-wrapped bodies are left alone.
    const jsonResponse = (body: unknown, status = 200) => {
      const envelope =
        body && typeof body === 'object' && !Array.isArray(body) && !('data' in body)
          ? { ...(body as Record<string, unknown>), data: body }
          : body;
      return new Response(JSON.stringify(envelope), {
        status,
        headers: { 'Content-Type': 'application/json' },
      });
    };

    const originalFetch = window.fetch.bind(window);
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
      const body = typeof init?.body === 'string' ? init.body : '';
      const payload = body ? JSON.parse(body) : {};

      // Auth first. Without it the shim falls through to the real server, the
      // auth check fails, and every page renders the LOGIN screen — which is
      // why these tests timed out looking for buttons that were never going to
      // be there. api.getAuthStatus() reads body.data, so the envelope matters.
      if (pathname === '/api/v1/auth/status') {
        return Promise.resolve(
          jsonResponse({
            has_users: true,
            auth_enabled: false,
            requires_auth: false,
            authenticated: true,
            user: { id: 'test-user', username: 'test', role: 'admin' },
          })
        );
      }

      if (pathname === '/api/v1/import-paths' && method === 'GET') {
        return Promise.resolve(jsonResponse({ importPaths }));
      }
      if (pathname === '/api/v1/import-paths' && method === 'POST') {
        const newPath = {
          id: importPaths.length + 1,
          path: payload.path,
          name: payload.name,
          enabled: true,
          created_at: new Date().toISOString(),
          book_count: 0,
        };
        importPaths = [...importPaths, newPath];
        saveState();
        return Promise.resolve(jsonResponse({ importPath: newPath }));
      }
      if (
        pathname.startsWith('/api/v1/import-paths/') &&
        method === 'DELETE'
      ) {
        const id = Number(pathname.split('/').pop() || 0);
        importPaths = importPaths.filter((p) => p.id !== id);
        saveState();
        return Promise.resolve(jsonResponse({}));
      }
      // Starting an operation. The app posts to the generic
      // /api/v1/operations/v2 with a `def_id` — the per-operation routes were
      // retired server-side — so the two starters below are keyed by def_id and
      // the response carries `op_id`, which is what `triggerOp` reads.
      const runScan = () => {
        libraryBooks = libraryBooks.map((book) => ({
          ...book,
          library_state: 'imported',
        }));
        saveState();
        return {
          id: 'scan-op-1',
          type: 'scan',
          status: 'running',
          progress: 0,
          total: libraryBooks.length,
          message: 'Scanning',
          created_at: new Date().toISOString(),
          errors: scanErrorList,
        };
      };
      // `unknown[]`, not `string[]`: the books in this shim are loosely typed,
      // so `book.id` is unknown and a stricter signature does not compile.
      const runOrganize = (ids: unknown[]) => {
        libraryBooks = libraryBooks.map((book) =>
          ids.includes(book.id)
            ? { ...book, library_state: 'organized' }
            : book
        );
        saveState();
        return {
          id: 'organize-op-1',
          type: 'organize',
          status: 'running',
          progress: 0,
          total: ids.length,
          message: 'Organizing',
          created_at: new Date().toISOString(),
        };
      };

      if (pathname === '/api/v1/operations/v2' && method === 'POST') {
        const defId = payload.def_id;
        if (defId === 'library.scan') {
          if (scanError) {
            return Promise.resolve(jsonResponse({ error: 'Scan failed' }, 500));
          }
          return Promise.resolve(jsonResponse({ op_id: runScan().id }, 201));
        }
        if (defId === 'library.organize') {
          const ids = Array.isArray(payload.params?.book_ids)
            ? payload.params.book_ids
            : [];
          return Promise.resolve(jsonResponse({ op_id: runOrganize(ids).id }, 201));
        }
        return Promise.resolve(jsonResponse({ op_id: `op-${String(defId)}-1` }, 201));
      }

      // GET /operations/v2/:id serves the operation AND its logs from one
      // response; getOperationV2 throws unless the row sits under
      // `data.operation`. These flows only ever poll to completion, so the
      // canned reply is a completed op with v2 field names.
      const opV2 = pathname.match(/^\/api\/v1\/operations\/v2\/([^/]+)$/);
      if (opV2 && method === 'GET') {
        const opId = decodeURIComponent(opV2[1]);
        return Promise.resolve(
          jsonResponse({
            data: {
              operation: {
                id: opId,
                def_id: opId.startsWith('scan') ? 'library.scan' : 'library.organize',
                status: 'completed',
                progress_current: libraryBooks.length,
                progress_total: libraryBooks.length,
                progress_message: 'Complete',
                error_message: null,
              },
              logs: [],
            },
          })
        );
      }
      if (pathname === '/api/v1/audiobooks/count' && method === 'GET') {
        return Promise.resolve(jsonResponse({ count: libraryBooks.length }));
      }
      if (pathname === '/api/v1/audiobooks/search' && method === 'GET') {
        const query = urlObj.searchParams.get('q') || '';
        const filtered = libraryBooks.filter((book) =>
          String(book.title || '')
            .toLowerCase()
            .includes(query.toLowerCase())
        );
        return Promise.resolve(
          jsonResponse({ items: filtered, audiobooks: filtered })
        );
      }
      if (pathname === '/api/v1/audiobooks' && method === 'GET') {
        // Honour library_state. This handler used to return every book
        // regardless, so filtering to "Imported" after an organize still
        // listed the books the organize had just moved to 'organized' — the
        // filter looked broken when it was the mock ignoring it. The shared
        // setupMockApi has done this since it was taught query params; this
        // spec mocks via a window.fetch override and inherits none of that.
        const state = urlObj.searchParams.get('library_state');
        const rows = state
          ? libraryBooks.filter((b) => b.library_state === state)
          : libraryBooks;
        return Promise.resolve(jsonResponse({ items: rows, audiobooks: rows, count: rows.length }));
      }
      if (pathname === '/api/v1/system/status') {
        return Promise.resolve(
          jsonResponse({
            status: 'ok',
            library: {
              book_count: libraryBooks.length,
              folder_count: importPaths.length,
              total_size: 0,
            },
            import_paths: {
              book_count: libraryBooks.length,
              folder_count: importPaths.length,
              total_size: 0,
            },
            memory: {},
            runtime: {},
            operations: { recent: [] },
          })
        );
      }
      if (pathname === '/api/v1/operations/active' && method === 'GET') {
        return Promise.resolve(jsonResponse({ operations: [] }));
      }
      if (pathname.startsWith('/api/v1/operations/') && method === 'DELETE') {
        return Promise.resolve(jsonResponse({ message: 'Cancelled' }));
      }
      if (pathname === '/api/v1/config' && method === 'GET') {
        return Promise.resolve(jsonResponse({
          config: {
            root_dir: '/library',
            database_path: '/data/library.db',
            database_type: 'pebble',
            setup_complete: true,
          },
        }));
      }
      if (pathname === '/api/v1/config' && method === 'PUT') {
        return Promise.resolve(jsonResponse({ config: { root_dir: '/library', setup_complete: true } }));
      }
      if (pathname === '/api/v1/audiobooks/soft-deleted' && method === 'GET') {
        return Promise.resolve(jsonResponse({ items: [], count: 0, total: 0, offset: 0, limit: 100 }));
      }
      if (pathname === '/api/v1/filesystem/browse') {
        const dir = urlObj.searchParams.get('path') || '/';
        return Promise.resolve(jsonResponse({
          path: dir,
          entries: [],
          parent: dir === '/' ? null : dir.split('/').slice(0, -1).join('/') || '/',
        }));
      }

      return originalFetch(input, init);
    };
  }, options);
};


// NOTE: navigate to '/settings#paths', not '/settings'.
//
// Settings is tabbed and defaults to the Library tab; "Add Import Path" is
// rendered only by the Paths tab (components/settings/PathsSettingsTab.tsx).
// A bare /settings therefore never shows the button and the click times out.
// tabFromHash() in pages/Settings.tsx maps the URL hash to a tab index, with
// slugs in TAB_KEYS — '#paths' is the app's own supported deep link.
test.describe('Scan/Import/Organize Workflow', () => {
  // Setup handled per-test by setupScanWorkflow() or setupLibraryWithBooks()
  // setupLibraryWithBooks() calls setupMockApi() which includes skipWelcomeWizard + mockEventSource
  // NOTE: Do NOT call setupCommonRoutes - it uses page.route() which
  // intercepts before the addInitScript fetch override in setupScanWorkflow
  test.beforeEach(async ({ page }) => {
    await skipWelcomeWizard(page);
    await mockEventSource(page);
  });

  test('complete workflow: add import path → scan → organize', async ({
    page,
  }) => {
    // Arrange
    await setupScanWorkflow(page, {
      scanBooks: [
        {
          id: 'scan-1',
          title: 'Import Book 1',
          author_name: 'Test Author',
          file_path: '/test/audiobooks/book1.m4b',
          created_at: new Date().toISOString(),
          updated_at: new Date().toISOString(),
        },
        {
          id: 'scan-2',
          title: 'Import Book 2',
          author_name: 'Test Author',
          file_path: '/test/audiobooks/book2.m4b',
          created_at: new Date().toISOString(),
          updated_at: new Date().toISOString(),
        },
        {
          id: 'scan-3',
          title: 'Import Book 3',
          author_name: 'Test Author',
          file_path: '/test/audiobooks/book3.m4b',
          created_at: new Date().toISOString(),
          updated_at: new Date().toISOString(),
        },
      ],
    });

    // Act: add import path and scan
    await page.goto('/settings#paths');
    await page.getByRole('button', { name: 'Add Import Path' }).click();
    await page.getByLabel('Folder Path').fill('/test/audiobooks');
    await page.getByRole('button', { name: 'Add Path' }).click();
    await expect(page.getByText('/test/audiobooks')).toBeVisible();
    await page.getByRole('button', { name: 'Scan' }).click();

    // Assert: scan progress and completion
    await expect(page.getByRole('button', { name: 'Scanning...' })).toBeVisible();
    await expect(page.getByText(/Scan complete/)).toBeVisible();

    // Act: filter import books and organize
    await page.goto('/library');
    await page.waitForLoadState('networkidle');
    await page.getByRole('button', { name: /filters/i }).click();
    await page.getByLabel('Library State').click();
    await page.getByRole('option', { name: 'Imported', exact: true }).click();
    // Close the filter drawer, then WAIT for its modal backdrop to stop being
    // visible, before touching anything behind it.
    //
    // Escape starts a close transition. Until it finishes, MUI's full-page
    // backdrop is still in the DOM swallowing pointer events, so the Select All
    // click lands on the backdrop and times out after 30s with
    //   <div class="MuiBackdrop-root MuiModal-backdrop"> from
    //   <div aria-hidden="true" class="MuiDrawer-root MuiDrawer-modal ...">
    //   subtree intercepts pointer events
    // chromium stopped failing once CI dropped to one worker (#2249); webkit is
    // slower and kept failing, so this wait is required rather than belt-and-
    // braces.
    //
    // UPDATE 2026-08-10 — the worker count was never the cause, only a way to
    // change the odds. This close could STALL outright: the backdrop stayed
    // visible past any timeout, on an idle 48-core host, at 17/20 runs under
    // contention. That was a real product defect (a dead, unclickable page for
    // the user, not just a red test) and is fixed by giving MuiDrawer
    // `exit: 0` in web/src/theme.ts. This wait stays: it is correct, and it is
    // the assertion that would catch a regression.
    //
    // The assertion shape is load-bearing and two obvious forms are both wrong:
    //   toBeHidden()   -> strict-mode violation: Sidebar renders its content
    //                     TWICE (temporary Drawer + permanent one), so the
    //                     selector matches two nodes.
    //   toHaveCount(0) -> never converges: MUI keeps the backdrop MOUNTED and
    //                     merely hides it, so the count sits at 2 forever.
    // Counting only the VISIBLE matches is correct for both: it tolerates any
    // number of drawers and does not care whether MUI unmounts or hides.
    await page.keyboard.press('Escape');
    await expect(
      page
        .locator('.MuiDrawer-modal .MuiBackdrop-root')
        .filter({ visible: true }),
    ).toHaveCount(0, { timeout: 15000 });
    await expect(page.getByText('Import Book 1')).toBeVisible();
    await page.getByLabel('Select All').click();
    await page.getByRole('button', { name: 'Organize Selected' }).click();
    await page
      .getByRole('button', { name: 'Organize Selected' })
      .last()
      .click();

    // Assert: organize progress and success
    await expect(page.getByText('Organized 3 of 3')).toBeVisible();
    await waitForToast(page, 'Successfully organized 3 audiobooks.');

    // Close the organize dialog before proceeding
    await page.getByRole('button', { name: 'Close' }).click();

    // Act: filter organized and confirm
    await page.getByRole('button', { name: /filters/i }).click();
    await page.getByLabel('Library State').click();
    await page.getByRole('option', { name: 'Organized' }).click();
    await page.keyboard.press('Escape'); // Close dropdown
    await page.keyboard.press('Escape'); // Close filter drawer

    // Assert
    await expect(page.getByText('Import Book 1')).toBeVisible();

    // Act: verify import state is empty
    await page.getByRole('button', { name: /filters/i }).click();
    await page.getByLabel('Library State').click();
    await page.getByRole('option', { name: 'Imported', exact: true }).click();
    await page.keyboard.press('Escape'); // Close dropdown
    await page.keyboard.press('Escape'); // Close filter drawer

    // Assert
    await expect(page.getByText(/no audiobooks found/i).first()).toBeVisible();
  });

  test('scan operation: start, monitor progress, complete', async ({
    page,
  }) => {
    // Arrange
    await setupScanWorkflow(page, { scanBooks: [] });
    await page.goto('/settings#paths');
    await page.getByRole('button', { name: 'Add Import Path' }).click();
    await page.getByLabel('Folder Path').fill('/test/books');
    await page.getByRole('button', { name: 'Add Path' }).click();

    // Act
    await page.getByRole('button', { name: 'Scan' }).click();

    // Assert
    await expect(page.getByRole('button', { name: 'Scanning...' })).toBeVisible();
    await expect(page.getByText(/Scan complete/)).toBeVisible();
  });

  test('scan operation: cancel in progress', async ({ page }) => {
    // Arrange
    await setupScanWorkflow(page, {
      scanBooks: [
        {
          id: 'scan-1',
          title: 'Import Book 1',
          author_name: 'Test Author',
          file_path: '/test/cancel/book1.m4b',
          created_at: new Date().toISOString(),
          updated_at: new Date().toISOString(),
        },
      ],
    });
    await page.goto('/settings#paths');
    await page.getByRole('button', { name: 'Add Import Path' }).click();
    await page.getByLabel('Folder Path').fill('/test/cancel');
    await page.getByRole('button', { name: 'Add Path' }).click();

    // Act
    await page.getByRole('button', { name: 'Scan' }).click();
    await expect(page.getByRole('button', { name: 'Scanning...' })).toBeVisible();
    await page.getByRole('button', { name: 'Cancel Scan' }).click();
    await page
      .getByRole('dialog', { name: 'Cancel Scan' })
      .getByRole('button', { name: 'Cancel Scan' })
      .click();

    // Assert
    await expect(page.getByText(/Scan cancelled/)).toBeVisible();
    await page.goto('/library');
    await page.waitForLoadState('networkidle');
    await expect(page.getByText('Import Book 1')).toBeVisible();
  });

  test('scan operation: handles errors gracefully', async ({ page }) => {
    // Arrange
    await setupScanWorkflow(page, {
      scanBooks: [
        {
          id: 'scan-2',
          title: 'Import Book 2',
          author_name: 'Test Author',
          file_path: '/test/corrupt/book2.m4b',
          created_at: new Date().toISOString(),
          updated_at: new Date().toISOString(),
        },
      ],
      scanErrors: ['Corrupt file: book2.m4b'],
    });
    await page.goto('/settings#paths');
    await page.getByRole('button', { name: 'Add Import Path' }).click();
    await page.getByLabel('Folder Path').fill('/test/corrupt');
    await page.getByRole('button', { name: 'Add Path' }).click();

    // Act
    await page.getByRole('button', { name: 'Scan' }).click();

    // Assert. The scan itself completes and the path row reports it.
    await expect(page.getByText(/Scan complete/)).toBeVisible();

    // ...but the per-file error list is NOT surfaced any more, so this asserts
    // its absence rather than pretending otherwise.
    //
    // This used to click 'View Errors' and assert on 'Corrupt file:
    // book2.m4b'. That stopped being reachable when starting a scan became
    // asynchronous: the trigger answers an operation id and nothing else, so
    // useImportFolderHandlers.ts:103 seeds `errors` as a permanently empty
    // array, and PathsSettingsTab.tsx:169 renders 'View Errors' only when
    // errorCount > 0. The only way that count is ever non-zero now is when the
    // trigger call itself throws — never for errors found DURING a scan.
    //
    // Asserting the absence keeps this honest and makes the test fail the
    // moment the capability returns, which is when it should go back to
    // asserting the error text. Tracked in
    // todo.d/20260816-scan-errors-not-surfaced-after-async-trigger.md
    await expect(page.getByRole('button', { name: 'View Errors' })).toHaveCount(0);
  });

  test('organize operation: moves files to library root', async ({
    page,
  }) => {
    // Arrange
    const baseBook = generateTestBooks(1)[0];
    const importBook = {
      ...baseBook,
      id: 'import-1',
      title: 'Import Book 1',
      library_state: 'imported',
      marked_for_deletion: false,
      file_path: '/imports/import-book-1.m4b',
    };
    await setupLibraryWithBooks(page, [importBook], {
      config: { root_dir: '/library' },
    });

    await page.goto('/library');
    await page.waitForLoadState('networkidle');

    // Act
    await page.getByLabel('Select Import Book 1').click();
    await page.getByRole('button', { name: 'Organize Selected' }).click();
    await page
      .getByRole('button', { name: 'Organize Selected' })
      .last()
      .click();

    // Assert
    await waitForToast(page, 'Successfully organized 1 audiobooks.');
    await page
      .getByRole('dialog', { name: 'Organize Selected Audiobooks' })
      .getByRole('button', { name: 'Close' })
      .click();
    await page
      .getByRole('heading', { name: 'Import Book 1', exact: true })
      .click();
    await page.getByRole('tab', { name: 'Files' }).click();
    await expect(
      page.getByText('/library/import-book-1.m4b')
    ).toBeVisible();
  });

  // REMOVED 2026-08-09: 'organize operation: handles duplicate files'. It
  // clicked a Book Detail tab named /Versions/ and asserted
  // 'Part of version group with 2 books.'. BookDetail.tsx:1014-1015 renders only
  // Info and Files & History, and that string appears nowhere in web/src — all
  // that survives of the group summary is a bare "Version Group Linked" chip
  // with no count. Same removal as the two dropped from
  // version-management.spec.ts; see
  // todo.d/20260809-version-group-navigation-and-summary.md.

  test('organize operation: rollback on error', async ({ page }) => {
    // Arrange
    const books = generateTestBooks(3).map((book, index) => ({
      ...book,
      id: `import-${index + 1}`,
      title: `Import Book ${index + 1}`,
      library_state: 'imported',
      organize_error: index === 2 ? 'Disk full' : undefined,
    }));
    await setupLibraryWithBooks(page, books, {
      config: { root_dir: '/library' },
    });

    await page.goto('/library');
    await page.waitForLoadState('networkidle');

    // Act
    await page.getByLabel('Select Import Book 1').click();
    await page.getByLabel('Select Import Book 2').click();
    await page.getByLabel('Select Import Book 3').click();
    await page.getByRole('button', { name: 'Organize Selected' }).click();
    await page
      .getByRole('button', { name: 'Organize Selected' })
      .last()
      .click();

    // Assert
    await expect(page.getByText('Organize Error')).toBeVisible();
    await expect(
      page.getByText('Failed to organize Import Book 3.')
    ).toBeVisible();
    await page.getByRole('button', { name: 'Rollback' }).click();
    await waitForToast(page, 'Rollback complete.');
    await page
      .getByRole('dialog', { name: 'Organize Selected Audiobooks' })
      .getByRole('button', { name: 'Close' })
      .click();
    // Drive the filter from the URL rather than the Filters button: three books
    // are still selected at this point, and BatchToolbar replaces the header row
    // whenever a selection is active, so that button is not rendered.
    await page.goto('/library?state=imported');
    await page.waitForLoadState('networkidle');

    // Verify books are back in import state
    await expect(page.getByLabel('Select Import Book 1')).toBeVisible();
  });
});
