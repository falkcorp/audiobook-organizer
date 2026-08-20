// file: web/tests/visual/compare-layout.mjs
// version: 1.1.0
// guid: 3f2c9a71-6d84-4b1e-9c05-8ae2d71f4b60
// last-edited: 2026-08-20
//
// Before/after layout comparison against a second running build.
//
// Why this exists: the MUI 6 -> 9 migration rewrote ~170 files of *styling*
// props via codemod. None of the existing gates can see the result. tsc proves
// types resolve, vitest proves assertions hold, and tests/smoke/routes.mjs
// proves each route renders without throwing -- a page whose Grid has collapsed
// to a single column with all spacing gone passes every one of them and reports
// OK. Grid v1 (negative container margins + item padding) and Grid v2 (gap) do
// not implement `spacing` identically, so a faithful prop translation is not
// automatically a faithful geometry translation.
//
// This is a comparison harness, not a pass/fail test: it flags geometry deltas
// worth a human's attention and writes PNG pairs to inspect. It is deliberately
// not wired into `npm test`, because it needs two servers on two ports.
//
// Usage:
//   ENABLE_AUTH=false <old-binary> serve --db /tmp/before.pebble --port 9798 &
//   ENABLE_AUTH=false <new-binary> serve --db /tmp/after.pebble  --port 9797 &
//   node tests/visual/compare-layout.mjs
//
// Both servers MUST be started with the SAME flags (in particular --dir) and
// with ENABLE_AUTH=false. With auth on, every route redirects
// to the login form, every screenshot pair matches perfectly, and the harness
// reports a clean run having compared nothing. Verify with
// `curl -sk https://127.0.0.1:$PORT/api/v1/auth/status` before trusting output.
import { chromium } from 'playwright';
import { mkdirSync } from 'node:fs';

const BEFORE = process.env.BEFORE_URL ?? 'https://127.0.0.1:9798';
const AFTER = process.env.AFTER_URL ?? 'https://127.0.0.1:9797';
const OUT =
  process.env.SHOT_DIR ?? '/Users/jdfalk/repos/temp_crap/.abo-logs/shots';

// Chosen for Grid density rather than route coverage: these are the surfaces
// the grid-props codemod touched most, and they render fully on an empty DB.
const ROUTES = [
  '/dashboard',
  '/settings',
  '/system',
  '/diagnostics',
  '/maintenance',
  '/library',
];

async function capture(browser, baseUrl, label) {
  const ctx = await browser.newContext({
    ignoreHTTPSErrors: true,
    viewport: { width: 1440, height: 900 },
  });
  // Suppress the first-run WelcomeWizard. Its completion flag lives in
  // localStorage (see App.tsx), not the database, so a fresh browser context
  // re-opens it on every run. Left open it adds a Dialog Paper and its own
  // column offsets to the probes, which reads as a layout delta between the two
  // builds when it is really just a difference in first-run state.
  await ctx.addInitScript(() => {
    try {
      localStorage.setItem('welcome_wizard_completed', 'true');
    } catch {
      // private-mode localStorage failures are not worth aborting the run
    }
  });
  const page = await ctx.newPage();
  const out = {};
  for (const route of ROUTES) {
    const errs = [];
    const onErr = e => errs.push(e.message);
    page.on('pageerror', onErr);
    await page.goto(baseUrl + route, {
      waitUntil: 'networkidle',
      timeout: 30000,
    });
    await page.waitForTimeout(700); // let MUI mount transitions settle
    await page.screenshot({
      path: `${OUT}/${label}${route.replace(/\//g, '_')}.png`,
      fullPage: true,
    });

    // distinctX is the most diagnostic probe: a collapsed Grid renders every
    // card at the same left offset, which is precisely the v1->v2 failure mode.
    const geo = await page.evaluate(() => {
      const cards = [
        ...document.querySelectorAll('.MuiPaper-root, .MuiCard-root'),
      ];
      const xs = new Set(
        cards.map(c => Math.round(c.getBoundingClientRect().x))
      );
      const doc = document.documentElement;
      return {
        height: Math.round(doc.scrollHeight),
        papers: cards.length,
        distinctX: xs.size,
        hOverflow: doc.scrollWidth > doc.clientWidth + 2,
      };
    });
    out[route] = { ...geo, errs: errs.length };
    page.off('pageerror', onErr);
  }
  await ctx.close();
  return out;
}

mkdirSync(OUT, { recursive: true });
const browser = await chromium.launch();
const before = await capture(browser, BEFORE, 'before');
const after = await capture(browser, AFTER, 'after');
await browser.close();

console.log(
  'route            papers b/a   distinctX b/a  height b/a       hOvf   flags'
);
let flagged = 0;
for (const r of ROUTES) {
  const b = before[r];
  const a = after[r];
  const flags = [];
  if (a.distinctX < b.distinctX) flags.push('COLUMNS-COLLAPSED');
  if (a.papers !== b.papers) flags.push('PAPER-COUNT');
  if (b.height && a.height > b.height * 1.25) flags.push('MUCH-TALLER');
  if (a.hOverflow && !b.hOverflow) flags.push('NEW-H-OVERFLOW');
  if (a.errs) flags.push('PAGE-ERROR');
  if (flags.length) flagged++;
  console.log(
    `${r.padEnd(16)} ${String(b.papers).padStart(3)}/${String(a.papers).padEnd(8)} ` +
      `${String(b.distinctX).padStart(3)}/${String(a.distinctX).padEnd(10)} ` +
      `${String(b.height).padStart(5)}/${String(a.height).padEnd(10)} ` +
      `${String(a.hOverflow).padEnd(6)} ${flags.join(',') || 'ok'}`
  );
}
console.log(
  `\n${flagged} route(s) flagged. PNG pairs in ${OUT} -- inspect them regardless of flags.`
);
