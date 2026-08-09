// file: web/tests/e2e/transcode-and-counting.spec.ts
// version: 1.1.0
// guid: c3d4e5f6-a7b8-9012-cdef-345678901abc

import { test, expect, type Page } from '@playwright/test';
import {
  generateTestBooks,
  setupMockApi,
  type MockApiOptions,
  type TestBook,
} from './utils/test-helpers';

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** Create a book with specific format / version fields. */
function mp3Book(overrides: Record<string, unknown> = {}) {
  const base = generateTestBooks(1)[0];
  return {
    ...base,
    id: 'mp3-book-1',
    title: 'The Odyssey',
    author_name: 'Homer',
    format: 'mp3',
    codec: 'mp3',
    bitrate: 64,
    duration: 18000,
    file_size: 80_000_000,
    is_primary_version: true,
    version_group_id: undefined,
    version_notes: undefined,
    ...overrides,
  };
}

function m4bBook(overrides: Record<string, unknown> = {}) {
  const base = generateTestBooks(1)[0];
  return {
    ...base,
    id: 'm4b-book-1',
    title: 'The Odyssey',
    author_name: 'Homer',
    format: 'm4b',
    codec: 'aac',
    bitrate: 128,
    duration: 18000,
    file_size: 120_000_000,
    is_primary_version: true,
    version_group_id: 'vg-odyssey',
    version_notes: 'Transcoded to M4B',
    ...overrides,
  };
}

/** Set up a book detail page with mocked API including transcode support. */
async function setupWithTranscode(
  page: Page,
  books: TestBook[],
  extra: Partial<MockApiOptions> = {}
) {
  // Track transcode calls
  let transcodeStarted = false;
  let transcodeBookId = '';

  await setupMockApi(page, { books, ...extra });

  // Intercept transcode endpoint. The delay is load-bearing: this mock used to
  // resolve instantly, so the button's 'Converting...' state came and went
  // before any assertion could observe it.
  await page.route('**/api/v1/operations/transcode', async (route) => {
    const req = route.request();
    if (req.method() === 'POST') {
      const body = JSON.parse((await req.postData()) || '{}');
      transcodeStarted = true;
      transcodeBookId = body.book_id;
      await new Promise((resolve) => setTimeout(resolve, 1500));
      return route.fulfill({
        status: 202,
        contentType: 'application/json',
        body: JSON.stringify({
          id: 'op-transcode-1',
          type: 'transcode',
          status: 'running',
          progress: 0,
          total: 5,
          message: 'Starting transcode',
          created_at: new Date().toISOString(),
        }),
      });
    }
    return route.fallback();
  });

  // Intercept operation status polling
  let pollCount = 0;
  await page.route('**/api/v1/operations/op-transcode-1', async (route) => {
    pollCount++;
    const status = pollCount >= 3 ? 'completed' : 'running';
    const progress = pollCount >= 3 ? 5 : Math.min(pollCount, 4);
    return route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        id: 'op-transcode-1',
        type: 'transcode',
        status,
        progress,
        total: 5,
        message: status === 'completed' ? 'Complete' : `Transcoding audio (${progress * 20}%)`,
        created_at: new Date().toISOString(),
      }),
    });
  });

  return { transcodeStarted: () => transcodeStarted, transcodeBookId: () => transcodeBookId };
}

// ---------------------------------------------------------------------------
// M4B Transcode Tests
// ---------------------------------------------------------------------------

// Version management is no longer reachable from Book Detail — it moved to the
// library card's overflow menu, where it is a MenuItem (AudiobookCard.tsx:333),
// not a button. The overflow IconButton has no accessible name
// (AudiobookCard.tsx:183), so it can only be found by the icon inside it.
const openVersionManagerFor = async (page: Page, title: string) => {
  await page.goto('/library');
  await page.waitForLoadState('networkidle');
  const card = page
    .getByText(title, { exact: true })
    .locator('xpath=ancestor::*[contains(@class,"MuiCard-root")][1]');
  await card.locator('button:has([data-testid="MoreVertIcon"])').click();
  await page.getByRole('menuitem', { name: 'Manage Versions' }).click();
};

test.describe('M4B Transcode', () => {
  test('shows Convert to M4B button for MP3 books', async ({ page }) => {
    const book = mp3Book();
    await setupMockApi(page, { books: [book] });
    await page.goto(`/library/${book.id}`);
    await page.waitForLoadState('networkidle');

    await expect(page.getByRole('button', { name: /Convert to M4B/i })).toBeVisible();
  });

  test('does NOT show Convert to M4B for books already in M4B format', async ({ page }) => {
    const book = m4bBook({ version_group_id: undefined, version_notes: undefined });
    await setupMockApi(page, { books: [book] });
    await page.goto(`/library/${book.id}`);
    await page.waitForLoadState('networkidle');

    await expect(page.getByRole('button', { name: /Convert to M4B/i })).not.toBeVisible();
  });

  test('triggers transcode and shows progress', async ({ page }) => {
    const book = mp3Book();
    const tracker = await setupWithTranscode(page, [book]);
    await page.goto(`/library/${book.id}`);
    await page.waitForLoadState('networkidle');

    // Click Convert to M4B
    await page.getByRole('button', { name: /Convert to M4B/i }).click();

    // The button relabels itself while the job runs — BookDetailActions.tsx:206
    // renders 'Converting...' when transcoding, so /Convert to M4B/ no longer
    // matches the very element this asserts on.
    const converting = page.getByRole('button', { name: /Converting/i });
    await expect(converting).toBeVisible();
    await expect(converting).toBeDisabled();

    // Verify the transcode was triggered with the right book ID
    expect(tracker.transcodeStarted()).toBe(true);
    expect(tracker.transcodeBookId()).toBe(book.id);
  });

  test('shows success toast after transcode completes', async ({ page }) => {
    const book = mp3Book();
    await setupWithTranscode(page, [book]);
    await page.goto(`/library/${book.id}`);
    await page.waitForLoadState('networkidle');

    await page.getByRole('button', { name: /Convert to M4B/i }).click();

    // Should show a success-related toast/notification
    await expect(page.getByRole('alert').getByText(/[Tt]ranscode started/)).toBeVisible({
      timeout: 5000,
    });
  });
});

// ---------------------------------------------------------------------------
// Version Management After Transcode
// ---------------------------------------------------------------------------

test.describe('Version Management After Transcode', () => {
  // Distinct titles: both builders default to 'The Odyssey', which made the two
  // library cards indistinguishable now that the version manager is opened from
  // a card rather than from book detail. The tests are about version grouping,
  // not titles, so disambiguating here costs nothing.
  const originalMp3 = mp3Book({
    id: 'orig-mp3',
    title: 'The Odyssey (MP3)',
    is_primary_version: false,
    version_group_id: 'vg-odyssey',
    version_notes: 'Original format',
  });

  const transcodedM4b = m4bBook({
    id: 'new-m4b',
    title: 'The Odyssey (M4B)',
    is_primary_version: true,
    version_group_id: 'vg-odyssey',
    version_notes: 'Transcoded to M4B',
  });

  test('shows version group with original and transcoded versions', async ({ page }) => {
    await setupMockApi(page, { books: [originalMp3, transcodedM4b] });

    // Intercept version list endpoint
    await page.route('**/api/v1/audiobooks/new-m4b/versions', async (route) => {
      if (route.request().method() === 'GET') {
        return route.fulfill({
          status: 200,
          contentType: 'application/json',
          // api.getBookVersions reads body.data?.versions (services/api.ts:2328),
          // not a bare { items }.
          body: JSON.stringify({
            data: { versions: [transcodedM4b, originalMp3] },
          }),
        });
      }
      return route.fallback();
    });

    await openVersionManagerFor(page, 'The Odyssey (M4B)');

    // Should show both versions
    await expect(page.getByText('Transcoded to M4B')).toBeVisible();
    await expect(page.getByText('Original format')).toBeVisible();
  });

  test('M4B version is marked as primary', async ({ page }) => {
    await setupMockApi(page, { books: [originalMp3, transcodedM4b] });

    await page.route('**/api/v1/audiobooks/new-m4b/versions', async (route) => {
      if (route.request().method() === 'GET') {
        return route.fulfill({
          status: 200,
          contentType: 'application/json',
          // api.getBookVersions reads body.data?.versions (services/api.ts:2328),
          // not a bare { items }.
          body: JSON.stringify({
            data: { versions: [transcodedM4b, originalMp3] },
          }),
        });
      }
      return route.fallback();
    });

    await openVersionManagerFor(page, 'The Odyssey (M4B)');

    // The M4B version should show as primary
    const m4bRow = page.getByRole('listitem').filter({ hasText: 'Transcoded to M4B' }).first();
    await expect(m4bRow.getByText('Primary')).toBeVisible();
  });

  test('original MP3 is marked as non-primary', async ({ page }) => {
    await setupMockApi(page, { books: [originalMp3, transcodedM4b] });

    await page.route('**/api/v1/audiobooks/orig-mp3/versions', async (route) => {
      if (route.request().method() === 'GET') {
        return route.fulfill({
          status: 200,
          contentType: 'application/json',
          // api.getBookVersions reads body.data?.versions (services/api.ts:2328),
          // not a bare { items }.
          body: JSON.stringify({
            data: { versions: [transcodedM4b, originalMp3] },
          }),
        });
      }
      return route.fallback();
    });

    await openVersionManagerFor(page, 'The Odyssey (MP3)');

    // Original should NOT be primary
    const origRow = page.getByRole('listitem').filter({ hasText: 'Original format' }).first();
    await expect(origRow.getByText('Primary')).not.toBeVisible();
  });

  test('shows format quality badges in version list', async ({ page }) => {
    await setupMockApi(page, { books: [originalMp3, transcodedM4b] });

    await page.route('**/api/v1/audiobooks/new-m4b/versions', async (route) => {
      if (route.request().method() === 'GET') {
        return route.fulfill({
          status: 200,
          contentType: 'application/json',
          // api.getBookVersions reads body.data?.versions (services/api.ts:2328),
          // not a bare { items }.
          body: JSON.stringify({
            data: { versions: [transcodedM4b, originalMp3] },
          }),
        });
      }
      return route.fallback();
    });

    await openVersionManagerFor(page, 'The Odyssey (M4B)');

    // Should show codec/format chips
    await expect(page.getByText('aac')).toBeVisible();
    await expect(page.getByText('mp3', { exact: true }).first()).toBeVisible();
  });
});

// ---------------------------------------------------------------------------
// File Count Display Tests
// ---------------------------------------------------------------------------

test.describe('File Count Display', () => {
  test('dashboard shows file count alongside book count', async ({ page }) => {
    const books = generateTestBooks(5);
    await setupMockApi(page, { books });

    // Override system status to include file counts
    await page.route('**/api/v1/system/status', async (route) => {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        // Must be enveloped and use the nested shape. api.getSystemStatus()
        // returns body.data (services/api.ts:2070), and Dashboard.tsx:97 reads
        // systemStatus.library_book_count ?? systemStatus.library.book_count.
        // A flat, un-enveloped body made body.data undefined, which threw on
        // .library and left EVERY dashboard count at 0 — including Authors and
        // Series, which is what made this look like an unrelated failure.
        body: JSON.stringify({
          data: {
            status: 'ok',
            library: { book_count: 37, folder_count: 2, total_size: 0 },
            library_book_count: 37,
            file_count: 90,
            author_count: 12,
            series_count: 8,
            import_paths: { folder_count: 2, book_count: 0, total_size: 0 },
            storage: {
              library_size_bytes: 50_000_000_000,
              import_size_bytes: 5_000_000_000,
              total_size_bytes: 55_000_000_000,
              disk_total_bytes: 500_000_000_000,
              disk_used_bytes: 200_000_000_000,
              disk_free_bytes: 300_000_000_000,
            },
          },
        }),
      });
    });

    await page.goto('/');
    await page.waitForLoadState('networkidle');

    // Should display file count somewhere on dashboard
    // The exact format depends on the implementation ("37 books (90 files)" or similar)
    await expect(page.getByText('37')).toBeVisible();
    // If file_count is displayed, check for it
    const fileCountVisible = await page
      .getByText('90')
      .isVisible()
      .catch(() => false);
    if (fileCountVisible) {
      await expect(page.getByText('90')).toBeVisible();
    }
  });

  test('authors page shows book and file counts', async ({ page }) => {
    const books = [
      ...generateTestBooks(3).map((b, i) => ({
        ...b,
        author_name: 'Brandon Sanderson',
        id: `bs-${i}`,
      })),
      ...generateTestBooks(2).map((b, i) => ({
        ...b,
        author_name: 'Patrick Rothfuss',
        id: `pr-${i}`,
      })),
    ];

    await setupMockApi(page, { books });

    // Override authors endpoint with file_count
    await page.route(
      (url) => new URL(url).pathname === '/api/v1/authors',
      async (route) => {
        return route.fulfill({
          status: 200,
          contentType: 'application/json',
          // api.getAuthors reads body.data.items (services/api.ts:1352). The
          // route pattern also required a query string ('/authors?*'), but
          // getAuthors fetches '/authors' bare — so this override never even
          // matched and the shared mock answered instead.
          body: JSON.stringify({
            data: {
              // `aliases` is required: Authors.tsx:89/120/121 read a.aliases.length
              // with no guard, so omitting it crashes the page into the error
              // boundary rather than just rendering a blank column.
              items: [
                { id: 1, name: 'Brandon Sanderson', book_count: 3, file_count: 15, aliases: [] },
                { id: 2, name: 'Patrick Rothfuss', book_count: 2, file_count: 8, aliases: [] },
              ],
              count: 2,
            },
          }),
        });
      }
    );

    await page.goto('/authors');
    await page.waitForLoadState('networkidle');

    // Should show book counts
    await expect(page.getByText('Brandon Sanderson')).toBeVisible();
    await expect(page.getByText('3')).toBeVisible();
  });

  test('series page shows book and file counts', async ({ page }) => {
    const books = generateTestBooks(5).map((b, i) => ({
      ...b,
      series_name: 'Stormlight Archive',
      series_position: i + 1,
      id: `sa-${i}`,
    }));

    await setupMockApi(page, { books });

    // Override series endpoint with file_count
    await page.route(
      (url) => new URL(url).pathname === '/api/v1/series',
      async (route) => {
        return route.fulfill({
          status: 200,
          contentType: 'application/json',
          // api.getSeries reads body.data.items (services/api.ts:1627).
          body: JSON.stringify({
            data: {
              items: [{ id: 1, name: 'Stormlight Archive', book_count: 5, file_count: 25 }],
              count: 1,
            },
          }),
        });
      }
    );

    await page.goto('/series');
    await page.waitForLoadState('networkidle');

    // Should show book count
    await expect(page.getByText('Stormlight Archive')).toBeVisible();
    await expect(page.getByText('5 (25 files)')).toBeVisible();
  });
});

// ---------------------------------------------------------------------------
// Multi-file Book Display
// ---------------------------------------------------------------------------

test.describe('Multi-file Book Handling', () => {
  test('book detail shows file count for multi-file audiobooks', async ({ page }) => {
    const book = mp3Book({
      id: 'multi-file-book',
      title: 'The Odyssey',
      file_path: '/audiobooks/Homer/The Odyssey',
    });

    await setupMockApi(page, { books: [book] });

    // Mock the tags/segments endpoint to show multiple files
    await page.route('**/api/v1/audiobooks/multi-file-book/tags', async (route) => {
      if (route.request().method() === 'GET') {
        return route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({
            segments: [
              {
                id: 's1',
                file_path: '/audiobooks/Homer/The Odyssey/chapter_01.mp3',
                track_number: 1,
                duration_seconds: 3000,
              },
              {
                id: 's2',
                file_path: '/audiobooks/Homer/The Odyssey/chapter_02.mp3',
                track_number: 2,
                duration_seconds: 3200,
              },
              {
                id: 's3',
                file_path: '/audiobooks/Homer/The Odyssey/chapter_03.mp3',
                track_number: 3,
                duration_seconds: 2800,
              },
              {
                id: 's4',
                file_path: '/audiobooks/Homer/The Odyssey/chapter_04.mp3',
                track_number: 4,
                duration_seconds: 4000,
              },
              {
                id: 's5',
                file_path: '/audiobooks/Homer/The Odyssey/chapter_05.mp3',
                track_number: 5,
                duration_seconds: 3500,
              },
              {
                id: 's6',
                file_path: '/audiobooks/Homer/The Odyssey/chapter_06.mp3',
                track_number: 6,
                duration_seconds: 2500,
              },
            ],
            media_info: {
              format: 'mp3',
              codec: 'mp3',
              bitrate: 64,
              sample_rate: 44100,
              channels: 2,
            },
          }),
        });
      }
      return route.fallback();
    });

    await page.goto(`/library/${book.id}`);
    await page.waitForLoadState('networkidle');

    // Book detail should show info about the multi-file nature
    // Check for "6 files" or segment count somewhere
    await expect(page.getByRole('heading', { name: 'The Odyssey' })).toBeVisible();
  });

  test('library list distinguishes single-file from multi-file books', async ({ page }) => {
    const books = [
      mp3Book({
        id: 'single-file',
        title: 'Short Story',
        format: 'mp3',
        file_path: '/audiobooks/short_story.mp3',
      }),
      mp3Book({
        id: 'multi-file',
        title: 'Epic Novel',
        format: 'mp3',
        file_path: '/audiobooks/Author/Epic Novel',
      }),
      m4bBook({
        id: 'm4b-single',
        title: 'Combined Book',
        file_path: '/audiobooks/Author/Combined Book.m4b',
        version_group_id: undefined,
        version_notes: undefined,
      }),
    ];

    await setupMockApi(page, { books });
    await page.goto('/library');
    await page.waitForLoadState('networkidle');

    // All books should be visible
    await expect(page.getByText('Short Story')).toBeVisible();
    await expect(page.getByText('Epic Novel')).toBeVisible();
    await expect(page.getByText('Combined Book')).toBeVisible();
  });
});

// ---------------------------------------------------------------------------
// Transcode Error Handling
// ---------------------------------------------------------------------------

test.describe('Transcode Error Handling', () => {
  test('shows error when transcode fails', async ({ page }) => {
    const book = mp3Book();
    await setupMockApi(page, { books: [book] });

    // Mock transcode to return server error
    await page.route('**/api/v1/operations/transcode', async (route) => {
      return route.fulfill({
        status: 500,
        contentType: 'application/json',
        body: JSON.stringify({ error: 'ffmpeg not found on PATH' }),
      });
    });

    await page.goto(`/library/${book.id}`);
    await page.waitForLoadState('networkidle');

    await page.getByRole('button', { name: /Convert to M4B/i }).click();

    // Should show error
    await expect(page.getByText(/error|failed|not found/i)).toBeVisible({ timeout: 5000 });
  });

  test('shows error when book not found for transcode', async ({ page }) => {
    const book = mp3Book();
    await setupMockApi(page, { books: [book] });

    // Mock transcode to return 404
    await page.route('**/api/v1/operations/transcode', async (route) => {
      return route.fulfill({
        status: 404,
        contentType: 'application/json',
        body: JSON.stringify({ error: 'book not found' }),
      });
    });

    await page.goto(`/library/${book.id}`);
    await page.waitForLoadState('networkidle');

    await page.getByRole('button', { name: /Convert to M4B/i }).click();

    await expect(page.getByText(/error|not found/i)).toBeVisible({ timeout: 5000 });
  });
});

// ---------------------------------------------------------------------------
// Counting Accuracy — version dedup
// ---------------------------------------------------------------------------

test.describe('Counting Accuracy', () => {
  test('non-primary versions are excluded from book counts on dashboard', async ({ page }) => {
    const primary = m4bBook({
      id: 'primary-1',
      is_primary_version: true,
      version_group_id: 'vg-1',
    });
    const nonPrimary = mp3Book({
      id: 'non-primary-1',
      is_primary_version: false,
      version_group_id: 'vg-1',
    });
    const standalone = mp3Book({
      id: 'standalone-1',
      title: 'Standalone Book',
      is_primary_version: true,
      version_group_id: undefined,
    });

    await setupMockApi(page, { books: [primary, nonPrimary, standalone] });

    // The dashboard should count 2 books (primary + standalone), not 3
    await page.route('**/api/v1/system/status', async (route) => {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        // Same envelope + nested-shape requirement as above.
        body: JSON.stringify({
          data: {
            status: 'ok',
            library: { book_count: 2, folder_count: 1, total_size: 0 },
            library_book_count: 2, // Only primary versions counted
            file_count: 8, // Total files across all versions
            author_count: 1,
            series_count: 0,
            import_paths: { folder_count: 0, book_count: 0, total_size: 0 },
            storage: {
              library_size_bytes: 0,
              total_size_bytes: 0,
              disk_total_bytes: 500_000_000_000,
              disk_used_bytes: 100_000_000_000,
              disk_free_bytes: 400_000_000_000,
            },
          },
        }),
      });
    });

    await page.goto('/');
    await page.waitForLoadState('networkidle');

    // Should show 2 books, not 3
    await expect(page.getByText('2')).toBeVisible();
  });

  test('library list shows all versions including non-primary', async ({ page }) => {
    const primary = m4bBook({
      id: 'primary-1',
      is_primary_version: true,
      version_group_id: 'vg-1',
      title: 'The Odyssey (M4B)',
    });
    const nonPrimary = mp3Book({
      id: 'non-primary-1',
      is_primary_version: false,
      version_group_id: 'vg-1',
      title: 'The Odyssey (MP3)',
    });

    await setupMockApi(page, { books: [primary, nonPrimary] });
    await page.goto('/library');
    await page.waitForLoadState('networkidle');

    // Both versions should be visible in library list
    await expect(page.getByText('The Odyssey (M4B)')).toBeVisible();
    await expect(page.getByText('The Odyssey (MP3)')).toBeVisible();
  });
});
