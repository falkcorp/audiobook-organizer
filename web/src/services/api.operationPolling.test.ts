// file: web/src/services/api.operationPolling.test.ts
// version: 1.0.0
// guid: de964edc-92bd-4672-8951-7f90a4a5a250
// last-edited: 2026-10-10

// Both API pollers against a stubbed fetch, driven by every status in the
// generated run-status table (web/src/generated/ops.ts, from
// internal/operations/state/state.go). A status the Go side calls settled must
// stop both pollers on the first read; a live one must keep them polling.
// A poller that missed a settled status would not fail, it would spin forever
// while the UI shows the operation still running.

import { beforeEach, describe, expect, it, vi } from 'vitest';

import { STATUS_PROPS, type OperationV2Status } from '../generated/ops';
import { pollOperation, pollOperationV2 } from './api';

function opResponse(status: string): Response {
  return new Response(JSON.stringify({ data: { operation: { id: 'op-1', status } } }), {
    status: 200,
    headers: { 'Content-Type': 'application/json' },
  });
}

let fetchMock: ReturnType<typeof vi.fn>;

beforeEach(() => {
  fetchMock = vi.fn();
  global.fetch = fetchMock as unknown as typeof fetch;
});

const allStatuses = Object.keys(STATUS_PROPS) as OperationV2Status[];
const settled = allStatuses.filter((s) => STATUS_PROPS[s].settled);
const live = ['queued', 'waiting_deps', 'running'];

const pollers = {
  pollOperation: (id: string) => pollOperation(id, undefined, 0),
  pollOperationV2: (id: string) => pollOperationV2(id, undefined, 0),
};

describe.each(Object.entries(pollers))('%s', (_name, poll) => {
  it('the table has a settled status for every interrupted* spelling and terminal state', () => {
    expect(settled).toEqual(
      expect.arrayContaining([
        'completed',
        'failed',
        'canceled',
        'interrupted_dropped',
        'interrupted_quiesced',
        'interrupted_ask',
        'interrupted_restart',
        'interrupted',
      ])
    );
  });

  it.each(settled)('stops on the first read of %s', async (status) => {
    fetchMock.mockImplementation(async () => opResponse(status));
    const op = await poll('op-1');
    expect(op.status).toBe(status);
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it('stops on an interrupted_* policy the table does not list yet', async () => {
    fetchMock.mockImplementation(async () => opResponse('interrupted_future'));
    await poll('op-1');
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it.each(live)('keeps polling on %s', async (status) => {
    let calls = 0;
    fetchMock.mockImplementation(async () => {
      calls += 1;
      return opResponse(calls <= 3 ? status : 'completed');
    });
    const op = await poll('op-1');
    expect(op.status).toBe('completed');
    expect(fetchMock).toHaveBeenCalledTimes(4);
  });
});
