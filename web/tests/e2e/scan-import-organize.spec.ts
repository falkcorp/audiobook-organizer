// file: web/tests/e2e/scan-import-organize.spec.ts
// version: 1.6.0
// guid: 6a7b8c9d-0e1f-2a3b-4c5d-6e7f8a9b0c1d
// last-edited: 2026-08-09

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

    const jsonResponse = (body: unknown, status = 200) =>
      new Response(JSON.stringify(body), {
        status,
        headers: { 'Content-Type': 'application/json' },
      });

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
      if (pathname === '/api/v1/operations/scan' && method === 'POST') {
        if (scanError) {
          return Promise.resolve(jsonResponse({ error: 'Scan failed' }, 500));
        }
        libraryBooks = libraryBooks.map((book) => ({
          ...book,
          library_state: 'import',
        }));
        saveState();
        return Promise.resolve(
          jsonResponse({
            id: 'scan-op-1',
            type: 'scan',
            status: 'running',
            progress: 0,
            total: libraryBooks.length,
            message: 'Scanning',
            created_at: new Date().toISOString(),
            errors: scanErrorList,
          })
        );
      }
      if (pathname === '/api/v1/operations/organize' && method === 'POST') {
        const ids = Array.isArray(payload.book_ids) ? payload.book_ids : [];
        libraryBooks = libraryBooks.map((book) =>
          ids.includes(book.id)
            ? { ...book, library_state: 'organized' }
            : book
        );
        saveState();
        return Promise.resolve(
          jsonResponse({
            id: 'organize-op-1',
            type: 'organize',
            status: 'running',
            progress: 0,
            total: ids.length,
            message: 'Organizing',
            created_at: new Date().toISOString(),
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
        return Promise.resolve(
          jsonResponse({ items: libraryBooks, audiobooks: libraryBooks })
        );
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
    await page.getByRole('option', { name: 'Import' }).click();
    // Close filter drawer before interacting with main content
    await page.keyboard.press('Escape');
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
    await page.getByRole('option', { name: 'Import' }).click();
    await page.keyboard.press('Escape'); // Close dropdown
    await page.keyboard.press('Escape'); // Close filter drawer

    // Assert
    await expect(page.getByText('No audiobooks found')).toBeVisible();
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

    // Assert
    await expect(page.getByText(/Scan complete/)).toBeVisible();
    await page.getByRole('button', { name: 'View Errors' }).click();
    await expect(page.getByText('Corrupt file: book2.m4b')).toBeVisible();
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
      library_state: 'import',
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

  test('organize operation: handles duplicate files', async ({ page }) => {
    // Arrange
    const baseBook = generateTestBooks(1)[0];
    const organizedBook = {
      ...baseBook,
      id: 'organized-1',
      title: 'Duplicate Book',
      library_state: 'organized',
      file_hash: 'dup-hash',
    };
    const importBook = {
      ...baseBook,
      id: 'import-dup',
      title: 'Duplicate Book (Import)',
      library_state: 'import',
      file_hash: 'dup-hash',
      file_path: '/imports/duplicate.m4b',
    };
    await setupLibraryWithBooks(page, [organizedBook, importBook], {
      config: { root_dir: '/library' },
    });

    await page.goto('/library');
    await page.waitForLoadState('networkidle');

    // Act
    await page.getByLabel('Select Duplicate Book (Import)').click();
    await page.getByRole('button', { name: 'Organize Selected' }).click();
    await page
      .getByRole('button', { name: 'Organize Selected' })
      .last()
      .click();
    const duplicateDialog = page.getByRole('dialog', {
      name: 'Duplicate File Detected',
    });
    await expect(duplicateDialog).toBeVisible();
    await duplicateDialog
      .getByRole('button', { name: 'Link as Version' })
      .click();

    // Assert
    await waitForToast(page, 'Successfully organized 1 audiobooks.');
    await page
      .getByRole('dialog', { name: 'Organize Selected Audiobooks' })
      .getByRole('button', { name: 'Close' })
      .click();
    await page
      .getByRole('heading', {
        name: 'Duplicate Book (Import)',
        exact: true,
      })
      .click();
    await page.getByRole('tab', { name: /Versions/ }).click();
    await expect(
      page.getByText('Part of version group with 2 books.')
    ).toBeVisible();
  });

  test('organize operation: rollback on error', async ({ page }) => {
    // Arrange
    const books = generateTestBooks(3).map((book, index) => ({
      ...book,
      id: `import-${index + 1}`,
      title: `Import Book ${index + 1}`,
      library_state: 'import',
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
    await page.getByRole('button', { name: /filters/i }).click();
    await page.getByLabel('Library State').click();
    await page.getByRole('option', { name: 'Import' }).click();
    // Verify books are back in import state (scroll may be needed for card view)
    await expect(
      page.getByLabel('Select Import Book 1')
    ).toBeVisible();
  });
});
