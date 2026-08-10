#!/usr/bin/env node
// file: web/tests/e2e/check-spec-discovery.mjs
// version: 1.2.0
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
 *   1. Every *.spec.ts on disk is discovered by at least one project. Catches:
 *      renames that break the glob, a stray `testIgnore` entry, a file that
 *      stopped matching *.spec.ts.
 *   2. Every file the GATE project (chromium — what the PR run executes)
 *      discovers contributes at least one NON-skipped test. Catches a
 *      whole-file `test.describe.skip(...)`, which is the same incident wearing
 *      a different hat: the file is discovered, and still runs nothing.
 *
 * NO HARD-CODED EXCLUSION LIST — that was version 1.0.0 and it was wrong.
 *
 * v1.0.0 hard-coded `[/^demo-/, /^interactive-/]` to mirror the `testIgnore`
 * in the chromium/webkit projects. That reintroduces the very failure this
 * script exists to catch, one step removed: widen `testIgnore` and the check
 * goes red, and the cheapest way to make it green again is to widen the copy
 * here — at which point a real spec file is silently uncovered and the guard
 * says OK.
 *
 * So discovery is run WITHOUT `--project`, and the union across all three
 * projects has to cover every file on disk. This works because the projects
 * partition the files rather than merely excluding some: chromium/webkit
 * `testIgnore` the `demo-*.spec.ts` and `interactive-*.spec.ts` globs, and
 * `chromium-record` carries the exact inverse as its `testMatch`. A file
 * dropped from the CI projects therefore has to be picked up by the record
 * project or it lands in `missing` — and moving a functional spec there takes
 * two deliberate config edits, both visible in a diff.
 *
 * Files outside the gate must be named in `GATE_EXEMPT` below. Merely printing
 * them was not enough: measured against this script, moving a functional spec
 * into the record project's `testMatch` left it undiscovered by CI and still
 * exited 0, which is the original incident wearing a third hat.
 *
 * `GATE_EXEMPT` is an allow-list of INTENT, not a mirror of config — that is
 * what makes it different from the v1.0.0 list it replaces. Nothing in
 * playwright.config.ts states "this file does not need to run in CI"; only a
 * human can. So a file leaving the gate stays red until someone writes its
 * name here, and that diff says exactly what it means. It is also checked in
 * both directions: a stale entry — one that is back in the gate, or gone from
 * disk — fails too, so the list cannot quietly rot.
 *
 * Side effect of dropping `--project`: the demo/interactive specs are now
 * PARSED at discovery time (not run — `--list` only loads files). They were not
 * before, so a syntax error in a demo spec will now fail this check. That is
 * arguably a small bonus, but it is a behaviour change, not an accident.
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
 * The skip COUNT printed here will read lower than the suite's own. `--list`
 * sees static `test.skip(...)` / `test.fixme(...)` annotations; it cannot see a
 * conditional `test.skip(cond, 'reason')` that decides at run time (auth-flow
 * has two, gated on endpoint availability). Those list as runnable and report
 * as skipped. That is a different question, not a discrepancy — this script
 * asks what CAN run, the suite reports what DID.
 *
 * Usage:  node tests/e2e/check-spec-discovery.mjs     (run from web/)
 */

import { execFileSync } from 'node:child_process';
import { readdirSync, statSync } from 'node:fs';
import { join, relative, sep } from 'node:path';
import { createRequire } from 'node:module';
import { fileURLToPath } from 'node:url';
import { dirname } from 'node:path';

const E2E_DIR = dirname(fileURLToPath(import.meta.url));
const WEB_DIR = join(E2E_DIR, '../..');
const CONFIG = 'tests/e2e/playwright.config.ts';

// The project the blocking PR run executes. Webkit adds a second engine
// nightly but discovers the same files, so gating on chromium alone is
// sufficient and halves the work.
const GATE_PROJECT = 'chromium';

// Spec files that legitimately do not run in CI: opt-in demo recordings driven
// by `npm run test:e2e:demo`, which need a live server with real media.
// Checked in BOTH directions — see the header. If you are here because the
// check went red, adding a name is a claim that the file does not need to run
// in CI. Make that claim deliberately or not at all.
const GATE_EXEMPT = new Set([
  'demo-full-workflow.spec.ts',
  'interactive-import-workflow.spec.ts',
]);

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

// Assert we are asking the PROJECT'S Playwright, not whatever `npx` found by
// walking up the tree. `npx` searches ancestor node_modules directories, and a
// git worktree is a SIBLING of the main checkout rather than a child — so a
// worktree missing its own `npm ci` inherits nothing from main and keeps
// walking, potentially all the way to an orphan ~/node_modules. That is not
// hypothetical: this script's own negative controls were first run under a
// stray Playwright 1.57.0 from $HOME instead of the pinned 1.62.1, and nothing
// anywhere said so. A guard verified with the wrong instrument is not verified.
const require = createRequire(join(WEB_DIR, 'package.json'));
let playwrightVersion;
try {
  const pkgPath = require.resolve('@playwright/test/package.json');
  if (!pkgPath.startsWith(WEB_DIR + sep)) {
    console.error(
      `FAIL: @playwright/test resolved to ${pkgPath}, which is outside this ` +
        `project.\nRun \`npm ci\` in ${WEB_DIR} — otherwise discovery is being ` +
        `reported by a different Playwright than CI runs.`
    );
    process.exit(1);
  }
  playwrightVersion = require(pkgPath).version;
} catch (err) {
  console.error(
    `FAIL: could not resolve @playwright/test from ${WEB_DIR} (${err.message}).\n` +
      `Run \`npm ci\` there first.`
  );
  process.exit(1);
}

const onDisk = specFilesOnDisk(E2E_DIR).sort();

if (onDisk.length === 0) {
  console.error('FAIL: no *.spec.ts files found under tests/e2e — glob is wrong.');
  process.exit(1);
}

// No `--project` filter: the union across every project is what makes the
// exclusion list unnecessary. See the header.
const raw = execFileSync(
  'npx',
  ['playwright', 'test', '-c', CONFIG, '--list', '--reporter=json'],
  { cwd: WEB_DIR, encoding: 'utf8', maxBuffer: 64 * 1024 * 1024, stdio: ['ignore', 'pipe', 'ignore'] }
);

const report = JSON.parse(raw);
const specs = [];
for (const suite of report.suites ?? []) collectSpecs(suite, specs);

/** file -> projectName -> { total, skipped } */
const byFile = new Map();
for (const spec of specs) {
  let perProject = byFile.get(spec.file);
  if (!perProject) {
    perProject = new Map();
    byFile.set(spec.file, perProject);
  }
  // One `tests` entry per project, so this counts per project exactly — no
  // every/some judgement call about what "the file is skipped" means across
  // engines.
  for (const t of spec.tests ?? []) {
    const entry = perProject.get(t.projectName) ?? { total: 0, skipped: 0 };
    entry.total += 1;
    if ((t.annotations ?? []).some((a) => a.type === 'skip' || a.type === 'fixme')) {
      entry.skipped += 1;
    }
    perProject.set(t.projectName, entry);
  }
}

const missing = [];
const allSkipped = [];
const outsideGate = [];
let gateTotal = 0;
let gateSkipped = 0;

for (const file of onDisk) {
  const perProject = byFile.get(file);
  if (!perProject || perProject.size === 0) {
    missing.push(file);
    continue;
  }
  const gate = perProject.get(GATE_PROJECT);
  if (!gate || gate.total === 0) {
    outsideGate.push({ file, projects: [...perProject.keys()] });
    continue;
  }
  gateTotal += gate.total;
  gateSkipped += gate.skipped;
  if (gate.total === gate.skipped) allSkipped.push(file);
}

// Bidirectional: unexpected departures from the gate fail, and so do stale
// allow-list entries, so the list cannot outlive the reason it was written.
const unexpectedlyOutside = outsideGate.filter((o) => !GATE_EXEMPT.has(o.file));
const staleExempt = [...GATE_EXEMPT].filter(
  (f) => !outsideGate.some((o) => o.file === f)
);

console.log(
  `spec-discovery: playwright ${playwrightVersion} (project-local); ` +
    `${onDisk.length} spec files on disk; ` +
    `${gateTotal} tests on ${GATE_PROJECT} (${gateSkipped} statically skipped); ` +
    `${outsideGate.length} exempt`
);

if (
  missing.length === 0 &&
  allSkipped.length === 0 &&
  unexpectedlyOutside.length === 0 &&
  staleExempt.length === 0
) {
  if (gateSkipped > 0) {
    const detail = [...byFile.entries()]
      .map(([f, p]) => [f, p.get(GATE_PROJECT)])
      .filter(([, e]) => e && e.skipped > 0)
      .map(([f, e]) => `${f} (${e.skipped})`)
      .join(', ');
    console.log(`spec-discovery: statically skipped tests live in ${detail}`);
  }
  console.log('spec-discovery: OK — every spec file contributes a runnable test.');
  process.exit(0);
}

if (missing.length > 0) {
  console.error(
    `\nFAIL: ${missing.length} spec file(s) exist on disk but NO project discovered ` +
      `any test in them:\n` +
      missing.map((f) => `  - ${f}`).join('\n') +
      `\n\nThe suite would still pass with these silently not running — that is the ` +
      `four-month outage this check exists to prevent. Likely causes: a testIgnore ` +
      `entry in ${CONFIG}, a rename that no longer matches *.spec.ts, or a syntax ` +
      `error that made the file yield no tests.\n` +
      `Do NOT "fix" this by excluding the file here; this script has no exclusion ` +
      `list on purpose.`
  );
}

if (unexpectedlyOutside.length > 0) {
  console.error(
    `\nFAIL: ${unexpectedlyOutside.length} spec file(s) are discovered, but not by ` +
      `the ${GATE_PROJECT} project that CI actually runs:\n` +
      unexpectedlyOutside
        .map((o) => `  - ${o.file} (only in: ${o.projects.join(', ')})`)
        .join('\n') +
      `\n\nThese never execute in CI. A file moved into the opt-in demo-recording ` +
      `project is as absent from the gate as one that was deleted. Either put it ` +
      `back in the chromium/webkit projects, or add it to GATE_EXEMPT in this file ` +
      `— which is a deliberate claim that it does not need to run in CI.`
  );
}

if (staleExempt.length > 0) {
  console.error(
    `\nFAIL: ${staleExempt.length} GATE_EXEMPT entr(ies) no longer describe ` +
      `anything:\n` +
      staleExempt.map((f) => `  - ${f}`).join('\n') +
      `\n\nEach is either back inside the ${GATE_PROJECT} gate or gone from disk. ` +
      `Remove it, so the allow-list keeps meaning what it says.`
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
