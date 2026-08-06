// file: web/eslint.config.mjs
// version: 1.3.0
// guid: 456e7890-b12c-34d5-c678-901234567890
// last-edited: 2026-08-06

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
      // eslint-plugin-react-hooks was bumped 5.2.0 -> 7.1.1 solely to clear
      // ESLint 10's peer range (5.2.0, 6.x and 7.0.x all cap at eslint ^9.0.0;
      // ^10.0.0 first appears in 7.1.0). Its 7.x `recommended` preset also adds
      // ~14 React Compiler rules (immutability, purity, refs, static-components,
      // set-state-in-effect, ...) at error severity, which is a decision about
      // how this codebase writes React — not a consequence of upgrading ESLint.
      // So we pin the exact pair that 5.2.0's `recommended` enabled, keeping the
      // toolchain bump behaviour-neutral. Adopting the compiler rules is a
      // separate, deliberate change: swap this pair back for
      // `...reactHooks.configs.recommended.rules`.
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
