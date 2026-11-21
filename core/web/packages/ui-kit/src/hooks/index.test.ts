// file: core/web/packages/ui-kit/src/hooks/index.test.ts
// version: 0.1.0
// guid: a2f6c8d3-91b4-4e75-8c20-5d7e3b1f9a46
// last-edited: 2026-11-21

import { describe, expect, it } from 'vitest';
import * as root from '../index';

describe('ui-kit root exports the hooks', () => {
  it.each(['useAsyncAction', 'useDebouncedSearch', 'useSettledSearch', 'serverAnsweredTerm', 'useRowSelection', 'applyRowClick', 'useKeyboardShortcuts'])(
    '%s',
    (name) => {
      expect(typeof (root as Record<string, unknown>)[name]).toBe('function');
    },
  );
});
