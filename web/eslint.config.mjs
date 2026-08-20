// file: web/eslint.config.mjs
// version: 1.4.0
// guid: 456e7890-b12c-34d5-c678-901234567890
// last-edited: 2026-08-19

import js from '@eslint/js';
import tseslint from 'typescript-eslint';
import reactHooks from 'eslint-plugin-react-hooks';
import reactRefresh from 'eslint-plugin-react-refresh';
import globals from 'globals';

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
