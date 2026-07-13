// file: web/src/pages/BookDetail.tsx
// version: 1.52.0
// guid: 4d2f7c6a-1b3e-4c5d-8f7a-9b0c1d2e3f4a
// last-edited: 2026-07-13

import { useCallback, useEffect, useState, useRef } from 'react';
import { useNavigate, useParams, useSearchParams } from 'react-router-dom';
import {
  Alert,
  Box,
  Button,
  CircularProgress,
  LinearProgress,
  Paper,
  Tab,
  Tabs,
} from '@mui/material';
import ArrowBackIcon from '@mui/icons-material/ArrowBack.js';
import type { Book, BookFile, BookSegment, BookTags, SegmentTags, OverridePayload, OrganizePreviewResponse } from '../services/api';
import * as api from '../services/api';
import { useToast } from '../components/toast/ToastProvider';
import type { Audiobook } from '../types';
import { formatBytes } from '../components/bookdetail/bookDetailUtils';
import { BookDetailHeader } from '../components/bookdetail/BookDetailHeader';
import { BookDetailStatusAlerts } from '../components/bookdetail/BookDetailStatusAlerts';
import { BookDetailActions } from '../components/bookdetail/BookDetailActions';
import { BookDetailInfoTab } from '../components/bookdetail/BookDetailInfoTab';
import { BookDetailFilesTab } from '../components/bookdetail/BookDetailFilesTab';
import { BookDetailDialogs, type MetadataRejection } from '../components/bookdetail/BookDetailDialogs';


export const BookDetail = () => {
  const { id } = useParams();
  const navigate = useNavigate();
  const [book, setBook] = useState<Book | null>(null);
  const [loading, setLoading] = useState(true);
  const [actionLoading, setActionLoading] = useState(false);
  const [actionLabel, setActionLabel] = useState<string | null>(null);
  const [fetchingMetadata, setFetchingMetadata] = useState(false);
  const [parsingWithAI, setParsingWithAI] = useState(false);
  const [rescanningFolder, setRescanningFolder] = useState(false);
  const [writingToFiles, setWritingToFiles] = useState(false);
  const [transcoding, setTranscoding] = useState(false);
  const [writeBackDialogOpen, setWriteBackDialogOpen] = useState(false);
  // preAIBook removed — use History to revert AI changes
  const { toast } = useToast();
  const [searchParams, setSearchParams] = useSearchParams();
  const activeTab = (['info', 'files'].includes(searchParams.get('tab') || '')
    ? searchParams.get('tab')
    : 'info') as 'info' | 'files';
  const setActiveTab = (v: 'info' | 'files') => {
    setSearchParams((prev) => { const next = new URLSearchParams(prev); next.set('tab', v); return next; }, { replace: true });
  };
  const [deleteDialogOpen, setDeleteDialogOpen] = useState(false);
  const [playlistDialogOpen, setPlaylistDialogOpen] = useState(false);
  const [deleteOptions, setDeleteOptions] = useState({
    softDelete: true,
    blockHash: true,
  });
  const [conflictDialogOpen, setConflictDialogOpen] = useState(false);
  const [pendingUpdate, setPendingUpdate] =
    useState<Partial<Book> | null>(null);
  const [purgeDialogOpen, setPurgeDialogOpen] = useState(false);
  const [purgeConfirmed, setPurgeConfirmed] = useState(false);
  const [versions, setVersions] = useState<Book[]>([]);
  const [itunesLinked, setItunesLinked] = useState(false);
  const [filesRefreshKey, setFilesRefreshKey] = useState(0);
  const refreshFilesTab = () => setFilesRefreshKey((k) => k + 1);
  const [compareSnapshotTs, setCompareSnapshotTs] = useState<string | null>(null);
  const [itunesPidCount, setItunesPidCount] = useState(0);
  const [itunesExternalIDs, setItunesExternalIDs] = useState<api.ExternalIDMapping[]>([]);
  const [linkSearchOpen, setLinkSearchOpen] = useState(false);
  const [linkSearchQuery, setLinkSearchQuery] = useState('');
  const [linkSearchResults, setLinkSearchResults] = useState<Book[]>([]);
  const [linkSearchLoading, setLinkSearchLoading] = useState(false);
  const [editDialogOpen, setEditDialogOpen] = useState(false);
  const [historyDialogOpen, setHistoryDialogOpen] = useState(false);
  const [metadataSearchOpen, setMetadataSearchOpen] = useState(false);
  const [segments, setSegments] = useState<BookSegment[]>([]);
  const [segmentsLoaded, setSegmentsLoaded] = useState(false);
  const [bookFiles, setBookFiles] = useState<BookFile[]>([]);
  const [relocateSegment, setRelocateSegment] = useState<BookSegment | null>(null);
  const [selectedSegmentIds, setSelectedSegmentIds] = useState<Set<string>>(new Set());
  const [segmentTags, setSegmentTags] = useState<SegmentTags | null>(null);
  const [segmentTagsLoading, setSegmentTagsLoading] = useState(false);
  const [splittingVersion, setSplittingVersion] = useState(false);
  const [splittingToBooks, setSplittingToBooks] = useState(false);
  // moveToVersionDialogOpen removed — "Move to Existing Version" is now inline
  const [organizePreviewDialogOpen, setOrganizePreviewDialogOpen] = useState(false);
  const [organizePreview, setOrganizePreview] = useState<OrganizePreviewResponse | null>(null);
  const [organizePreviewLoading, setOrganizePreviewLoading] = useState(false);
  const [applyingOrganize, setApplyingOrganize] = useState(false);
  const [expandedTagStep, setExpandedTagStep] = useState(false);
  // Sonarr-style version expansion
  const [expandedVersionIds, setExpandedVersionIds] = useState<Set<string>>(new Set());
  const [expandedSegmentVersionIds, setExpandedSegmentVersionIds] = useState<Set<string>>(new Set());
  const [versionSegments, setVersionSegments] = useState<Record<string, BookSegment[]>>({});
  const [versionFileTags, setVersionFileTags] = useState<Record<string, BookTags | null>>({});
  // versionFileTagsLoading removed — TagComparison handles its own loading
  const [, setVersionFileTagsLoading] = useState<Set<string>>(new Set());

  // Detailed tags with source attribution (CAT-1, PR #548)
  const [detailedTags, setDetailedTags] = useState<api.DetailedBookTag[]>([]);

  const [rejections, setRejections] = useState<MetadataRejection[]>([]);
  const [rejHistoryOpen, setRejHistoryOpen] = useState(false);

  // Derived multi-select state
  const isSingleSelect = selectedSegmentIds.size === 1;
  const singleSelectedId = isSingleSelect ? Array.from(selectedSegmentIds)[0] : null;

  // Refs for cleanup
  const rescanTimeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  // Load detailed tags for source attribution (CAT-1 / PR #548)
  useEffect(() => {
    if (!id) return;
    let cancelled = false;
    api.getBookTagsDetailed(id)
      .then((data) => { if (!cancelled) setDetailedTags(data); })
      .catch(() => { if (!cancelled) setDetailedTags([]); });
    return () => { cancelled = true; };
  }, [id, filesRefreshKey]);

  // loadBook/loadVersions accept an optional `isStale` check so the
  // id-keyed load effect below can detect when its `id` has been
  // superseded by a faster navigation and skip applying a stale response
  // (BOOKDETAIL-RACE: navigating book A -> book B quickly, or A's response
  // simply arriving after B's, used to let A's setBook overwrite B's).
  // Callers outside that effect (button handlers, onRefresh, etc.) call
  // these with no argument, which is unconditional/unchanged behavior.
  const loadBook = useCallback(async (isStale?: () => boolean) => {
    if (!id) return;
    setLoading(true);
    try {
      const data = await api.getBook(id);
      if (isStale?.()) return;
      setBook(data);
    } catch (error) {
      if (isStale?.()) return;
      if (error instanceof api.ApiError) {
        if (error.status === 404) {
          setBook(null);
          return;
        }
        if (error.status === 401) {
          toast('Session expired.', 'error');
          navigate('/login');
          return;
        }
      }
      console.error('Failed to load book', error);
      toast('Failed to load audiobook details.', 'error');
    } finally {
      // Don't clear loading for a stale request — a newer request for a
      // different id may still be in flight and already owns `loading`.
      if (!isStale?.()) setLoading(false);
    }
  }, [id, navigate, toast]);

  // Silent refresh: re-fetches book without showing the full-page loading spinner
  const refreshBook = useCallback(async () => {
    if (!id) return;
    try {
      const data = await api.getBook(id);
      setBook(data);
    } catch {
      // Silent refresh - errors are non-critical
    }
  }, [id]);

  const loadVersions = useCallback(async (isStale?: () => boolean) => {
    if (!id) return;
    try {
      const data = await api.getBookVersions(id);
      if (isStale?.()) return;
      setVersions(data);
    } catch (error) {
      if (isStale?.()) return;
      console.error('Failed to load versions', error);
    }
  }, [id]);


  // Primary id-keyed load. A `cancelled` flag (same idiom used elsewhere in
  // this file, e.g. the linkSearch and tag-preload effects) guards every
  // setState below so a slow/out-of-order response for a book we've already
  // navigated away from can't overwrite state for the book now on screen
  // (BOOKDETAIL-RACE).
  useEffect(() => {
    let cancelled = false;
    const isStale = () => cancelled;
    loadBook(isStale);
    loadVersions(isStale);
    // Load external ID info (iTunes linkage)
    api.getBookExternalIDs(id!).then((data) => {
      if (cancelled) return;
      setItunesLinked(data.itunes_linked);
      setItunesPidCount(data.total);
      setItunesExternalIDs(data.external_ids.filter((e) => e.source === 'itunes' && !e.tombstoned));
    }).catch(() => {});
    // Load rejection history (META-REJ-1)
    fetch(`/api/v1/audiobooks/${id}/metadata-rejections`, { credentials: 'include' })
      .then((r) => r.ok ? r.json() : Promise.reject(r))
      .then((data) => { if (!cancelled) setRejections(data.rejections ?? []); })
      .catch(() => {});
    return () => { cancelled = true; };
  }, [id, loadBook, loadVersions]);

  // Inline version link search
  useEffect(() => {
    if (!linkSearchOpen) {
      setLinkSearchQuery('');
      setLinkSearchResults([]);
      return;
    }
    const q = linkSearchQuery.trim();
    if (!q) { setLinkSearchResults([]); return; }
    let cancelled = false;
    setLinkSearchLoading(true);
    let timer: ReturnType<typeof setTimeout> | null = null;
    timer = setTimeout(async () => {
      try {
        const results = await api.searchBooks(q, 10);
        if (!cancelled) setLinkSearchResults(results.filter(r => r.id !== book?.id && !versions.some(v => v.id === r.id)));
      } catch { /* ignore */ }
      finally { if (!cancelled) setLinkSearchLoading(false); }
    }, 300);
    return () => { cancelled = true; if (timer) window.clearTimeout(timer); };
  }, [linkSearchOpen, linkSearchQuery, book?.id, versions]);

  const handleInlineLinkVersion = async (targetId: string) => {
    if (!book) return;
    try {
      await api.linkBookVersion(book.id, targetId);
      setLinkSearchOpen(false);
      loadVersions();
      loadBook();
      toast('Version linked', 'success');
    } catch {
      toast('Failed to link version', 'error');
    }
  };

  // Reset file/segment state when navigating between books (FE-4).
  useEffect(() => {
    setSegmentsLoaded(false);
    setBookFiles([]);
    setSegments([]);
  }, [id]);

  // Load book files from the canonical book_files endpoint.
  // Falls back to legacy segments endpoint if book_files is unavailable.
  useEffect(() => {
    if (!id || segmentsLoaded) return;
    const loadFiles = async () => {
      try {
        const result = await api.getBookFiles(id);
        if (result.files && result.files.length > 0) {
          setBookFiles(result.files);
        } else {
          // No book_files rows yet — try legacy segments endpoint (which now proxies to book_files)
          const data = await api.getBookSegments(id);
          setSegments(data);
        }
      } catch {
        try {
          const data = await api.getBookSegments(id);
          setSegments(data);
        } catch {
          // Neither endpoint available
        }
      } finally {
        setSegmentsLoaded(true);
      }
    };
    loadFiles();
  }, [id, segmentsLoaded]);

  // Reload files for the current book.
  const reloadCurrentBookFiles = useCallback(async (bookId: string) => {
    try {
      const result = await api.getBookFiles(bookId);
      if (result.files && result.files.length > 0) {
        setBookFiles(result.files);
        setSegments([]);
        return;
      }
    } catch {
      // fall through
    }
    try {
      const segs = await api.getBookSegments(bookId);
      setSegments(segs);
      setBookFiles([]);
    } catch {
      // ignore
    }
  }, []);

  // Load segment tags when selection changes
  const loadSegmentTags = useCallback(async (segmentId: string) => {
    if (!id) return;
    setSegmentTagsLoading(true);
    try {
      const data = await api.getSegmentTags(id, segmentId);
      setSegmentTags(data);
    } catch {
      setSegmentTags(null);
    } finally {
      setSegmentTagsLoading(false);
    }
  }, [id]);

  useEffect(() => {
    if (isSingleSelect && singleSelectedId) {
      loadSegmentTags(singleSelectedId);
    } else {
      setSegmentTags(null);
    }
  }, [selectedSegmentIds, isSingleSelect, singleSelectedId, loadSegmentTags]);

  const handleDelete = async () => {
    if (!book) return;
    setActionLoading(true);
    setActionLabel(
      deleteOptions.softDelete ? 'Soft deleting...' : 'Deleting...'
    );
    try {
      const result = await api.deleteBook(book.id, {
        softDelete: deleteOptions.softDelete,
        blockHash: deleteOptions.blockHash,
      });
      const baseMessage = deleteOptions.softDelete
        ? 'Audiobook marked for deletion.'
        : 'Audiobook deleted permanently.';
      const blockNotice = deleteOptions.blockHash
        ? result.blocked
          ? ' Hash blocked.'
          : ' Hash could not be blocked.'
        : '';
      const severity =
        deleteOptions.blockHash && !result.blocked ? 'warning' : 'success';
      toast(`${baseMessage}${blockNotice}`, severity);
      setDeleteDialogOpen(false);
      await loadBook();
    } catch (error) {
      console.error('Failed to delete audiobook', error);
      toast('Failed to delete audiobook.', 'error');
    } finally {
      setActionLabel(null);
      setActionLoading(false);
    }
  };

  const handleRestore = async () => {
    if (!book) return;
    setActionLoading(true);
    setActionLabel('Restoring...');
    try {
      await api.restoreSoftDeletedBook(book.id);
      toast('Audiobook restored.', 'success');
      await loadBook();
    } catch (error) {
      console.error('Failed to restore audiobook', error);
      toast('Failed to restore audiobook.', 'error');
    } finally {
      setActionLabel(null);
      setActionLoading(false);
    }
  };

  const handleRescanFiles = async () => {
    if (!book) return;
    setActionLoading(true);
    setActionLabel('Rescanning files...');
    try {
      const result = await api.rescanBookFiles(book.id);
      const changed = result.old_total !== result.new_total;
      toast(
        changed
          ? `File size updated (${formatBytes(result.old_total)} → ${formatBytes(result.new_total)})`
          : `Files in sync, no changes (${result.file_count} file${result.file_count === 1 ? '' : 's'} checked)`,
        'success'
      );
      await loadBook();
    } catch (error) {
      console.error('Failed to rescan files', error);
      toast('Failed to rescan files.', 'error');
    } finally {
      setActionLabel(null);
      setActionLoading(false);
    }
  };

  const handlePurge = async () => {
    if (!book) return;
    setActionLoading(true);
    setActionLabel('Purging...');
    try {
      await api.deleteBook(book.id, { softDelete: false, blockHash: false });
      toast('Audiobook permanently deleted.', 'success');
      navigate('/library');
    } catch (error) {
      console.error('Failed to purge audiobook', error);
      toast('Failed to purge audiobook.', 'error');
    } finally {
      setActionLabel(null);
      setActionLoading(false);
    }
  };

  const handleQuarantine = async () => {
    if (!book) return;
    setActionLoading(true);
    setActionLabel('Quarantining...');
    try {
      await api.quarantineBook(book.id, 'manually quarantined');
      toast('Book moved to .failed/.', 'success');
      await loadBook();
    } catch (error) {
      console.error('Failed to quarantine book', error);
      toast('Failed to quarantine book.', 'error');
    } finally {
      setActionLabel(null);
      setActionLoading(false);
    }
  };

  const handleUnquarantine = async () => {
    if (!book) return;
    setActionLoading(true);
    setActionLabel('Restoring from failed...');
    try {
      await api.unquarantineBook(book.id);
      toast('Book restored from .failed/.', 'success');
      await loadBook();
    } catch (error) {
      console.error('Failed to unquarantine book', error);
      toast('Failed to restore book from quarantine.', 'error');
    } finally {
      setActionLabel(null);
      setActionLoading(false);
    }
  };

  const handleFetchMetadata = async () => {
    if (!book) return;
    setFetchingMetadata(true);
    try {
      const result = await api.fetchBookMetadata(book.id);
      setBook(result.book);
      // Re-fetch enriched book (with populated authors array) and tags
      await refreshBook();
      refreshFilesTab(); // reload tags and changelog
      toast(
        result.message ||
          `Metadata refreshed from ${result.source || 'provider'}.`,
        'success'
      );
    } catch (error: unknown) {
      console.error('Failed to fetch metadata', error);
      const msg =
        error instanceof Error ? error.message : 'Metadata fetch failed.';
      toast(msg, 'error');
    } finally {
      setFetchingMetadata(false);
    }
  };

  const handleTranscode = async () => {
    if (!book) return;
    setTranscoding(true);
    try {
      await api.startTranscode(book.id);
      toast('Transcode started. The book will be converted to M4B.', 'success');
    } catch (error: unknown) {
      console.error('Failed to start transcode', error);
      const msg =
        error instanceof Error ? error.message : 'Transcode failed to start.';
      toast(msg, 'error');
    } finally {
      setTranscoding(false);
    }
  };

  const handleWriteBackMetadata = async () => {
    if (!book) return;
    setWritingToFiles(true);
    setWriteBackDialogOpen(false);
    try {
      const result = await api.writeBackMetadata(book.id);
      toast(result.message || 'Metadata written to files.', 'success');
      refreshFilesTab(); // reload tags and changelog after write
      await refreshBook(); // refresh book data to reflect any changes
    } catch (error: unknown) {
      console.error('Failed to write metadata to files', error);
      const msg =
        error instanceof Error ? error.message : 'Write to files failed.';
      toast(msg, 'error');
    } finally {
      setWritingToFiles(false);
    }
  };

  const handlePreviewOrganize = async () => {
    if (!book) return;
    setOrganizePreviewLoading(true);
    try {
      const preview = await api.previewOrganize(book.id);
      setOrganizePreview(preview);
      setExpandedTagStep(false);
      setOrganizePreviewDialogOpen(true);
    } catch (error: unknown) {
      console.error('Failed to preview organize', error);
      const msg =
        error instanceof Error ? error.message : 'Preview organize failed.';
      toast(msg, 'error');
    } finally {
      setOrganizePreviewLoading(false);
    }
  };

  const handleApplyOrganize = async () => {
    if (!book) return;
    setApplyingOrganize(true);
    setOrganizePreviewDialogOpen(false);
    try {
      const result = await api.organizeBook(book.id);
      toast(result.message || 'Organize applied successfully.', 'success');
      await refreshBook();
    } catch (error: unknown) {
      console.error('Failed to organize book', error);
      const msg =
        error instanceof Error ? error.message : 'Organize failed.';
      toast(msg, 'error');
    } finally {
      setApplyingOrganize(false);
    }
  };

  const handleSplitVersion = async () => {
    if (!book || selectedSegmentIds.size === 0) return;
    setSplittingVersion(true);
    try {
      const newBook = await api.splitVersion(book.id, Array.from(selectedSegmentIds));
      toast(`Created new version: ${newBook.title}`, 'success');
      setSelectedSegmentIds(new Set());
      loadBook();
      loadVersions();
      // Reload segments since some moved
      await reloadCurrentBookFiles(book.id);
    } catch (error: unknown) {
      const msg = error instanceof Error ? error.message : 'Failed to split version';
      toast(msg, 'error');
    } finally {
      setSplittingVersion(false);
    }
  };

  const handleSplitToBooks = async () => {
    if (!book || selectedSegmentIds.size === 0) return;
    setSplittingToBooks(true);
    try {
      const result = await api.splitSegmentsToBooks(book.id, Array.from(selectedSegmentIds));
      toast(`Created ${result.count} new book${result.count !== 1 ? 's' : ''}`, 'success');
      setSelectedSegmentIds(new Set());
      loadBook();
      await reloadCurrentBookFiles(book.id);
    } catch (error: unknown) {
      const msg = error instanceof Error ? error.message : 'Failed to split to books';
      toast(msg, 'error');
    } finally {
      setSplittingToBooks(false);
    }
  };

  const handleMoveToVersion = async (targetBookId: string) => {
    if (!book || selectedSegmentIds.size === 0) return;
    try {
      await api.moveSegments(book.id, Array.from(selectedSegmentIds), targetBookId);
      toast('Segments moved to version', 'success');
      setSelectedSegmentIds(new Set());
      // Dialog removed — move-to-version is now inline
      loadBook();
      loadVersions();
      await reloadCurrentBookFiles(book.id);
    } catch (error: unknown) {
      const msg = error instanceof Error ? error.message : 'Failed to move segments';
      toast(msg, 'error');
    }
  };

  // Auto-expand current book's version when versions load (or when there are none).
  // When there are no linked versions the tray list falls back to [book], so we
  // must also expand the book itself in that case.
  useEffect(() => {
    if (id) {
      setExpandedVersionIds(new Set([id]));
    }
  }, [id, versions.length]);

  const toggleVersionExpanded = useCallback(async (versionId: string) => {
    setExpandedVersionIds(prev => {
      const next = new Set(prev);
      if (next.has(versionId)) {
        next.delete(versionId);
      } else {
        next.add(versionId);
      }
      return next;
    });
    // Load files for this version if not already loaded
    if (!versionSegments[versionId]) {
      try {
        const result = await api.getBookFiles(versionId);
        if (result.files && result.files.length > 0) {
          // Convert BookFile to BookSegment shape for versionSegments
          const segs: BookSegment[] = result.files.map(f => ({
            id: f.id,
            file_path: f.file_path,
            format: f.format || '',
            size_bytes: f.file_size || 0,
            duration_seconds: (f.duration || 0) / 1000,
            track_number: f.track_number,
            total_tracks: f.track_count,
            active: !f.missing,
            file_exists: f.file_exists,
          }));
          setVersionSegments(prev => ({ ...prev, [versionId]: segs }));
        } else {
          const segs = await api.getBookSegments(versionId);
          setVersionSegments(prev => ({ ...prev, [versionId]: segs }));
        }
      } catch {
        try {
          const segs = await api.getBookSegments(versionId);
          setVersionSegments(prev => ({ ...prev, [versionId]: segs }));
        } catch {
          setVersionSegments(prev => ({ ...prev, [versionId]: [] }));
        }
      }
    }
    // Load file tags if not already loaded
    if (versionFileTags[versionId] === undefined) {
      setVersionFileTagsLoading(prev => new Set(prev).add(versionId));
      try {
        const tags = await api.getBookTags(versionId);
        setVersionFileTags(prev => ({ ...prev, [versionId]: tags }));
      } catch {
        setVersionFileTags(prev => ({ ...prev, [versionId]: null }));
      } finally {
        setVersionFileTagsLoading(prev => {
          const next = new Set(prev);
          next.delete(versionId);
          return next;
        });
      }
    }
  }, [versionSegments, versionFileTags]);

  // Keep current book's segments synced
  useEffect(() => {
    if (id && segments.length > 0) {
      setVersionSegments(prev => ({ ...prev, [id]: segments }));
    }
  }, [id, segments]);

  // Preload current-book tag data so multi-file summaries can render immediately.
  useEffect(() => {
    if (!id || versionFileTags[id] !== undefined) return;
    let cancelled = false;
    setVersionFileTagsLoading(prev => new Set(prev).add(id));
    api.getBookTags(id)
      .then((tags) => {
        if (!cancelled) {
          setVersionFileTags(prev => ({ ...prev, [id]: tags }));
        }
      })
      .catch(() => {
        if (!cancelled) {
          setVersionFileTags(prev => ({ ...prev, [id]: null }));
        }
      })
      .finally(() => {
        if (!cancelled) {
          setVersionFileTagsLoading(prev => {
            const next = new Set(prev);
            next.delete(id);
            return next;
          });
        }
      });
    return () => {
      cancelled = true;
    };
  }, [id, versionFileTags]);

  const handleSetPrimary = async (versionId: string) => {
    try {
      await api.setPrimaryVersion(versionId);
      toast('Primary version updated', 'success');
      loadVersions();
      loadBook();
    } catch {
      toast('Failed to set primary version', 'error');
    }
  };

  const handleParseWithAI = async () => {
    if (!book) return;
    setParsingWithAI(true);
    try {
      const result = await api.parseAudiobookWithAI(book.id);
      setBook(result.book);
      toast(result.message || 'AI parsing completed. Use History to revert if needed.', 'success');
    } catch (error: unknown) {
      console.error('Failed to parse with AI', error);
      const msg =
        error instanceof Error ? error.message : 'AI parsing failed.';
      toast(msg, 'error');
    } finally {
      setParsingWithAI(false);
    }
  };

  const handleRescanFolder = async () => {
    if (!book) return;
    setRescanningFolder(true);
    try {
      // Get parent directory from file_path
      const lastSlash = book.file_path.lastIndexOf('/');
      const folderPath = lastSlash > 0 ? book.file_path.substring(0, lastSlash) : book.file_path;
      await api.startScan(folderPath, undefined, true);
      toast(`Scanning folder: ${folderPath}`, 'success');
      // Refresh book and segments after a short delay to let scan complete
      if (rescanTimeoutRef.current) clearTimeout(rescanTimeoutRef.current);
      rescanTimeoutRef.current = setTimeout(async () => {
        await refreshBook();
      }, 3000);
    } catch (error: unknown) {
      console.error('Failed to rescan folder', error);
      const msg =
        error instanceof Error ? error.message : 'Folder rescan failed.';
      toast(msg, 'error');
    } finally {
      setRescanningFolder(false);
    }
  };

  // Cleanup timeouts on unmount
  useEffect(() => {
    return () => {
      if (rescanTimeoutRef.current) {
        clearTimeout(rescanTimeoutRef.current);
      }
    };
  }, []);

  // versionSummary removed — tab label is now static "Files & History"

  const mapBookToAudiobook = (current: Book): Audiobook => ({
    id: current.id,
    title: current.title,
    author: current.author_name,
    narrator: current.narrator,
    series: current.series_name,
    series_number: current.series_position,
    language: current.language,
    publisher: current.publisher,
    description: current.description,
    duration_seconds: current.duration,
    file_path: current.file_path,
    original_filename: current.original_filename,
    cover_path: current.cover_image,
    format: current.format,
    bitrate_kbps: current.bitrate,
    codec: current.codec,
    sample_rate_hz: current.sample_rate,
    channels: current.channels,
    bit_depth: current.bit_depth,
    quality: current.quality,
    is_primary_version: current.is_primary_version,
    version_group_id: current.version_group_id,
    version_notes: current.version_notes,
    file_hash: current.file_hash,
    original_file_hash: current.original_file_hash,
    organized_file_hash: current.organized_file_hash,
    library_state: current.library_state,
    quantity: current.quantity,
    marked_for_deletion: current.marked_for_deletion,
    marked_for_deletion_at: current.marked_for_deletion_at,
    created_at: current.created_at,
    updated_at: current.updated_at,
    work_id: current.work_id,
  });

  const handleEditSave = async (updated: Audiobook, dirtyFields: Set<string>) => {
    if (!book) return;
    setActionLoading(true);
    setActionLabel('Saving changes...');

    // Map form field names to API field names for overrides
    const FIELD_TO_API: Record<string, string> = {
      title: 'title',
      author: 'author_name',
      narrator: 'narrator',
      series: 'series_name',
      series_number: 'series_position',
      genre: 'genre',
      year: 'audiobook_release_year',
      language: 'language',
      publisher: 'publisher',
      isbn10: 'isbn10',
      isbn13: 'isbn13',
      description: 'description',
    };

    // Build payload outside try so it's accessible in catch for conflict retry
    const payload: Partial<Book> & { overrides?: Record<string, OverridePayload> } = {
      title: updated.title ?? '',
      description: updated.description ?? '',
      publisher: updated.publisher ?? '',
      language: updated.language ?? '',
      narrator: updated.narrator ?? '',
      series_position: updated.series_number ?? undefined,
      audiobook_release_year:
        updated.audiobook_release_year ||
        updated.year ||
        book.audiobook_release_year ||
        undefined,
      print_year: updated.year || book.print_year || undefined,
      isbn:
        updated.isbn13 ||
        updated.isbn10 ||
        book.isbn ||
        '',
      author_name: updated.author ?? '',
      series_name: updated.series ?? '',
    };

    // Auto-lock edited fields (Plex-style: edits are automatically locked)
    if (dirtyFields && dirtyFields.size > 0) {
      const overrides: Record<string, OverridePayload> = {};
      for (const field of dirtyFields) {
        const apiField = FIELD_TO_API[field];
        if (!apiField) continue;
        const value = field === 'year'
          ? (updated.year ?? updated.audiobook_release_year ?? null)
          : field === 'series_number'
            ? (updated.series_number ?? null)
            : ((updated as unknown as Record<string, unknown>)[field] ?? null);
        overrides[apiField] = { value, locked: true };
      }
      if (Object.keys(overrides).length > 0) {
        payload.overrides = overrides;
      }
    }

    try {
      const saved = await api.updateBook(book.id, payload);
      setBook(saved);
      toast('Metadata saved. Edited fields are now locked.', 'success');
      setEditDialogOpen(false);
      refreshFilesTab(); // reload tags/changelog to reflect saved changes
    } catch (error) {
      if (error instanceof api.ApiError) {
        if (error.status === 409) {
          setPendingUpdate(payload);
          setConflictDialogOpen(true);
          toast('Book was updated by another user.', 'warning');
          return;
        }
        if (error.status === 401) {
          toast('Session expired.', 'error');
          navigate('/login');
          return;
        }
      }
      console.error('Failed to update metadata', error);
      toast('Failed to update metadata.', 'error');
    } finally {
      setActionLabel(null);
      setActionLoading(false);
    }
  };

  const handleConflictReload = async () => {
    setConflictDialogOpen(false);
    setPendingUpdate(null);
    await loadBook();
  };

  const handleConflictOverwrite = async () => {
    if (!book || !pendingUpdate) return;
    setActionLoading(true);
    setActionLabel('Overwriting...');
    try {
      const saved = await api.updateBook(book.id, {
        ...pendingUpdate,
        force_update: true,
      });
      setBook(saved);
      toast('Metadata saved to database (overwrite).', 'success');
      setEditDialogOpen(false);
      setConflictDialogOpen(false);
      setPendingUpdate(null);
    } catch (error) {
      console.error('Failed to overwrite metadata', error);
      toast('Failed to overwrite metadata.', 'error');
    } finally {
      setActionLabel(null);
      setActionLoading(false);
    }
  };

  if (loading) {
    return (
      <Box
        display="flex"
        alignItems="center"
        justifyContent="center"
        height="100%"
      >
        <CircularProgress />
      </Box>
    );
  }

  if (!book) {
    return (
      <Box p={3}>
        <Alert severity="error">Audiobook not found.</Alert>
        <Button
          startIcon={<ArrowBackIcon />}
          sx={{ mt: 2 }}
          onClick={() => {
            const returnUrl = sessionStorage.getItem('library_return_url');
            if (returnUrl) navigate(returnUrl);
            else navigate('/library');
          }}
        >
          Back to Library
        </Button>
      </Box>
    );
  }

  const isSoftDeleted = book.marked_for_deletion ?? false;

  return (
    <Box p={3} sx={{ height: '100%', overflowY: 'auto' }}>
      {actionLoading && <LinearProgress sx={{ position: 'fixed', top: 0, left: 0, right: 0, zIndex: 9999 }} />}
      {actionLabel && (
        <Alert severity="info" sx={{ mb: 2 }}>
          {actionLabel}
        </Alert>
      )}

      <BookDetailHeader
        book={book}
        bookFiles={bookFiles}
        segments={segments}
        itunesLinked={itunesLinked}
        itunesPidCount={itunesPidCount}
        activeTab={activeTab}
        onBack={() => {
          const returnUrl = sessionStorage.getItem('library_return_url');
          if (returnUrl) navigate(returnUrl);
          else navigate('/library');
        }}
        onSetActiveTab={setActiveTab}
      />

      <BookDetailStatusAlerts
        book={book}
        actionLoading={actionLoading}
        onRestore={handleRestore}
        onUnquarantine={handleUnquarantine}
        onOpenPurgeDialog={() => setPurgeDialogOpen(true)}
        onQuarantine={handleQuarantine}
      />

      <BookDetailActions
        book={book}
        isSoftDeleted={isSoftDeleted}
        loading={loading}
        actionLoading={actionLoading}
        parsingWithAI={parsingWithAI}
        rescanningFolder={rescanningFolder}
        fetchingMetadata={fetchingMetadata}
        transcoding={transcoding}
        organizePreviewLoading={organizePreviewLoading}
        applyingOrganize={applyingOrganize}
        writingToFiles={writingToFiles}
        onOpenHistory={() => setHistoryDialogOpen(true)}
        onParseWithAI={handleParseWithAI}
        onRescanFolder={handleRescanFolder}
        onRescanFiles={handleRescanFiles}
        onRefresh={() => { loadBook(); loadVersions(); refreshFilesTab(); }}
        onOpenEdit={() => setEditDialogOpen(true)}
        onFetchMetadata={handleFetchMetadata}
        onOpenMetadataSearch={() => setMetadataSearchOpen(true)}
        onOpenPlaylist={() => setPlaylistDialogOpen(true)}
        onTranscode={handleTranscode}
        onPreviewOrganize={handlePreviewOrganize}
        onOpenWriteBack={() => setWriteBackDialogOpen(true)}
        onOpenDelete={() => { setDeleteOptions({ softDelete: true, blockHash: true }); setDeleteDialogOpen(true); }}
        onRestore={handleRestore}
      />

      <Paper sx={{ p: 2, mb: 3 }}>
        <Tabs value={activeTab} onChange={(_, v) => setActiveTab(v)} textColor="primary" indicatorColor="primary" variant="scrollable">
          <Tab label="Info" value="info" />
          <Tab label="Files &amp; History" value="files" />
        </Tabs>
      </Paper>

      {activeTab === 'info' && (
        <BookDetailInfoTab
          book={book}
          bookId={id!}
          singleSelectedId={singleSelectedId}
          segmentTags={segmentTags}
          segmentTagsLoading={segmentTagsLoading}
          detailedTags={detailedTags}
          toast={toast}
        />
      )}

      {activeTab === 'files' && (
        <BookDetailFilesTab
          book={book}
          versions={versions}
          bookFiles={bookFiles}
          segments={segments}
          versionSegments={versionSegments}
          versionFileTags={versionFileTags}
          expandedVersionIds={expandedVersionIds}
          expandedSegmentVersionIds={expandedSegmentVersionIds}
          selectedSegmentIds={selectedSegmentIds}
          filesRefreshKey={filesRefreshKey}
          compareSnapshotTs={compareSnapshotTs}
          itunesLinked={itunesLinked}
          itunesPidCount={itunesPidCount}
          itunesExternalIDs={itunesExternalIDs}
          linkSearchOpen={linkSearchOpen}
          linkSearchQuery={linkSearchQuery}
          linkSearchResults={linkSearchResults}
          linkSearchLoading={linkSearchLoading}
          splittingVersion={splittingVersion}
          splittingToBooks={splittingToBooks}
          onSetLinkSearchOpen={setLinkSearchOpen}
          onSetLinkSearchQuery={setLinkSearchQuery}
          onInlineLinkVersion={handleInlineLinkVersion}
          onToggleVersionExpanded={toggleVersionExpanded}
          onSetExpandedSegmentVersionIds={setExpandedSegmentVersionIds}
          onSetSelectedSegmentIds={setSelectedSegmentIds}
          onSetActiveTab={setActiveTab}
          onSetRelocateSegment={setRelocateSegment}
          onSetPrimary={handleSetPrimary}
          onUnlinkVersion={async (vid) => {
            try {
              await api.unlinkBookVersion(book.id, vid);
              loadVersions();
              loadBook();
              toast('Version unlinked', 'success');
            } catch {
              toast('Failed to unlink version', 'error');
            }
          }}
          onMoveToVersion={handleMoveToVersion}
          onSplitVersion={handleSplitVersion}
          onSplitToBooks={handleSplitToBooks}
          onClearCompareSnapshot={setCompareSnapshotTs}
          onExtractTrackInfo={async () => {
            try {
              const result = await api.extractTrackInfo(book.id);
              toast(`Updated track numbers for ${result.updated} of ${result.total} files`, 'success');
              if (id) {
                await reloadCurrentBookFiles(id);
              }
            } catch {
              toast('Failed to extract track info', 'error');
            }
          }}
          onLoadBook={loadBook}
          onRefreshFiles={refreshFilesTab}
        />
      )}

      <BookDetailDialogs
        book={book}
        segments={segments}
        deleteDialogOpen={deleteDialogOpen}
        deleteOptions={deleteOptions}
        onCloseDelete={() => setDeleteDialogOpen(false)}
        onSetDeleteOptions={setDeleteOptions}
        onDelete={handleDelete}
        organizePreviewDialogOpen={organizePreviewDialogOpen}
        organizePreview={organizePreview}
        expandedTagStep={expandedTagStep}
        applyingOrganize={applyingOrganize}
        onSetExpandedTagStep={setExpandedTagStep}
        onCloseOrganizePreview={() => setOrganizePreviewDialogOpen(false)}
        onApplyOrganize={handleApplyOrganize}
        writeBackDialogOpen={writeBackDialogOpen}
        writingToFiles={writingToFiles}
        onCloseWriteBack={() => setWriteBackDialogOpen(false)}
        onWriteBackMetadata={handleWriteBackMetadata}
        purgeDialogOpen={purgeDialogOpen}
        purgeConfirmed={purgeConfirmed}
        actionLoading={actionLoading}
        onSetPurgeConfirmed={setPurgeConfirmed}
        onClosePurge={() => { setPurgeDialogOpen(false); setPurgeConfirmed(false); }}
        onPurge={handlePurge}
        editDialogOpen={editDialogOpen}
        editAudiobook={mapBookToAudiobook(book)}
        onCloseEdit={() => setEditDialogOpen(false)}
        onEditSave={handleEditSave}
        conflictDialogOpen={conflictDialogOpen}
        onCloseConflict={() => setConflictDialogOpen(false)}
        onConflictReload={handleConflictReload}
        onConflictOverwrite={handleConflictOverwrite}
        historyDialogOpen={historyDialogOpen}
        onCloseHistory={() => setHistoryDialogOpen(false)}
        onUndoComplete={loadBook}
        metadataSearchOpen={metadataSearchOpen}
        onCloseMetadataSearch={() => setMetadataSearchOpen(false)}
        onMetadataApplied={setBook}
        toast={toast}
        relocateSegment={relocateSegment}
        onCloseRelocate={() => setRelocateSegment(null)}
        onRelocated={async () => { if (id) await reloadCurrentBookFiles(id); }}
        rejections={rejections}
        rejHistoryOpen={rejHistoryOpen}
        onSetRejHistoryOpen={setRejHistoryOpen}
        playlistDialogOpen={playlistDialogOpen}
        onClosePlaylist={() => setPlaylistDialogOpen(false)}
      />
    </Box>
  );
};
