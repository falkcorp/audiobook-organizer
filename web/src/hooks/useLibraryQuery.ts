// file: web/src/hooks/useLibraryQuery.ts
// version: 1.4.0
// guid: d4e5f6a7-b8c9-0123-def0-123456789003
// last-edited: 2026-08-08

import { useState, useEffect, useCallback, useRef } from 'react';
import { useNavigate } from 'react-router-dom';
import * as api from '../services/api';
import { buildCacheKey, useLibraryCache } from '../stores/useLibraryCache';
import { SortField, SortOrder } from '../types';
import type { Audiobook } from '../types';
import type { ParsedSearch } from '../utils/searchParser';
import type { ImportPath } from '../pages/libraryTypes';

/** First retry delay after a failed load. Doubles per consecutive failure. */
const RETRY_BASE_DELAY_MS = 500;
/**
 * Ceiling on the retry backoff. Chosen well under the ~40s memdb warmup window
 * so the Library repopulates within a few seconds of the backend answering,
 * rather than sitting dark for another full backoff period.
 */
const RETRY_MAX_DELAY_MS = 5000;

interface UseLibraryQueryFilters {
  author?: string;
  series?: string;
  genre?: string;
  language?: string;
  libraryState?: string;
  showFailed?: boolean;
  hasFileErrors?: boolean;
  fingerprintStatus?: 'none' | 'complete' | 'partial';
  coveragePercentMin?: number;
  coveragePercentMax?: number;
}

interface UseLibraryQueryParams {
  page: number;
  itemsPerPage: number;
  debouncedSearch: string;
  parsedSearch: ParsedSearch | null;
  filters: UseLibraryQueryFilters;
  selectedTags: string[];
  sortBy: SortField;
  sortOrder: SortOrder;
  activeScanOp: api.Operation | null;
  activeOrganizeOp: api.Operation | null;
  setImportPaths: React.Dispatch<React.SetStateAction<ImportPath[]>>;
  navigate: ReturnType<typeof useNavigate>;
  toast: (message: string, severity?: 'success' | 'error' | 'warning' | 'info', action?: { label: string; onClick: () => void }) => void;
  buildFieldFilters: () => Array<{ field: string; value: string; negated: boolean }>;
  convertBook: (book: api.Book) => Audiobook;
}

export function useLibraryQuery({
  page,
  itemsPerPage,
  debouncedSearch,
  parsedSearch,
  filters,
  selectedTags,
  sortBy,
  sortOrder,
  activeScanOp,
  activeOrganizeOp,
  setImportPaths,
  navigate,
  toast,
  buildFieldFilters,
  convertBook,
}: UseLibraryQueryParams) {
  const [audiobooks, setAudiobooks] = useState<Audiobook[]>([]);
  const [totalCount, setTotalCount] = useState(0);
  const [loading, setLoading] = useState(false);
  // Non-null while the most recent load failed. Consumers MUST consult this
  // before rendering an "empty library" state: an empty `audiobooks` array
  // means "we have nothing to show", which is not the same as "there is
  // nothing to show". The backend is unreachable for ~40s after every deploy
  // while memdb warms over the whole library, so this is routine, not exotic.
  const [loadError, setLoadError] = useState<Error | null>(null);
  // True while a failed load waits on its backoff timer, so the UI can say
  // "reconnecting" instead of showing a silent spinner forever.
  const [isRetrying, setIsRetrying] = useState(false);
  const retryAttemptRef = useRef(0);
  const retryTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const [totalPages, setTotalPages] = useState(1);
  const [softDeletedBooks, setSoftDeletedBooks] = useState<Audiobook[]>([]);
  const [softDeletedCount, setSoftDeletedCount] = useState(0);
  const [softDeletedLoading, setSoftDeletedLoading] = useState(false);

  // Tracks the most recently issued loadAudiobooks call. A response is only
  // applied if it's still the latest one in flight — otherwise a slower,
  // superseded request (e.g. a stale page/offset from just before a
  // page-size or filter change) can resolve after the corrected request and
  // overwrite good data with stale data.
  const latestRequestIdRef = useRef(0);

  // AbortController for the in-flight loadAudiobooks fetch, so a slow tag
  // filter (or any slow query) can be cancelled from the UI. Same pattern as
  // UnifiedDedupTab.tsx / CandidateCompareDrawer.tsx.
  const abortControllerRef = useRef<AbortController | null>(null);

  const loadSoftDeleted = useCallback(async () => {
    setSoftDeletedLoading(true);
    try {
      const { items, count } = await api.getSoftDeletedBooks(10000, 0);
      setSoftDeletedBooks(items);
      setSoftDeletedCount(count);
    } catch (e) {
      console.error('Failed to load soft-deleted books', e);
      setSoftDeletedBooks([]);
      setSoftDeletedCount(0);
    } finally {
      setSoftDeletedLoading(false);
    }
  }, []);

  // Held so scheduleRetry can invoke the latest loadAudiobooks without the two
  // useCallbacks depending on each other (which cannot be expressed directly).
  const loadAudiobooksRef = useRef<(() => Promise<void>) | null>(null);

  const cancelPendingRetry = useCallback(() => {
    if (retryTimerRef.current !== null) {
      clearTimeout(retryTimerRef.current);
      retryTimerRef.current = null;
    }
  }, []);

  /** Clears failure state after a load resolves successfully. */
  const clearLoadFailure = useCallback(() => {
    cancelPendingRetry();
    retryAttemptRef.current = 0;
    setIsRetrying(false);
    setLoadError(null);
  }, [cancelPendingRetry]);

  /**
   * Re-attempts a failed load after an exponential backoff, capped at
   * RETRY_MAX_DELAY_MS. Retries indefinitely by design: the failure modes this
   * exists for (memdb warmup after a deploy, a restart, a brief network drop)
   * all resolve on their own, and giving up would strand the user on a screen
   * that never recovers without a manual refresh. The cap keeps recovery
   * prompt once the backend returns.
   */
  const scheduleRetry = useCallback(() => {
    cancelPendingRetry();
    const delay = Math.min(RETRY_MAX_DELAY_MS, RETRY_BASE_DELAY_MS * 2 ** retryAttemptRef.current);
    retryAttemptRef.current += 1;
    setIsRetrying(true);
    retryTimerRef.current = setTimeout(() => {
      retryTimerRef.current = null;
      void loadAudiobooksRef.current?.();
    }, delay);
  }, [cancelPendingRetry]);

  const loadAudiobooks = useCallback(async () => {
    const requestId = ++latestRequestIdRef.current;
    abortControllerRef.current?.abort();
    const controller = new AbortController();
    abortControllerRef.current = controller;
    setLoading(true);
    try {
      const offset = (page - 1) * itemsPerPage;
      const fieldFilters = buildFieldFilters();
      const searchText = parsedSearch ? parsedSearch.freeText : debouncedSearch;
      let tagsParam: string[] | undefined;
      if (selectedTags && selectedTags.length > 0) {
        tagsParam = selectedTags;
      } else {
        const parsedTag = parsedSearch?.fieldFilters.find((f) => f.field === 'tag' && !f.negated)?.value;
        if (parsedTag) tagsParam = [parsedTag];
      }

      // 'deleted' is a client-side concept (marked_for_deletion flag); send no library_state to server
      const libraryState = filters.libraryState === 'deleted' ? undefined : filters.libraryState;

      // Check cache before fetching
      const filterStr = JSON.stringify({ fieldFilters, tagsParam, libraryState, showFailed: filters.showFailed, hasFileErrors: filters.hasFileErrors, fingerprintStatus: filters.fingerprintStatus, coveragePercentMin: filters.coveragePercentMin, coveragePercentMax: filters.coveragePercentMax });
      const cacheKey = buildCacheKey(page, itemsPerPage, searchText, filterStr, sortBy, sortOrder);
      const cached = useLibraryCache.getState().getCached(cacheKey);
      if (cached) {
        setAudiobooks(cached.audiobooks);
        setTotalCount(cached.totalCount);
        setTotalPages(cached.totalPages);
        setImportPaths(cached.importPaths);
        setLoading(false);
        // A cache hit is a resolved load, so any prior failure is over.
        clearLoadFailure();
        return;
      }

      const [page_, folders] = await Promise.all([
        searchText
          ? api.searchBooksPage(searchText, itemsPerPage, offset, filters.showFailed, controller.signal)
          : api.getBooks(itemsPerPage, offset, {
              sortBy,
              sortOrder,
              tags: tagsParam,
              libraryState,
              filters: fieldFilters.length > 0 ? JSON.stringify(fieldFilters) : undefined,
              showFailed: filters.showFailed,
              hasFileErrors: filters.hasFileErrors,
              fingerprintStatus: filters.fingerprintStatus,
              coveragePercentMin: filters.coveragePercentMin,
              coveragePercentMax: filters.coveragePercentMax,
              signal: controller.signal,
            }),
        api.getImportPaths(controller.signal),
      ]);

      // A newer loadAudiobooks call has since been issued (e.g. page size or
      // filters changed again while this request was in flight) — its
      // response, not this stale one, should win. Drop this result.
      if (requestId !== latestRequestIdRef.current) {
        return;
      }

      const items = page_.items;
      const serverCount = page_.count;

      let convertedBooks: Audiobook[] = items.map(convertBook);

      // Client-side filter for deleted state (marked_for_deletion flag, no server equivalent)
      if (filters.libraryState === 'deleted') {
        convertedBooks = convertedBooks.filter((book) => book.marked_for_deletion);
      }

      const total = serverCount ?? convertedBooks.length;
      const totalPages = Math.max(1, Math.ceil(total / itemsPerPage));
      const importPathsData = folders.map((folder) => ({
        id: folder.id,
        path: folder.path,
        status: 'idle' as const,
        book_count: folder.book_count,
      }));

      // Cache the results
      useLibraryCache.getState().setCached(cacheKey, {
        audiobooks: convertedBooks,
        totalCount: total,
        totalPages,
        importPaths: importPathsData,
      });

      setAudiobooks(convertedBooks);
      setTotalCount(total);
      setTotalPages(totalPages);
      setImportPaths(importPathsData);
      // Success: this result is authoritative, so an empty list here really
      // does mean an empty library and the empty state may render.
      clearLoadFailure();
    } catch (error) {
      // A cancelled fetch (user clicked Cancel, or a newer request superseded
      // this one and aborted it) is not a failure — skip the error toast/
      // empty-state handling entirely. loading/audiobooks state is already
      // whatever cancelLoad() or the newer request set it to.
      if (error instanceof Error && error.name === 'AbortError') {
        return;
      }
      if (error instanceof api.ApiError && error.status === 401) {
        navigate('/login');
        return;
      }
      if (requestId !== latestRequestIdRef.current) {
        return;
      }
      if (error instanceof api.ApiError && error.status >= 500) {
        toast('Server error occurred.', 'error');
      }
      const message = error instanceof Error ? error.message : 'Failed to load audiobooks.';
      if (message.toLowerCase().includes('timeout')) {
        toast('Request timed out.', 'error');
      }
      console.error('Failed to load audiobooks:', error);
      // Deliberately do NOT clear `audiobooks` here. Emptying the list on
      // failure is what made a transient backend outage render as "No
      // Audiobooks Found" — the most alarming possible way to display a
      // temporary error to someone with a 44,000-book library. Keeping the
      // last known-good page means a mid-session blip leaves the shelf intact,
      // and `loadError` tells the UI to say "reconnecting" over the top.
      setLoadError(error instanceof Error ? error : new Error(String(error)));
      // A 4xx is a real client-side problem and retrying cannot fix it.
      // Anything else — network failure, connection refused, 5xx, or the
      // memdb-warmup window after a deploy — is transient by assumption.
      const isTransient =
        !(error instanceof api.ApiError) || error.status === 0 || error.status >= 500;
      if (isTransient) {
        scheduleRetry();
      }
    } finally {
      if (requestId === latestRequestIdRef.current) {
        setLoading(false);
      }
    }
  }, [buildFieldFilters, debouncedSearch, filters, itemsPerPage, page, parsedSearch, selectedTags, sortBy, sortOrder, navigate, toast, setImportPaths, convertBook, clearLoadFailure, scheduleRetry]);

  // Keep the retry timer pointed at the current closure, so a retry that fires
  // after filters/page changed re-runs the CURRENT query rather than the stale
  // one that happened to fail.
  useEffect(() => {
    loadAudiobooksRef.current = loadAudiobooks;
  }, [loadAudiobooks]);

  // Reload books when scan/organize completes
  useEffect(() => {
    if (activeScanOp?.status === 'completed' || activeScanOp?.status === 'failed') {
      loadAudiobooks();
    }
  }, [activeScanOp?.status, loadAudiobooks]);

  useEffect(() => {
    if (activeOrganizeOp?.status === 'completed' || activeOrganizeOp?.status === 'failed') {
      loadAudiobooks();
    }
  }, [activeOrganizeOp?.status, loadAudiobooks]);

  // Auto-refresh books every 10s while a scan is active
  const isUnmountedRef = useRef(false);
  useEffect(() => {
    isUnmountedRef.current = false;
    if (!activeScanOp || activeScanOp.status === 'completed' || activeScanOp.status === 'failed') {
      return;
    }
    const interval = window.setInterval(() => {
      if (!isUnmountedRef.current) {
        loadAudiobooks();
      }
    }, 10000);
    return () => {
      isUnmountedRef.current = true;
      window.clearInterval(interval);
    };
  }, [activeScanOp, loadAudiobooks]);

  // clearLibraryCache drops every cached page. Call before loadAudiobooks()
  // after any mutation that hard/soft-deletes, merges, or combines books —
  // otherwise the next reload can serve a stale cached page (same
  // page/itemsPerPage/search/filter/sort key) that still lists books which
  // no longer exist, until the cache's own TTL expires on its own.
  const clearLibraryCache = useCallback(() => {
    useLibraryCache.getState().clear();
  }, []);

  // cancelLoad aborts the in-flight loadAudiobooks fetch and flips loading
  // off immediately — don't wait for the aborted promise to reject and run
  // through its own finally block, since that adds a round trip's worth of
  // latency to what should feel instant.
  const cancelLoad = useCallback(() => {
    abortControllerRef.current?.abort();
    setLoading(false);
    // An explicit cancel must also stop the retry loop, or the request the
    // user just cancelled would quietly come back a second later.
    cancelPendingRetry();
    retryAttemptRef.current = 0;
    setIsRetrying(false);
  }, [cancelPendingRetry]);

  // Never leave a retry timer running past unmount.
  useEffect(() => cancelPendingRetry, [cancelPendingRetry]);

  return {
    audiobooks,
    setAudiobooks,
    totalCount,
    setTotalCount,
    loading,
    loadError,
    isRetrying,
    totalPages,
    softDeletedBooks,
    softDeletedCount,
    softDeletedLoading,
    loadAudiobooks,
    loadSoftDeleted,
    clearLibraryCache,
    cancelLoad,
  };
}
