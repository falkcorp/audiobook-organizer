// file: core/web/packages/shell/test/sanitizeReturn.test.ts
// version: 0.1.0
// guid: 16560de2-1f3f-4543-a3c2-2f4484774f1e
// last-edited: 2026-11-22

// Guards the two client-side navigation sinks (Login.tsx's location.state.from,
// BookDetail.tsx's sessionStorage['library_return_url']) and the shell's own
// ?return= (safeReturn). sanitizeReturn is the one client-side implementation;
// core's Go oidc.SanitizeReturn applies the same rules, and both are checked
// against the shared payload table core/auth/oidc/testdata/return_payloads.json.
// Rejected input answers '' so callers fall back to a known-safe default.

import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { describe, expect, it } from 'vitest';
import { safeReturn, sanitizeReturn } from '../src/auth/safeReturn';

describe('sanitizeReturn — rejects open-redirect payloads', () => {
  it.each([
    ['empty string', ''],
    ['null', null],
    ['undefined', undefined],
    ['protocol-relative //', '//evil.com'],
    ['protocol-relative // with path', '//evil.com/phish'],
    ['backslash variant /\\evil.com', '/\\evil.com'],
    ['double backslash \\\\evil.com', '\\\\evil.com'],
    ['backslash mid-path', '/dashboard\\evil.com'],
    ['absolute https URL', 'https://evil.com'],
    ['absolute http URL', 'http://evil.com'],
    ['protocol-relative uppercase scheme host lookalike', 'https://yoursite.evil.com'],
    ['host-prefix lookalike without scheme', 'yoursite.evil.com/dashboard'],
    ['javascript: URL', 'javascript:alert(1)'],
    ['data: URL', 'data:text/html,<script>alert(1)</script>'],
    ['encoded protocol-relative %2f%2f', '%2f%2fevil.com'],
    ['encoded protocol-relative mixed case %2F%2F', '%2F%2Fevil.com'],
    ['bare host, no leading slash', 'evil.com'],
    ['bare path, no leading slash', 'dashboard'],
  ])('rejects %s (%p) → ""', (_label, input) => {
    expect(sanitizeReturn(input)).toBe('');
  });
});

describe('sanitizeReturn — accepts ordinary internal paths (anti-over-suppression)', () => {
  it.each([
    ['/dashboard', '/dashboard'],
    ['/library', '/library'],
    ['/book/abc123', '/book/abc123'],
    ['/fingerprints?x=1', '/fingerprints?x=1'],
    ['a path with query and fragment', '/library?tag=metadata#section'],
    ['the root path alone', '/'],
  ])('passes %s through unchanged', (_label, input) => {
    expect(sanitizeReturn(input)).toBe(input);
  });
});

// The shared table (core/auth/oidc/testdata/return_payloads.json): the Go
// implementation's tests read the same file, so a payload added there is
// checked on both sides.
const table = JSON.parse(
  readFileSync(resolve(__dirname, '../../../../auth/oidc/testdata/return_payloads.json'), 'utf8'),
) as { reject: string[]; accept: string[] };

describe('sanitizeReturn — the shared return-path payload table', () => {
  it('has both lists', () => {
    expect(table.reject.length).toBeGreaterThan(0);
    expect(table.accept.length).toBeGreaterThan(0);
  });
  it.each(table.reject.map((p) => [p]))('refuses %j', (input) => {
    expect(sanitizeReturn(input)).toBe('');
    expect(safeReturn(input)).toBe('/');
  });
  it.each(table.accept.map((p) => [p]))('keeps %j unchanged', (input) => {
    expect(sanitizeReturn(input)).toBe(input);
    expect(safeReturn(input)).toBe(input);
  });
});

describe('sanitizeReturn — dot segments that resolve to a scheme-relative URL', () => {
  it.each([['/.//x'], ['/%2e//x'], ['/./%2F/x'], ['/a/..//x']])('refuses %j', (input) => {
    expect(sanitizeReturn(input)).toBe('');
  });
});
