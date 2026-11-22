// file: web/src/test/e2eEventStreamGuard.test.ts
// version: 0.1.0
// guid: 8b2e6d41-3a97-4c05-b1f8-d04a7e9c5623
// last-edited: 2026-11-22

// Guard for the e2e specs' event streams (F6-03 flip). The app's main stream is a fetch of
// GET /api/core/v1/events, which Playwright's route.fulfill cannot hold open and a
// window.EventSource mock never sees, so a mocked page that is not given
// web/tests/e2e/utils/mockCoreEvents.ts would run a real stream against nothing: a
// refusal loop and a permanent "Connection lost". The mock lives in the shared helper that
// every mocked page goes through, so this reads the e2e sources (vitest excludes tests/e2e
// and tsc includes only src, so the check lives here, like mockCoreEvents.test.ts) and holds
// three things:
//  1. mockEventSource, which setupMockApi and setup-modes call, installs mockCoreEvents;
//  2. no spec or helper answers the events path itself with page.route, which would bypass
//     the shim's frame protocol and its escape guard;
//  3. nothing under tests/e2e or src names the retired /api/events stream.

import { readdirSync, readFileSync, statSync } from 'node:fs';
import { join, relative } from 'node:path';
import { describe, expect, it } from 'vitest';

const WEB = join(__dirname, '..', '..');
const E2E = join(WEB, 'tests', 'e2e');
const SRC = join(WEB, 'src');

function sourcesUnder(dir: string, out: string[] = []): string[] {
  for (const name of readdirSync(dir)) {
    if (name === 'node_modules' || name === 'generated') continue;
    const path = join(dir, name);
    if (statSync(path).isDirectory()) sourcesUnder(path, out);
    else if (/\.(ts|tsx|mjs)$/.test(name)) out.push(path);
  }
  return out;
}

const rel = (p: string) => relative(WEB, p);
const read = (p: string) => readFileSync(p, 'utf8');

/** The body of `export async function <name>` up to the next top-level export. */
function bodyOf(source: string, name: string): string {
  const start = source.indexOf(`export async function ${name}(`);
  expect(start, `${name} not found`).toBeGreaterThanOrEqual(0);
  const next = source.indexOf('\nexport ', start + 1);
  return source.slice(start, next === -1 ? undefined : next);
}

describe('e2e event stream mock', () => {
  it('is installed by mockEventSource, which every mocked page goes through', () => {
    const helpers = read(join(E2E, 'utils', 'test-helpers.ts'));
    expect(bodyOf(helpers, 'mockEventSource')).toContain('await mockCoreEvents(page)');
    expect(bodyOf(helpers, 'setupMockApi')).toContain('await mockEventSource(page)');
    expect(read(join(E2E, 'utils', 'setup-modes.ts'))).toContain('await mockEventSource(page)');
  });

  it('is the only thing that answers /api/core/v1/events in an e2e file', () => {
    const offenders = sourcesUnder(E2E)
      .filter((p) => !p.endsWith(join('utils', 'mockCoreEvents.ts')))
      .filter((p) => /route\([^)]*\/api\/core\/v1\/events/.test(read(p)))
      .map(rel);
    expect(offenders).toEqual([]);
  });

  it('leaves no reader of the retired /api/events stream', () => {
    // A path that is exactly /api/events, not /api/core/v1/events, /api/v1/events or a prefix of one.
    const retired = /['"`]\/api\/events(?![\w/-])/;
    const offenders = [...sourcesUnder(E2E), ...sourcesUnder(SRC)]
      .filter((p) => !p.endsWith('e2eEventStreamGuard.test.ts'))
      .filter((p) => retired.test(read(p)))
      .map(rel);
    expect(offenders).toEqual([]);
  });
});
