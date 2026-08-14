// file: tests/e2e/global-setup.ts
// version: 1.2.0
// guid: 2f7b4e91-8c05-4d63-a1f2-6b98e0c473da
// last-edited: 2026-08-14

import { execFileSync } from 'child_process';
import { readFileSync, statSync } from 'fs';
import { createRequire } from 'module';
import { dirname, join, sep } from 'path';
import { fileURLToPath } from 'url';
import { E2E_PORT } from './e2e-env';

const __dirname = dirname(fileURLToPath(import.meta.url));
const REPO_ROOT = join(__dirname, '../../..');
const WEB_DIR = join(__dirname, '../..');
// Derived per worktree (H113) — two agents in different worktrees can no
// longer contend for one port. E2E_PORT env var overrides.
const PORT = E2E_PORT;

/**
 * Fail loudly when the suite is about to test a STALE server.
 *
 * `playwright.config.ts` sets `reuseExistingServer: !process.env.CI`, so a
 * local run silently attaches to whatever is already listening on 8484 —
 * including a server built from a bundle that predates the code under test.
 *
 * That is not hypothetical. On 2026-08-08 a local run reported 130 passed /
 * 0 failed against an hours-old server, and the suite was in fact ~50% red;
 * the real number (146 failed of 288) only surfaced once the server was
 * killed first. The whole e2e repair effort exists because that kind of
 * false green went unnoticed for four months.
 *
 * The documented workaround was "remember to run
 * `ps -o lstart -p $(lsof -ti :8484)` first". Relying on a human to remember
 * a manual check is what failed. This does it automatically.
 *
 * How it decides, in two steps — both matter:
 *
 *   1. Is this server one WE started? `webServer` and `globalSetup` overlap, so
 *      a naive check flags Playwright's own server. Anything that started at or
 *      after this process did is ours, and is skipped.
 *   2. Otherwise it is a REUSED server. Compare when it started against when the
 *      build artifacts were last written. A server that started before the
 *      binary or the frontend bundle it is meant to be serving cannot be
 *      running that code.
 *
 * Deliberately fails rather than auto-killing: the process on 8484 may be a
 * dev server someone is using, and silently killing it would trade one
 * surprise for another. The error says exactly what to run.
 *
 * No-ops under CI (nothing to reuse — the config always builds its own) and
 * no-ops when nothing is listening.
 */
export default async function globalSetup(): Promise<void> {
  assertProjectLocalPlaywright();

  if (process.env.CI) return;
  if (process.env.E2E_SKIP_STALE_CHECK === '1') {
    console.warn(
      '[e2e] stale-server check disabled via E2E_SKIP_STALE_CHECK — you are responsible for what is on :8484',
    );
    return;
  }

  const pid = listeningPid();
  if (pid === null) return; // nothing to reuse; webServer will build and start one

  const startedAt = processStartTime(pid);
  if (startedAt === null) return; // cannot determine; do not block the run on a guess

  // Only a server that predates THIS Playwright process can be a reused one.
  //
  // This check is load-bearing and was learned the hard way: the first version
  // of this guard compared the server against the build artifacts alone, and
  // fired on Playwright's OWN freshly-started server (server 13:54:25, binary
  // written 13:54:38) because `webServer` and `globalSetup` overlap. A guard
  // that cries wolf on every clean run gets disabled within a day, which would
  // have left the original hole open while looking like it was covered.
  //
  // `ps -o lstart` is truncated to whole seconds, so a server started 0.9s
  // into our own lifetime reports as earlier than us. The epsilon absorbs that.
  const ourStart = new Date(Date.now() - process.uptime() * 1000);
  const TRUNCATION_EPSILON_MS = 2000;
  if (startedAt.getTime() >= ourStart.getTime() - TRUNCATION_EPSILON_MS) {
    return; // this run started it; nothing to be stale relative to
  }

  // The two things the server is meant to be serving. web/dist is what the Go
  // binary embeds, so a frontend-only change with no rebuild is stale too.
  const artifacts: Array<[string, string]> = [
    ['Go binary', join(REPO_ROOT, 'audiobook-organizer')],
    ['frontend bundle', join(REPO_ROOT, 'web/dist/index.html')],
  ];

  const staler: string[] = [];
  for (const [label, path] of artifacts) {
    let builtAt: Date;
    try {
      builtAt = statSync(path).mtime;
    } catch {
      continue; // not built here (e.g. fresh clone) — nothing to compare against
    }
    if (builtAt.getTime() > startedAt.getTime()) {
      staler.push(
        `  ${label}: built ${builtAt.toISOString()}, but the server started ${startedAt.toISOString()}`,
      );
    }
  }

  if (staler.length === 0) {
    // Freshness passed — now IDENTITY (H113). A sibling worktree's server that
    // just rebuilt passes every timestamp check while serving a bundle with
    // none of this worktree's changes; on 2026-08-11 a gate ran green-path
    // assertions against exactly that. Hashed asset filenames are
    // content-addressed, so "same names as my web/dist" is "same bundle".
    await assertServedBundleIsOurs(pid);
    return;
  }

  throw new Error(
    [
      '',
      `STALE SERVER on 127.0.0.1:${PORT} (pid ${pid}).`,
      '',
      'It started BEFORE the code it is supposed to be serving was built, so this',
      'run would test an old bundle and report a green that means nothing. This is',
      'the exact failure that hid a ~50% red suite on 2026-08-08.',
      '',
      ...staler,
      '',
      'Fix:',
      `  kill -9 ${pid}`,
      '',
      'Then re-run. Playwright will build and start its own server.',
      '',
      'If you really do want to test against what is already running, set',
      'E2E_SKIP_STALE_CHECK=1 — but then the result is only as trustworthy as',
      'that server.',
      '',
    ].join('\n'),
  );
}

/**
 * Fail when the suite is being driven by a Playwright that is not this
 * project's.
 *
 * `npx` searches ancestor node_modules directories. A git worktree is a
 * SIBLING of the main checkout rather than a child, so a worktree created
 * without its own `npm ci` inherits nothing from main and keeps walking — in
 * this repo, all the way to an orphan /Users/<user>/node_modules belonging to
 * an unrelated project. On 2026-08-10 that silently supplied Playwright 1.57.0
 * to a worktree while CI ran the pinned 1.62.1, and nothing anywhere said so.
 *
 * Same shape as the stale-server check above: the run still produces a number,
 * the number just does not mean what it appears to. `npm run test:e2e` also
 * checks this via check-spec-discovery.mjs, but a bare `npx playwright test`
 * skips the npm script entirely — and that is the command people actually type
 * when iterating on one spec. This is the layer that covers it.
 *
 * Runs under CI too. `npm ci` makes it a formality there, but a mismatch would
 * be at its most confusing in a CI log, and the assertion costs nothing.
 */
function assertProjectLocalPlaywright(): void {
  const req = createRequire(join(WEB_DIR, 'package.json'));
  let resolved: string;
  try {
    resolved = req.resolve('@playwright/test/package.json');
  } catch (err) {
    throw new Error(
      `\nCannot resolve @playwright/test from ${WEB_DIR}: ${(err as Error).message}\n` +
        `Run \`npm ci\` there before running the suite.\n`,
      { cause: err },
    );
  }
  if (!resolved.startsWith(WEB_DIR + sep)) {
    throw new Error(
      [
        '',
        'FOREIGN PLAYWRIGHT.',
        '',
        `@playwright/test resolved to:\n  ${resolved}`,
        `which is outside this project (${WEB_DIR}).`,
        '',
        'npx walked up past this worktree and found someone else\'s install, so the',
        'suite would run on a different version than CI does — and would still',
        'report a pass/fail count that looks authoritative.',
        '',
        'Fix:',
        `  cd ${WEB_DIR} && npm ci`,
        '',
      ].join('\n'),
    );
  }
}

/**
 * Assert the REUSED server serves THIS worktree's bundle (identity, not
 * freshness). Compares the hashed asset filenames in the served index.html
 * against the local web/dist/index.html. Only called for reused servers —
 * a server this run started is trivially ours.
 */
async function assertServedBundleIsOurs(pid: number): Promise<void> {
  let local: string;
  try {
    local = readFileSync(join(REPO_ROOT, 'web/dist/index.html'), 'utf8');
  } catch {
    return; // nothing built locally yet; the freshness check already ran
  }
  let served: string;
  try {
    const res = await fetch(`http://127.0.0.1:${PORT}/index.html`);
    if (!res.ok) return; // not serving HTML — webServer will handle it
    served = await res.text();
  } catch {
    return; // unreachable — webServer will start its own
  }
  const assetNames = (html: string): string[] =>
    [...html.matchAll(/assets\/[A-Za-z0-9._-]+-[A-Za-z0-9_-]{8,}\.(?:js|css)/g)]
      .map((m) => m[0])
      .sort();
  const localAssets = assetNames(local);
  const servedAssets = assetNames(served);
  if (localAssets.length === 0) return; // unhashed build; nothing to compare
  if (JSON.stringify(localAssets) === JSON.stringify(servedAssets)) return;
  throw new Error(
    [
      '',
      `FOREIGN BUNDLE on 127.0.0.1:${PORT} (pid ${pid}).`,
      '',
      'The server passes the freshness check but is serving a DIFFERENT build',
      'than this worktree\'s web/dist — almost certainly a sibling worktree\'s',
      'server. Assertions against it say nothing about this worktree\'s code',
      '(this exact false-green shipped-almost on 2026-08-11).',
      '',
      `  local  assets: ${localAssets.join(', ') || '(none)'}`,
      `  served assets: ${servedAssets.join(', ') || '(none)'}`,
      '',
      'Fix:',
      `  kill -9 ${pid}`,
      '',
      'Then re-run; Playwright will build and start this worktree\'s own server.',
      '',
    ].join('\n'),
  );
}

/** PID listening on the e2e port, or null if nothing is. */
function listeningPid(): number | null {
  try {
    const out = execFileSync('lsof', ['-ti', `:${PORT}`], {
      encoding: 'utf8',
      stdio: ['ignore', 'pipe', 'ignore'],
    }).trim();
    if (!out) return null;
    // Multiple PIDs can share a port (forked workers). The first is enough:
    // they are the same server, so any of them dates the run.
    const pid = Number.parseInt(out.split('\n')[0], 10);
    return Number.isFinite(pid) ? pid : null;
  } catch {
    return null; // lsof missing or nothing listening — both mean "no reuse"
  }
}

/**
 * When the process started, or null if it cannot be determined.
 *
 * `ps -o lstart=` is the portable-enough answer on macOS and Linux, and it is
 * the same command the handoff notes told people to run by hand.
 */
function processStartTime(pid: number): Date | null {
  try {
    const raw = execFileSync('ps', ['-o', 'lstart=', '-p', String(pid)], {
      encoding: 'utf8',
      stdio: ['ignore', 'pipe', 'ignore'],
    }).trim();
    if (!raw) return null;
    const parsed = new Date(raw);
    return Number.isNaN(parsed.getTime()) ? null : parsed;
  } catch {
    return null;
  }
}
