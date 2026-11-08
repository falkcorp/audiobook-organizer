// file: core/web/eslint.config.mjs
// version: 1.0.0
// guid: 06dc7cb9-ae19-45b2-a0ec-f5b60f4c0727
// last-edited: 2026-11-08

import js from '@eslint/js';
import globals from 'globals';
import tseslint from 'typescript-eslint';

// The import boundary (07 §7.3.1, FE-16): nothing in core/web imports from outside
// core/web, because core/web becomes the split repository's npm root unchanged. A
// specifier that climbs out of its package into ABO's web/ or names an ABO alias
// fails lint. `import/no-relative-packages` (a specifier that resolves into a
// sibling package's directory instead of its name) needs eslint-plugin-import-x and
// is filed as a follow-up with the second package (todo.d/2026-11-08-core-web-no-relative-packages.md).
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

export default tseslint.config(
  { ignores: ['**/dist/**', '**/node_modules/**', '**/coverage/**', '**/src/generated/**'] },
  js.configs.recommended,
  {
    files: ['**/*.{ts,tsx,mjs}'],
    extends: [...tseslint.configs.recommended],
    languageOptions: { globals: { ...globals.browser, ...globals.node } },
    rules: { 'no-restricted-imports': ['error', outsideCoreWeb] },
  },
);
