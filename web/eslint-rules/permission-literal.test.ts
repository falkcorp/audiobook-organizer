// file: web/eslint-rules/permission-literal.test.ts
// version: 0.1.0
// guid: 8b2f6c14-5e97-4a03-b1d8-0c63e7a94f25
// last-edited: 2026-11-21

// The permission-literal rule of 07 section 7.18, run through ESLint's own
// engine: the fixtures below are source strings, so they sit outside `eslint .`
// and outside the tsc include, and the rule is proved without a file that would
// fail the lint it exists to run.

import { ESLint, Linter } from 'eslint';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import tseslint from 'typescript-eslint';
import { describe, expect, it } from 'vitest';
import { permissionLiteralRestrictions } from './permission-literal.mjs';

const RULE = 'no-restricted-syntax';

function lint(code: string): { line: number; ruleId: string | null }[] {
  const config = [
    {
      files: ['**/*.tsx'],
      languageOptions: { parser: tseslint.parser, parserOptions: { ecmaFeatures: { jsx: true } } },
      rules: { [RULE]: ['error', ...permissionLiteralRestrictions] },
    },
  ];
  return new Linter()
    .verify(code, config as never, { filename: 'fixture.tsx' })
    .map((m) => ({ line: m.line, ruleId: m.ruleId }));
}

describe('permission-literal rule: what it refuses', () => {
  it.each([
    ['a string literal passed to usePermission', "usePermission('library.delete');"],
    ['a string literal passed to useAction', "useAction('library.delete');"],
    ['a string literal passed to hasPermission', "me.hasPermission('library.delete');"],
    ['a string literal passed to HEAD\'s useHasPermission', "useHasPermission('library.delete');"],
    ['a string literal passed to HEAD\'s holdsPermission', "holdsPermission(auth, 'library.delete');"],
    ['a template literal with no substitution', 'usePermission(`library.delete`);'],
    ['a *_PERMISSION constant', "export const LIBRARY_DELETE_PERMISSION = 'library.delete';"],
    ['a permission property', "const gate = { permission: 'library.delete' };"],
    ['a *_PERMISSION property', "const gate = { THROTTLE_PERMISSION: 'scan.trigger' };"],
    ['a permission JSX attribute', '<Gate permission="library.delete" />;'],
    ['a braced permission JSX attribute', "<Gate permission={'library.delete'} />;"],
    ['an API-key scope list constant', "const ALL_SCOPES = ['library.view', 'users.manage'];"],
    ['a scopes property', "createKey({ name: 'k', scopes: ['library.view'] });"],
  ])('%s', (_name, code) => {
    const found = lint(code);
    expect(found.length).toBeGreaterThan(0);
    expect(found.every((m) => m.ruleId === RULE)).toBe(true);
    expect(found.every((m) => m.line === 1)).toBe(true);
  });

  it('reports every literal of a scope list on its own line', () => {
    const found = lint(["const ALL_SCOPES = [", "  'library.view',", "  'users.manage',", '];'].join('\n'));
    expect(found.map((m) => m.line)).toEqual([2, 3]);
  });
});

describe('permission-literal rule: what it lets through', () => {
  it.each([
    ['an imported identifier', 'usePermission(LIBRARY_DELETE);'],
    ['a manifest member', 'usePermission(manifest.permissions.libraryDelete);'],
    ['a generated-client member', 'hasPermission(client.permissions.libraryDelete);'],
    ['an identifier constant', 'export const LIBRARY_DELETE_PERMISSION = manifest.permissions.libraryDelete;'],
    ['a scope list of identifiers', 'const ALL_SCOPES = [manifest.permissions.libraryView];'],
    ['a literal under another name', "const palette = { primary: 'grey.500' }; call('library.delete');"],
    ['a number in a permission position', 'usePermission(1);'],
    ['a regexp in a permission property', 'const gate = { permission: /x/ };'],
    ['a similarly named attribute', '<Gate permissionHint="library.delete" />;'],
  ])('%s', (_name, code) => {
    expect(lint(code)).toEqual([]);
  });
});

describe('permission-literal rule over web/src', () => {
  const webRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');

  async function findings(ignores: string[]): Promise<string[]> {
    const eslint = new ESLint({
      cwd: webRoot,
      overrideConfigFile: true,
      overrideConfig: [
        {
          files: ['src/**/*.{ts,tsx}'],
          ignores,
          languageOptions: { parser: tseslint.parser, parserOptions: { ecmaFeatures: { jsx: true } } },
          linterOptions: { noInlineConfig: true, reportUnusedDisableDirectives: 'off' },
          rules: { [RULE]: ['error', ...permissionLiteralRestrictions] },
        },
      ] as never,
    });
    const results = await eslint.lintFiles(['src']);
    return results
      .filter((r) => r.messages.some((m) => m.ruleId === RULE))
      .map((r) => path.relative(webRoot, r.filePath))
      .sort();
  }

  it('finds nothing in any hand-written file: no pending list is left (F4-08 B8)', async () => {
    expect(await findings(['src/**/*.test.{ts,tsx}', 'src/generated/**'])).toEqual([]);
  }, 120_000);

  it('would find the generated permissions file, so its exemption is the only one and is load-bearing', async () => {
    expect(await findings(['src/**/*.test.{ts,tsx}'])).toEqual(['src/generated/permissions.ts']);
  }, 120_000);
});
