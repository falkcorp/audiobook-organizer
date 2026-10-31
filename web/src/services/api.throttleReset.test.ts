// file: web/src/services/api.throttleReset.test.ts
// version: 1.0.0
// guid: c41d7e08-95b3-4a6f-8e2d-6f0a3b9c7d15
// last-edited: 2026-10-30

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { getProviderThrottles, startThrottleReset } from './api';

const mockFetch = vi.fn();

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function postedBody(): Record<string, unknown> {
  const init = mockFetch.mock.calls[0][1] as RequestInit;
  return JSON.parse(String(init.body));
}

describe('throttle reset client', () => {
  beforeEach(() => {
    global.fetch = mockFetch as unknown as typeof fetch;
  });
  afterEach(() => {
    mockFetch.mockReset();
  });

  it('startThrottleReset sends dry_run:false and the provider', async () => {
    mockFetch.mockResolvedValueOnce(json({ op_id: 'op-9' }, 202));
    const res = await startThrottleReset('audible');
    expect(res.operation_id).toBe('op-9');
    expect(postedBody()).toEqual({
      def_id: 'maintenance.throttle-reset',
      params: { dry_run: false, provider: 'audible' },
    });
  });

  it('startThrottleReset with no provider omits the key but still sends dry_run:false', async () => {
    mockFetch.mockResolvedValueOnce(json({ op_id: 'op-9' }, 202));
    await startThrottleReset();
    expect(postedBody().params).toEqual({ dry_run: false });
  });

  it('getProviderThrottles unwraps the throttles list', async () => {
    mockFetch.mockResolvedValueOnce(
      json({ data: { count: 1, throttles: [{ provider_id: 'audible', seconds_remaining: 5 }] } })
    );
    const list = await getProviderThrottles();
    expect(list).toHaveLength(1);
    expect(list[0].provider_id).toBe('audible');
  });
});
