// file: web/src/hooks/useLibraryQuery.ts
// version: 1.0.0
// guid: d4e5f6a7-b8c9-0123-def0-123456789003
// last-edited: 2026-06-22

import { useState, useEffect, useCallback, useRef } from 'react';
import { useNavigate } from 'react-router-dom';
import * as api from '../services/api';
import { buildCacheKey, useLibraryCache } from '../stores/useLibraryCache';
import { SortField, SortOrder } from '../types';
import type { Audiobook } from '../types';
import type { ParsedSearch } from '../utils/searchParser';
import type { ImportPath } from '../pages/libraryTypes';

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
  const [totalPages, setTotalPages] = useState(1);
  const [softDeletedBooks, setSoftDeletedBooks] = useState<Audiobook[]>([]);
  const [softDeletedCount, setSoftDeletedCount] = useState(0);
  const [softDeletedLoading, setSoftDeletedLoading] = useState(false);

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

  const loadAudiobooks = useCallback(async () => {
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
        return;
      }

      const [page_, folders] = await Promise.all([
        searchText
          ? api.searchBooksPage(searchText, itemsPerPage, offset, filters.showFailed)
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
            }),
        api.getImportPaths(),
      ]);

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
    } catch (error) {
      if (error instanceof api.ApiError && error.status === 401) {
        navigate('/login');
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
      setAudiobooks([]);
      setTotalPages(1);
    } finally {
      setLoading(false);
    }
  }, [buildFieldFilters, debouncedSearch, filters, itemsPerPage, page, parsedSearch, selectedTags, sortBy, sortOrder, navigate, toast, setImportPaths, convertBook]);

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

  return {
    audiobooks,
    setAudiobooks,
    totalCount,
    setTotalCount,
    loading,
    totalPages,
    softDeletedBooks,
    softDeletedCount,
    softDeletedLoading,
    loadAudiobooks,
    loadSoftDeleted,
  };
}
