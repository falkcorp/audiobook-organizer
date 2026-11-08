// file: core/web/packages/api-client/test/client-coverage.test.ts
// version: 1.0.0
// guid: 5b633f88-74c8-4dfb-bd71-840017f98696
// last-edited: 2026-11-08

// `npm run test:client-coverage` (name coined by R1-30): the generated client covers
// every operation of core/openapi/core.json, and regenerating from the committed
// document changes nothing. core.json may hold no operation yet (it has none until
// core's first native route), so neither check asserts a count: a document with
// operations and one without are both covered, the first by generate.test.ts.

import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { describe, expect, it } from 'vitest';
import type { OpenAPI3 } from 'openapi-typescript';
import { generate, operationsOf } from '../generator/generate';
import { coreOperations } from '../src/generated/operations';

// vitest runs with the package as its cwd (npm -w and `cd` both).
const pkg = process.cwd();
const doc = JSON.parse(readFileSync(join(pkg, '..', '..', '..', 'openapi', 'core.json'), 'utf8')) as OpenAPI3;

describe('the generated core client', () => {
  it('has an operations row for every operation of core.json', () => {
    const have = new Set(coreOperations.map((o) => `${o.method} ${o.path}`));
    const missing = operationsOf(doc)
      .map((o) => `${o.method} ${o.path}`)
      .filter((k) => !have.has(k));
    expect(missing).toEqual([]);
    expect(coreOperations).toHaveLength(operationsOf(doc).length);
  });

  it('declares every operationId in the generated types', () => {
    const types = readFileSync(join(pkg, 'src', 'generated', 'core.ts'), 'utf8');
    const missing = operationsOf(doc)
      .map((o) => o.operationId)
      .filter((id): id is string => id !== null && !new RegExp(`\\b${id}\\b`).test(types));
    expect(missing).toEqual([]);
  });

  it('regenerates from the committed core.json with no diff', async () => {
    const files = await generate(doc);
    for (const [name, text] of Object.entries(files)) {
      expect(readFileSync(join(pkg, 'src', 'generated', name), 'utf8'), `src/generated/${name} is stale`).toBe(text);
    }
  });
});
