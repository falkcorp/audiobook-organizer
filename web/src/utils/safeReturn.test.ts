// file: web/src/utils/safeReturn.test.ts
// version: 1.0.0
// guid: 4f1a2b3c-5d6e-7f80-9a1b-2c3d4e5f6a7b
// last-edited: 2026-08-21

// Guards the two client-side navigation sinks (Login.tsx's location.state.from,
// BookDetail.tsx's sessionStorage['library_return_url']) that previously handed
// an attacker-controlled string straight to react-router's navigate() with no
// validation. This is a TypeScript port of sanitizeReturn in
// internal/server/handlers/oauth_login.go: same allow-list (a single leading
// '/', no second leading '/' or '\', no backslash anywhere), same reject-to-''
// contract so callers fall back to a known-safe default instead of throwing or
// leaving the user stuck.

import { describe, expect, it } from 'vitest';
import { sanitizeReturn } from './safeReturn';

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
