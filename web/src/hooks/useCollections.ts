// file: web/src/hooks/useCollections.ts
// version: 1.0.0
// guid: 2672340e-bb50-4079-a288-928e62a50163
// last-edited: 2026-10-18

import { useCallback, useEffect, useRef, useState } from 'react';
import { useAuth } from '../contexts/AuthContext';
import { isAbortError } from '../utils/apiFetch';
import {
  type Collection,
  type CollectionCreateInput,
  type CollectionType,
  type CollectionUpdateInput,
  addBooksToCollection,
  createCollection,
  deleteCollection,
  getCollection,
  listCollections,
  materializeCollection,
  removeBookFromCollection,
  updateCollection,
} from '../services/collectionApi';

export const COLLECTIONS_MANAGE_PERMISSION = 'collections.manage';

/**
 * Whether the caller may write collections. Advisory (every route enforces its
 * own permission) and fail-closed: until auth has initialised, or when the
 * signed-in user's permission list is missing or empty, there are no write
 * controls. The one exception is an install with authentication OFF: there the
 * server's permission gate is a no-op and every caller may write, so hiding the
 * controls would lock a single-user install out of its own page.
 */
export function useCanManageCollections(): boolean {
  const { initialized, requiresAuth, user } = useAuth();
  if (!initialized) return false;
  if (!requiresAuth) return true;
  return user?.permissions?.includes(COLLECTIONS_MANAGE_PERMISSION) ?? false;
}

function messageOf(err: unknown, fallback: string): string {
  return err instanceof Error && err.message ? err.message : fallback;
}

export interface UseCollectionsResult {
  collections: Collection[];
  /** Server-side total, which exceeds collections.length only when the page cap was hit. */
  count: number;
  loading: boolean;
  /** The list request's failure message; null while loading or on success. */
  error: string | null;
  canManage: boolean;
  reload: () => Promise<void>;
  create: (input: CollectionCreateInput) => Promise<Collection>;
  update: (id: string, patch: CollectionUpdateInput) => Promise<Collection>;
  remove: (id: string) => Promise<void>;
  materialize: (id: string) => Promise<Collection>;
}

/**
 * The collection list: one fetch on mount (and when the type filter changes),
 * aborted on unmount, with a request counter so a slow earlier response can
 * never overwrite a newer one. Every write refetches. Mutations reject with the
 * server's error (CollectionConflictError for 409, CollectionUnavailableError
 * for 503) so the caller can show the message where the user is looking.
 */
export function useCollections(type?: CollectionType): UseCollectionsResult {
  const canManage = useCanManageCollections();
  const [collections, setCollections] = useState<Collection[]>([]);
  const [count, setCount] = useState(0);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const requestId = useRef(0);
  const controller = useRef<AbortController | null>(null);

  const reload = useCallback(async () => {
    controller.current?.abort();
    const ctl = new AbortController();
    controller.current = ctl;
    const mine = ++requestId.current;
    setLoading(true);
    setError(null);
    try {
      const res = await listCollections(type, 200, 0, ctl.signal);
      if (mine !== requestId.current) return;
      setCollections(res.collections);
      setCount(res.count);
    } catch (err) {
      if (isAbortError(err) || mine !== requestId.current) return;
      setCollections([]);
      setCount(0);
      setError(messageOf(err, 'Failed to load collections'));
    } finally {
      if (mine === requestId.current) setLoading(false);
    }
  }, [type]);

  useEffect(() => {
    void reload();
    return () => {
      requestId.current++;
      controller.current?.abort();
    };
  }, [reload]);

  const create = useCallback(
    async (input: CollectionCreateInput) => {
      const created = await createCollection(input);
      await reload();
      return created;
    },
    [reload]
  );
  const update = useCallback(
    async (id: string, patch: CollectionUpdateInput) => {
      const updated = await updateCollection(id, patch);
      await reload();
      return updated;
    },
    [reload]
  );
  const remove = useCallback(
    async (id: string) => {
      await deleteCollection(id);
      await reload();
    },
    [reload]
  );
  const materialize = useCallback(
    async (id: string) => {
      const col = await materializeCollection(id);
      await reload();
      return col;
    },
    [reload]
  );

  return { collections, count, loading, error, canManage, reload, create, update, remove, materialize };
}

export interface UseCollectionResult {
  collection: Collection | null;
  loading: boolean;
  /** Failure to load the collection; null otherwise. */
  error: string | null;
  canManage: boolean;
  /** GET again. For a dynamic collection the server re-evaluates the query. */
  refresh: () => Promise<void>;
  update: (patch: CollectionUpdateInput) => Promise<Collection>;
  materialize: () => Promise<Collection>;
  addBooks: (bookIds: string[]) => Promise<Collection>;
  removeBook: (bookId: string) => Promise<Collection>;
  remove: () => Promise<void>;
}

/**
 * One collection for the detail page. Same guards as useCollections: a single
 * fetch on mount/id change, abort on unmount, stale-response counter. Each
 * write stores the collection the server answered with.
 */
export function useCollection(id: string | undefined): UseCollectionResult {
  const canManage = useCanManageCollections();
  const [collection, setCollection] = useState<Collection | null>(null);
  const [loading, setLoading] = useState(Boolean(id));
  const [error, setError] = useState<string | null>(null);
  const requestId = useRef(0);
  const controller = useRef<AbortController | null>(null);

  const refresh = useCallback(async () => {
    if (!id) return;
    controller.current?.abort();
    const ctl = new AbortController();
    controller.current = ctl;
    const mine = ++requestId.current;
    setLoading(true);
    setError(null);
    try {
      const col = await getCollection(id, ctl.signal);
      if (mine !== requestId.current) return;
      setCollection(col);
    } catch (err) {
      if (isAbortError(err) || mine !== requestId.current) return;
      setError(messageOf(err, 'Failed to load collection'));
    } finally {
      if (mine === requestId.current) setLoading(false);
    }
  }, [id]);

  useEffect(() => {
    setCollection(null);
    void refresh();
    return () => {
      requestId.current++;
      controller.current?.abort();
    };
  }, [refresh]);

  const store = useCallback(async (p: Promise<Collection>) => {
    const col = await p;
    // A write answer supersedes any in-flight read.
    requestId.current++;
    setLoading(false);
    setCollection(col);
    return col;
  }, []);

  const update = useCallback(
    (patch: CollectionUpdateInput) => store(updateCollection(id ?? '', patch)),
    [id, store]
  );
  const materialize = useCallback(() => store(materializeCollection(id ?? '')), [id, store]);
  const addBooks = useCallback(
    (bookIds: string[]) => store(addBooksToCollection(id ?? '', bookIds)),
    [id, store]
  );
  const removeBook = useCallback(
    (bookId: string) => store(removeBookFromCollection(id ?? '', bookId)),
    [id, store]
  );
  const remove = useCallback(async () => {
    await deleteCollection(id ?? '');
  }, [id]);

  return { collection, loading, error, canManage, refresh, update, materialize, addBooks, removeBook, remove };
}
