// file: core/web/eslint.config.mjs
// version: 1.0.0
// guid: 06dc7cb9-ae19-45b2-a0ec-f5b60f4c0727
// last-edited: 2026-11-08

import js from '@eslint/js';
import globals from 'globals';
import importX from 'eslint-plugin-import-x';
import tseslint from 'typescript-eslint';

// The import boundary (07 §7.3.1, FE-16): nothing in core/web imports from outside
// core/web, because core/web becomes the split repository's npm root unchanged. A
// specifier that climbs out of its package into ABO's web/ or names an ABO alias
// fails lint. `import-x/no-relative-packages` refuses a specifier that resolves
// into a sibling package's directory instead of naming the package (added with
// the second and third packages, ui-kit and shell).
const outsideCoreWeb = {
  patterns: [
    {
      group: ['**/web/src/**', '**/web/vite.config*', '@abo/*', '@/*'],
      message: 'core/web imports nothing from ABO (07 §7.3.1 import boundary).',
    },
    {
      // Three levels up from packages/<name>/src/ is core/web's own root; a fourth
      // leaves core/web.
      group: ['../../../../*', '../../../../**'],
      message: 'A specifier may not resolve outside core/web (07 §7.3.1 import boundary).',
    },
  ],
};

// ui-kit is presentational (07 §7.3.1, §7.11): its sources import neither the
// API client nor the shell; a piece that needs data takes it as a prop. Its
// vite.config.ts, outside src/, may import the one shell file it needs, the
// unbuilt preset @mediacore/shell/vite.
const uiKitBoundary = {
  patterns: [
    ...outsideCoreWeb.patterns,
    {
      group: ['@mediacore/api-client', '@mediacore/api-client/*', '@mediacore/shell', '@mediacore/shell/*'],
      message: 'ui-kit imports neither @mediacore/api-client nor @mediacore/shell (07 §7.3.1).',
    },
  ],
};

export default tseslint.config(
  { ignores: ['**/dist/**', '**/node_modules/**', '**/coverage/**', '**/src/generated/**'] },
  js.configs.recommended,
  {
    files: ['**/*.{ts,tsx,mjs}'],
    extends: [...tseslint.configs.recommended],
    languageOptions: { globals: { ...globals.browser, ...globals.node } },
    plugins: { 'import-x': importX },
    rules: {
      'no-restricted-imports': ['error', outsideCoreWeb],
      'import-x/no-relative-packages': 'error',
    },
  },
  {
    files: ['packages/ui-kit/src/**/*.{ts,tsx}'],
    rules: { 'no-restricted-imports': ['error', uiKitBoundary] },
  },
);
