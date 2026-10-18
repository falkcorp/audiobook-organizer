// file: web/src/lib/queryClient.ts
// version: 1.0.0
// guid: 5d2f8a17-9c4e-4b61-a3d0-7e1b6c94f258
// last-edited: 2026-10-18

import { QueryClient } from '@tanstack/react-query';
import { eventSourceManager } from '../services/eventSourceManager';

type Retry = boolean | ((failureCount: number, err: unknown) => boolean);

/** No retry on a 4xx (an ApiError carries `status`): a 404 or 403 shows at once. */
const retryNon4xx: Retry = (count, err) => {
  const status = (err as { status?: unknown } | null)?.status;
  return count < 1 && !(typeof status === 'number' && status >= 400 && status < 500);
};

/** The decided defaults (07-F3); tests pass `retry: false`. */
export function createQueryClient(retry: Retry = retryNon4xx): QueryClient {
  return new QueryClient({
    defaultOptions: {
      queries: {
        staleTime: 30_000,
        gcTime: 300_000,
        retry,
        refetchOnWindowFocus: false,
        refetchOnReconnect: true,
        throwOnError: false,
      },
      mutations: { retry: 0 },
    },
  });
}

export const queryClient = createQueryClient();

/** Every query key: domain first, parameters object last, never a file_path. */
export const queryKeys = {
  books: { all: () => ['books'] as const },
  ops: { v2: (operationId: string) => ['ops', 'v2', operationId] as const },
  repairs: {
    fixers: () => ['repairs', 'fixers'] as const,
    // Under the fixer list: read, and reloaded, with it.
    ownerStatus: () => ['repairs', 'fixers', 'owner-status'] as const,
    rows: (fixerId: string, planOpId: string, params: object) =>
      ['repairs', 'rows', fixerId, planOpId, params] as const,
  },
};

/**
 * Event stream -> cache. Polls stay: the stream gives up after 5 reconnects.
 * `operation.status` names its op in `id` and `data.operation_id`.
 */
export function startQuerySync(
  manager: Pick<typeof eventSourceManager, 'subscribe'> = eventSourceManager,
  client = queryClient
): () => void {
  return manager.subscribe((ev) => {
    if (ev.type === 'books.changed') void client.invalidateQueries({ queryKey: ['books'] });
    const id = ev.type === 'operation.status' ? (ev.id ?? ev.data?.operation_id) : null;
    if (typeof id === 'string') void client.invalidateQueries({ queryKey: queryKeys.ops.v2(id) });
  });
}
