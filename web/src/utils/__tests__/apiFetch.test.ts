// file: web/src/utils/__tests__/apiFetch.test.ts
// version: 1.0.0
// guid: 5b8d4e21-6c39-47af-9d0e-1a3f7c2b8e45
// last-edited: 2026-08-11

import { describe, it, expect, vi, afterEach } from 'vitest';
import { apiFetch, ApiTimeoutError, isAbortError } from '../apiFetch';

const originalFetch = global.fetch;

afterEach(() => {
  global.fetch = originalFetch;
  vi.restoreAllMocks();
});

/** A fetch that never resolves on its own — it only settles when aborted. */
const hangingFetch = vi.fn((_url: string, init?: RequestInit) =>
  new Promise<Response>((_resolve, reject) => {
    init?.signal?.addEventListener('abort', () =>
      reject(new DOMException('The operation was aborted.', 'AbortError')),
    );
  }),
);

describe('apiFetch timeout', () => {
  it('aborts and throws ApiTimeoutError when the server never responds', async () => {
    global.fetch = hangingFetch as unknown as typeof fetch;

    // The server sets WriteTimeout: 0, so without this the request hangs forever.
    await expect(apiFetch('/api/v1/activity', { timeoutMs: 20 })).rejects.toBeInstanceOf(
      ApiTimeoutError,
    );
  });

  it('does not time out when no timeoutMs is given (default stays unbounded)', async () => {
    const abortable = new AbortController();
    global.fetch = hangingFetch as unknown as typeof fetch;

    const pending = apiFetch('/api/v1/activity/compact', { signal: abortable.signal });
    const settled = vi.fn();
    void pending.catch(settled);

    await new Promise((r) => setTimeout(r, 50));
    expect(settled).not.toHaveBeenCalled();

    // Long-running endpoints (compact, scan) must keep working.
    abortable.abort();
    await expect(pending).rejects.toSatisfy(isAbortError);
  });

  it('reports a caller abort as AbortError, not as a timeout', async () => {
    const controller = new AbortController();
    global.fetch = hangingFetch as unknown as typeof fetch;

    const pending = apiFetch('/api/v1/activity', {
      timeoutMs: 10_000,
      signal: controller.signal,
    });
    controller.abort();

    // Callers swallow their own aborts; a timeout must stay distinguishable
    // from one or real failures get hidden again.
    const err = await pending.catch((e: unknown) => e);
    expect(isAbortError(err)).toBe(true);
    expect(err).not.toBeInstanceOf(ApiTimeoutError);
  });
});
