// file: web/tests/smoke/routes.mjs
// version: 1.0.0
// guid: 18d11aa3-09de-42e9-9350-eb5fe5660c2f
// last-edited: 2026-08-19
//
// Per-route bundle smoke test. Loads every route against a running server and
// fails on any uncaught page error.
//
// This exists because neither of the other suites can catch the failure it
// targets. The vitest suites render components through jsdom and never touch
// bundled output; a broken chunk boundary is invisible to them. The Playwright
// e2e suite does load the real bundle, but only exercises a handful of routes.
// The previous Vite 8 attempt was reverted for a React #130 crash that came from
// a module-resolution fault at a *chunk* boundary -- so route coverage, not
// interaction depth, is what detects it.
//
// Usage:
//   ENABLE_AUTH=false ./audiobook-organizer serve --db /tmp/x.pebble --port 9797 &
//   node tests/smoke/routes.mjs https://localhost:9797
//
// ENABLE_AUTH=false is required. With auth on, every route renders the login
// form and returns HTTP 200, so the run passes without loading a single lazy
// chunk. The isLogin assertion below turns that false green into a failure.

import { chromium } from 'playwright';

const base = process.argv[2] || 'https://localhost:9797';
const ROUTES = [
  '/', '/dashboard', '/library', '/series', '/authors', '/authors/dedup',
  '/books/dedup', '/dedup', '/dedup/labels', '/files', '/works', '/playlists',
  '/fingerprints', '/activity', '/operations', '/diagnostics', '/system',
  '/users', '/settings', '/maintenance', '/versions',
];

const b = await chromium.launch();
const ctx = await b.newContext({ ignoreHTTPSErrors: true });
let bad = 0;

for (const r of ROUTES) {
  const p = await ctx.newPage();
  const pageErrors = [];
  const consoleErrors = [];
  p.on('pageerror', (e) => pageErrors.push(e.message));
  p.on('console', (m) => {
    if (m.type() === 'error') consoleErrors.push(m.text());
  });

  await p
    .goto(base + r, { waitUntil: 'networkidle', timeout: 30000 })
    .catch((e) => pageErrors.push('NAV: ' + e.message));
  await p.waitForTimeout(1200);

  const len = await p
    .evaluate(() => document.getElementById('root')?.innerHTML.length ?? -1)
    .catch(() => -2);
  const txt = (await p.evaluate(() => document.body?.innerText || '').catch(() => ''))
    .slice(0, 60)
    .replace(/\s+/g, ' ');

  // A login redirect renders fine and returns 200, but exercises none of the
  // route's own code. Treat it as a failure, not a pass.
  const isLogin = /Sign in to access/.test(txt);
  const ok = len > 500 && pageErrors.length === 0 && !isLogin;
  if (!ok) bad++;
  const why = isLogin ? ' [LOGIN-REDIRECT: route not exercised]' : '';

  console.log(
    `${ok ? 'OK  ' : 'FAIL'} ${r.padEnd(18)} len=${String(len).padEnd(6)} ` +
      `pageErr=${pageErrors.length} consoleErr=${consoleErrors.length} | ${txt}${why}`
  );
  if (pageErrors.length) console.log('      PAGEERR: ' + JSON.stringify(pageErrors.slice(0, 3)));
  if (consoleErrors.length) console.log('      CONSERR: ' + JSON.stringify(consoleErrors.slice(0, 3)));
  await p.close();
}

console.log(`\nRESULT: ${ROUTES.length - bad}/${ROUTES.length} routes clean`);
await b.close();
process.exit(bad ? 1 : 0);
