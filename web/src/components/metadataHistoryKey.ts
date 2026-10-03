// file: web/src/components/metadataHistoryKey.ts
// version: 1.0.0
// guid: 6fc49ca0-425d-42a1-8e86-61b8092f93f2
// last-edited: 2026-10-03

import type { MetadataChangeRecord } from '../services/api';

/**
 * A history row's identity. The store stamps every row of one edit with the
 * same id (the edit's time in nanoseconds), so the id alone repeats across
 * the fields that edit changed; id plus field is unique.
 */
export function historyRowKey(record: Pick<MetadataChangeRecord, 'id' | 'field'>): string {
  return `${record.id}:${record.field}`;
}
