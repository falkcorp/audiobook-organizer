// file: core/web/packages/api-client/generator/cli.ts
// version: 1.0.0
// guid: 03330667-43fb-480a-a5ed-4ff5c9bec43f
// last-edited: 2026-11-08

// `node generator/cli.ts generate` writes src/generated/ from core/openapi/core.json;
// `check` regenerates in memory and exits 1, naming each file, when the committed
// copy differs (the freshness gate, as scripts/ci/check_generated_types.sh is for
// tygo). Run from the package by its npm scripts.

import { readFileSync, writeFileSync, mkdirSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import type { OpenAPI3 } from 'openapi-typescript';
import { generate } from './generate.ts';

const pkg = join(dirname(fileURLToPath(import.meta.url)), '..');
// core/web/packages/api-client -> core/openapi/core.json
const corePath = join(pkg, '..', '..', '..', 'openapi', 'core.json');
const outDir = join(pkg, 'src', 'generated');

const mode = process.argv[2];
if (mode !== 'generate' && mode !== 'check') {
  console.error('usage: cli.ts generate|check');
  process.exit(2);
}

const doc = JSON.parse(readFileSync(corePath, 'utf8')) as OpenAPI3;
const files = await generate(doc);

if (mode === 'generate') {
  mkdirSync(outDir, { recursive: true });
  for (const [name, text] of Object.entries(files)) writeFileSync(join(outDir, name), text);
  console.log(`generated ${Object.keys(files).join(', ')} from core/openapi/core.json`);
} else {
  const stale = Object.entries(files)
    .filter(([name, text]) => {
      try {
        return readFileSync(join(outDir, name), 'utf8') !== text;
      } catch {
        return true;
      }
    })
    .map(([name]) => name);
  if (stale.length > 0) {
    console.error(`src/generated/${stale.join(', ')} is stale: run 'npm run generate -w @mediacore/api-client'`);
    process.exit(1);
  }
  console.log('generated API client is fresh');
}
