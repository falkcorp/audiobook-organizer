// file: web/src/stores/useOperationsStore.labels.test.ts
// version: 1.0.1
// guid: 6a1f3d82-c4e9-4b07-95a2-7e08d1b3f56c
// last-edited: 2026-11-21

// The toast text startPolling and beginOptimistic build from an operation's type comes
// from the def-id label table the bell uses (operationDisplayName), not from a copy of
// its own. The store kept a private copy of the old tail-keyed table, stale in the same
// ways the bell's was: ol_dump_import, dedup-scan, and no label at all for the def ids
// wrapTrigger passes ("Library.Scan starting…"). Replaces the baseline that pinned it.

import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { operationDisplayName } from '../components/layout/operationsFormat';
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

describe('useOperationsStore toast labels', () => {
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
    expect(started('ol_dump_import')).toBe('OpenLibrary Dump Import started');
    expect(starting('metadata_candidate_fetch')).toBe('Metadata Fetch (Batch) starting…');
  });

  it('labels the def ids wrapTrigger passes, each one differently', () => {
    expect(starting('library.scan')).toBe('Library Scan starting…');
    expect(starting('acoustid.scan')).toBe('AcoustID fingerprint scan starting…');
    expect(starting('reconcile.scan')).toBe('Reconcile Scan starting…');
    expect(starting('dedup.full-scan')).toBe('Full dedup scan starting…');
  });

  it('says the same thing the bell does for every type', () => {
    for (const type of ['scan', 'organize', 'ol_dump_import', 'dedup.llm-review', 'no.such-def']) {
      expect(starting(type), type).toBe(`${operationDisplayName({ type })} starting…`);
    }
  });

  it('keeps no label table of its own', () => {
    const source = readFileSync(resolve(import.meta.dirname, 'useOperationsStore.ts'), 'utf8');
    // Any of the strings the private table held would mean a second copy is back.
    for (const label of ['Library Scan', 'iTunes Import', 'Metadata Fetch', 'Dedup Scan']) {
      expect(source, label).not.toContain(label);
    }
  });
});
