// file: web/tests/e2e/shell-mode.spec.ts
// version: 0.1.0
// guid: a330d8f7-2f7f-458a-81e4-a7b820a5410b
// last-edited: 2026-11-22

// F9-03: driving ABO's pages through the app shell. The shell boots on
// /api/core/v1 (07 section 7.12), which the legacy bundle never calls, so a
// shell-mode spec installs the core API mock (utils/mockCoreApi.ts, opt-in) beside
// the legacy /api/v1 mocks. Until commit 2 puts main.tsx on createShell the served
// bundle is still the legacy one: the first block proves the mock layer works in a
// real browser, and the second holds the specs that need the shell.
//
// The page paths come from utils/paths: `paths` today and `shellPaths` (the 07
// section 7.11 table) once the flip's commit 2 switches the suite over.

import { expect, test } from '@playwright/test';
import { installCoreApiMock, coreModule, coreTaskRun } from './utils/mockCoreApi';
import { paths, shellPaths } from './utils/paths';

test.describe('core API mock layer', () => {
  test('answers the shell boot routes in a real page, stamps X-App-Build and refuses the rest by name', async ({ page }) => {
    const core = await installCoreApiMock(page, {
      modules: [coreModule('books', '1.0.0', 'Books', { nav: ['library'] })],
      taskRuns: [coreTaskRun('run-1', 'running')],
      // The legacy bundle does not call core: keep its other core clients quiet.
    });
    await page.goto(paths.home);

    const probe = (path: string, init?: { method?: string; body?: string }) =>
      page.evaluate(
        async ([p, i]) => {
          const res = await fetch(p as string, {
            method: (i as { method?: string } | undefined)?.method ?? 'GET',
            headers: { 'Content-Type': 'application/json' },
            body: (i as { body?: string } | undefined)?.body,
          });
          return { status: res.status, build: res.headers.get('X-App-Build'), type: res.headers.get('Content-Type'), text: await res.text() };
        },
        [path, init] as const,
      );

    const status = await probe('/api/core/v1/auth/status');
    expect(status).toMatchObject({ status: 200, build: 'dev' });
    expect(JSON.parse(status.text)).toEqual({ has_users: true, auth_disabled: false, setup_required: false });

    expect(JSON.parse((await probe('/api/core/v1/me')).text)).toMatchObject({ user: { username: 'batman' }, permissions: ['core:admin'] });

    const modules = JSON.parse((await probe('/api/core/v1/modules')).text) as { app: { id: string }; modules: { id: string }[] };
    expect(modules.app.id).toBe('abo');
    expect(modules.modules.map((m) => m.id)).toEqual(['books']);

    const runs = JSON.parse((await probe('/api/core/v1/tasks/runs')).text) as { runs: { id: string }[] };
    expect(runs.runs.map((r) => r.id)).toEqual(['run-1']);

    const logout = await probe('/api/core/v1/auth/logout', { method: 'POST' });
    expect(logout.status).toBe(204);
    expect((await probe('/api/core/v1/me')).status).toBe(401);

    const unknown = await probe('/api/core/v1/gadgets');
    expect(unknown).toMatchObject({ status: 501, type: 'application/problem+json' });
    expect(core.unhandled().map((r) => r.path)).toContain('/api/core/v1/gadgets');
    expect(() => core.assertNoUnhandled()).toThrow(/gadgets/);
  });

  test('leaves the event stream to the shim and the legacy /api/v1 routes alone', async ({ page }) => {
    const core = await installCoreApiMock(page);
    await page.goto(paths.home);
    const events = await page.evaluate(async () => {
      const res = await fetch('/api/core/v1/events?topic=core.task.run-changed.v1');
      const reader = res.body!.getReader();
      const first = new TextDecoder().decode((await reader.read()).value);
      await reader.cancel();
      return { status: res.status, type: res.headers.get('Content-Type'), first };
    });
    expect(events).toMatchObject({ status: 200, type: 'text/event-stream' });
    expect(core.requests().some((r) => r.path.endsWith('/events'))).toBe(false);
    core.events!.assertNoUnmockedRequests();
  });

  test('is opt-in: a page that never installs it reaches the real server for /api/core/v1', async ({ page }) => {
    await page.goto(paths.home);
    const res = await page.evaluate(async () => (await fetch('/api/core/v1/gadgets')).headers.get('X-App-Build'));
    // Not the mock's 'dev' stamp: whatever the real server answers.
    expect(res).not.toBe('dev');
  });
});

test.describe('ABO pages in the shell', () => {
  test.skip(true, 'needs F9-03 commit 2 (main.tsx on createShell, books module, /books/ routes) and commit 3 (UIBundle serves the shell bundle)');

  test('TestFormerPagePathIsNotFound: a former path renders the shell NotFound, the new one the page', async ({ page }) => {
    await installCoreApiMock(page, { modules: [coreModule('books', '1.0.0', 'Books', { pages: ['library'] })] });
    await page.goto(paths.library);
    await expect(page.getByText('Page not found')).toBeVisible();
    await page.goto(shellPaths.library);
    await expect(page.getByText('Page not found')).toHaveCount(0);
  });

  test('the shell boots on the mock and shows the signed-in layout', async ({ page }) => {
    const core = await installCoreApiMock(page);
    await page.goto(shellPaths.home);
    await expect(page.getByRole('navigation')).toBeVisible();
    core.assertNoUnhandled();
  });

  test('a signed-out visit lands on the shell login page', async ({ page }) => {
    await installCoreApiMock(page, { auth: 'anonymous' });
    await page.goto(shellPaths.home);
    await expect(page).toHaveURL(new RegExp(`${shellPaths.login}$`));
  });
});
