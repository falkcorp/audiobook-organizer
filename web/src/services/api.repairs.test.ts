// file: web/src/services/api.repairs.test.ts
// version: 1.1.0
// guid: 6f2d9a58-1b74-4c3e-8a06-d7e5b2c4f913
// last-edited: 2026-10-06
//
// The repairs client against a stubbed fetch, asserting on the request that
// actually leaves the browser.
//
// The one that matters most: the apply body. The server treats an apply
// WITHOUT `dry_run: false` as a preview that writes nothing (owner rule
// 2026-09-25), so a client that dropped the flag would report success while
// changing nothing. The lane tests mock this module and cannot see the body.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  getRepairApplyResult,
  getRepairPlanRows,
  listRepairFixers,
  pollOperationV2,
  REPAIRS_OWNER_APPLY_HEADER,
  startRepairApply,
  startRepairOwnerApply,
  startRepairPlan,
} from './api';

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

let fetchMock: ReturnType<typeof vi.fn>;

beforeEach(() => {
  fetchMock = vi.fn();
  global.fetch = fetchMock as unknown as typeof fetch;
});

afterEach(() => {
  vi.useRealTimers();
});

function lastCall(): { url: string; init: RequestInit } {
  const [url, init] = fetchMock.mock.calls[fetchMock.mock.calls.length - 1];
  return { url: String(url), init: init as RequestInit };
}

describe('startRepairOwnerApply', () => {
  it('posts exactly {plan_op_id, row_id} to owner-apply with the CSRF header', async () => {
    fetchMock.mockResolvedValue(
      jsonResponse(202, {
        data: { operation_id: 'op-own', def_id: 'repairs.apply', fixer_id: 'frag', status: 'queued' },
      })
    );
    const started = await startRepairOwnerApply('frag', 'plan-1', 'manual:b1');
    const { url, init } = lastCall();
    expect(url).toBe('/api/v1/repairs/frag/owner-apply');
    expect(init.method).toBe('POST');
    expect(new Headers(init.headers).get(REPAIRS_OWNER_APPLY_HEADER)).toBe('1');
    expect(JSON.parse(String(init.body))).toStrictEqual({ plan_op_id: 'plan-1', row_id: 'manual:b1' });
    expect(started.operation_id).toBe('op-own');
  });

  it('throws the server message on a 403 (API key, not the owner)', async () => {
    fetchMock.mockResolvedValue(
      jsonResponse(403, { error: 'owner rows are applied only by the owner, signed in interactively', status: 403 })
    );
    await expect(startRepairOwnerApply('frag', 'plan-1', 'manual:b1')).rejects.toThrow(/signed in interactively/);
  });
});

describe('startRepairApply', () => {
  it('posts exactly {plan_op_id, row_ids, dry_run:false}', async () => {
    fetchMock.mockResolvedValue(
      jsonResponse(202, {
        data: { operation_id: 'op-1', def_id: 'repairs.apply', fixer_id: 'vg', status: 'queued' },
      })
    );
    const started = await startRepairApply('vg', 'plan-1', ['g1', 'g2']);
    const { url, init } = lastCall();
    expect(url).toBe('/api/v1/repairs/vg/apply');
    expect(init.method).toBe('POST');
    expect(JSON.parse(String(init.body))).toStrictEqual({
      plan_op_id: 'plan-1',
      row_ids: ['g1', 'g2'],
      dry_run: false,
    });
    expect(started.operation_id).toBe('op-1');
  });

  it('throws the server message on a 409', async () => {
    fetchMock.mockResolvedValue(
      jsonResponse(409, { error: 'repairs: plan op is not completed', status: 409 })
    );
    await expect(startRepairApply('vg', 'plan-1', ['g1'])).rejects.toMatchObject({
      message: 'repairs: plan op is not completed',
      status: 409,
    });
  });
});

describe('startRepairPlan', () => {
  it('posts with no body when there are no fixer params', async () => {
    fetchMock.mockResolvedValue(
      jsonResponse(202, {
        data: { operation_id: 'plan-2', def_id: 'repairs.plan', fixer_id: 'vg', status: 'queued' },
      })
    );
    await startRepairPlan('vg');
    const { url, init } = lastCall();
    expect(url).toBe('/api/v1/repairs/vg/plan');
    expect(init.method).toBe('POST');
    expect(init.body).toBeUndefined();
  });

  it('rejects a 202 without an operation id instead of polling "undefined"', async () => {
    fetchMock.mockResolvedValue(jsonResponse(202, { data: {} }));
    await expect(startRepairPlan('vg')).rejects.toThrow(/operation id/);
  });
});

describe('getRepairPlanRows', () => {
  it('sends filter, offset and a limit clamped to the server cap', async () => {
    fetchMock.mockResolvedValue(
      jsonResponse(200, { data: { rows: [], total: 0, applicable: 0, skipped_by_kind: {} } })
    );
    await getRepairPlanRows('vg', 'plan-1', { filter: 'skipped', offset: 100, limit: 9999 });
    const { url } = lastCall();
    const u = new URL(url, 'http://x');
    expect(u.pathname).toBe('/api/v1/repairs/vg/plan/plan-1/rows');
    expect(u.searchParams.get('filter')).toBe('skipped');
    expect(u.searchParams.get('offset')).toBe('100');
    expect(u.searchParams.get('limit')).toBe('500');
  });
});

describe('listRepairFixers', () => {
  it('reads data.fixers and rejects any other shape', async () => {
    fetchMock.mockResolvedValueOnce(
      jsonResponse(200, { data: { fixers: [{ id: 'vg', title: 'T', description: 'D' }] } })
    );
    expect(await listRepairFixers()).toEqual([{ id: 'vg', title: 'T', description: 'D' }]);
    fetchMock.mockResolvedValueOnce(jsonResponse(200, { data: {} }));
    await expect(listRepairFixers()).rejects.toThrow(/Unexpected response shape/);
  });
});

describe('getRepairApplyResult', () => {
  it('rejects a result that is not an apply result', async () => {
    fetchMock.mockResolvedValue(jsonResponse(200, { data: { result_data: 'garbled' } }));
    await expect(getRepairApplyResult('op-1')).rejects.toThrow(/could not be read/);
  });
});

describe('pollOperationV2 with a signal', () => {
  it('stops between polls when aborted', async () => {
    vi.useFakeTimers();
    fetchMock.mockImplementation(async () =>
      jsonResponse(200, { data: { operation: { id: 'op-1', status: 'running' } } })
    );
    const ctrl = new AbortController();
    const p = pollOperationV2('op-1', undefined, 1000, { signal: ctrl.signal });
    const settled = expect(p).rejects.toMatchObject({ name: 'AbortError' });
    await vi.advanceTimersByTimeAsync(0);
    expect(fetchMock).toHaveBeenCalledTimes(1);
    ctrl.abort();
    await settled;
    await vi.advanceTimersByTimeAsync(5000);
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });
});
