// file: core/web/e2e/bundle.spec.ts
// version: 0.1.0
// guid: 0a7c3e59-4d18-4b92-8f60-c2e9b1d7a345
// last-edited: 2026-11-20

// The bundle check (07 §7.18, FE-18): /login with an empty cache fetches no
// shell-page or module-page chunk, and the entry and mui chunks stay under
// their budgets.

import { expect, test } from '@playwright/test';

/** gzip-free byte budgets of the two eager chunks (FE-18). */
const BUDGET = { entry: 200 * 1024, mui: 450 * 1024 };

test('/login with an empty cache fetches no page chunk', async ({ page }) => {
  const scripts: { url: string; bytes: number }[] = [];
  page.on('response', async (r) => {
    if (r.request().resourceType() === 'script' && new URL(r.url()).pathname.startsWith('/assets/')) {
      scripts.push({ url: new URL(r.url()).pathname, bytes: (await r.body()).length });
    }
  });
  await page.goto('/login', { waitUntil: 'networkidle' });
  const names = scripts.map((s) => s.url);
  expect(names.some((n) => /\/index-[^/]+\.js$/.test(n)), `entry chunk among ${names}`).toBe(true);
  // hello's module pages are lazy: neither its page nor its settings panel loads on /login.
  expect(names.filter((n) => /GreetingPage|HelloSettings/.test(n))).toEqual([]);
  const entry = scripts.find((s) => /\/index-[^/]+\.js$/.test(s.url))!;
  const mui = scripts.find((s) => /\/mui-[^/]+\.js$/.test(s.url));
  expect(entry.bytes).toBeLessThan(BUDGET.entry);
  if (mui) expect(mui.bytes).toBeLessThan(BUDGET.mui);
});

test('the page carries the three preset metas and the boot script the CSP hashes', async ({ page, request }) => {
  const res = await request.get('/login');
  expect(res.headers()['cache-control']).toBe('no-store');
  const csp = res.headers()['content-security-policy'] ?? res.headers()['content-security-policy-report-only'];
  expect(csp).toMatch(/script-src 'self' 'sha256-[A-Za-z0-9+/=]+'/);
  await page.goto('/login');
  for (const name of ['mediacore-build', 'mediacore-app', 'mediacore-theme']) {
    await expect(page.locator(`meta[name="${name}"]`)).toHaveCount(1);
  }
  await expect(page.locator('meta[name="mediacore-app"]')).toHaveAttribute('content', 'hello');
  // The boot script ran under the CSP: the mode attribute is set before the app mounts.
  await expect(page.locator('html')).toHaveAttribute('data-mui-color-scheme', /^(light|dark)$/);
});
