// file: tests/e2e/book-detail.spec.ts
// version: 1.13.0
// guid: 2a3b4c5d-6e7f-8a9b-0c1d-2e3f4a5b6c7d
// last-edited: 2026-08-07

import { expect, test } from '@playwright/test';
import { mockEventSource } from './utils/test-helpers';

const bookId = 'book-1';

type BookState = {
  id: string;
  title: string;
  author_name: string;
  file_path: string;
  file_hash: string;
  original_file_hash: string;
  organized_file_hash: string;
  library_state: string;
  marked_for_deletion: boolean;
  marked_for_deletion_at?: string;
  created_at: string;
  updated_at: string;
};

const createInitialBook = (): BookState => ({
  id: bookId,
  title: 'The Test Book',
  author_name: 'Jane Tester',
  file_path: '/library/test-book.m4b',
  file_hash: 'hash-file',
  original_file_hash: 'hash-original',
  organized_file_hash: 'hash-organized',
  library_state: 'organized',
  marked_for_deletion: false,
  created_at: new Date('2024-01-01T12:00:00Z').toISOString(),
  updated_at: new Date('2024-01-02T12:00:00Z').toISOString(),
});

// Note: Using mockEventSource from test-helpers instead of this local definition
// to avoid duplication. This file also has custom route setup below.

const setupRoutes = async (page: import('@playwright/test').Page) => {
  // Mock EventSource FIRST to prevent SSE connection attempts
  await mockEventSource(page);

  // Skip welcome wizard
  await page.addInitScript(() => {
    localStorage.setItem('welcome_wizard_completed', 'true');
  });

  const initialBook = createInitialBook();
  const tags = {
    media_info: {
      codec: 'M4B',
      bitrate: 192,
      sample_rate: 44100,
      channels: 2,
      bit_depth: 16,
      quality: '192kbps AAC',
      duration: 3600,
    },
    tags: {
      title: {
        file_value: 'File Title',
        fetched_value: 'Fetched Title',
        stored_value: initialBook.title,
        override_value: null,
        override_locked: false,
        effective_value: initialBook.title,
        effective_source: 'stored',
      },
      author_name: {
        file_value: 'File Author',
        fetched_value: 'Fetched Author',
        stored_value: initialBook.author_name,
        override_value: null,
        override_locked: false,
        effective_value: initialBook.author_name,
        effective_source: 'stored',
      },
      narrator: {
        file_value: 'File Narrator',
        fetched_value: 'Fetched Narrator',
        stored_value: 'Stored Narrator',
        override_value: 'Override Narrator',
        override_locked: true,
        effective_value: 'Override Narrator',
        effective_source: 'override',
      },
      publisher: {
        file_value: 'File Publisher',
        fetched_value: 'Fetched Publisher',
        stored_value: 'Stored Publisher',
        override_value: null,
        override_locked: false,
        effective_value: 'Stored Publisher',
        effective_source: 'stored',
      },
      language: {
        file_value: 'en',
        fetched_value: 'en',
        stored_value: 'en',
        override_value: null,
        override_locked: false,
        effective_value: 'en',
        effective_source: 'stored',
      },
      audiobook_release_year: {
        file_value: 2020,
        fetched_value: 2021,
        stored_value: 2022,
        override_value: null,
        override_locked: false,
        effective_value: 2022,
        effective_source: 'stored',
      },
    },
  };
  await page.addInitScript(
    ({
      bookId: injectedBookId,
      bookData,
      tagsData,
    }: {
      bookId: string;
      bookData: BookState;
      tagsData: typeof tags;
    }) => {
      let book = { ...bookData };
      let purged = false;
      (window as unknown as { __lastDeleteUrl?: string }).__lastDeleteUrl = '';
      const tagState = { ...tagsData };

      const recomputeEffective = (field: string) => {
        const entry = tagState.tags[field as keyof typeof tagState.tags];
        if (!entry) return;
        const effective =
          entry.override_value ??
          entry.stored_value ??
          entry.fetched_value ??
          entry.file_value;
        let source = '';
        if (
          entry.override_value !== null &&
          entry.override_value !== undefined
        ) {
          source = 'override';
        } else if (
          entry.stored_value !== null &&
          entry.stored_value !== undefined
        ) {
          source = 'stored';
        } else if (
          entry.fetched_value !== null &&
          entry.fetched_value !== undefined
        ) {
          source = 'fetched';
        } else if (
          entry.file_value !== null &&
          entry.file_value !== undefined
        ) {
          source = 'file';
        }
        entry.effective_value = effective as never;
        entry.effective_source = source;
      };

      Object.keys(tagState.tags).forEach((field) => recomputeEffective(field));

      // The frontend api layer unwraps a { data: ... } envelope on most
      // endpoints; serve both the top-level fields and the envelope so raw
      // readers and `.data` readers both work.
      const jsonResponse = (body: unknown, status = 200) => {
        const payload =
          body && typeof body === 'object' && !Array.isArray(body) && !('data' in body)
            ? { ...(body as Record<string, unknown>), data: body }
            : body;
        return new Response(JSON.stringify(payload), {
          status,
          headers: { 'Content-Type': 'application/json' },
        });
      };

      const originalFetch = window.fetch.bind(window);
      window.fetch = (input: RequestInfo | URL, init?: RequestInit) => {
        const url = typeof input === 'string' ? input : input.url;
        const method = (init?.method || 'GET').toUpperCase();

        // Auth — the app checks these on boot; without a mock they hit the
        // real backend, which 401s and bounces the page to the Login screen.
        if (url.includes('/api/v1/auth/status')) {
          return Promise.resolve(
            jsonResponse({
              auth_enabled: true,
              has_users: false,
              requires_auth: false,
              bootstrap_ready: true,
            })
          );
        }
        if (url.includes('/api/v1/auth/me')) {
          return Promise.resolve(jsonResponse({ user: null }));
        }

        // Health/system
        if (url.includes('/api/v1/health')) {
          return Promise.resolve(jsonResponse({ status: 'ok' }));
        }
        if (url.includes('/api/v1/system/status')) {
          return Promise.resolve(
            jsonResponse({
              status: 'ok',
              library: { book_count: 1, folder_count: 1, total_size: 0 },
              import_paths: { book_count: 0, folder_count: 0, total_size: 0 },
              memory: {},
              runtime: {},
              operations: { recent: [] },
            })
          );
        }

        // Book files (Files & History tab) — must be handled before the
        // generic book route; getBookFiles unwraps { data: { files, count } }.
        if (url.includes(`/api/v1/audiobooks/${injectedBookId}/files`)) {
          return Promise.resolve(
            jsonResponse({
              files: [
                {
                  id: 'file-1',
                  book_id: injectedBookId,
                  file_path: book.file_path,
                  file_size: 1024 * 1024,
                  format: 'm4b',
                  file_hash: book.file_hash,
                },
              ],
              count: 1,
            })
          );
        }

        // Legacy segments endpoint — expects { data: [] } (an array).
        if (url.includes(`/api/v1/audiobooks/${injectedBookId}/segments`)) {
          return Promise.resolve(jsonResponse({ data: [] }));
        }

        // Book detail + list — match only the bare book URL (optionally with a
        // query string), so subresource routes (/files, /history, ...) never
        // fall through to this handler and crash array consumers.
        const bareBookUrl = new RegExp(
          `/api/v1/audiobooks/${injectedBookId}(\\?|$)`
        );
        if (bareBookUrl.test(url)) {
          if (method === 'GET') {
            if (purged) {
              return Promise.resolve(jsonResponse({}, 404));
            }
            return Promise.resolve(jsonResponse(book));
          }
          if (method === 'PUT') {
            const body = init?.body ? JSON.parse(init.body as string) : {};
            book = { ...book, ...body };
            if (body.overrides) {
              Object.entries(
                body.overrides as Record<
                  string,
                  { value?: unknown; clear?: boolean; locked?: boolean }
                >
              ).forEach(([key, override]) => {
                if (!tagState.tags[key]) return;
                if (override.clear) {
                  tagState.tags[key].override_value = null;
                  tagState.tags[key].override_locked = false;
                  recomputeEffective(key);
                  return;
                }
                if (override.locked === false) {
                  tagState.tags[key].override_locked = false;
                  recomputeEffective(key);
                  return;
                }
                if (override.value !== undefined) {
                  tagState.tags[key].override_value = override.value as never;
                  tagState.tags[key].override_locked = true;
                  if (key === 'title') {
                    book = { ...book, title: String(override.value) };
                  }
                  if (key === 'author_name') {
                    book = { ...book, author_name: String(override.value) };
                  }
                  if (key === 'series_name') {
                    book = { ...book, series_name: String(override.value) };
                  }
                  recomputeEffective(key);
                }
              });
            }
            Object.keys(body).forEach((key) => {
              if (key === 'overrides') return;
              if (tagState.tags[key]) {
                tagState.tags[key].stored_value = body[key];
                tagState.tags[key].override_value = body[key];
                tagState.tags[key].override_locked = true;
                recomputeEffective(key);
              }
            });
            return Promise.resolve(jsonResponse(book));
          }
          if (method === 'DELETE') {
            (
              window as unknown as { __lastDeleteUrl?: string }
            ).__lastDeleteUrl = url;
            const softDelete = url.includes('soft_delete=true');
            if (softDelete) {
              book = {
                ...book,
                library_state: 'deleted',
                marked_for_deletion: true,
                marked_for_deletion_at: new Date().toISOString(),
              };
            } else {
              purged = true;
            }
            return Promise.resolve(jsonResponse({}));
          }
        }

        if (url.endsWith('/api/v1/audiobooks')) {
          return Promise.resolve(
            jsonResponse({ items: [book], audiobooks: [book] })
          );
        }

        if (url.includes(`/api/v1/audiobooks/${injectedBookId}/tags`)) {
          return Promise.resolve(jsonResponse(tagState));
        }

        // Versions
        if (url.includes(`/api/v1/audiobooks/${injectedBookId}/versions`)) {
          return Promise.resolve(
            jsonResponse({
              versions: [
                { ...book, is_primary_version: true },
                {
                  ...book,
                  id: 'book-2',
                  title: 'Second Version',
                  is_primary_version: false,
                },
              ],
            })
          );
        }

        // Restore
        if (url.includes(`/api/v1/audiobooks/${injectedBookId}/restore`)) {
          book = {
            ...book,
            library_state: 'organized',
            marked_for_deletion: false,
            marked_for_deletion_at: undefined,
          };
          return Promise.resolve(jsonResponse({}));
        }

        // Metadata refresh
        if (
          url.includes(`/api/v1/audiobooks/${injectedBookId}/fetch-metadata`)
        ) {
          book = { ...book, title: 'Refreshed Title' };
          return Promise.resolve(
            jsonResponse({ message: 'refreshed', book, source: 'test' })
          );
        }

        // AI parse
        if (
          url.includes(`/api/v1/audiobooks/${injectedBookId}/parse-with-ai`)
        ) {
          book = { ...book, description: 'AI parsed desc' };
          return Promise.resolve(
            jsonResponse({ message: 'parsed', book, confidence: 'high' })
          );
        }

        // Fallback
        return originalFetch(input, init);
      };
    },
    { bookId, bookData: initialBook, tagsData: tags }
  );

  return {
    // Simple accessor for assertions if needed later
    getBook: () => initialBook,
  };
};

test.describe('Book Detail page', () => {
  test.beforeEach(async () => {
    // Each test calls setupRoutes which handles all setup including EventSource mocking
    // No setup needed here to avoid conflicts
  });

  test('renders info, files, and versions tabs', async ({ page }) => {
    await setupRoutes(page);
    await page.goto(`/library/${bookId}`);

    await expect(
      page.getByRole('heading', { name: 'The Test Book' })
    ).toBeVisible();
    await page.getByRole('tab', { name: 'Files' }).click();
    await expect(page.getByText('File Path')).toBeVisible();

    await page.getByRole('tab', { name: /Versions/ }).click();
    await expect(page.getByText(/Versions/).first()).toBeVisible();
    await expect(
      page.getByText(/Second Version|No additional versions linked yet/i)
    ).toBeVisible();
  });

  test('soft delete, restore, and purge flow', async ({ page }) => {
    await setupRoutes(page);
    await page.goto(`/library/${bookId}`);

    await page.getByRole('button', { name: 'Delete' }).click();
    await page.getByRole('button', { name: 'Soft Delete' }).click();
    await page.waitForFunction(
      () =>
        (
          window as unknown as { __lastDeleteUrl?: string }
        ).__lastDeleteUrl?.includes('block_hash=true'),
      null,
      { timeout: 5000 }
    );

    await expect(
      page.getByText('Audiobook marked for deletion.')
    ).toBeVisible();
    await expect(page.getByText('Soft Deleted')).toBeVisible();

    await page
      .getByRole('button', { name: /^Restore$/ })
      .last()
      .click();
    await expect(page.getByText('Audiobook restored.')).toBeVisible();
    await expect(page.getByText('Soft Deleted')).not.toBeVisible();

    // Soft delete again to open purge dialog
    await page.getByRole('button', { name: 'Delete' }).click();
    await page.getByRole('button', { name: 'Soft Delete' }).click();
    await page.getByRole('button', { name: 'Purge' }).click();
    await expect(
      page.getByRole('dialog', { name: 'Purge Audiobook' })
    ).toBeVisible();
    // Must check the confirmation checkbox before purging
    await page.getByRole('checkbox').check();
    await page.getByRole('button', { name: 'Purge Permanently' }).click();
    await expect(page).toHaveURL(/\/library$/);
  });

  test('metadata refresh and AI parse actions', async ({ page }) => {
    await setupRoutes(page);
    await page.goto(`/library/${bookId}`);

    await page.getByRole('button', { name: 'Fetch Metadata' }).click();
    await expect(
      page.getByRole('heading', { name: 'Refreshed Title' })
    ).toBeVisible();

    await page.getByRole('button', { name: 'Parse with AI' }).click();
    await expect(page.getByText('AI parsed desc')).toBeVisible();
  });

});
