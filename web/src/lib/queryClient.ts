// file: web/src/lib/queryClient.ts
// version: 1.0.0
// guid: 4c7e2a91-5b38-4f06-9d1e-a83f6b20c457
// last-edited: 2026-10-17

import { QueryClient } from '@tanstack/react-query';
import { eventSourceManager } from '../services/eventSourceManager';

/** An ApiError (or any error carrying an HTTP status) in the 400 range. */
export function is4xx(err: unknown): boolean {
  const status = (err as { status?: unknown } | null)?.status;
  return typeof status === 'number' && status >= 400 && status < 500;
}

export const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 30_000,
      gcTime: 5 * 60_000,
      // A 404 ("no repairs endpoints") or a 403 shows at once; others retry once.
      retry: (count, err) => count < 1 && !is4xx(err),
      refetchOnWindowFocus: false,
      refetchOnReconnect: true,
      throwOnError: false,
    },
    mutations: { retry: 0 },
  },
});

/** Every query key. Domain first, parameters object last, never a file_path. */
export const queryKeys = {
  books: { all: () => ['books'] as const },
  ops: { v2: (operationId: string) => ['ops', 'v2', operationId] as const },
  repairs: {
    fixers: () => ['repairs', 'fixers'] as const,
    ownerStatus: () => ['repairs', 'owner-status'] as const,
    rows: (fixerId: string, planOpId: string, params: Record<string, unknown>) =>
      ['repairs', 'rows', fixerId, planOpId, params] as const,
  },
};

/**
 * Invalidates queries from the server's event stream; polling stays, since the
 * stream gives up after its reconnects. The operation id of `operation.status`
 * is the event's `id` and `data.operation_id` (realtime.SendOperationStatus).
 */
export function startQuerySync(
  manager: Pick<typeof eventSourceManager, 'subscribe'> = eventSourceManager,
  client = queryClient
): () => void {
  return manager.subscribe((ev) => {
    if (ev.type === 'books.changed') {
      void client.invalidateQueries({ queryKey: queryKeys.books.all() });
    } else if (ev.type === 'operation.status') {
      const id = ev.id ?? ev.data?.operation_id;
      if (typeof id === 'string') void client.invalidateQueries({ queryKey: queryKeys.ops.v2(id) });
    }
  });
}
