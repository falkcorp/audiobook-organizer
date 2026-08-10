#!/usr/bin/env node
// file: web/tests/e2e/check-spec-discovery.mjs
// version: 1.0.0
// guid: 5b1c93a7-6e42-4d08-9f31-2a7c8e05b4d6
// last-edited: 2026-08-10

/**
 * Fails if any e2e spec file on disk contributes no runnable test.
 *
 * WHY THIS EXISTS
 *
 * Six spec files were disabled by accident and nobody noticed for four months.
 * Nothing was red the whole time — that is the point. A spec file that stops
 * being discovered does not fail the suite, it shrinks it, and Playwright exits
 * 0 either way. Every layer was silent:
 *
 *   - playwright.config.ts  — `testIgnore` excludes files with no output
 *   - the CI job            — gates on exit code, which stays 0
 *   - `make test-e2e`       — same, locally
 *
 * A pass count is not a coverage claim unless something asserts what SHOULD
 * have run. This is that assertion.
 *
 * WHAT IT CHECKS
 *
 *   1. Every non-demo, non-interactive *.spec.ts on disk appears in Playwright's
 *      own discovery output. Catches: renames that break the glob, a stray
 *      `testIgnore` entry, a file that stopped matching *.spec.ts.
 *   2. Every such file contributes at least one NON-skipped test. Catches a
 *      whole-file `test.describe.skip(...)`, which is the same incident wearing
 *      a different hat — the file is discovered, and still runs nothing.
 *
 * Deliberately NOT a total-count baseline. A committed "expect >= N tests"
 * number has to be bumped on every PR that adds a test, which trains people to
 * bump it without reading it — and a guard people edit reflexively is not a
 * guard. Per-file presence is self-maintaining: adding a spec file needs no
 * update here, and deleting one is a visible, reviewable diff.
 *
 * Individual skipped TESTS are reported but do not fail: `test.skip` on one
 * case with a written reason is a legitimate way to park a missing feature.
 * Silently losing a whole FILE is not.
 *
 * Usage:  node tests/e2e/check-spec-discovery.mjs     (run from web/)
 */

import { execFileSync } from 'node:child_process';
import { readdirSync, statSync } from 'node:fs';
import { join, relative } from 'node:path';
import { fileURLToPath } from 'node:url';
import { dirname } from 'node:path';

const E2E_DIR = dirname(fileURLToPath(import.meta.url));
const WEB_DIR = join(E2E_DIR, '../..');
const CONFIG = 'tests/e2e/playwright.config.ts';

// Mirrors the `testIgnore` in playwright.config.ts's chromium/webkit projects.
// If that list changes, this must change with it — a mismatch here shows up as
// a loud failure naming the file, not as silent under-coverage.
const EXCLUDED = [/^demo-/, /^interactive-/];

function specFilesOnDisk(dir) {
  const found = [];
  for (const entry of readdirSync(dir)) {
    const full = join(dir, entry);
    if (statSync(full).isDirectory()) {
      found.push(...specFilesOnDisk(full));
    } else if (entry.endsWith('.spec.ts')) {
      found.push(relative(E2E_DIR, full));
    }
  }
  return found;
}

function collectSpecs(suite, out) {
  for (const spec of suite.specs ?? []) out.push(spec);
  for (const child of suite.suites ?? []) collectSpecs(child, out);
}

const onDisk = specFilesOnDisk(E2E_DIR)
  .filter((f) => !EXCLUDED.some((re) => re.test(f)))
  .sort();

if (onDisk.length === 0) {
  console.error('FAIL: no *.spec.ts files found under tests/e2e — glob is wrong.');
  process.exit(1);
}

// One project is enough: discovery is per-file and both projects share the
// same testIgnore. Running both would only double the work.
const raw = execFileSync(
  'npx',
  ['playwright', 'test', '-c', CONFIG, '--project=chromium', '--list', '--reporter=json'],
  { cwd: WEB_DIR, encoding: 'utf8', maxBuffer: 64 * 1024 * 1024, stdio: ['ignore', 'pipe', 'ignore'] }
);

const report = JSON.parse(raw);
const specs = [];
for (const suite of report.suites ?? []) collectSpecs(suite, specs);

/** file -> { total, skipped } */
const byFile = new Map();
for (const spec of specs) {
  const entry = byFile.get(spec.file) ?? { total: 0, skipped: 0 };
  entry.total += 1;
  const skipped = (spec.tests ?? []).every((t) =>
    (t.annotations ?? []).some((a) => a.type === 'skip' || a.type === 'fixme')
  );
  if (skipped) entry.skipped += 1;
  byFile.set(spec.file, entry);
}

const missing = [];
const allSkipped = [];
for (const file of onDisk) {
  const entry = byFile.get(file);
  if (!entry || entry.total === 0) {
    missing.push(file);
  } else if (entry.total === entry.skipped) {
    allSkipped.push(file);
  }
}

const totalSkipped = [...byFile.values()].reduce((n, e) => n + e.skipped, 0);
console.log(
  `spec-discovery: ${onDisk.length} files on disk, ${specs.length} tests discovered ` +
    `(${totalSkipped} skipped) on chromium`
);

if (missing.length === 0 && allSkipped.length === 0) {
  if (totalSkipped > 0) {
    const detail = [...byFile.entries()]
      .filter(([, e]) => e.skipped > 0)
      .map(([f, e]) => `${f} (${e.skipped})`)
      .join(', ');
    console.log(`spec-discovery: skipped tests live in ${detail}`);
  }
  console.log('spec-discovery: OK — every spec file contributes a runnable test.');
  process.exit(0);
}

if (missing.length > 0) {
  console.error(
    `\nFAIL: ${missing.length} spec file(s) exist on disk but Playwright discovered ` +
      `no tests in them:\n` +
      missing.map((f) => `  - ${f}`).join('\n') +
      `\n\nThe suite would still pass with these silently not running — that is the ` +
      `four-month outage this check exists to prevent. Likely causes: a testIgnore ` +
      `entry in ${CONFIG}, a rename that no longer matches *.spec.ts, or a syntax ` +
      `error that made the file yield no tests.`
  );
}

if (allSkipped.length > 0) {
  console.error(
    `\nFAIL: ${allSkipped.length} spec file(s) are discovered but every test in them ` +
      `is skipped:\n` +
      allSkipped.map((f) => `  - ${f}`).join('\n') +
      `\n\nA whole file skipped runs exactly as much as a file that was never ` +
      `discovered. Skip individual tests with a written reason instead, or delete ` +
      `the file deliberately in a reviewable diff.`
  );
}

process.exit(1);
