// file: web/tests/e2e/collections.spec.ts
// version: 1.0.1
// guid: bd2cb2a2-6bcf-4036-9269-c4563947e32d
// last-edited: 2026-10-26

import { test, expect, type Page, type Route } from '@playwright/test';
import { setupMockApi } from './utils/test-helpers';

// The page against a mocked API. The collection routes are served by a small
// in-memory store so create -> list -> open round-trips; /auth/me is overridden
// per test to control the caller's permissions.

interface MockCollection {
  id: string;
  name: string;
  description?: string;
  type: 'static' | 'dynamic';
  book_ids?: string[];
  query?: string;
  sort_json?: string;
  limit?: number;
  materialized_book_ids?: string[];
  created_by_user_id: string;
  version: number;
  created_at: string;
  updated_at: string;
}

const STAMP = '2026-10-18T00:00:00Z';

const seeded: MockCollection[] = [
  {
    id: 'c-static',
    name: 'Test Static Collection',
    description: 'Hand picked',
    type: 'static',
    book_ids: ['b1', 'b2'],
    created_by_user_id: 'u1',
    version: 1,
    created_at: STAMP,
    updated_at: STAMP,
  },
  {
    id: 'c-dynamic',
    name: 'Test Dynamic Collection',
    type: 'dynamic',
    query: 'duration:>20h read_status:unstarted',
    materialized_book_ids: ['b2'],
    created_by_user_id: 'u1',
    version: 1,
    created_at: STAMP,
    updated_at: STAMP,
  },
];

const BOOKS: Record<string, { id: string; title: string; author_name: string }> = {
  b1: { id: 'b1', title: 'Test Book One', author_name: 'Test Author' },
  b2: { id: 'b2', title: 'Test Book Two', author_name: 'Test Author' },
};

const json = (body: unknown, status = 200) => ({
  status,
  contentType: 'application/json',
  body: JSON.stringify(body),
});

async function mockCollectionsApi(page: Page, permissions: string[]) {
  const store: MockCollection[] = JSON.parse(JSON.stringify(seeded));
  const posted: unknown[] = [];

  await setupMockApi(page, {
    auth: {
      auth_enabled: true,
      has_users: true,
      requires_auth: true,
      currentUser: {
        id: 'u1',
        username: 'tester',
        email: 'tester@example.test',
        roles: ['viewer'],
        status: 'active',
        created_at: STAMP,
      },
    },
  });

  // Registered after setupMockApi, so these win over its catch-all.
  await page.route('**/api/v1/auth/me', (route) =>
    route.fulfill(
      json({
        data: {
          user: {
            id: 'u1',
            username: 'tester',
            email: 'tester@example.test',
            roles: ['viewer'],
            status: 'active',
            created_at: STAMP,
            permissions,
          },
        },
      })
    )
  );

  await page.route(/\/api\/v1\/audiobooks\?.*\bids=/, (route) => {
    const ids = (new URL(route.request().url()).searchParams.get('ids') ?? '').split(',');
    const items = ids.map((id) => BOOKS[id]).filter(Boolean);
    return route.fulfill(json({ data: { items, count: items.length } }));
  });

  const handle = async (route: Route) => {
    const req = route.request();
    const url = new URL(req.url());
    const rest = url.pathname.replace('/api/v1/collections', '').replace(/^\//, '');
    const method = req.method();

    if (rest === '' && method === 'GET') {
      const type = url.searchParams.get('type');
      const items = store.filter((c) => !type || c.type === type);
      return route.fulfill(json({ items, count: items.length, limit: 200, offset: 0 }));
    }
    if (rest === '' && method === 'POST') {
      const body = req.postDataJSON() as Record<string, unknown>;
      posted.push(body);
      const created: MockCollection = {
        id: `c-new-${store.length}`,
        name: String(body.name),
        description: body.description as string | undefined,
        type: body.type as 'static' | 'dynamic',
        book_ids: body.book_ids as string[] | undefined,
        query: body.query as string | undefined,
        // a dynamic collection is evaluated once at creation
        materialized_book_ids: body.type === 'dynamic' ? ['b1'] : undefined,
        created_by_user_id: 'u1',
        version: 1,
        created_at: STAMP,
        updated_at: STAMP,
      };
      store.push(created);
      return route.fulfill(json({ data: created }, 201));
    }
    const found = store.find((c) => c.id === rest);
    if (!found) return route.fulfill(json({ error: 'collection not found' }, 404));
    if (method === 'GET') return route.fulfill(json({ data: found }));
    if (method === 'DELETE') {
      store.splice(store.indexOf(found), 1);
      return route.fulfill(json({ data: { id: found.id, deleted: true } }));
    }
    return route.fulfill(json({ error: 'not mocked' }, 500));
  };
  await page.route(/\/api\/v1\/collections(\/[^?]*)?(\?.*)?$/, handle);

  return { store, posted };
}

test.describe('Collections page', () => {
  test('lists collections and opens one with its members', async ({ page }) => {
    await mockCollectionsApi(page, ['library.view', 'collections.manage']);
    await page.goto('/collections');

    await expect(page.getByText('Test Static Collection')).toBeVisible();
    await expect(page.getByText('Test Dynamic Collection')).toBeVisible();

    await page.getByText('Test Static Collection').click();
    await expect(page).toHaveURL(/\/collections\/c-static$/);
    await expect(page.getByRole('link', { name: 'Test Book One' })).toBeVisible();
    await expect(page.getByRole('link', { name: 'Test Book Two' })).toBeVisible();
    await expect(page.getByRole('heading', { name: 'Members (2)' })).toBeVisible();
  });

  test('clicking a count lands on the member view', async ({ page }) => {
    await mockCollectionsApi(page, ['library.view', 'collections.manage']);
    await page.goto('/collections');

    await page.getByRole('button', { name: /the 1 books in Test Dynamic Collection/ }).click();
    await expect(page).toHaveURL(/\/collections\/c-dynamic$/);
    await expect(page.getByRole('link', { name: 'Test Book Two' })).toBeVisible();
  });

  test('creates a static collection and lists it, with the exact payload', async ({ page }) => {
    const { posted } = await mockCollectionsApi(page, ['library.view', 'collections.manage']);
    await page.goto('/collections');

    await page.getByRole('button', { name: /New Collection/ }).click();
    const dialog = page.getByRole('dialog');
    await dialog.getByLabel(/^Name/).fill('Test New Static');
    await dialog.getByLabel(/Description/).fill('Made on the page');
    await dialog.getByRole('button', { name: 'Create' }).click();

    await expect(page.getByText('Test New Static')).toBeVisible();
    // This is the payload the ABS list route returns the collection for (07-F5a).
    expect(posted).toEqual([
      { type: 'static', name: 'Test New Static', description: 'Made on the page' },
    ]);
  });

  test('creates a dynamic collection and shows its preview', async ({ page }) => {
    const { posted } = await mockCollectionsApi(page, ['library.view', 'collections.manage']);
    await page.goto('/collections');

    await page.getByRole('button', { name: /New Collection/ }).click();
    const dialog = page.getByRole('dialog');
    await dialog.getByRole('button', { name: 'Dynamic' }).click();
    await dialog.getByLabel(/^Name/).fill('Test New Dynamic');
    await dialog.getByRole('button', { name: 'Use this query' }).first().click();
    await expect(
      dialog.getByText(/reading state of whoever is viewing the collection/)
    ).toBeVisible();
    await dialog.getByRole('button', { name: 'Create' }).click();

    await expect(page.getByText('Test New Dynamic')).toBeVisible();
    expect(posted).toEqual([
      {
        type: 'dynamic',
        name: 'Test New Dynamic',
        query: 'read_status:finished last_played:>-30d',
      },
    ]);

    await page.getByText('Test New Dynamic').click();
    await page.getByRole('button', { name: 'Preview' }).click();
    await expect(page.getByRole('link', { name: 'Test Book One' })).toBeVisible();
  });

  test('a user without collections.manage sees the lists but no New or Delete', async ({
    page,
  }) => {
    await mockCollectionsApi(page, ['library.view']);
    await page.goto('/collections');

    await expect(page.getByText('Test Static Collection')).toBeVisible();
    await expect(page.getByRole('button', { name: /New Collection/ })).toHaveCount(0);
    await expect(page.getByRole('button', { name: /^Delete/ })).toHaveCount(0);

    await page.getByText('Test Static Collection').click();
    await expect(page.getByRole('link', { name: 'Test Book One' })).toBeVisible();
    await expect(page.getByRole('button', { name: 'Delete' })).toBeDisabled();
    await expect(page.getByRole('button', { name: 'Add books' })).toBeDisabled();
  });
});
