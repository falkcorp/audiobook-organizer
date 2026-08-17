// file: src/services/api.test.ts
// version: 1.4.0
// guid: 0a1b2c3d-4e5f-6a7b-8c9d-0e1f2a3b4c5d
// last-edited: 2026-08-16

import { vi, describe, it, expect, beforeEach, afterEach } from 'vitest';
import {
  isOperationTerminal,
  getImportPaths,
  addImportPath,
  addImportPathDetailed,
  removeImportPath,
  bulkFetchMetadata,
  batchWriteBackMetadata,
} from './api';

const mockFetch = vi.fn();

describe('api import paths', () => {
  beforeEach(() => {
    // Allow overriding fetch in tests
    global.fetch = mockFetch as unknown as typeof fetch;
  });

  afterEach(() => {
    mockFetch.mockReset();
  });

  it('getImportPaths returns import paths list', async () => {
    mockFetch.mockResolvedValueOnce(
      new Response(
        JSON.stringify({
          data: {
            importPaths: [
              {
                id: 1,
                path: '/tmp',
                name: 'Tmp',
                enabled: true,
                created_at: 'now',
                book_count: 0,
              },
            ],
          },
        }),
        {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        }
      )
    );

    const paths = await getImportPaths();
    expect(paths).toEqual([
      {
        id: 1,
        path: '/tmp',
        name: 'Tmp',
        enabled: true,
        created_at: 'now',
        book_count: 0,
      },
    ]);
    expect(mockFetch).toHaveBeenCalledWith('/api/v1/import-paths', expect.any(Object));
  });

  it('addImportPath returns created import path', async () => {
    mockFetch.mockResolvedValueOnce(
      new Response(
        JSON.stringify({
          data: {
            importPath: {
              id: 2,
              path: '/new',
              name: 'New',
              enabled: true,
              created_at: 'now',
              book_count: 0,
            },
          },
        }),
        {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        }
      )
    );

    const created = await addImportPath('/new', 'New');
    expect(created.path).toBe('/new');
    expect(mockFetch).toHaveBeenCalledWith(
      '/api/v1/import-paths',
      expect.any(Object)
    );
  });

  it('addImportPathDetailed returns detailed response', async () => {
    mockFetch.mockResolvedValueOnce(
      new Response(
        JSON.stringify({
          data: {
            importPath: {
              id: 3,
              path: '/detailed',
              name: 'Detailed',
              enabled: true,
              created_at: 'now',
              book_count: 0,
            },
            scan_operation_id: 'op-1',
          },
        }),
        {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        }
      )
    );

    const detailed = await addImportPathDetailed('/detailed', 'Detailed');
    expect(detailed.importPath.id).toBe(3);
    expect(detailed.scan_operation_id).toBe('op-1');
  });

  it('removeImportPath calls delete endpoint', async () => {
    mockFetch.mockResolvedValueOnce(new Response(null, { status: 200 }));

    await removeImportPath(4);
    expect(mockFetch).toHaveBeenCalledWith('/api/v1/import-paths/4', expect.objectContaining({ method: 'DELETE' }));
  });

  it('bulkFetchMetadata posts book ids and returns response', async () => {
    mockFetch.mockResolvedValueOnce(
      new Response(
        JSON.stringify({
          data: {
            updated_count: 1,
            total_count: 2,
            results: [
              {
                book_id: 'id-1',
                status: 'updated',
                applied_fields: ['publisher'],
                fetched_fields: ['publisher'],
              },
            ],
            source: 'Open Library',
          },
        }),
        {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        }
      )
    );

    const response = await bulkFetchMetadata(['id-1', 'id-2'], false);
    expect(response.updated_count).toBe(1);
    expect(response.total_count).toBe(2);
    expect(mockFetch).toHaveBeenCalledWith('/api/v1/metadata/bulk-fetch', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({ book_ids: ['id-1', 'id-2'], only_missing: false }),
    }));
  });

  it('batchWriteBackMetadata posts book ids and rename flag', async () => {
    mockFetch.mockResolvedValueOnce(
      new Response(
        JSON.stringify({
          data: {
            written: 2,
            written_files: 3,
            renamed: 1,
            failed: 0,
            errors: [],
          },
        }),
        {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        }
      )
    );

    const response = await batchWriteBackMetadata(['id-1', 'id-2'], true);
    expect(response.written).toBe(2);
    expect(response.renamed).toBe(1);
    expect(mockFetch).toHaveBeenCalledWith('/api/v1/audiobooks/batch-write-back', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({ book_ids: ['id-1', 'id-2'], organize: true, force: false }),
    }));
  });
});

describe('isOperationTerminal', () => {
  // The exhaustive list of statuses the v2 registry can write, taken from
  // internal/operations/registry/legacy_op_status.go. Restated here on purpose:
  // this is the frontend's claim about the backend's vocabulary, and the two
  // drifting apart is the defect these tests exist to catch. The poller does
  // not crash when they disagree — it spins at 1s forever while the UI shows
  // the operation still running.
  const terminal = [
    'completed',
    'failed',
    'canceled',
    'interrupted',
    'interrupted_ask',
    'interrupted_dropped',
    'interrupted_quiesced',
    'interrupted_restart',
  ];
  const nonTerminal = ['queued', 'running', 'interrupting'];

  it.each(terminal)('treats %s as terminal', (status) => {
    expect(isOperationTerminal(status)).toBe(true);
  });

  it.each(nonTerminal)('treats %s as not terminal', (status) => {
    expect(isOperationTerminal(status)).toBe(false);
  });

  it('does not mistake the transitional interrupting state for interrupted', () => {
    // The prefix rule is only safe because 'interrupting' does not start with
    // 'interrupted'. If a future status did, the rule would need revisiting.
    expect('interrupting'.startsWith('interrupted')).toBe(false);
  });

  it('accepts a status variant it has never been told about', () => {
    // The point of matching the prefix rather than a list: the registry mints
    // one interrupted_<policy> status per resume policy, and a hardcoded list
    // hangs the poller the day a policy is added. OP_V2_TERMINAL had exactly
    // this bug — it omitted interrupted_quiesced, the default for three of the
    // four policies.
    expect(isOperationTerminal('interrupted_some_future_policy')).toBe(true);
  });

  it('rejects the misspelling that caused the original hang', () => {
    // The backend mints 'canceled'; the poller waited for 'cancelled'. Asserting
    // the wrong spelling is NOT terminal keeps anyone from "fixing" the drift by
    // teaching the frontend to accept both and leaving the mismatch in place.
    expect(isOperationTerminal('cancelled')).toBe(false);
  });
});
