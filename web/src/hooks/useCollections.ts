// file: web/src/hooks/useCollections.ts
// version: 1.1.0
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
 * own permission) and fail-closed: until auth has initialised, when the server's
 * auth setting is unknown (the status call failed), when authentication is on
 * but nobody is signed in (including a fresh install with no users yet), and
 * when the user's permission list is missing or empty, there are no write
 * controls. The one open case is authentication explicitly OFF
 * (auth_enabled === false): there the server's permission gate is a no-op and
 * every caller may write, so hiding the controls would lock a single-user
 * install out of its own page.
 */
export function useCanManageCollections(): boolean {
  const { initialized, authEnabled, user } = useAuth();
  if (!initialized) return false;
  if (authEnabled === false) return true;
  return user?.permissions?.includes(COLLECTIONS_MANAGE_PERMISSION) ?? false;
}

function messageOf(err: unknown, fallback: string): string {
  return err instanceof Error && err.message ? err.message : fallback;
}

export interface UseCollectionsResult {
  collections: Collection[];
  /** Server-side total, which exceeds collections.length only when the page cap was hit. */
  count: number;
  /**
   * True only while there is nothing for the current filter to show yet (first
   * load, or a switch to another type). A reload of what is already on screen
   * sets `refreshing` instead and keeps the rows mounted.
   */
  loading: boolean;
  /** A reload is in flight and rows for this filter are already on screen. */
  refreshing: boolean;
  /** The list request's failure message; null while loading or on success. Rows from the last success stay. */
  error: string | null;
  canManage: boolean;
  reload: () => Promise<void>;
  create: (input: CollectionCreateInput) => Promise<Collection>;
  update: (id: string, patch: CollectionUpdateInput) => Promise<Collection>;
  remove: (id: string) => Promise<void>;
  materialize: (id: string) => Promise<Collection>;
}

interface ListData {
  type: CollectionType | undefined;
  collections: Collection[];
  count: number;
}

/**
 * The collection list: one fetch on mount (and when the type filter changes),
 * aborted on unmount, with a request counter so a slow earlier response can
 * never overwrite a newer one. Every write refetches, keeping the current rows
 * mounted meanwhile. Mutations reject with the server's error
 * (CollectionConflictError for 409, CollectionUnavailableError for 503) so the
 * caller can show the message where the user is looking.
 */
export function useCollections(type?: CollectionType): UseCollectionsResult {
  const canManage = useCanManageCollections();
  const [data, setData] = useState<ListData | null>(null);
  const [inFlight, setInFlight] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const requestId = useRef(0);
  const controller = useRef<AbortController | null>(null);

  const reload = useCallback(async () => {
    controller.current?.abort();
    const ctl = new AbortController();
    controller.current = ctl;
    const mine = ++requestId.current;
    setInFlight(true);
    setError(null);
    try {
      const res = await listCollections(type, 200, 0, ctl.signal);
      if (mine !== requestId.current) return;
      setData({ type, collections: res.collections, count: res.count });
    } catch (err) {
      if (isAbortError(err) || mine !== requestId.current) return;
      setError(messageOf(err, 'Failed to load collections'));
    } finally {
      if (mine === requestId.current) setInFlight(false);
    }
  }, [type]);

  useEffect(() => {
    let live = true;
    // Started from a microtask (house pattern, see useServerMatchingCount) so no
    // state is set synchronously in the effect body.
    void Promise.resolve().then(() => (live ? reload() : undefined));
    return () => {
      live = false;
      // A request counter, not a DOM node: reading .current at cleanup time is the point.
      // eslint-disable-next-line react-hooks/exhaustive-deps
      requestId.current++;
      controller.current?.abort();
    };
  }, [reload]);

  const hasRows = data !== null && data.type === type;

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

  return {
    collections: hasRows ? data.collections : [],
    count: hasRows ? data.count : 0,
    loading: inFlight && !hasRows,
    refreshing: inFlight && hasRows,
    error,
    canManage,
    reload,
    create,
    update,
    remove,
    materialize,
  };
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
 * write stores the collection the server answered with, unless the route moved
 * to another collection while the write was in flight.
 */
export function useCollection(id: string | undefined): UseCollectionResult {
  const canManage = useCanManageCollections();
  const [collection, setCollection] = useState<Collection | null>(null);
  const [loading, setLoading] = useState(Boolean(id));
  const [error, setError] = useState<string | null>(null);
  const requestId = useRef(0);
  const controller = useRef<AbortController | null>(null);
  const currentId = useRef(id);

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
    currentId.current = id;
    let live = true;
    void Promise.resolve().then(() => {
      if (!live) return undefined;
      setCollection(null);
      return refresh();
    });
    return () => {
      live = false;
      // A request counter, not a DOM node: reading .current at cleanup time is the point.
      // eslint-disable-next-line react-hooks/exhaustive-deps
      requestId.current++;
      controller.current?.abort();
    };
  }, [id, refresh]);

  const store = useCallback(async (forId: string | undefined, p: Promise<Collection>) => {
    const col = await p;
    // The route moved to another collection mid-write: that answer is not for this page.
    if (currentId.current !== forId) return col;
    // A write answer supersedes any in-flight read.
    requestId.current++;
    setLoading(false);
    setCollection(col);
    return col;
  }, []);

  const update = useCallback(
    (patch: CollectionUpdateInput) => store(id, updateCollection(id ?? '', patch)),
    [id, store]
  );
  const materialize = useCallback(() => store(id, materializeCollection(id ?? '')), [id, store]);
  const addBooks = useCallback(
    (bookIds: string[]) => store(id, addBooksToCollection(id ?? '', bookIds)),
    [id, store]
  );
  const removeBook = useCallback(
    (bookId: string) => store(id, removeBookFromCollection(id ?? '', bookId)),
    [id, store]
  );
  const remove = useCallback(async () => {
    await deleteCollection(id ?? '');
  }, [id]);

  return {
    collection,
    loading,
    error,
    canManage,
    refresh,
    update,
    materialize,
    addBooks,
    removeBook,
    remove,
  };
}
