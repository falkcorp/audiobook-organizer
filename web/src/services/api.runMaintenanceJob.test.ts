// file: web/src/services/api.runMaintenanceJob.test.ts
// version: 1.0.0
// guid: 9445b98e-c2ed-4a4f-b5f0-8e04191dd4d2
// last-edited: 2026-09-19
//
// runMaintenanceJob builds the POST body. An omitted dryRun must OMIT the
// dry_run key so the server applies the job's advertised default (a dry run for
// every job that advertises one); sending dry_run:false by default turned the
// generic Manual Fixes "Run" button into a real bulk mutation.

import { vi, describe, it, expect, beforeEach, afterEach } from 'vitest';
import { runMaintenanceJob } from './api';

function capturingFetch() {
  return vi.fn(async (_url: unknown, _init?: RequestInit) => {
    void _url;
    void _init;
    return {
      ok: true,
      status: 202,
      json: async () => ({ data: { operation_id: 'op-9' } }),
    } as unknown as Response;
  });
}

function bodyOf(fetchMock: ReturnType<typeof capturingFetch>): Record<string, unknown> {
  expect(fetchMock).toHaveBeenCalledTimes(1);
  const init = fetchMock.mock.calls[0][1];
  return JSON.parse(String(init?.body ?? '{}'));
}

describe('runMaintenanceJob', () => {
  let fetchMock: ReturnType<typeof capturingFetch>;
  beforeEach(() => {
    fetchMock = capturingFetch();
    global.fetch = fetchMock as unknown as typeof fetch;
  });
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it('omits dry_run when the caller does not choose, so the advertised default applies', async () => {
    const res = await runMaintenanceJob('merge-chapter-groups');
    expect(bodyOf(fetchMock)).not.toHaveProperty('dry_run');
    expect(res.operation_id).toBe('op-9');
  });

  it('sends an explicit dry_run and keeps custom params', async () => {
    await runMaintenanceJob('merge-chapter-groups', false, { min_files: 3, dry_run: true });
    expect(bodyOf(fetchMock)).toEqual({ min_files: 3, dry_run: false });
  });
});
