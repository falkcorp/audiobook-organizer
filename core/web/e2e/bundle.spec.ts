// file: core/web/e2e/bundle.spec.ts
// version: 0.1.0
// guid: 0a7c3e59-4d18-4b92-8f60-c2e9b1d7a345
// last-edited: 2026-11-20

// The bundle check (07 §7.18, FE-18): /login with an empty cache fetches no
// shell-page or module-page chunk, and the entry and mui chunks stay under
// their budgets.

import { readdirSync } from 'node:fs';
import { join } from 'node:path';
import { expect, test } from '@playwright/test';

/** Chunks that hold pages: the shell's own (one lazy chunk, 07 §7.10) and hello's module page and panel. */
const PAGE_CHUNK = /shellPages|GreetingPage|HelloSettings/;

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
  // Shell pages and hello's module page and settings panel are lazy: none loads on /login.
  expect(names.filter((n) => PAGE_CHUNK.test(n))).toEqual([]);
  const entry = scripts.find((s) => /\/index-[^/]+\.js$/.test(s.url))!;
  const mui = scripts.find((s) => /\/mui-[^/]+\.js$/.test(s.url));
  expect(mui, `mui chunk among ${names}`).toBeTruthy();
  expect(entry.bytes).toBeLessThan(BUDGET.entry);
  expect(mui!.bytes).toBeLessThan(BUDGET.mui);
});

test('the build has the page chunks the /login check looks for', () => {
  // Without this, a build that inlined every page into the entry would pass the check above.
  const assets = readdirSync(join(import.meta.dirname, '../../examples/hello/web/dist/assets'));
  for (const page of ['shellPages', 'GreetingPage', 'HelloSettings']) {
    expect(assets.some((a) => a.startsWith(`${page}-`) && a.endsWith('.js')), `${page} chunk in ${assets}`).toBe(true);
  }
});

test('the page carries the three preset metas and the boot script the CSP hashes', async ({ page, request }) => {
  const res = await request.get('/login');
  expect(res.headers()['cache-control']).toBe('no-store');
  const csp = res.headers()['content-security-policy'] ?? res.headers()['content-security-policy-report-only'];
  expect(csp).toMatch(/script-src 'self' 'sha256-[A-Za-z0-9+/=]+'/);
  // A blocked inline script raises securitypolicyviolation and a console error.
  // Asserting their absence is what proves the boot script ran: the
  // data-mui-color-scheme check below alone is blind, because MUI sets that
  // attribute too once the app mounts.
  await page.addInitScript(() => {
    const w = window as unknown as { __cspViolations: string[] };
    w.__cspViolations = [];
    document.addEventListener('securitypolicyviolation', (e) => w.__cspViolations.push(`${e.violatedDirective} ${e.blockedURI}`));
  });
  const cspErrors: string[] = [];
  page.on('console', (m) => {
    if (m.type() === 'error' && /Content Security Policy/i.test(m.text())) cspErrors.push(m.text());
  });
  await page.goto('/login', { waitUntil: 'networkidle' });
  expect(await page.evaluate(() => (window as unknown as { __cspViolations: string[] }).__cspViolations)).toEqual([]);
  expect(cspErrors).toEqual([]);
  for (const name of ['mediacore-build', 'mediacore-app', 'mediacore-theme']) {
    await expect(page.locator(`meta[name="${name}"]`)).toHaveCount(1);
  }
  await expect(page.locator('meta[name="mediacore-app"]')).toHaveAttribute('content', 'hello');
  // The mode attribute is set (by the boot script, then by MUI).
  await expect(page.locator('html')).toHaveAttribute('data-mui-color-scheme', /^(light|dark)$/);
});
