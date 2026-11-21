// file: core/web/packages/ui-kit/src/components/index.test.ts
// version: 0.1.0
// guid: 1a3d32a4-6c8a-45c7-8eed-99556dcda53a
// last-edited: 2026-11-21

import { describe, expect, it } from 'vitest';
import * as root from '../index';

describe('ui-kit root exports the components', () => {
  it.each([
    'AnnouncementBanner',
    'BulkConfirmDialog',
    'ColumnPicker',
    'ErrorBoundary',
    'FilterTagBar',
    'KeyboardShortcutsDialog',
    'ResizableHeaderCell',
    'ServerFileBrowser',
    'useConfigurableTable',
  ])('%s', (name) => {
    expect(['function', 'object']).toContain(typeof (root as Record<string, unknown>)[name]);
  });
});
