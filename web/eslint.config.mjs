// file: web/eslint.config.mjs
// version: 1.5.0
// guid: 456e7890-b12c-34d5-c678-901234567890
// last-edited: 2026-10-10

import js from '@eslint/js';
import tseslint from 'typescript-eslint';
import reactHooks from 'eslint-plugin-react-hooks';
import reactRefresh from 'eslint-plugin-react-refresh';
import globals from 'globals';
import { PERMISSION_LITERAL_PENDING_FILES, permissionLiteralRestrictions } from './eslint-rules/permission-literal.mjs';

export default tseslint.config(
  {
    ignores: ['dist/**', 'node_modules/**', 'tests/e2e/playwright-report/**', 'tests/e2e/test-results/**'],
  },
  js.configs.recommended,
  {
    files: ['**/*.{ts,tsx}'],
    extends: [...tseslint.configs.recommended],
    languageOptions: {
      globals: {
        ...globals.browser,
        ...globals.es2020,
        ...globals.node,
        React: 'readonly',
        RequestInit: 'readonly',
        HeadersInit: 'readonly',
        process: 'readonly',
        __dirname: 'readonly',
        global: 'readonly',
      },
    },
    plugins: {
      'react-hooks': reactHooks,
      'react-refresh': reactRefresh,
    },
    rules: {
      // eslint-plugin-react-hooks 7.x's `recommended` preset carries the React
      // Compiler rules (immutability, purity, refs, static-components,
      // set-state-in-effect, ...). They are enabled here at `warn`, not `error`:
      // the compiler is on (see vite.config.ts) and bails out of components it
      // cannot prove safe rather than miscompiling them, so a violation costs
      // optimization, not correctness -- it should not fail the build. Reporting
      // them keeps the backlog visible instead of letting it grow silently.
      //
      // Measured 2026-08-19 against babel-plugin-react-compiler's own logger:
      // these rules account for ~5% of actual bailouts. 93% are `try/finally`
      // and friends, which the compiler simply does not lower yet -- no lint
      // rule reports those. See docs/react-compiler-adoption.md.
      ...Object.fromEntries(
        Object.keys(reactHooks.configs.recommended.rules).map((rule) => [rule, 'warn'])
      ),
      // The two rules that predate the compiler stay at their original severity.
      'react-hooks/rules-of-hooks': 'error',
      'react-hooks/exhaustive-deps': 'warn',
      'react-refresh/only-export-components': [
        'warn',
        { allowConstantExport: true },
      ],
      '@typescript-eslint/no-explicit-any': 'warn',
      '@typescript-eslint/no-unused-vars': [
        'error',
        {
          argsIgnorePattern: '^_',
          varsIgnorePattern: '^_',
          caughtErrorsIgnorePattern: '^_',
        },
      ],
      'no-undef': 'off',
    },
  },
  {
    // Every browser request goes through apiFetch (src/utils/apiFetch.ts) so an
    // expired Cloudflare Access session surfaces as ApiAuthRedirectError instead
    // of a 200 login page that callers read as success. apiFetch itself and the
    // tests (which stub globalThis.fetch) are the only places allowed to touch
    // the raw API.
    files: ['src/**/*.{ts,tsx}'],
    ignores: ['src/utils/apiFetch.ts', 'src/**/*.test.{ts,tsx}'],
    rules: {
      'no-restricted-globals': [
        'error',
        {
          name: 'fetch',
          message: 'Use apiFetch from utils/apiFetch (or a services/* function) instead of raw fetch.',
        },
      ],
      'no-restricted-properties': [
        'error',
        ...['window', 'globalThis', 'self'].map((object) => ({
          object,
          property: 'fetch',
          message: 'Use apiFetch from utils/apiFetch (or a services/* function) instead of raw fetch.',
        })),
      ],
    },
  },
  {
    // 07 section 7.18 (FE-11, FE2-17): no string literal in a permission
    // position (a usePermission/useAction/hasPermission argument, a permission
    // or *_PERMISSION property or constant, an API-key scope list) unless it
    // comes from manifest.permissions or the generated client. The selectors
    // and their fixtures live in eslint-rules/permission-literal.mjs.
    //
    // PERMISSION_LITERAL_PENDING_FILES are the five files whose literals
    // (ABO's legacy permission constants and APIKeysTab's scope list) go
    // through the generated client in the SPA's switch; that change deletes
    // the list. The rule is not weakened for them beyond that exact list:
    // eslint-rules/permission-literal.test.ts fails if the list names a file
    // that is clean or misses one that is not.
    files: ['src/**/*.{ts,tsx}'],
    ignores: ['src/**/*.test.{ts,tsx}', ...PERMISSION_LITERAL_PENDING_FILES],
    rules: {
      'no-restricted-syntax': ['error', ...permissionLiteralRestrictions],
    },
  },
  {
    files: ['**/*.{js,mjs,cjs}'],
    languageOptions: {
      globals: {
        ...globals.node,
        process: 'readonly',
        __dirname: 'readonly',
      },
    },
    rules: {
      'no-undef': 'off',
    },
  },
);
