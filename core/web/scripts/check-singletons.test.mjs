// file: core/web/scripts/check-singletons.test.mjs
// version: 0.1.0
// guid: 1c8d4a62-5e07-4b93-9f2a-6d0e3b7c8f15
// last-edited: 2026-11-20

import assert from 'node:assert/strict';
import { test } from 'node:test';
import { findDuplicates } from './check-singletons.mjs';

test('one copy of each singleton passes', () => {
  assert.deepEqual(findDuplicates(['/w/node_modules/react/index.js', '/w/node_modules/react/cjs/react.production.js', '/w/node_modules/@mui/material/Button/Button.js']), []);
});

test('React from two package directories fails', () => {
  const d = findDuplicates(['/w/core/web/node_modules/react/index.js', '/w/web/node_modules/react/index.js']);
  assert.equal(d.length, 1);
  assert.equal(d[0].name, 'react');
});

test('react/compiler-runtime from two paths fails', () => {
  const d = findDuplicates(['/a/node_modules/react/compiler-runtime.js', '/b/node_modules/react/compiler-runtime.js']);
  assert.deepEqual(d.map((x) => x.name), ['react/compiler-runtime']);
});

test('react/compiler-runtime is not filed as react', () => {
  // One React, two runtimes: only the runtime is duplicated.
  const d = findDuplicates(['/w/node_modules/react/index.js', '/a/node_modules/react/compiler-runtime.js', '/b/node_modules/react/compiler-runtime.js']);
  assert.deepEqual(d.map((x) => x.name), ['react/compiler-runtime']);
});
