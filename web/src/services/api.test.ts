// file: src/services/api.test.ts
// version: 1.7.0
// guid: 0a1b2c3d-4e5f-6a7b-8c9d-0e1f2a3b4c5d
// last-edited: 2026-09-11

import { vi, describe, it, expect, beforeEach, afterEach } from 'vitest';
import {
  getOperationTimeline,
  isOperationTerminal,
  getImportPaths,
  addImportPath,
  addImportPathDetailed,
  removeImportPath,
  bulkFetchMetadata,
  batchWriteBackMetadata,
  batchFetchCandidates,
  getMaintenanceWindowStatus,
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

// ---------------------------------------------------------------------------
// batchFetchCandidates: the envelope hop
// ---------------------------------------------------------------------------
//
// This suite exists because the component-level test could not catch the bug it
// was written to guard. `ReviewWorkspace.refetchStale.test.tsx` does
// `vi.mock('../../services/api')`, so the real `batchFetchCandidates` never runs
// there -- the fixture stands in for the function at its own boundary and
// asserts nothing about how that function reads the wire.
//
// The defect: a bare `return response.json()` behind a FLAT declared return
// type handed callers the whole `{data:{...}}` envelope. `operation_id` was
// therefore ALWAYS undefined, and all three callers' `if (!resp.operation_id)`
// guard fired on every SUCCESSFUL fetch, showing an "already being fetched"
// toast for an operation the server had in fact enqueued. It also made
// `withOptimisticOperation` pick no id and silently drop its bell placeholder.
//
// These tests drive the real function against a real enveloped Response, which
// is the only level at which that bug is observable.
describe('batchFetchCandidates envelope unwrapping', () => {
  beforeEach(() => {
    global.fetch = mockFetch as unknown as typeof fetch;
  });

  afterEach(() => {
    mockFetch.mockReset();
  });

  function enveloped(inner: unknown, status = 200) {
    return new Response(JSON.stringify({ data: inner }), {
      status,
      headers: { 'Content-Type': 'application/json' },
    });
  }

  // The regression gate. Fails against the pre-fix `return response.json()`,
  // which resolved to `{data:{...}}` and left operation_id undefined.
  it('unwraps the data envelope so operation_id reaches the caller', async () => {
    mockFetch.mockResolvedValueOnce(
      enveloped(
        { operation_id: 'op-1', total_books: 2, message: 'metadata candidate fetch started' },
        202
      )
    );

    const resp = await batchFetchCandidates({ book_ids: ['a', 'c'] });

    expect(resp.operation_id).toBe('op-1');
    expect(resp.total_books).toBe(2);
    // The envelope must not survive into the caller's view of the response.
    expect(resp).not.toHaveProperty('data');
  });

  // The property the three callers actually branch on, stated the way they
  // state it, so a future regression reads as the user-visible symptom rather
  // than as a shape mismatch.
  it('makes the started response pass the callers "did it start" guard', async () => {
    mockFetch.mockResolvedValueOnce(enveloped({ operation_id: 'op-1', total_books: 1 }, 202));

    const resp = await batchFetchCandidates({ book_ids: ['a'] });

    expect(Boolean(resp.operation_id)).toBe(true);
  });

  // The other side of that guard: the server really does decline sometimes, and
  // it says so with an EMPTY STRING rather than by omitting the key. The
  // "already being fetched" toast is correct here and must still fire.
  it('preserves the empty operation_id the server sends when it declines to start', async () => {
    mockFetch.mockResolvedValueOnce(
      enveloped({
        operation_id: '',
        book_count: 0,
        skipped: 3,
        message: 'All 3 books are already being fetched in another operation',
      })
    );

    const resp = await batchFetchCandidates({ book_ids: ['a', 'b', 'c'] });

    expect(resp.operation_id).toBe('');
    expect(Boolean(resp.operation_id)).toBe(false);
    expect(resp.skipped).toBe(3);
    expect(resp.message).toMatch(/already being fetched/);
  });

  // `body.data ?? body` rather than `body.data`: the caller branches on a
  // field's PRESENCE, so a response that arrives unwrapped should degrade to
  // working rather than to a false-positive toast.
  it('falls back to the body when a response arrives unwrapped', async () => {
    mockFetch.mockResolvedValueOnce(
      new Response(JSON.stringify({ operation_id: 'op-flat' }), {
        status: 202,
        headers: { 'Content-Type': 'application/json' },
      })
    );

    const resp = await batchFetchCandidates({ book_ids: ['a'] });

    expect(resp.operation_id).toBe('op-flat');
  });
});

// GET /maintenance-window/status answers the standard {"data": {...}} envelope,
// but the client returned response.json() raw -- so callers got the envelope
// and every field read as undefined. The Maintenance tab rendered blanks and
// "Invalid Date" without ever throwing, which is why it went unnoticed.
describe('getMaintenanceWindowStatus envelope', () => {
  beforeEach(() => {
    global.fetch = mockFetch as unknown as typeof fetch;
  });

  afterEach(() => {
    mockFetch.mockReset();
  });

  it('unwraps the data envelope so the fields land at the top level', async () => {
    mockFetch.mockResolvedValueOnce(
      new Response(
        JSON.stringify({
          data: {
            enabled: true,
            window_start: 2,
            window_end: 5,
            last_run_date: '2026-09-09',
            next_run_estimate: '2026-09-11T02:00:00Z',
            currently_running: false,
          },
        }),
        { status: 200, headers: { 'Content-Type': 'application/json' } }
      )
    );

    const status = await getMaintenanceWindowStatus();

    expect(status.enabled).toBe(true);
    expect(status.window_start).toBe(2);
    expect(status.window_end).toBe(5);
    expect(status.next_run_estimate).toBe('2026-09-11T02:00:00Z');
    expect(status.currently_running).toBe(false);
    // The envelope must not survive into the returned object.
    expect((status as unknown as { data?: unknown }).data).toBeUndefined();
  });

  // `body.data ?? body`, matching getBookFileHashStats: an unwrapped response
  // still yields usable fields rather than a page full of undefined.
  it('falls back to the body when a response arrives unwrapped', async () => {
    mockFetch.mockResolvedValueOnce(
      new Response(
        JSON.stringify({
          enabled: false,
          window_start: 1,
          window_end: 4,
          last_run_date: '',
          next_run_estimate: '',
          currently_running: false,
        }),
        { status: 200, headers: { 'Content-Type': 'application/json' } }
      )
    );

    const status = await getMaintenanceWindowStatus();

    expect(status.enabled).toBe(false);
    expect(status.window_start).toBe(1);
    expect(status.window_end).toBe(4);
  });
});

// getOperationTimeline used to catch everything and return [] — a 500 and a
// dead network both resolved to the same value as "no operations in the
// window", so every consumer rendered a down server as an idle one (WEB-05).
// These pin the fetch to the buildApiError convention the rest of api.ts uses.
describe('getOperationTimeline failure handling', () => {
  beforeEach(() => {
    global.fetch = mockFetch as unknown as typeof fetch;
  });

  afterEach(() => {
    mockFetch.mockReset();
  });

  it('rejects on a non-2xx response instead of resolving to an empty list', async () => {
    mockFetch.mockResolvedValueOnce(
      new Response(JSON.stringify({ error: 'timeline store unavailable' }), {
        status: 500,
        headers: { 'Content-Type': 'application/json' },
      })
    );

    await expect(getOperationTimeline()).rejects.toMatchObject({
      message: 'timeline store unavailable',
      status: 500,
    });
  });

  it('rejects on a network failure instead of resolving to an empty list', async () => {
    mockFetch.mockRejectedValueOnce(new TypeError('Failed to fetch'));

    await expect(getOperationTimeline()).rejects.toThrow('Failed to fetch');
  });

  it('still resolves the operations list on a 2xx', async () => {
    mockFetch.mockResolvedValueOnce(
      new Response(JSON.stringify({ data: { operations: [{ id: 'op-1' }], matched: 1 } }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    );

    await expect(getOperationTimeline()).resolves.toEqual([{ id: 'op-1' }]);
  });
});
