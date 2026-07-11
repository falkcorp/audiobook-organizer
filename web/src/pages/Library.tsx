// file: web/src/pages/Library.tsx
// version: 1.77.0
// guid: 3f4a5b6c-7d8e-9f0a-1b2c-3d4e5f6a7b8c
// last-edited: 2026-07-03

import { useState, useEffect, useCallback, useRef } from 'react';
import { useNavigate, useSearchParams } from 'react-router-dom';
import {
  Box,
  Button,
  Dialog,
  DialogTitle,
  DialogContent,
  DialogActions,
  TextField,
} from '@mui/material';
import RefreshIcon from '@mui/icons-material/Refresh';
import CachedIcon from '@mui/icons-material/Cached';
import { ViewMode } from '../components/audiobooks/SearchBar';
import { useColumnConfig } from '../hooks/useColumnConfig';
import { useLibraryFilters } from '../hooks/useLibraryFilters';
import { useLibraryQuery } from '../hooks/useLibraryQuery';
import { useLibrarySelection } from '../hooks/useLibrarySelection';
import { useToast } from '../components/toast/ToastProvider';
import type { Audiobook } from '../types';
import { SortField, SortOrder } from '../types';
import { parseSearch, type ParsedSearch } from '../utils/searchParser';
import * as api from '../services/api';
import type { SavedFilterPreset } from '../services/api';
import {
  eventSourceManager,
  type EventSourceEvent,
  type EventSourceStatus,
} from '../services/eventSourceManager';
import { pollOperation } from '../utils/operationPolling';
import { useOperationsStore } from '../stores/useOperationsStore';
import { withOptimisticOperation } from '../utils/withOptimisticOperation';
import { STORAGE_KEYS } from '../lib/storageKeys';
import { LibraryToolbar } from '../components/library/LibraryToolbar';
import { LibraryBookGrid } from '../components/library/LibraryBookGrid';
import { LibraryDialogs } from '../components/library/LibraryDialogs';
import type {
  ImportPath,
  BulkActionResult,
  BulkActionProgress,
  DuplicateAction,
  DuplicateDialogState,
  OrganizeErrorState,
} from './libraryTypes';
import { evictOldestOpLogKey, MAX_OPERATION_LOG_KEYS } from './libraryOperationLogs';

// Types ImportPath, BulkActionResult, BulkActionProgress, DuplicateAction,
// DuplicateDialogState, OrganizeErrorState imported from './libraryTypes'

const convertApiBook = (book: api.Book): Audiobook => ({
  id: book.id,
  title: book.title,
  author: book.author_name || 'Unknown',
  narrator: book.narrator,
  series: book.series_name,
  series_number: book.series_position,
  genre: book.genre,
  language: book.language,
  publisher: book.publisher,
  edition: book.edition,
  description: book.description,
  audiobook_release_year: book.audiobook_release_year,
  year: book.audiobook_release_year || book.print_year,
  print_year: book.print_year,
  isbn10: book.isbn10,
  isbn13: book.isbn13,
  duration_seconds: book.duration,
  cover_url: book.cover_url,
  file_path: book.file_path,
  original_filename: book.original_filename,
  itunes_path: book.itunes_path,
  format: book.format || book.file_path.split('.').pop()?.toUpperCase() || 'Unknown',
  file_size_bytes: book.file_size,
  quality: book.quality,
  bitrate_kbps: book.bitrate,
  codec: book.codec,
  sample_rate_hz: book.sample_rate,
  channels: book.channels,
  bit_depth: book.bit_depth,
  file_hash: book.file_hash,
  is_primary_version: book.is_primary_version,
  version_group_id: book.version_group_id,
  version_notes: book.version_notes,
  created_at: book.created_at,
  updated_at: book.updated_at,
  library_state: book.library_state,
  metadata_review_status: book.metadata_review_status,
  metadata_updated_at: book.metadata_updated_at,
  last_written_at: book.last_written_at,
  marked_for_deletion: book.marked_for_deletion,
  marked_for_deletion_at: book.marked_for_deletion_at,
  original_file_hash: book.original_file_hash,
  organized_file_hash: book.organized_file_hash,
  organize_error: book.organize_error,
  work_id: book.work_id,
});

const buildHashCandidates = (book: Audiobook): string[] => {
  const hashes: string[] = [];
  if (book.file_hash) hashes.push(book.file_hash);
  if (book.original_file_hash) hashes.push(book.original_file_hash);
  if (book.organized_file_hash) hashes.push(book.organized_file_hash);
  return hashes;
};

// getResultLabel is defined in ./libraryTypes and used by LibraryDialogs

interface LibraryProps {
  defaultPreset?: 'fingerprints' | 'standard';
}

export const Library = ({ defaultPreset = 'standard' }: LibraryProps) => {
  const [searchParams, setSearchParams] = useSearchParams();
  const navigate = useNavigate();
  const { toast } = useToast();
  const initialSearch = searchParams.get('search') ?? '';
  const initialViewMode = (searchParams.get('view') as ViewMode) || ('grid' as ViewMode);
  const initialSortBy = ((): SortField => {
    const value = searchParams.get('sort');
    if (value && Object.values(SortField).includes(value as SortField)) {
      return value as SortField;
    }
    return SortField.Title;
  })();
  const initialSortOrder =
    searchParams.get('order') === SortOrder.Descending ? SortOrder.Descending : SortOrder.Ascending;
  const initialPage = Math.max(
    1,
    parseInt(searchParams.get('page') || localStorage.getItem(STORAGE_KEYS.LIBRARY_PAGE) || '1', 10)
  );
  // Clamp both ends: an unclamped ?limit= URL (or stale localStorage) can
  // request the whole 44K-book library into the DOM — same OOM class as the
  // useLibraryCache leak. 1000 is the largest offered page-size option.
  const initialItemsPerPage = Math.min(
    1000,
    Math.max(
      10,
      parseInt(
        searchParams.get('limit') ||
          localStorage.getItem(STORAGE_KEYS.LIBRARY_ITEMS_PER_PAGE) ||
          '20',
        10
      ) || 20
    )
  );
  const [searchQuery, setSearchQuery] = useState(initialSearch);
  const [debouncedSearch, setDebouncedSearch] = useState('');
  const [viewMode, setViewMode] = useState<ViewMode>(initialViewMode);
  const [sortBy, setSortBy] = useState<SortField>(initialSortBy);
  const [sortOrder, setSortOrder] = useState<SortOrder>(initialSortOrder);
  const [page, setPage] = useState(initialPage);
  const [itemsPerPage, setItemsPerPage] = useState(initialItemsPerPage);
  const [editingAudiobook, setEditingAudiobook] = useState<Audiobook | null>(null);
  const [batchEditOpen, setBatchEditOpen] = useState(false);
  const [versionManagementOpen, setVersionManagementOpen] = useState(false);
  const [versionManagingAudiobook, setVersionManagingAudiobook] = useState<Audiobook | null>(null);

  const {
    filterOpen,
    setFilterOpen,
    filters,
    handleFiltersChange: baseHandleFiltersChange,
    selectedTags,
    setSelectedTags,
    handleTagFilterChange,
    refreshTags,
    availableAuthors,
    availableSeries,
    availableGenres,
    availableLanguages,
    availableTags,
    getActiveFilterCount,
  } = useLibraryFilters({ searchParams, onFiltersChange: () => setPage(1) });
  const [parsedSearch, setParsedSearch] = useState<ParsedSearch>(() => parseSearch(initialSearch));
  const [bulkTagDialogOpen, setBulkTagDialogOpen] = useState(false);
  const [bulkRatingDialogOpen, setBulkRatingDialogOpen] = useState(false);

  // Saved filter presets (USER-QUICK-FILTERS)
  const [savedPresets, setSavedPresets] = useState<SavedFilterPreset[]>([]);
  const [presetDialogOpen, setPresetDialogOpen] = useState(false);
  const [presetDialogName, setPresetDialogName] = useState('');
  const [editingPresetId, setEditingPresetId] = useState<string | null>(null);

  useEffect(() => {
    api
      .getSavedFilterPresets()
      .then(setSavedPresets)
      .catch((err) => {
        console.error('Failed to load saved filter presets:', err);
      });
  }, []);

  const handleOpenSavePresetDialog = useCallback(() => {
    setEditingPresetId(null);
    setPresetDialogName('');
    setPresetDialogOpen(true);
  }, []);

  const handleOpenRenamePresetDialog = useCallback((preset: SavedFilterPreset) => {
    setEditingPresetId(preset.id);
    setPresetDialogName(preset.name);
    setPresetDialogOpen(true);
  }, []);

  const handleClosePresetDialog = useCallback(() => {
    setPresetDialogOpen(false);
  }, []);

  const handleConfirmPresetDialog = useCallback(async () => {
    const name = presetDialogName.trim();
    if (!name) return;
    let updated: SavedFilterPreset[];
    if (editingPresetId) {
      updated = savedPresets.map((p) => (p.id === editingPresetId ? { ...p, name } : p));
    } else {
      const newPreset: SavedFilterPreset = {
        id:
          typeof crypto !== 'undefined' && 'randomUUID' in crypto
            ? crypto.randomUUID()
            : `preset-${Date.now()}-${Math.random().toString(36).slice(2)}`,
        name,
        filters,
        selectedTags,
      };
      updated = [...savedPresets, newPreset];
    }
    try {
      await api.saveSavedFilterPresets(updated);
      setSavedPresets(updated);
      setPresetDialogOpen(false);
      toast(editingPresetId ? 'Preset renamed' : 'Filter preset saved', 'success');
    } catch (err) {
      console.error('Failed to save filter preset:', err);
      toast('Failed to save filter preset', 'error');
    }
  }, [presetDialogName, editingPresetId, savedPresets, filters, selectedTags, toast]);

  const handleApplyPreset = useCallback(
    (preset: SavedFilterPreset) => {
      baseHandleFiltersChange(preset.filters);
      if (preset.selectedTags) {
        setSelectedTags(preset.selectedTags);
      }
    },
    [baseHandleFiltersChange, setSelectedTags]
  );

  const handleDeletePreset = useCallback(
    async (preset: SavedFilterPreset) => {
      const updated = savedPresets.filter((p) => p.id !== preset.id);
      try {
        await api.saveSavedFilterPresets(updated);
        setSavedPresets(updated);
        toast('Filter preset deleted', 'success');
      } catch (err) {
        console.error('Failed to delete filter preset:', err);
        toast('Failed to delete filter preset', 'error');
      }
    },
    [savedPresets, toast]
  );

  // Column config
  const {
    columns: columnDefs,
    visibleColumnIds,
    columnWidths,
    toggleColumn,
    resizeColumn,
    resetToDefaults: resetColumnsToDefaults,
  } = useColumnConfig(defaultPreset);

  // Import path management
  const [importPaths, setImportPaths] = useState<ImportPath[]>([]);
  const [importPathsExpanded, setImportPathsExpanded] = useState(false);
  const [addPathDialogOpen, setAddPathDialogOpen] = useState(false);
  const [newImportPath, setNewImportPath] = useState('');
  const [showServerBrowser, setShowServerBrowser] = useState(false);
  const [systemStatus, setSystemStatus] = useState<api.SystemStatus | null>(null);
  const [organizeRunning, setOrganizeRunning] = useState(false);
  const [activeScanOp, setActiveScanOp] = useState<api.Operation | null>(null);
  const [activeOrganizeOp, setActiveOrganizeOp] = useState<api.Operation | null>(null);

  const [storageDrawerOpen, setStorageDrawerOpen] = useState(false);
  const [operationLogs, setOperationLogs] = useState<
    Record<
      string,
      {
        level: string;
        message: string;
        details?: string;
        timestamp: number;
        expanded?: boolean;
      }[]
    >
  >({});
  const logContainerRefs = useRef<Record<string, HTMLDivElement | null>>({});
  const bulkFetchCancelRef = useRef(false);
  const bulkOrganizeCancelRef = useRef(false);
  const [softDeletedExpanded, setSoftDeletedExpanded] = useState(false);
  const [deleteDialogOpen, setDeleteDialogOpen] = useState(false);
  const [bookPendingDelete, setBookPendingDelete] = useState<Audiobook | null>(null);
  const [deleteOptions, setDeleteOptions] = useState({
    softDelete: true,
    blockHash: true,
  });
  const [deleteInProgress, setDeleteInProgress] = useState(false);
  const [purgeDialogOpen, setPurgeDialogOpen] = useState(false);
  const [purgeDeleteFiles, setPurgeDeleteFiles] = useState(false);
  const [purgeInProgress, setPurgeInProgress] = useState(false);
  const [purgingBookId, setPurgingBookId] = useState<string | null>(null);
  const [restoringBookId, setRestoringBookId] = useState<string | null>(null);
  const [batchDeleteDialogOpen, setBatchDeleteDialogOpen] = useState(false);
  const [batchDeleteInProgress, setBatchDeleteInProgress] = useState(false);
  const [batchRestoreInProgress, setBatchRestoreInProgress] = useState(false);
  const [mergeDialogOpen, setMergeDialogOpen] = useState(false);
  const [combineDialogOpen, setCombineDialogOpen] = useState(false);
  const [combineInProgress, setCombineInProgress] = useState(false);
  const [combineOverrideTitle, setCombineOverrideTitle] = useState('');
  const [combineOverrideAuthor, setCombineOverrideAuthor] = useState('');
  const [combineOverrideNarrator, setCombineOverrideNarrator] = useState('');
  const [batchPlaylistOpen, setBatchPlaylistOpen] = useState(false);
  const [mergePrimaryId, setMergePrimaryId] = useState<string>('');
  // pendingFetchOpId tracks the in-flight metadata fetch so we can
  // auto-open the review dialog when it completes.
  const [pendingFetchOpId, setPendingFetchOpId] = useState<string | null>(null);
  const [metadataReviewOpen, setMetadataReviewOpen] = useState(!!searchParams.get('reviewOp'));
  const [mergeInProgress, setMergeInProgress] = useState(false);
  const sseStatusRef = useRef<EventSourceStatus['state'] | null>(null);

  const [importFileDialogOpen, setImportFileDialogOpen] = useState(false);
  const [importFilePath, setImportFilePath] = useState('');
  const [importFilePaths, setImportFilePaths] = useState<string[]>([]);
  const [importFileOrganize, setImportFileOrganize] = useState(true);
  const [importFileInProgress, setImportFileInProgress] = useState(false);
  const [manualImportDialogOpen, setManualImportDialogOpen] = useState(false);
  const [manualImportPath, setManualImportPath] = useState('');
  const [manualImportError, setManualImportError] = useState<string | null>(null);
  const [manualImportInProgress, setManualImportInProgress] = useState(false);
  const [manualImportOp, setManualImportOp] = useState<api.OperationV2 | null>(null);

  // Per-action loading states for dynamic UI
  const [scanningAll, setScanningAll] = useState(false);
  const [scanningPathId, setScanningPathId] = useState<string | null>(null);
  const [removingPathId, setRemovingPathId] = useState<string | null>(null);

  const [bulkFetchDialogOpen, setBulkFetchDialogOpen] = useState(false);
  const [bulkSearchOpen, setBulkSearchOpen] = useState(false);
  const [bulkFetchInProgress] = useState(false);
  const [bulkFetchProgress, setBulkFetchProgress] = useState<BulkActionProgress | null>(null);
  const [bulkOrganizeDialogOpen, setBulkOrganizeDialogOpen] = useState(false);
  const [bulkOrganizeInProgress, setBulkOrganizeInProgress] = useState(false);
  const [bulkOrganizeProgress, setBulkOrganizeProgress] = useState<BulkActionProgress | null>(null);
  const [bulkWriteBackDialogOpen, setBulkWriteBackDialogOpen] = useState(false);
  const [bulkWriteBackInProgress, setBulkWriteBackInProgress] = useState(false);
  const [bulkWriteBackRename, setBulkWriteBackRename] = useState(false);
  const [bulkWriteBackForce, setBulkWriteBackForce] = useState(false);
  const [bulkWriteBackResult, setBulkWriteBackResult] = useState<api.BatchWriteBackResponse | null>(
    null
  );
  const [bulkSaveAllDialogOpen, setBulkSaveAllDialogOpen] = useState(false);
  const [bulkSaveAllEstimate] = useState<number | null>(null);
  const [bulkSaveAllRename, setBulkSaveAllRename] = useState(false);
  const [bulkSaveAllStarting, setBulkSaveAllStarting] = useState(false);
  const [duplicateDialog, setDuplicateDialog] = useState<DuplicateDialogState | null>(null);
  const duplicateResolverRef = useRef<((action: DuplicateAction) => void) | null>(null);
  const [bulkOrganizeError, setBulkOrganizeError] = useState<OrganizeErrorState | null>(null);
  const bulkOrganizeSnapshotRef = useRef<Map<string, Audiobook>>(new Map());
  const pollingCleanupRef = useRef<(() => void) | null>(null);
  const manualImportPollCleanupRef = useRef<(() => void) | null>(null);

  // Cleanup polling on unmount
  useEffect(() => {
    return () => {
      if (pollingCleanupRef.current) {
        pollingCleanupRef.current();
        pollingCleanupRef.current = null;
      }
      if (manualImportPollCleanupRef.current) {
        manualImportPollCleanupRef.current();
        manualImportPollCleanupRef.current = null;
      }
    };
  }, []);

  // SSE subscription for live operation progress & logs + historical hydration
  useEffect(() => {
    // Hydrate UI from v2 store on reload (UOS-13: no v1 getActiveOperations call).
    // The store is already populated via SSE + loadFromServer at app level.
    (async () => {
      const active = useOperationsStore.getState().activeOperations;
      for (const op of active) {
        const partial: api.Operation = {
          id: op.id,
          type: op.type,
          status: op.status,
          progress: op.progress,
          total: op.total,
          message: op.message,
          created_at: new Date().toISOString(),
        } as api.Operation;
        if (op.type === 'scan') setActiveScanOp(partial);
        if (op.type === 'organize') setActiveOrganizeOp(partial);
        // Hydrate historical tail logs (last 100)
        try {
          const hist = await api.getOperationLogsTail(op.id, 100);
          if (hist && hist.length) {
            setOperationLogs((prev) => {
              const capped = evictOldestOpLogKey(prev, op.id, MAX_OPERATION_LOG_KEYS);
              return {
                ...capped,
                [op.id]: hist.map((h: api.OperationLog) => ({
                  level: h.level,
                  message: h.message,
                  details: h.details,
                  timestamp: Date.parse(h.created_at) || Date.now(),
                })),
              };
            });
          }
        } catch (_e) {
          // ignore hydration errors
        }
      }
    })();

    const unsubscribe = eventSourceManager.subscribe(
      (evt: EventSourceEvent) => {
        if (!evt || !evt.type) return;
        if (evt.type === 'heartbeat') return; // Ignore heartbeat messages

        if (evt.type === 'operation.log' && evt.data?.operation_id) {
          const opId = String(evt.data.operation_id);
          setOperationLogs((prev) => {
            const capped = evictOldestOpLogKey(prev, opId, MAX_OPERATION_LOG_KEYS);
            const existing = capped[opId] || [];
            const next = [
              ...existing,
              {
                level: String(evt.data?.level ?? 'info'),
                message: String(evt.data?.message ?? ''),
                details: evt.data?.details as string | undefined,
                timestamp: Date.now(),
              },
            ];
            return { ...capped, [opId]: next.slice(-200) };
          });
        } else if (evt.type === 'operation.progress' && evt.data?.operation_id) {
          const opId = String(evt.data.operation_id);
          const update = (op: api.Operation | null): api.Operation | null => {
            if (!op || op.id !== opId) return op;
            return {
              ...op,
              progress: Number(evt.data?.current ?? 0),
              total: Number(evt.data?.total ?? 0),
              message: String(evt.data?.message ?? ''),
            };
          };
          setActiveScanOp((prev) => update(prev));
          setActiveOrganizeOp((prev) => update(prev));
        } else if (evt.type === 'operation.status' && evt.data?.operation_id) {
          const opId = String(evt.data.operation_id);
          const status = String(evt.data?.status ?? '');
          const finalize = (op: api.Operation | null): api.Operation | null => {
            if (!op || op.id !== opId) return op;
            return { ...op, status };
          };
          setActiveScanOp((prev) => finalize(prev));
          setActiveOrganizeOp((prev) => finalize(prev));
        }
      },
      (status: EventSourceStatus) => {
        const previousState = sseStatusRef.current;
        sseStatusRef.current = status.state;

        if (
          (status.state === 'reconnecting' || status.state === 'error') &&
          previousState !== status.state
        ) {
          toast('Connection lost. Reconnecting...', 'warning');
        } else if (status.state === 'open') {
          if (previousState && previousState !== 'open') {
            toast('Connection restored.', 'success');
          }
        }

        if (status.state === 'reconnecting' && status.delayMs) {
          console.warn(
            `EventSource connection lost (attempt ${status.attempt}), reconnecting in ${Math.round(status.delayMs / 1000)}s...`
          );
        }
      }
    );

    return () => {
      unsubscribe();
    };
  }, [toast]);

  // Reset loading states when operations complete (reload handled after loadAudiobooks definition)
  useEffect(() => {
    if (activeScanOp?.status === 'completed' || activeScanOp?.status === 'failed') {
      setScanningAll(false);
      setScanningPathId(null);
    }
  }, [activeScanOp?.status]);

  useEffect(() => {
    if (activeOrganizeOp?.status === 'completed' || activeOrganizeOp?.status === 'failed') {
      setOrganizeRunning(false);
    }
  }, [activeOrganizeOp?.status]);

  // Auto-scroll effect when logs update (placed at component top-level, not inside JSX)
  useEffect(() => {
    Object.entries(logContainerRefs.current).forEach(([, el]) => {
      if (!el) return;
      const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 20;
      if (atBottom) {
        el.scrollTop = el.scrollHeight;
      }
    });
  }, [operationLogs]);

  // Debounce search query
  useEffect(() => {
    const timer = setTimeout(() => {
      setDebouncedSearch(searchQuery);
    }, 300);

    return () => {
      if (timer) clearTimeout(timer);
    };
  }, [searchQuery]);

  const isInitialMount = useRef(true);
  useEffect(() => {
    if (isInitialMount.current) {
      isInitialMount.current = false;
      return;
    }
    setPage(1);
  }, [searchQuery, filters, selectedTags, sortBy, sortOrder, itemsPerPage]);

  // Sync state FROM URL when user navigates (back/forward) or edits URL directly
  const isInternalUpdate = useRef(false);
  useEffect(() => {
    // Explicit full-reset request (Sidebar's "All Books" link uses
    // /library?reset=1). Checked BEFORE the isInternalUpdate guard below
    // so a reset request can never be swallowed as an internal echo of a
    // prior filter/search/sort change — that swallow is what previously
    // left tag (and other) filters "stuck" after clicking All Books.
    if (searchParams.get('reset') === '1') {
      setPage(1);
      setSearchQuery('');
      setSortBy(SortField.Title);
      setSortOrder(SortOrder.Ascending);
      setViewMode('grid');
      setItemsPerPage(20);
      setSelectedTags([]);
      baseHandleFiltersChange({});
      isInternalUpdate.current = true;
      setSearchParams(new URLSearchParams(), { replace: true });
      return;
    }
    if (isInternalUpdate.current) {
      isInternalUpdate.current = false;
      return;
    }
    const urlPage = Math.max(1, parseInt(searchParams.get('page') || localStorage.getItem(STORAGE_KEYS.LIBRARY_PAGE) || '1', 10));
    const urlSearch = searchParams.get('search') ?? '';
    const urlSort = (searchParams.get('sort') as SortField) || SortField.Title;
    const urlOrder =
      searchParams.get('order') === SortOrder.Descending
        ? SortOrder.Descending
        : SortOrder.Ascending;
    const urlView = (searchParams.get('view') as ViewMode) || 'grid';
    const urlLimit = Math.max(10, parseInt(searchParams.get('limit') || '20', 10));

    const urlTag = searchParams.get('tag') || '';

    if (urlPage !== page) setPage(urlPage);
    if (urlSearch !== searchQuery) setSearchQuery(urlSearch);
    if (urlSort !== sortBy) setSortBy(urlSort);
    if (urlOrder !== sortOrder) setSortOrder(urlOrder);
    if (urlView !== viewMode) setViewMode(urlView);
    if (urlLimit !== itemsPerPage) setItemsPerPage(urlLimit);
    if (urlTag !== (selectedTags[0] || '')) setSelectedTags(urlTag ? [urlTag] : []);
  }, [searchParams]); // eslint-disable-line react-hooks/exhaustive-deps

  const prevPageRef = useRef(page);
  const reviewOpRef = useRef(searchParams.get('reviewOp'));
  useEffect(() => {
    reviewOpRef.current = searchParams.get('reviewOp');
  }, [searchParams]);

  useEffect(() => {
    const params = new URLSearchParams();

    if (searchQuery) params.set('search', searchQuery);
    if (filters.author) params.set('author', filters.author);
    if (filters.series) params.set('series', filters.series);
    if (filters.genre) params.set('genre', filters.genre);
    if (filters.language) params.set('language', filters.language);
    if (filters.libraryState) params.set('state', filters.libraryState);
    if (sortBy !== SortField.Title) params.set('sort', sortBy);
    if (sortOrder !== SortOrder.Ascending) params.set('order', sortOrder);
    if (viewMode !== 'grid') params.set('view', viewMode);
    params.set('page', page.toString());
    if (itemsPerPage !== 20) params.set('limit', itemsPerPage.toString());
    if (selectedTags.length > 0) params.set('tag', selectedTags[0]);
    // Preserve reviewOp if present (via ref to avoid infinite loop)
    if (reviewOpRef.current) params.set('reviewOp', reviewOpRef.current);

    // Push a new history entry when page changes so back button works;
    // replace for other changes (search typing, etc.) to avoid history spam.
    const pageChanged = prevPageRef.current !== page;
    prevPageRef.current = page;
    isInternalUpdate.current = true;
    setSearchParams(params, { replace: !pageChanged });
    localStorage.setItem(STORAGE_KEYS.LIBRARY_PAGE, page.toString());
  }, [filters, itemsPerPage, page, searchQuery, selectedTags, setSearchParams, sortBy, sortOrder, viewMode]);

  const buildFieldFilters = useCallback(() => {
    const fieldFilters: Array<{ field: string; value: string; negated: boolean }> = [];
    if (filters.author) fieldFilters.push({ field: 'author', value: filters.author, negated: false });
    if (filters.series) fieldFilters.push({ field: 'series', value: filters.series, negated: false });
    if (filters.genre) fieldFilters.push({ field: 'genre', value: filters.genre, negated: false });
    if (filters.language) fieldFilters.push({ field: 'language', value: filters.language, negated: false });
    if (parsedSearch) {
      for (const ff of parsedSearch.fieldFilters) {
        if (ff.field !== 'tag') fieldFilters.push({ field: ff.field, value: ff.value, negated: ff.negated });
      }
    }
    return fieldFilters;
  }, [filters, parsedSearch]);

  const {
    audiobooks,
    setAudiobooks,
    totalCount,
    loading,
    totalPages,
    softDeletedBooks,
    softDeletedCount,
    softDeletedLoading,
    loadAudiobooks,
    loadSoftDeleted,
    clearLibraryCache,
  } = useLibraryQuery({
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
    convertBook: convertApiBook,
  });

  const {
    selectedAudiobooks,
    setSelectedAudiobooks,
    crossPageFilter,
    setCrossPageFilter,
    selectedIds,
    effectiveSelectedIds,
    effectiveSelectedCount,
    hasSelection,
    allOnPageSelected,
    someOnPageSelected,
    selectedHasDeleted,
    selectedHasActive,
    selectedHasImport,
    showSelectAllBanner,
    handleToggleSelect,
    handleToggleSelectAllOnPage,
    handleClearSelection,
    handleSelectAllItems,
  } = useLibrarySelection({
    audiobooks,
    totalCount,
    debouncedSearch,
    parsedSearch,
    filters,
    selectedTags,
    buildFieldFilters,
  });

  const handleManualImport = () => {
    setImportFilePath('');
    setImportFilePaths([]);
    setImportFileOrganize(true);
    setImportFileDialogOpen(true);
  };

  const handleOpenManualPathImport = () => {
    setManualImportPath('');
    setManualImportError(null);
    setManualImportOp(null);
    setManualImportDialogOpen(true);
  };

  const handleAddImportFilePath = () => {
    const trimmed = importFilePath.trim();
    if (!trimmed) return;
    setImportFilePaths((prev) => (prev.includes(trimmed) ? prev : [...prev, trimmed]));
    setImportFilePath('');
  };

  const handleToggleImportFilePath = (path: string) => {
    setImportFilePaths((prev) =>
      prev.includes(path) ? prev.filter((p) => p !== path) : [...prev, path]
    );
  };

  const handleRemoveImportFilePath = (path: string) => {
    setImportFilePaths((prev) => prev.filter((p) => p !== path));
  };

  const handleImportFile = async () => {
    const manualPath = importFilePath.trim();
    const targets = [...importFilePaths];
    if (manualPath && !targets.includes(manualPath)) {
      targets.push(manualPath);
    }

    if (targets.length === 0) {
      toast('Select one or more files to import from the server.', 'info');
      return;
    }

    setImportFileInProgress(true);
    try {
      const results = await Promise.allSettled(
        targets.map((path) => api.importFile(path, importFileOrganize))
      );
      const failures = results.filter((result) => result.status === 'rejected');
      if (failures.length === 0) {
        toast(
          targets.length === 1
            ? 'Import started successfully.'
            : `Import started for ${targets.length} files.`,
          'success'
        );
      } else {
        const successCount = targets.length - failures.length;
        toast(
          failures.length === targets.length
            ? 'Failed to import selected files.'
            : `Imported ${successCount} of ${targets.length} files.`,
          'warning'
        );
      }
      setImportFileDialogOpen(false);
      setImportFilePath('');
      setImportFilePaths([]);
      clearLibraryCache();
      await loadAudiobooks();
    } catch (error) {
      console.error('Failed to import file:', error);
      const message = error instanceof Error ? error.message : 'Failed to import file.';
      toast(message, 'error');
    } finally {
      setImportFileInProgress(false);
    }
  };

  const startManualImportPolling = (operationId: string) => {
    if (manualImportPollCleanupRef.current) {
      manualImportPollCleanupRef.current();
    }

    let cleanedUp = false;
    let timeoutId: ReturnType<typeof setTimeout> | null = null;
    const terminalStatuses = [
      'completed',
      'failed',
      'canceled',
      'interrupted_dropped',
      'interrupted_restart',
    ];

    const poll = async () => {
      try {
        const op = await api.getOperationV2(operationId);
        if (cleanedUp) return;
        setManualImportOp(op);
        if (terminalStatuses.includes(op.status)) {
          setManualImportInProgress(false);
          manualImportPollCleanupRef.current = null;
          if (op.status === 'completed') {
            toast('Manual import completed.', 'success');
            setManualImportDialogOpen(false);
            setManualImportPath('');
            setManualImportError(null);
            clearLibraryCache();
            await loadAudiobooks();
          } else {
            const message = op.error_message || 'Manual import failed.';
            setManualImportError(message);
            toast(message, 'error');
          }
          return;
        }
        timeoutId = setTimeout(poll, 2000);
      } catch (error) {
        if (cleanedUp) return;
        const message = error instanceof Error ? error.message : 'Failed to poll manual import.';
        setManualImportInProgress(false);
        setManualImportError(message);
        toast(message, 'error');
        manualImportPollCleanupRef.current = null;
      }
    };

    timeoutId = setTimeout(poll, 2000);
    manualImportPollCleanupRef.current = () => {
      cleanedUp = true;
      if (timeoutId) {
        clearTimeout(timeoutId);
      }
    };
  };

  const handleManualPathImport = async () => {
    const path = manualImportPath.trim();
    if (!path) {
      setManualImportError('Enter an absolute path to import.');
      return;
    }

    setManualImportInProgress(true);
    setManualImportError(null);
    setManualImportOp(null);
    try {
      const { operation_id: operationId } = await api.startLibraryImport(path);
      if (!operationId) {
        throw new Error('Manual import did not return an operation ID.');
      }
      toast('Manual import started.', 'info');
      startManualImportPolling(operationId);
    } catch (error) {
      const message = error instanceof Error ? error.message : 'Failed to start manual import.';
      setManualImportInProgress(false);
      setManualImportError(message);
      toast(message, 'error');
    }
  };

  // Load audiobooks when filters change
  useEffect(() => {
    loadAudiobooks();
    // Load system status for library storage section
    (async () => {
      try {
        const status = await api.getSystemStatus();
        setSystemStatus(status);
      } catch (e) {
        console.error('Failed to load system status', e);
      }
    })();
  }, [loadAudiobooks]);

  useEffect(() => {
    loadSoftDeleted();
  }, [loadSoftDeleted]);

  // Watch the operations store: open the review dialog when a metadata fetch op completes.
  const activeOperations = useOperationsStore((state) => state.activeOperations);
  useEffect(() => {
    if (!pendingFetchOpId) return;
    const op = activeOperations.find((o) => o.id === pendingFetchOpId);
    if (!op) return;
    if (op.status === 'completed') {
      setMetadataReviewOpen(true);
      toast('Metadata fetch complete — review results.', 'success');
    } else if (op.status === 'failed') {
      toast('Metadata fetch failed.', 'error');
    }
  }, [activeOperations, pendingFetchOpId, toast]);

  const handleEdit = useCallback((audiobook: Audiobook) => {
    setEditingAudiobook(audiobook);
  }, []);

  const handleDelete = useCallback((audiobook: Audiobook) => {
    setBookPendingDelete(audiobook);
    setDeleteOptions({ softDelete: true, blockHash: true });
    setDeleteDialogOpen(true);
  }, []);

  const handleSaveMetadata = async (audiobook: Audiobook) => {
    try {
      const saved = await api.updateBook(audiobook.id, audiobook);
      // Update local state with server response
      setAudiobooks((prev) => prev.map((ab) => (ab.id === audiobook.id ? saved : ab)));
      setEditingAudiobook(null);
      toast('Metadata saved.', 'success');
    } catch (error) {
      console.error('Failed to save audiobook:', error);
      toast('Failed to save metadata. Please try again.', 'error');
    }
  };

  const handleConfirmDelete = async () => {
    if (!bookPendingDelete) return;
    setDeleteInProgress(true);
    try {
      const result = await api.deleteBook(bookPendingDelete.id, {
        softDelete: deleteOptions.softDelete,
        blockHash: deleteOptions.blockHash,
      });
      const baseMessage = deleteOptions.softDelete
        ? 'Audiobook was soft deleted and hidden from the library.'
        : 'Audiobook was deleted.';
      const blockNotice = deleteOptions.blockHash
        ? result.blocked
          ? ' Hash blocked.'
          : ' Hash could not be blocked.'
        : '';
      const severity = deleteOptions.blockHash && !result.blocked ? 'warning' : 'success';
      toast(`${baseMessage}${blockNotice}`, severity);
      setDeleteDialogOpen(false);
      setBookPendingDelete(null);
      clearLibraryCache();
      await loadAudiobooks();
      await loadSoftDeleted();
    } catch (error) {
      console.error('Failed to delete audiobook:', error);
      toast('Failed to delete audiobook. Please try again.', 'error');
    } finally {
      setDeleteInProgress(false);
    }
  };

  const handleCloseDeleteDialog = () => {
    setDeleteDialogOpen(false);
    setBookPendingDelete(null);
  };

  const handleBatchDelete = async () => {
    if (!hasSelection) return;
    // Cross-page filter delete is not yet supported — it would require iterating
    // potentially 60K+ books per-ID. Ask the user to narrow the selection.
    if (crossPageFilter !== null) {
      toast('Cross-page delete is not yet supported. Narrow your selection to the current page first.', 'info');
      setBatchDeleteDialogOpen(false);
      return;
    }
    setBatchDeleteInProgress(true);
    try {
      const activeBooks = selectedAudiobooks.filter((book) => !book.marked_for_deletion);
      const idsToDelete = activeBooks.map((b) => b.id);
      const results = await Promise.all(
        idsToDelete.map((id) => api.deleteBook(id, { softDelete: true, blockHash: true }))
      );
      const blockedFailures = results.filter((result) => result.blocked !== true).length;
      const baseMessage = `Soft deleted ${activeBooks.length} selected audiobooks.`;
      if (blockedFailures > 0) {
        toast(
          `${baseMessage} ${blockedFailures} hash${blockedFailures === 1 ? '' : 'es'} could not be blocked.`,
          'warning'
        );
      } else {
        toast(baseMessage, 'success');
      }
      setCrossPageFilter(null);
      setSelectedAudiobooks([]);
      clearLibraryCache();
      await loadAudiobooks();
      await loadSoftDeleted();
    } catch (error) {
      console.error('Failed to batch delete audiobooks:', error);
      toast('Failed to delete selected audiobooks.', 'error');
    } finally {
      setBatchDeleteInProgress(false);
      setBatchDeleteDialogOpen(false);
    }
  };

  const handleBatchRestore = async () => {
    if (!hasSelection) return;
    setBatchRestoreInProgress(true);
    try {
      const deletedBooks = selectedAudiobooks.filter((book) => book.marked_for_deletion);
      await Promise.all(deletedBooks.map((book) => api.restoreSoftDeletedBook(book.id)));
      toast(`Restored ${deletedBooks.length} selected audiobooks.`, 'success');
      setSelectedAudiobooks([]);
      setCrossPageFilter(null);
      clearLibraryCache();
      await loadAudiobooks();
      await loadSoftDeleted();
    } catch (error) {
      console.error('Failed to restore selected audiobooks:', error);
      toast('Failed to restore selected audiobooks.', 'error');
    } finally {
      setBatchRestoreInProgress(false);
    }
  };

  const handleMergeAsVersions = async () => {
    if (selectedAudiobooks.length < 2) return;
    setMergeInProgress(true);
    try {
      const keepId = mergePrimaryId || selectedAudiobooks[0].id;
      const mergeIds = selectedAudiobooks.filter((b) => b.id !== keepId).map((b) => b.id);
      await api.mergeBooks(keepId, mergeIds);
      toast(`Merged ${selectedAudiobooks.length} books as versions.`, 'success');
      setSelectedAudiobooks([]);
      setCrossPageFilter(null);
      setMergeDialogOpen(false);
      clearLibraryCache();
      await loadAudiobooks();
    } catch (error) {
      console.error('Failed to merge books:', error);
      toast('Failed to merge books.', 'error');
    } finally {
      setMergeInProgress(false);
    }
  };

  // handleCombineIntoOneBook combines the selected single-file books into ONE
  // multi-file book on the chosen survivor (mergePrimaryId), hard-deleting the
  // absorbed shells. Distinct from handleMergeAsVersions (version-group merge).
  const handleCombineIntoOneBook = async () => {
    if (selectedAudiobooks.length < 2) return;
    setCombineInProgress(true);
    try {
      const keepId = mergePrimaryId || selectedAudiobooks[0].id;
      const mergeIds = selectedAudiobooks.filter((b) => b.id !== keepId).map((b) => b.id);
      const override = (combineOverrideTitle || combineOverrideAuthor || combineOverrideNarrator)
        ? { title: combineOverrideTitle || undefined, author: combineOverrideAuthor || undefined, narrator: combineOverrideNarrator || undefined }
        : undefined;
      const result = await api.combineBooks(keepId, mergeIds, override);
      toast(
        `Combined ${result.files_moved} files into one book; removed ${result.books_deleted} entries.`,
        'success',
      );
      setSelectedAudiobooks([]);
      setCrossPageFilter(null);
      setCombineOverrideTitle('');
      setCombineOverrideAuthor('');
      setCombineOverrideNarrator('');
      setCombineDialogOpen(false);
      clearLibraryCache();
      await loadAudiobooks();
    } catch (error) {
      console.error('Failed to combine books:', error);
      toast('Failed to combine books.', 'error');
    } finally {
      setCombineInProgress(false);
    }
  };

  const handlePurgeOne = async (book: Audiobook) => {
    setPurgingBookId(book.id);
    try {
      await api.deleteBook(book.id, { softDelete: false, blockHash: false });
      toast(`"${book.title}" was purged from the library.`, 'success');
      clearLibraryCache();
      await loadAudiobooks();
      await loadSoftDeleted();
    } catch (error) {
      console.error('Failed to purge audiobook', error);
      toast('Failed to purge audiobook.', 'error');
    } finally {
      setPurgingBookId(null);
    }
  };

  const handleRestoreOne = async (book: Audiobook) => {
    setRestoringBookId(book.id);
    try {
      await api.restoreSoftDeletedBook(book.id);
      toast(`"${book.title}" was restored to the library.`, 'success');
      clearLibraryCache();
      await loadAudiobooks();
      await loadSoftDeleted();
    } catch (error) {
      console.error('Failed to restore audiobook', error);
      toast('Failed to restore audiobook.', 'error');
    } finally {
      setRestoringBookId(null);
    }
  };

  const handleConfirmPurge = async () => {
    setPurgeInProgress(true);
    try {
      const result = await api.purgeSoftDeletedBooks(purgeDeleteFiles);
      toast(
        `Purged ${result.purged} soft-deleted ${result.purged === 1 ? 'book' : 'books'}.`,
        'success'
      );
      setPurgeDialogOpen(false);
      setPurgeDeleteFiles(false);
      clearLibraryCache();
      await loadAudiobooks();
      await loadSoftDeleted();
    } catch (error) {
      console.error('Failed to purge soft-deleted books', error);
      toast('Failed to purge soft-deleted books.', 'error');
    } finally {
      setPurgeInProgress(false);
    }
  };

  const handleBatchSave = async (updates: Partial<Audiobook>) => {
    try {
      // Use the single-call batch API instead of N individual
      // PUT requests. One round trip, one DB write loop. The
      // old path did Promise.allSettled(N × updateBook) which
      // was both slower and noisier in the activity log.
      const result = await api.batchUpdateBooks(effectiveSelectedIds, updates as Record<string, unknown>);
      if (result.failed > 0) {
        toast(
          `Updated ${result.updated} audiobooks, ${result.failed} failed.`,
          'warning'
        );
      } else {
        toast(
          `Updated metadata for ${result.updated} audiobooks.`,
          'success'
        );
      }
      clearLibraryCache();
      loadAudiobooks();
      setSelectedAudiobooks([]);
      setCrossPageFilter(null);
      setBatchEditOpen(false);
    } catch (error) {
      console.error('Failed to batch update audiobooks:', error);
      toast('Failed to update audiobooks. Please try again.', 'error');
    }
  };

  const handleClick = useCallback(
    (audiobook: Audiobook) => {
      // Save current library URL so BookDetail can return here directly
      sessionStorage.setItem(
        'library_return_url',
        window.location.pathname + window.location.search
      );
      navigate(`/library/${audiobook.id}`);
    },
    [navigate]
  );

  const handleVersionManage = (audiobook: Audiobook) => {
    setVersionManagingAudiobook(audiobook);
    setVersionManagementOpen(true);
  };

  const handleVersionManagementClose = () => {
    setVersionManagementOpen(false);
    setVersionManagingAudiobook(null);
  };

  const handleVersionUpdate = () => {
    clearLibraryCache();
    loadAudiobooks();
  };

  const handleFetchMetadata = async (audiobook: Audiobook) => {
    try {
      await api.fetchBookMetadata(audiobook.id);
      // Reload audiobooks to show updated data
      clearLibraryCache();
      loadAudiobooks();
    } catch (error) {
      console.error('Failed to fetch metadata:', error);
      toast('Failed to fetch metadata. Please try again.', 'error');
    }
  };

  const handleBulkFetchMetadata = async () => {
    if (!hasSelection) {
      toast('Select audiobooks to fetch metadata for.', 'info');
      return;
    }
    try {
      // Build a SelectionSpec: use a filter for cross-page selections so the
      // server resolves IDs at execution time; use explicit IDs for page selections.
      const selection: api.SelectionSpec = crossPageFilter !== null
        ? { filter: crossPageFilter }
        : { book_ids: effectiveSelectedIds };
      await api.startBulkMetadataFetch(selection);
      toast(
        `Metadata fetch queued for ${effectiveSelectedCount.toLocaleString()} books — watch the bell for progress.`,
        'success'
      );
      setSelectedAudiobooks([]);
      setCrossPageFilter(null);
    } catch (error) {
      console.error('Failed to start bulk metadata fetch:', error);
      toast('Failed to start bulk metadata fetch.', 'error');
    }
  };

  const handleCancelBulkFetch = () => {
    if (!bulkFetchInProgress) {
      setBulkFetchDialogOpen(false);
      setBulkFetchProgress(null);
      return;
    }
    bulkFetchCancelRef.current = true;
  };

  const handleBulkWriteBack = async () => {
    const ids = effectiveSelectedIds.filter((id) => {
      // When cross-page selection is active, we can't filter by marked_for_deletion.
      // Pass all selected IDs; the backend skips deleted books gracefully.
      if (crossPageFilter !== null) return true;
      const book = selectedAudiobooks.find((b) => b.id === id);
      return book ? !book.marked_for_deletion : true;
    });
    if (ids.length === 0) {
      toast('Select active audiobooks to save to files.', 'info');
      return;
    }

    setBulkWriteBackInProgress(true);
    try {
      const result = await withOptimisticOperation('batch_save_to_files', () =>
        api.batchWriteBackMetadata(ids, bulkWriteBackRename, bulkWriteBackForce),
      );
      toast(`Saving ${ids.length} books to files…`, 'success');
      void result;
      setBulkWriteBackDialogOpen(false);
      setCrossPageFilter(null);
      setSelectedAudiobooks([]);
    } catch (error) {
      console.error('Failed to start save to files:', error);
      toast('Failed to start save to files.', 'error');
    } finally {
      setBulkWriteBackInProgress(false);
    }
  };

  const handleCloseBulkWriteBackDialog = () => {
    if (bulkWriteBackInProgress) {
      return;
    }
    setBulkWriteBackDialogOpen(false);
    setBulkWriteBackResult(null);
    setBulkWriteBackRename(false);
  };

  const handleBulkSaveAll = async () => {
    setBulkSaveAllStarting(true);
    try {
      const result = await api.bulkWriteBackMetadata({ rename: bulkSaveAllRename });
      if (result.operation_id) {
        toast(
          `Bulk save started for ${result.estimated_books} books. Check Activity for progress.`,
          'success'
        );
        setBulkSaveAllDialogOpen(false);
      } else {
        toast(result.message || 'No books matched the filters.', 'info');
      }
    } catch (error) {
      console.error('Failed to start bulk write-back:', error);
      toast('Failed to start bulk save operation.', 'error');
    } finally {
      setBulkSaveAllStarting(false);
    }
  };

  const handleCloseBulkSaveAllDialog = () => {
    if (bulkSaveAllStarting) return;
    setBulkSaveAllDialogOpen(false);
  };

  const requestDuplicateAction = (
    duplicate: Audiobook,
    existing: Audiobook
  ): Promise<DuplicateAction> =>
    new Promise((resolve) => {
      duplicateResolverRef.current = resolve;
      setDuplicateDialog({ duplicate, existing });
    });

  const handleDuplicateAction = (action: DuplicateAction) => {
    if (duplicateResolverRef.current) {
      duplicateResolverRef.current(action);
      duplicateResolverRef.current = null;
    }
    setDuplicateDialog(null);
  };

  const handleBulkOrganize = async () => {
    if (!hasSelection) {
      toast('Select audiobooks to organize.', 'info');
      return;
    }

    const importBooks = selectedAudiobooks.filter((book) => book.library_state === 'imported');
    if (importBooks.length === 0) {
      toast('Select import-state audiobooks to organize.', 'info');
      return;
    }
    const importBookIds = importBooks.map((book) => book.id);

    setBulkOrganizeInProgress(true);
    setBulkOrganizeError(null);
    bulkOrganizeCancelRef.current = false;
    const snapshot = new Map<string, Audiobook>();
    importBooks.forEach((book) => {
      snapshot.set(book.id, { ...book });
    });
    bulkOrganizeSnapshotRef.current = snapshot;

    const total = importBooks.length;
    const results: BulkActionResult[] = [];
    let completed = 0;
    let encounteredError = false;
    setBulkOrganizeProgress({ total, completed, results: [] });

    const organizedByHash = new Map<string, Audiobook>();
    audiobooks
      .filter((item) => item.library_state === 'organized')
      .forEach((item) => {
        buildHashCandidates(item).forEach((hash) => {
          organizedByHash.set(hash, item);
        });
      });

    const findDuplicate = (target: Audiobook): Audiobook | null => {
      for (const hash of buildHashCandidates(target)) {
        const match = organizedByHash.get(hash);
        if (match && match.id !== target.id) {
          return match;
        }
      }
      return null;
    };

    try {
      await api.startOrganize(undefined, undefined, importBookIds);

      for (const book of importBooks) {
        if (bulkOrganizeCancelRef.current) {
          break;
        }
        const organizeError = book.organize_error;
        if (organizeError) {
          const errorMessage = `Failed to organize ${book.title || 'audiobook'}.`;
          results.push({
            book_id: book.id,
            title: book.title,
            status: 'error',
            message: organizeError,
          });
          completed += 1;
          setBulkOrganizeProgress({
            total,
            completed,
            results: [...results],
          });
          setBulkOrganizeError({
            book,
            message: errorMessage,
          });
          encounteredError = true;
          break;
        }

        const duplicate = findDuplicate(book);
        if (duplicate) {
          const action = await requestDuplicateAction(book, duplicate);
          if (action === 'skip') {
            results.push({
              book_id: book.id,
              title: book.title,
              status: 'skipped',
              message: 'Skipped duplicate file.',
            });
            completed += 1;
            setBulkOrganizeProgress({
              total,
              completed,
              results: [...results],
            });
            continue;
          }
          if (action === 'link') {
            const groupId = duplicate.version_group_id || `group-${duplicate.id}`;
            await api.linkBookVersion(duplicate.id, book.id);
            setAudiobooks((prev) =>
              prev.map((item) => {
                if (item.id === duplicate.id) {
                  return {
                    ...item,
                    version_group_id: groupId,
                    is_primary_version: true,
                  };
                }
                if (item.id === book.id) {
                  return { ...item, version_group_id: groupId };
                }
                return item;
              })
            );
          }
          if (action === 'replace') {
            setAudiobooks((prev) =>
              prev.map((item) =>
                item.id === duplicate.id ? { ...item, marked_for_deletion: true } : item
              )
            );
          }
        }

        results.push({
          book_id: book.id,
          title: book.title,
          status: 'organized',
        });
        setAudiobooks((prev) =>
          prev.map((ab) => (ab.id === book.id ? { ...ab, library_state: 'organized' } : ab))
        );
        buildHashCandidates(book).forEach((hash) => {
          organizedByHash.set(hash, book);
        });
        completed += 1;
        setBulkOrganizeProgress({ total, completed, results: [...results] });
      }

      if (bulkOrganizeCancelRef.current) {
        toast('Organize cancelled.', 'info');
      } else if (!encounteredError) {
        toast(`Successfully organized ${completed} audiobooks.`, 'success');
        setSelectedAudiobooks([]);
        setCrossPageFilter(null);
      }

      if (!bulkOrganizeCancelRef.current && !encounteredError) {
        clearLibraryCache();
        await loadAudiobooks();
      }
    } catch (error) {
      console.error('Failed to organize selected audiobooks:', error);
      toast('Failed to organize selected audiobooks.', 'error');
    } finally {
      setBulkOrganizeInProgress(false);
      bulkOrganizeCancelRef.current = false;
    }
  };

  const handleCancelBulkOrganize = () => {
    if (!bulkOrganizeInProgress) {
      setBulkOrganizeDialogOpen(false);
      setBulkOrganizeProgress(null);
      setBulkOrganizeError(null);
      return;
    }
    bulkOrganizeCancelRef.current = true;
  };

  const handleCloseOrganizeError = () => {
    setBulkOrganizeError(null);
  };

  const handleOrganizeRollback = async () => {
    const snapshot = bulkOrganizeSnapshotRef.current;
    if (!snapshot.size) {
      setBulkOrganizeError(null);
      return;
    }

    try {
      for (const book of snapshot.values()) {
        await api.updateBook(book.id, {
          library_state: book.library_state,
          file_path: book.file_path,
          organized_file_hash: book.organized_file_hash,
        });
      }
      toast('Rollback complete.', 'success');
      setBulkOrganizeError(null);
      clearLibraryCache();
      await loadAudiobooks();
    } catch (error) {
      console.error('Failed to rollback organize:', error);
      toast('Rollback failed.', 'error');
    }
  };

  const handleParseWithAI = async (audiobook: Audiobook) => {
    try {
      await api.parseAudiobookWithAI(audiobook.id);
      // Reload audiobooks to show updated data
      clearLibraryCache();
      loadAudiobooks();
    } catch (error) {
      console.error('Failed to parse with AI:', error);
      toast('Failed to parse with AI. Please try again.', 'error');
    }
  };

  const handleFiltersChange = baseHandleFiltersChange;

  const handleSortChange = (newSort: SortField) => {
    setSortBy(newSort);
    if (newSort === SortField.CreatedAt) {
      setSortOrder(SortOrder.Descending);
    }
  };

  const handleColumnSortChange = (sortKey: string, order: 'asc' | 'desc') => {
    setSortBy(sortKey as SortField);
    setSortOrder(order === 'asc' ? SortOrder.Ascending : SortOrder.Descending);
  };

  const libraryBookCount =
    systemStatus?.library_book_count ?? systemStatus?.library.book_count ?? 0;
  const importBookCount =
    systemStatus?.import_book_count ?? systemStatus?.import_paths?.book_count ?? 0;
  const librarySizeBytes =
    systemStatus?.library_size_bytes ?? systemStatus?.library.total_size ?? 0;
  const importSizeBytes =
    systemStatus?.import_size_bytes ?? systemStatus?.import_paths?.total_size ?? 0;

  // Import path management handlers
  const handleAddImportPath = async () => {
    if (!newImportPath.trim()) return;

    try {
      const detailed = await api.addImportPathDetailed(
        newImportPath,
        newImportPath.split('/').pop() || 'Library'
      );
      const importPath = detailed.importPath;
      const newPath: ImportPath = {
        id: importPath.id,
        path: importPath.path,
        status: detailed.scan_operation_id ? 'scanning' : 'idle',
        book_count: importPath.book_count,
      };
      setImportPaths((prev) => [...prev, newPath]);
      setNewImportPath('');
      setShowServerBrowser(false);
      setAddPathDialogOpen(false);

      // If scan started, poll status until complete then refresh folders
      if (detailed.scan_operation_id) {
        const opId = detailed.scan_operation_id;
        const pollInterval = 2000;
        let attempts = 0;
        const maxAttempts = 150; // ~5 minutes
        const pollTimerRef: { current: NodeJS.Timeout | null } = { current: null };
        const isUnmountedPollRef = { current: false };

        const poll = async () => {
          if (isUnmountedPollRef.current) return;
          try {
            const op = await api.getOperationStatus(opId);
            if (isUnmountedPollRef.current) return;
            if (op.status === 'completed' || op.status === 'failed' || op.status === 'canceled') {
              // Refresh folder list to get updated book counts
              const folders = await api.getImportPaths();
              if (!isUnmountedPollRef.current) {
                setImportPaths(
                  folders.map((f) => ({
                    id: f.id,
                    path: f.path,
                    status: 'idle',
                    book_count: f.book_count,
                  }))
                );
              }
              return; // stop polling
            }
            attempts++;
            if (attempts < maxAttempts && !isUnmountedPollRef.current) {
              pollTimerRef.current = setTimeout(poll, pollInterval);
            }
          } catch (_e) {
            if (isUnmountedPollRef.current) return;
            attempts++;
            if (attempts < maxAttempts) {
              pollTimerRef.current = setTimeout(poll, pollInterval);
            }
          }
        };
        pollTimerRef.current = setTimeout(poll, pollInterval);
      }
    } catch (error) {
      console.error('Failed to add import path:', error);
    }
  };

  const handleServerBrowserSelect = (path: string, isDir: boolean) => {
    if (isDir) {
      setNewImportPath(path);
    }
  };

  const handleRemoveImportPath = async (id: number) => {
    setRemovingPathId(id.toString());
    try {
      await api.removeImportPath(id);
      setImportPaths((prev) => prev.filter((p) => p.id !== id));
    } catch (error) {
      console.error('Failed to remove import path:', error);
    } finally {
      setRemovingPathId(null);
    }
  };

  const startPolling = (opId: string, type: 'scan' | 'organize') => {
    const cleanup = pollOperation(
      opId,
      { intervalMs: 2000 },
      (op) => {
        if (type === 'scan') setActiveScanOp(op);
        else setActiveOrganizeOp(op);
      },
      async (op) => {
        if (type === 'scan') {
          const folders = await api.getImportPaths();
          setImportPaths(
            folders.map((f) => ({
              id: f.id,
              path: f.path,
              status: 'idle',
              book_count: f.book_count,
            }))
          );
          setActiveScanOp(op);
        } else {
          setOrganizeRunning(false);
          setActiveOrganizeOp(op);
        }
        clearLibraryCache();
        loadAudiobooks();
        pollingCleanupRef.current = null; // Clear when operation completes
      },
      (err) => {
        console.warn('Polling error', err);
        if (type === 'organize') setOrganizeRunning(false);
        pollingCleanupRef.current = null; // Clear on error
      }
    );
    // Store cleanup function so it can be called on unmount
    pollingCleanupRef.current = cleanup;
  };

  const handleScanImportPath = async (id: number) => {
    setScanningPathId(id.toString());
    try {
      const pathEntry = importPaths.find((p) => p.id === id);
      const path = pathEntry?.path;
      if (!path) return;
      setImportPaths((prev) => prev.map((p) => (p.id === id ? { ...p, status: 'scanning' } : p)));
      const op = await api.startScan(path);
      startPolling(op.id, 'scan');
    } catch (error) {
      console.error('Failed to scan import path:', error);
      setImportPaths((prev) => prev.map((p) => (p.id === id ? { ...p, status: 'idle' } : p)));
      setScanningPathId(null);
    }
  };

  const handleScanAll = async () => {
    setScanningAll(true);
    try {
      // Mark all paths scanning
      setImportPaths((prev) => prev.map((p) => ({ ...p, status: 'scanning' })));
      const op = await api.startScan(); // no folder path -> scan all
      startPolling(op.id, 'scan');
    } catch (error) {
      console.error('Failed to start full scan:', error);
      setImportPaths((prev) => prev.map((p) => ({ ...p, status: 'idle' })));
      setScanningAll(false);
    }
  };

  const handleFullRescan = async () => {
    try {
      // Mark all paths scanning
      setImportPaths((prev) => prev.map((p) => ({ ...p, status: 'scanning' })));
      // Force full rescan with database path updates
      const op = await api.startScan(undefined, undefined, true);
      startPolling(op.id, 'scan');
    } catch (error) {
      console.error('Failed to start full rescan:', error);
      setImportPaths((prev) => prev.map((p) => ({ ...p, status: 'idle' })));
    }
  };

  const handleFingerprintRescanFull = async () => {
    if (!window.confirm('Re-fingerprint ALL files? This may take a while for large libraries.')) {
      return;
    }
    try {
      const op = await api.triggerFingerprintBackfill('all');
      toast(`Fingerprint rescan started. Operation ID: ${op.id}`, 'success');
    } catch (error) {
      console.error('Failed to trigger fingerprint rescan:', error);
      toast('Failed to start fingerprint rescan', 'error');
    }
  };

  const handleFingerprintRescanMissing = async () => {
    try {
      const op = await api.triggerFingerprintBackfill('missing');
      toast(`Fingerprint rescan started. Operation ID: ${op.id}`, 'success');
    } catch (error) {
      console.error('Failed to trigger fingerprint rescan:', error);
      toast('Failed to start fingerprint rescan', 'error');
    }
  };

  const handleOrganizeLibrary = async () => {
    try {
      setOrganizeRunning(true);
      const op = await api.startOrganize();
      startPolling(op.id, 'organize');
    } catch (e) {
      console.error('Failed to start organize', e);
      setOrganizeRunning(false);
    }
  };

  const handleFetchReview = async () => {
    try {
      const ids = effectiveSelectedIds;
      const resp = await withOptimisticOperation('metadata_candidate_fetch', () =>
        api.batchFetchCandidates({ book_ids: ids }),
      );
      const opId = resp.operation_id;
      if (!opId) {
        toast('All selected books are already being fetched.', 'info');
        return;
      }
      setPendingFetchOpId(opId);
      toast(
        `Metadata fetch started for ${ids.length} book${ids.length !== 1 ? 's' : ''}. Click Review when complete to open candidates.`,
        'info',
      );
    } catch { toast('Failed to start metadata fetch', 'error'); }
  };

  const handleFetchAllUnmatched = async () => {
    try {
      // Insert the bell placeholder BEFORE the round-trip — server-side
      // selection of "all unmatched" is slow on big libraries and the
      // user previously got zero feedback while it ran.
      const resp = await withOptimisticOperation('metadata_candidate_fetch', () =>
        api.batchFetchCandidates({ selection: { filter: { only_unmatched: true } } }),
      );
      if (!resp.operation_id) {
        toast(resp.message ?? 'All books already have matched candidates.', 'info');
        return;
      }
      toast(
        `Fetching metadata for ${resp.book_count ?? 'unmatched'} books — check the operations list for progress.`,
        'info',
      );
    } catch {
      toast('Failed to start unmatched fetch', 'error');
    }
  };

  const handleResumeReview = async () => {
    try {
      const cached = await api.listCachedCandidates('pending');
      if (!cached.entries.length) {
        toast('No books with pending metadata candidates found. Click Fetch Selected to populate the cache.', 'info');
        return;
      }
      setMetadataReviewOpen(true);
      toast(`${cached.entries.length} book${cached.entries.length === 1 ? '' : 's'} ready for review.`, 'info');
    } catch {
      toast('Failed to load pending review', 'error');
    }
  };

  return (
    <Box
      sx={{
        height: '100%',
        display: 'flex',
        flexDirection: 'column',
        overflow: 'hidden',
      }}
    >
      <LibraryToolbar
        hasSelection={hasSelection}
        selectedAudiobooks={selectedAudiobooks}
        batchRestoreInProgress={batchRestoreInProgress}
        selectedHasActive={selectedHasActive}
        selectedHasDeleted={selectedHasDeleted}
        selectedHasImport={selectedHasImport}
        organizeRunning={organizeRunning}
        activeScanOp={activeScanOp}
        activeOrganizeOp={activeOrganizeOp}
        storageDrawerOpen={storageDrawerOpen}
        systemStatus={systemStatus}
        softDeletedCount={softDeletedCount}
        libraryBookCount={libraryBookCount}
        importBookCount={importBookCount}
        librarySizeBytes={librarySizeBytes}
        importSizeBytes={importSizeBytes}
        visibleColumnIds={visibleColumnIds}
        toggleColumn={toggleColumn}
        resetColumnsToDefaults={resetColumnsToDefaults}
        getActiveFilterCount={getActiveFilterCount}
        savedPresets={savedPresets}
        onSaveCurrentAsPreset={handleOpenSavePresetDialog}
        onApplyPreset={handleApplyPreset}
        onRenamePreset={handleOpenRenamePresetDialog}
        onDeletePreset={handleDeletePreset}
        onBatchEdit={() => setBatchEditOpen(true)}
        onFetchReview={handleFetchReview}
        onFetchAllUnmatched={handleFetchAllUnmatched}
        onResumeReview={handleResumeReview}
        onSearchMetadata={() => setBulkSearchOpen(true)}
        onSaveToFiles={() => { setBulkWriteBackResult(null); setBulkWriteBackRename(false); setBulkWriteBackDialogOpen(true); }}
        onOrganizeSelected={() => setBulkOrganizeDialogOpen(true)}
        onMergeAsVersions={() => { setMergePrimaryId(selectedAudiobooks[0]?.id || ''); setMergeDialogOpen(true); }}
        onCombineIntoOneBook={() => { setMergePrimaryId(selectedAudiobooks[0]?.id || ''); setCombineDialogOpen(true); }}
        onTagClick={() => setBulkTagDialogOpen(true)}
        onRateClick={() => setBulkRatingDialogOpen(true)}
        onDeleteSelected={() => setBatchDeleteDialogOpen(true)}
        onRestoreSelected={handleBatchRestore}
        onManualImport={handleManualImport}
        onManualPathImport={handleOpenManualPathImport}
        onFilterOpen={() => setFilterOpen(true)}
        onOrganizeLibrary={handleOrganizeLibrary}
        onFullRescan={handleFullRescan}
        onPurgeOpen={() => setPurgeDialogOpen(true)}
        onStorageDrawerClose={() => setStorageDrawerOpen(false)}
        navigate={navigate}
      />

      <Box sx={{ flex: 1, overflowY: 'auto', minHeight: 0, pb: 3 }}>
        {defaultPreset === 'fingerprints' && (
          <Box sx={{ display: 'flex', gap: 1, mb: 2, px: 2, pt: 2 }}>
            <Button
              variant="contained"
              startIcon={<RefreshIcon />}
              onClick={handleFingerprintRescanFull}
              size="small"
            >
              Full Rescan Fingerprints
            </Button>
            <Button
              variant="outlined"
              startIcon={<CachedIcon />}
              onClick={handleFingerprintRescanMissing}
              size="small"
            >
              Rescan Missing Only
            </Button>
          </Box>
        )}

        <LibraryBookGrid
          audiobooks={audiobooks}
          loading={loading}
          searchQuery={searchQuery}
          setSearchQuery={setSearchQuery}
          setParsedSearch={setParsedSearch}
          viewMode={viewMode}
          setViewMode={setViewMode}
          sortBy={sortBy}
          handleSortChange={handleSortChange}
          sortOrder={sortOrder}
          setSortOrder={setSortOrder}
          setStorageDrawerOpen={setStorageDrawerOpen}
          importPaths={importPaths}
          handleManualImport={handleManualImport}
          setAddPathDialogOpen={setAddPathDialogOpen}
          handleScanAll={handleScanAll}
          scanningAll={scanningAll}
          page={page}
          setPage={setPage}
          totalPages={totalPages}
          totalCount={totalCount}
          itemsPerPage={itemsPerPage}
          setItemsPerPage={setItemsPerPage}
          allOnPageSelected={allOnPageSelected}
          someOnPageSelected={someOnPageSelected}
          handleToggleSelectAllOnPage={handleToggleSelectAllOnPage}
          hasSelection={hasSelection}
          effectiveSelectedCount={effectiveSelectedCount}
          handleClearSelection={handleClearSelection}
          showSelectAllBanner={showSelectAllBanner}
          handleSelectAllItems={handleSelectAllItems}
          handleEdit={handleEdit}
          handleDelete={handleDelete}
          handleClick={handleClick}
          handleVersionManage={handleVersionManage}
          handleFetchMetadata={handleFetchMetadata}
          handleParseWithAI={handleParseWithAI}
          selectedIds={selectedIds}
          handleToggleSelect={handleToggleSelect}
          columnDefs={columnDefs}
          columnWidths={columnWidths}
          handleColumnSortChange={handleColumnSortChange}
          resizeColumn={resizeColumn}
          visibleColumnIds={visibleColumnIds}
          onToggleColumn={toggleColumn}
          softDeletedCount={softDeletedCount}
          softDeletedBooks={softDeletedBooks}
          softDeletedLoading={softDeletedLoading}
          softDeletedExpanded={softDeletedExpanded}
          restoringBookId={restoringBookId}
          purgeInProgress={purgeInProgress}
          purgingBookId={purgingBookId}
          onToggleSoftDeletedExpanded={() => setSoftDeletedExpanded(!softDeletedExpanded)}
          loadSoftDeleted={loadSoftDeleted}
          handleRestoreOne={handleRestoreOne}
          handlePurgeOne={handlePurgeOne}
          filterOpen={filterOpen}
          setFilterOpen={setFilterOpen}
          filters={filters}
          handleFiltersChange={handleFiltersChange}
          availableAuthors={availableAuthors}
          availableSeries={availableSeries}
          availableGenres={availableGenres}
          availableLanguages={availableLanguages}
          availableTags={availableTags}
          selectedTags={selectedTags}
          handleTagFilterChange={handleTagFilterChange}
        />

        <LibraryDialogs
          selectedAudiobooks={selectedAudiobooks}
          setSelectedAudiobooks={setSelectedAudiobooks}
          hasSelection={hasSelection}
          selectedHasActive={selectedHasActive}
          selectedHasImport={selectedHasImport}
          toast={toast}
          loadAudiobooks={loadAudiobooks}
          refreshTags={refreshTags}
          editingAudiobook={editingAudiobook}
          setEditingAudiobook={setEditingAudiobook}
          handleSaveMetadata={handleSaveMetadata}
          batchEditOpen={batchEditOpen}
          setBatchEditOpen={setBatchEditOpen}
          handleBatchSave={handleBatchSave}
          bulkTagDialogOpen={bulkTagDialogOpen}
          setBulkTagDialogOpen={setBulkTagDialogOpen}
          availableTags={availableTags}
          bulkRatingDialogOpen={bulkRatingDialogOpen}
          setBulkRatingDialogOpen={setBulkRatingDialogOpen}
          mergeDialogOpen={mergeDialogOpen}
          setMergeDialogOpen={setMergeDialogOpen}
          mergePrimaryId={mergePrimaryId}
          setMergePrimaryId={setMergePrimaryId}
          mergeInProgress={mergeInProgress}
          handleMergeAsVersions={handleMergeAsVersions}
          combineDialogOpen={combineDialogOpen}
          setCombineDialogOpen={setCombineDialogOpen}
          combineInProgress={combineInProgress}
          combineOverrideTitle={combineOverrideTitle}
          setCombineOverrideTitle={setCombineOverrideTitle}
          combineOverrideAuthor={combineOverrideAuthor}
          setCombineOverrideAuthor={setCombineOverrideAuthor}
          combineOverrideNarrator={combineOverrideNarrator}
          setCombineOverrideNarrator={setCombineOverrideNarrator}
          handleCombineIntoOneBook={handleCombineIntoOneBook}
          batchDeleteDialogOpen={batchDeleteDialogOpen}
          setBatchDeleteDialogOpen={setBatchDeleteDialogOpen}
          batchDeleteInProgress={batchDeleteInProgress}
          handleBatchDelete={handleBatchDelete}
          bulkOrganizeDialogOpen={bulkOrganizeDialogOpen}
          handleCancelBulkOrganize={handleCancelBulkOrganize}
          bulkOrganizeProgress={bulkOrganizeProgress}
          bulkOrganizeInProgress={bulkOrganizeInProgress}
          handleBulkOrganize={handleBulkOrganize}
          bulkWriteBackDialogOpen={bulkWriteBackDialogOpen}
          handleCloseBulkWriteBackDialog={handleCloseBulkWriteBackDialog}
          bulkWriteBackRename={bulkWriteBackRename}
          setBulkWriteBackRename={setBulkWriteBackRename}
          bulkWriteBackForce={bulkWriteBackForce}
          setBulkWriteBackForce={setBulkWriteBackForce}
          bulkWriteBackResult={bulkWriteBackResult}
          bulkWriteBackInProgress={bulkWriteBackInProgress}
          handleBulkWriteBack={handleBulkWriteBack}
          bulkSaveAllDialogOpen={bulkSaveAllDialogOpen}
          handleCloseBulkSaveAllDialog={handleCloseBulkSaveAllDialog}
          bulkSaveAllEstimate={bulkSaveAllEstimate}
          bulkSaveAllRename={bulkSaveAllRename}
          setBulkSaveAllRename={setBulkSaveAllRename}
          bulkSaveAllStarting={bulkSaveAllStarting}
          handleBulkSaveAll={handleBulkSaveAll}
          duplicateDialog={duplicateDialog}
          handleDuplicateAction={handleDuplicateAction}
          bulkOrganizeError={bulkOrganizeError}
          handleCloseOrganizeError={handleCloseOrganizeError}
          handleOrganizeRollback={handleOrganizeRollback}
          importFileDialogOpen={importFileDialogOpen}
          setImportFileDialogOpen={setImportFileDialogOpen}
          importFilePath={importFilePath}
          setImportFilePath={setImportFilePath}
          handleAddImportFilePath={handleAddImportFilePath}
          importFilePaths={importFilePaths}
          handleToggleImportFilePath={handleToggleImportFilePath}
          handleRemoveImportFilePath={handleRemoveImportFilePath}
          importFileOrganize={importFileOrganize}
          setImportFileOrganize={setImportFileOrganize}
          importFileInProgress={importFileInProgress}
          handleImportFile={handleImportFile}
          manualImportDialogOpen={manualImportDialogOpen}
          setManualImportDialogOpen={setManualImportDialogOpen}
          manualImportPath={manualImportPath}
          setManualImportPath={setManualImportPath}
          manualImportError={manualImportError}
          manualImportInProgress={manualImportInProgress}
          manualImportOp={manualImportOp}
          handleManualPathImport={handleManualPathImport}
          bulkFetchDialogOpen={bulkFetchDialogOpen}
          handleCancelBulkFetch={handleCancelBulkFetch}
          bulkFetchProgress={bulkFetchProgress}
          bulkFetchInProgress={bulkFetchInProgress}
          handleBulkFetchMetadata={handleBulkFetchMetadata}
          bulkSearchOpen={bulkSearchOpen}
          setBulkSearchOpen={setBulkSearchOpen}
          metadataReviewOpen={metadataReviewOpen}
          setMetadataReviewOpen={setMetadataReviewOpen}

          versionManagingAudiobook={versionManagingAudiobook}
          versionManagementOpen={versionManagementOpen}
          handleVersionManagementClose={handleVersionManagementClose}
          handleVersionUpdate={handleVersionUpdate}
          deleteDialogOpen={deleteDialogOpen}
          handleCloseDeleteDialog={handleCloseDeleteDialog}
          bookPendingDelete={bookPendingDelete}
          deleteOptions={deleteOptions}
          setDeleteOptions={setDeleteOptions}
          deleteInProgress={deleteInProgress}
          handleConfirmDelete={handleConfirmDelete}
          purgeDialogOpen={purgeDialogOpen}
          setPurgeDialogOpen={setPurgeDialogOpen}
          purgeDeleteFiles={purgeDeleteFiles}
          setPurgeDeleteFiles={setPurgeDeleteFiles}
          softDeletedCount={softDeletedCount}
          purgeInProgress={purgeInProgress}
          handleConfirmPurge={handleConfirmPurge}
          addPathDialogOpen={addPathDialogOpen}
          setAddPathDialogOpen={setAddPathDialogOpen}
          showServerBrowser={showServerBrowser}
          setShowServerBrowser={setShowServerBrowser}
          newImportPath={newImportPath}
          setNewImportPath={setNewImportPath}
          handleAddImportPath={handleAddImportPath}
          handleServerBrowserSelect={handleServerBrowserSelect}
          importPaths={importPaths}
          importPathsExpanded={importPathsExpanded}
          setImportPathsExpanded={setImportPathsExpanded}
          scanningAll={scanningAll}
          handleScanAll={handleScanAll}
          scanningPathId={scanningPathId}
          handleScanImportPath={handleScanImportPath}
          removingPathId={removingPathId}
          handleRemoveImportPath={handleRemoveImportPath}
          batchPlaylistOpen={batchPlaylistOpen}
          setBatchPlaylistOpen={setBatchPlaylistOpen}
        />

        <Dialog open={presetDialogOpen} onClose={handleClosePresetDialog}>
          <DialogTitle>{editingPresetId ? 'Rename preset' : 'Save current filters as preset'}</DialogTitle>
          <DialogContent>
            <TextField
              autoFocus
              margin="dense"
              label="Preset name"
              fullWidth
              value={presetDialogName}
              onChange={(e) => setPresetDialogName(e.target.value)}
            />
          </DialogContent>
          <DialogActions>
            <Button onClick={handleClosePresetDialog}>Cancel</Button>
            <Button onClick={handleConfirmPresetDialog} disabled={!presetDialogName.trim()} variant="contained">
              Save
            </Button>
          </DialogActions>
        </Dialog>
      </Box>
    </Box>
  );
};
