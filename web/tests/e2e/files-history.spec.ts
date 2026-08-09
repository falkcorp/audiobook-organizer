// file: tests/e2e/files-history.spec.ts
// version: 1.2.0
// guid: bd99e21f-38d1-4976-8ac2-43060c5fc17a

import { expect, test } from '@playwright/test';
import { mockEventSource } from './utils/test-helpers';

const bookId = 'book-fh-1';

const createBook = (overrides: Record<string, unknown> = {}) => ({
  id: bookId,
  title: 'Files History Test Book',
  author_name: 'Test Author',
  file_path: '/library/test-book.m4b',
  file_hash: 'hash-1',
  original_file_hash: 'hash-orig',
  organized_file_hash: 'hash-org',
  library_state: 'organized',
  format: 'm4b',
  codec: 'AAC',
  bitrate: 128,
  duration: 7200,
  file_size: 52428800,
  marked_for_deletion: false,
  is_primary_version: true,
  created_at: '2025-01-01T00:00:00Z',
  updated_at: '2025-01-02T00:00:00Z',
  ...overrides,
});

const createVersions = () => [
  createBook({ is_primary_version: true }),
  {
    ...createBook({
      id: 'book-fh-2',
      title: 'MP3 Version',
      format: 'mp3',
      codec: 'MP3',
      bitrate: 192,
      duration: 7200,
      file_size: 78643200,
      file_path: '/library/test-book-mp3/chapter01.mp3',
      is_primary_version: false,
    }),
  },
];

const createTags = () => ({
  media_info: { codec: 'AAC', bitrate: 128, sample_rate: 44100, channels: 2 },
  tags: {
    title: {
      file_value: 'Files History Test Book',
      stored_value: 'Files History Test Book',
      effective_value: 'Files History Test Book',
      effective_source: 'stored',
    },
    author_name: {
      file_value: 'Test Author',
      stored_value: 'Test Author',
      effective_value: 'Test Author',
      effective_source: 'stored',
    },
    narrator: {
      file_value: null,
      stored_value: null,
      effective_value: null,
      effective_source: '',
    },
    series_name: {
      file_value: null,
      stored_value: null,
      effective_value: null,
      effective_source: '',
    },
    publisher: {
      file_value: 'Test Publisher',
      stored_value: 'Test Publisher',
      effective_value: 'Test Publisher',
      effective_source: 'stored',
    },
    language: {
      file_value: 'en',
      stored_value: 'en',
      effective_value: 'en',
      effective_source: 'stored',
    },
    isbn13: {
      file_value: null,
      stored_value: null,
      effective_value: null,
      effective_source: '',
    },
  },
});

const createChangelog = () => ({
  entries: [
    {
      timestamp: '2025-01-02T12:00:00Z',
      type: 'tag_write',
      summary: 'Tags written — title, author',
    },
    {
      timestamp: '2025-01-01T10:00:00Z',
      type: 'import',
      summary: 'Imported from /imports/test-book.m4b',
    },
  ],
});

const setupRoutes = async (page: import('@playwright/test').Page) => {
  await mockEventSource(page);

  await page.addInitScript(() => {
    localStorage.setItem('welcome_wizard_completed', 'true');
  });

  const book = createBook();
  const versions = createVersions();
  const tags = createTags();
  const changelog = createChangelog();

  await page.addInitScript(
    ({
      bookId: injectedBookId,
      bookData,
      versionsData,
      tagsData,
      changelogData,
    }: {
      bookId: string;
      bookData: typeof book;
      versionsData: typeof versions;
      tagsData: typeof tags;
      changelogData: typeof changelog;
    }) => {
      // Adds the { data: ... } envelope the real API returns. This spec mocks
      // by patching window.fetch rather than using page.route + setupMockApi,
      // so it gets none of the shared handlers and needs its own copy.
      // Additive — top-level keys are preserved, so both `body.x` and
      // `body.data.x` readers work. Arrays and already-wrapped bodies are
      // left alone.
      const jsonResponse = (body: unknown, status = 200) => {
        const envelope = Array.isArray(body)
          ? { data: body }
          : body && typeof body === 'object' && !('data' in body)
            ? { ...(body as Record<string, unknown>), data: body }
            : body;
        return new Response(JSON.stringify(envelope), {
          status,
          headers: { 'Content-Type': 'application/json' },
        });
      };

      const originalFetch = window.fetch.bind(window);
      window.fetch = (input: RequestInfo | URL, init?: RequestInit) => {
        const url = typeof input === 'string' ? input : input.url;

        // Auth first. Without it the shim falls through to the real server,
        // the auth check fails, and the app renders the LOGIN screen — so
        // every assertion here was looking for a tab on a page that was never
        // going to show it. api.getAuthStatus() reads body.data.
        if (url.includes('/api/v1/auth/status')) {
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

        // Changelog
        if (url.includes(`/api/v1/audiobooks/${injectedBookId}/changelog`)) {
          return Promise.resolve(jsonResponse(changelogData));
        }

        // Tags (with optional compare_id or snapshot_ts)
        if (url.includes('/tags')) {
          const hasCompare = url.includes('compare_id=') || url.includes('snapshot_ts=');
          if (hasCompare) {
            // Add comparison_value to each tag
            const compTags = JSON.parse(JSON.stringify(tagsData));
            for (const key of Object.keys(compTags.tags)) {
              compTags.tags[key].comparison_value = `Compared ${key}`;
            }
            return Promise.resolve(jsonResponse(compTags));
          }
          return Promise.resolve(jsonResponse(tagsData));
        }

        // Versions
        if (url.includes(`/api/v1/audiobooks/${injectedBookId}/versions`)) {
          return Promise.resolve(jsonResponse({ versions: versionsData }));
        }

        // Segments (legacy). api.getBookSegments reads body.data, so a bare []
        // would deserialise to undefined and crash the page on `.length`.
        if (url.includes('/segments')) {
          return Promise.resolve(jsonResponse([]));
        }

        // Book files — the canonical endpoint BookDetail tries first. Without
        // this branch the URL fell through to the book-detail catch-all below,
        // which returned the book object; `result.files` was then undefined and
        // the page fell back to /segments.
        if (url.includes(`/api/v1/audiobooks/${injectedBookId}/files`)) {
          return Promise.resolve(jsonResponse({ files: [], count: 0 }));
        }

        // External IDs. api.getBookExternalIDs reads the TOP-LEVEL body (no
        // envelope) and destructures `external_ids` — not `ids`.
        if (url.includes('/external-ids')) {
          return Promise.resolve(
            jsonResponse({ itunes_linked: false, total: 0, external_ids: [] })
          );
        }

        // Book detail
        if (
          url.includes(`/api/v1/audiobooks/${injectedBookId}`) &&
          !url.includes('/tags') &&
          !url.includes('/versions') &&
          !url.includes('/changelog') &&
          !url.includes('/segments') &&
          !url.includes('/files') &&
          !url.includes('/external-ids')
        ) {
          return Promise.resolve(jsonResponse(bookData));
        }

        // Book list
        if (url.endsWith('/api/v1/audiobooks')) {
          return Promise.resolve(
            jsonResponse({ items: [bookData], count: 1 })
          );
        }

        return originalFetch(input, init);
      };
    },
    {
      bookId,
      bookData: book,
      versionsData: versions,
      tagsData: tags,
      changelogData: changelog,
    }
  );
};

test.describe('Files & History tab', () => {
  test.beforeEach(async ({ page }) => {
    await setupRoutes(page);
    await page.goto(`/library/${bookId}?tab=files`);
  });

  test('tab shows "Files & History" label', async ({ page }) => {
    const tab = page.getByRole('tab', { name: /files & history/i });
    await expect(tab).toBeVisible();
  });

  test('format trays render grouped by format', async ({ page }) => {
    // Should have M4B and MP3 format trays
    const m4bTray = page.locator('[data-testid="format-tray-m4b"]');
    const mp3Tray = page.locator('[data-testid="format-tray-mp3"]');

    await expect(m4bTray).toBeVisible();
    await expect(mp3Tray).toBeVisible();

    // M4B tray should show Primary badge
    await expect(m4bTray.getByText('Primary')).toBeVisible();

    // MP3 tray should show format info
    await expect(mp3Tray.getByText(/MP3/)).toBeVisible();
  });

  test('tag comparison table and dropdown render inside an open tray', async ({ page }) => {
    // Expand the M4B format tray
    const m4bTray = page.locator('[data-testid="format-tray-m4b"]');
    await m4bTray.click();

    // Should show key tag badges
    await expect(page.getByText(/\u2713 title/i).first()).toBeVisible();

    // There is no longer a collapse toggle — TagComparison's `expanded` state
    // is initialised to true and never set, so the table is unconditionally
    // rendered. The tray's own <Collapse> has no unmountOnExit, so these
    // assertions must be toBeVisible(): the cells are in the DOM even while
    // the tray is shut.
    await expect(page.getByTestId('tag-comparison-select').first()).toBeVisible();
    await expect(page.getByRole('cell', { name: 'File', exact: true }).first()).toBeVisible();
    await expect(page.getByRole('cell', { name: 'DB', exact: true }).first()).toBeVisible();
  });

  test('change log section renders', async ({ page }) => {
    // Changelog section should be visible
    const changelogSection = page.getByTestId('changelog-section');
    await expect(changelogSection).toBeVisible();
    await expect(changelogSection.getByText('Change Log')).toBeVisible();

    // Should show timeline entries
    const timeline = page.getByTestId('changelog-timeline');
    await expect(timeline).toBeVisible();

    // Should have entries for tag_write and import
    await expect(page.getByText(/Tags written/)).toBeVisible();
    await expect(page.getByText(/Imported from/)).toBeVisible();

    // The "Compare snapshot" link was replaced by making the whole tag_write
    // row clickable, so drive the flow rather than looking for the old label:
    // open the tray (which mounts TagComparison), click the row, and assert
    // the comparison banner it is meant to trigger.
    await page.locator('[data-testid="format-tray-m4b"]').click();
    await page.getByText(/Tags written/).click();
    await expect(page.getByTestId('snapshot-comparison-banner').first()).toBeVisible();
  });
});
