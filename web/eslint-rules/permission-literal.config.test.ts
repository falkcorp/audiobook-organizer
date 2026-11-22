// file: web/eslint-rules/permission-literal.config.test.ts
// version: 0.1.0
// guid: c4e1a7d9-2b58-4f36-8a70-5d9b3e61f082
// last-edited: 2026-11-21

// 07 section 7.18's done-check: the committed eslint.config.mjs, not a copy of
// the rule, fails on a fixture that passes a string literal to usePermission,
// and passes the same call when the permission comes from the manifest.

import { ESLint } from 'eslint';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';

const webRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const eslint = new ESLint({ cwd: webRoot });

async function messages(code: string, filePath: string) {
  const [result] = await eslint.lintText(code, { filePath: path.join(webRoot, filePath) });
  return result.messages.filter((m) => m.ruleId === 'no-restricted-syntax');
}

describe('eslint.config.mjs permission-literal rule', () => {
  const fixture = "import { usePermission } from '../hooks/usePermission';\nexport const f = () => usePermission('library.delete');\n";

  it('fails a string literal passed to usePermission under web/src', async () => {
    const found = await messages(fixture, 'src/components/Fixture.tsx');
    expect(found).toHaveLength(1);
    expect(found[0].line).toBe(2);
  });

  it('passes a permission that comes from the manifest', async () => {
    const code = fixture.replace("'library.delete'", 'manifest.permissions.libraryDelete');
    expect(await messages(code, 'src/components/Fixture.tsx')).toEqual([]);
  });

  it('does not run over a test file', async () => {
    expect(await messages(fixture, 'src/components/Fixture.test.tsx')).toEqual([]);
  });

  it('holds every file under src to the rule, the five files that once had literals included', async () => {
    for (const file of [
      'src/components/settings/APIKeysTab.tsx',
      'src/components/system/ThrottlePanel.tsx',
      'src/hooks/useCanOrganizeLibrary.ts',
      'src/hooks/useCollections.ts',
      'src/hooks/useLibraryPermission.ts',
      'src/hooks/someNewHook.ts',
    ]) {
      expect(await messages(fixture, file), file).toHaveLength(1);
    }
  });

  it('exempts only the generated permissions file', async () => {
    expect(await messages(fixture, 'src/generated/permissions.ts')).toEqual([]);
    expect(await messages(fixture, 'src/generator/permissions.ts')).toHaveLength(1);
  });
});
