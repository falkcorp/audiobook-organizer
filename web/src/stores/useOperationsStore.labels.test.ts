// file: web/src/stores/useOperationsStore.labels.test.ts
// version: 1.0.0
// guid: 6a1f3d82-c4e9-4b07-95a2-7e08d1b3f56c
// last-edited: 2026-11-21

// BASELINE of the toast text startPolling and beginOptimistic build from an
// operation's type, before it shares the def-id label table with the bell. The
// store carries its own copy of the old table, stale in the same ways the bell's
// was; this pins what it says today, so the change that removes the copy shows up
// as a deliberate flip of these assertions. Replaced in that commit.

import { beforeEach, describe, expect, it, vi } from 'vitest';
import { useOperationsStore } from './useOperationsStore';

const notifications = vi.hoisted(() => ({ messages: [] as string[] }));

vi.mock('../services/api');
vi.mock('./useAppStore', () => ({
  useAppStore: {
    getState: () => ({
      addNotification: (message: string) => {
        notifications.messages.push(message);
      },
    }),
  },
}));

describe('useOperationsStore toast labels (baseline, private table)', () => {
  beforeEach(() => {
    notifications.messages = [];
    useOperationsStore.setState({
      operations: {},
      activeOperations: [],
      loadError: null,
      polling: false,
    });
  });

  const started = (type: string) => {
    useOperationsStore.getState().startPolling('op-1', type);
    return notifications.messages.at(-1);
  };
  const starting = (type: string) => {
    useOperationsStore.getState().beginOptimistic(type);
    return notifications.messages.at(-1);
  };

  it('labels the legacy short type codes callers pass', () => {
    expect(started('scan')).toBe('Library Scan started');
    expect(started('itunes_import')).toBe('iTunes Import started');
    expect(starting('metadata_candidate_fetch')).toBe('Metadata Fetch (Batch) starting…');
  });

  it('labels ol_dump_import with a name no def has, and de-slugs what wrapTrigger passes', () => {
    expect(started('ol_dump_import')).toBe('Open Library Import started');
    // wrapTrigger hands beginOptimistic a def id; the private table has none of them.
    expect(starting('library.scan')).toBe('Library.Scan starting…');
    expect(starting('acoustid.scan')).toBe('Acoustid.Scan starting…');
    expect(starting('dedup.scan')).toBe('Dedup.Scan starting…');
  });

  it('keeps labels for type codes no caller passes any more', () => {
    expect(started('isbn_enrichment')).toBe('ISBN Enrichment (retired) started');
    expect(started('dedup-scan')).toBe('Dedup Scan started');
  });
});
