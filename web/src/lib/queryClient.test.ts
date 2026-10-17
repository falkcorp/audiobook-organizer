// file: web/src/lib/queryClient.test.ts
// version: 1.0.0
// guid: 0b6f2d83-7a41-4e9c-8c35-f1d94a6e2b07
// last-edited: 2026-10-17

import { describe, expect, it, vi } from 'vitest';
import { QueryClient } from '@tanstack/react-query';
import type { EventSourceListener } from '../services/eventSourceManager';
import { is4xx, queryClient, queryKeys, startQuerySync } from './queryClient';

describe('queryClient defaults', () => {
  const q = queryClient.getDefaultOptions().queries!;

  it('sets the decided defaults', () => {
    expect(q.staleTime).toBe(30_000);
    expect(q.gcTime).toBe(300_000);
    expect(q.refetchOnWindowFocus).toBe(false);
    expect(q.refetchOnReconnect).toBe(true);
    expect(q.throwOnError).toBe(false);
    expect(queryClient.getDefaultOptions().mutations?.retry).toBe(0);
  });

  it('retries a 500 or a network error once, and a 4xx never', () => {
    const retry = q.retry as (count: number, err: unknown) => boolean;
    const status = (s: number) => Object.assign(new Error('x'), { status: s });
    expect(retry(0, status(404))).toBe(false);
    expect(retry(0, status(403))).toBe(false);
    expect(retry(0, status(500))).toBe(true);
    expect(retry(1, status(500))).toBe(false);
    expect(retry(0, new TypeError('Failed to fetch'))).toBe(true);
    expect(is4xx(null)).toBe(false);
  });
});

describe('queryKeys', () => {
  it('puts the domain first and the parameters object last', () => {
    expect(queryKeys.repairs.fixers()).toEqual(['repairs', 'fixers']);
    expect(queryKeys.repairs.ownerStatus()[0]).toBe('repairs');
    expect(queryKeys.repairs.rows('f', 'p', { offset: 0 })).toEqual([
      'repairs',
      'rows',
      'f',
      'p',
      { offset: 0 },
    ]);
    expect(queryKeys.ops.v2('op-1')).toEqual(['ops', 'v2', 'op-1']);
    expect(queryKeys.books.all()).toEqual(['books']);
  });
});

describe('startQuerySync', () => {
  it('invalidates books and the named operation, and unsubscribes', () => {
    let listener: EventSourceListener = () => {};
    const unsubscribe = vi.fn();
    const manager = {
      subscribe: vi.fn((l: EventSourceListener) => {
        listener = l;
        return unsubscribe;
      }),
    };
    const client = new QueryClient();
    const invalidate = vi.spyOn(client, 'invalidateQueries').mockResolvedValue();

    const stop = startQuerySync(manager, client);
    listener({ type: 'books.changed' });
    listener({ type: 'operation.status', id: 'op-7', data: { operation_id: 'op-7' } });
    listener({ type: 'operation.status', data: { operation_id: 'op-8' } });
    listener({ type: 'operation.log', id: 'op-9' });

    expect(invalidate.mock.calls.map((c) => c[0]?.queryKey)).toEqual([
      ['books'],
      ['ops', 'v2', 'op-7'],
      ['ops', 'v2', 'op-8'],
    ]);
    stop();
    expect(unsubscribe).toHaveBeenCalledTimes(1);
  });
});
