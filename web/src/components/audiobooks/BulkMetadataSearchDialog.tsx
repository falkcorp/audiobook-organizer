// file: web/src/components/audiobooks/BulkMetadataSearchDialog.tsx
// version: 1.7.0
// guid: d4e5f6a7-b8c9-0d1e-2f3a-4b5c6d7e8f9a
// last-edited: 2026-09-12

import { useCallback, useEffect, useState, useRef } from 'react';
import { applyFieldClick } from './fieldRangeSelect';
import {
  Avatar,
  Box,
  Button,
  Checkbox,
  Chip,
  CircularProgress,
  Collapse,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  FormControlLabel,
  IconButton,
  InputAdornment,
  LinearProgress,
  List,
  ListItem,
  Stack,
  Switch,
  TextField,
  Tooltip,
  Typography,
} from '@mui/material';
import SearchIcon from '@mui/icons-material/Search';
import FolderOpenIcon from '@mui/icons-material/FolderOpen';
import ExpandMoreIcon from '@mui/icons-material/ExpandMore';
import ExpandLessIcon from '@mui/icons-material/ExpandLess';
import HeadphonesIcon from '@mui/icons-material/Headphones';
import NavigateBeforeIcon from '@mui/icons-material/NavigateBefore';
import NavigateNextIcon from '@mui/icons-material/NavigateNext';
import SkipNextIcon from '@mui/icons-material/SkipNext';
import CheckCircleIcon from '@mui/icons-material/CheckCircle';
import UndoIcon from '@mui/icons-material/Undo';
import type { Audiobook } from '../../types';
import type { BookFile, MetadataCandidate } from '../../services/api';
import * as api from '../../services/api';

interface BulkMetadataSearchDialogProps {
  open: boolean;
  books: Audiobook[];
  onClose: () => void;
  onComplete: () => void;
  toast: (
    message: string,
    severity?: 'success' | 'error' | 'warning' | 'info',
    action?: { label: string; onClick: () => void }
  ) => void;
}

const FIELD_OPTIONS = [
  'title',
  'author',
  'narrator',
  'series',
  'series_position',
  'year',
  'publisher',
  'isbn',
  'cover_url',
  'description',
  'language',
] as const;

const FIELD_LABELS: Record<string, string> = {
  title: 'Title',
  author: 'Author',
  narrator: 'Narrator',
  series: 'Series',
  series_position: 'Series Position',
  year: 'Year',
  publisher: 'Publisher',
  isbn: 'ISBN',
  cover_url: 'Cover Image',
  description: 'Description',
  language: 'Language',
};

const SOURCE_COLORS: Record<string, 'primary' | 'secondary' | 'success' | 'warning' | 'info'> = {
  openlibrary: 'primary',
  google_books: 'secondary',
  audible: 'success',
  goodreads: 'warning',
  manual: 'info',
};

type BookStatus = 'pending' | 'applied' | 'skipped';

function formatDuration(seconds: number): string {
  const h = Math.floor(seconds / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  if (h > 0) return `${h}h ${m}m`;
  return `${m}m`;
}

function formatFileSize(bytes: number): string {
  if (bytes >= 1073741824) return `${(bytes / 1073741824).toFixed(1)} GB`;
  if (bytes >= 1048576) return `${(bytes / 1048576).toFixed(0)} MB`;
  return `${(bytes / 1024).toFixed(0)} KB`;
}

function basename(p: string): string {
  if (!p) return '';
  const parts = p.split('/');
  return parts[parts.length - 1] || p;
}

export function BulkMetadataSearchDialog({
  open,
  books,
  onClose,
  onComplete,
  toast,
}: BulkMetadataSearchDialogProps) {
  // The wizard's position is tracked by book id, not by list index: with
  // "Skip applied" on, applying a book removes it from filteredBooks, which
  // shifts every later index down by one. An index would then silently skip
  // the next book on every apply. null means "the first book in the list".
  const [currentBookId, setCurrentBookId] = useState<string | null>(null);
  const [query, setQuery] = useState('');
  const [authorQuery, setAuthorQuery] = useState('');
  const [narratorQuery, setNarratorQuery] = useState('');
  const [seriesQuery, setSeriesQuery] = useState('');
  const [showAdvanced, setShowAdvanced] = useState(false);
  const [results, setResults] = useState<MetadataCandidate[]>([]);
  const [loading, setLoading] = useState(false);
  const [previewCover, setPreviewCover] = useState<string | null>(null);
  const [applying, setApplying] = useState(false);
  const [expandedCard, setExpandedCard] = useState<number | null>(null);
  const [selectedFields, setSelectedFields] = useState<Set<string>>(new Set());
  // Anchor for shift-click range selection over the visible field rows.
  const fieldAnchorRef = useRef<string | null>(null);
  const [bookStatuses, setBookStatuses] = useState<Map<string, BookStatus>>(new Map());
  const [writeToFiles, setWriteToFiles] = useState(true);
  const [undoing, setUndoing] = useState(false);
  // On by default: the wizard is a queue of books still needing metadata, so
  // books already matched (on the server, or applied in this session) are
  // hidden. Turning it off shows every selected book, applied ones marked.
  const [skipApplied, setSkipApplied] = useState(true);
  const [sourceFilter, setSourceFilter] = useState<string | null>(null);
  const [sortResults, setSortResults] = useState<'score' | 'source'>('score');
  // Files list for the current book — always shown so the user can confirm
  // the file set even for single-file books. Eager-loaded on book change.
  const [files, setFiles] = useState<BookFile[]>([]);
  const [filesLoading, setFilesLoading] = useState(false);
  const [filesExpanded, setFilesExpanded] = useState(false);
  // Stack of book ids applied in this session so the persistent Undo button
  // can revert the last apply even after navigating away / banner dismissal.
  const [appliedStack, setAppliedStack] = useState<{ id: string; title: string }[]>([]);
  // The parent keeps this component mounted and only toggles `open`, so its
  // state outlives a close. A request still in flight when the user closes the
  // dialog would otherwise write into that state after handleClose reset it:
  // a phantom "Undo Last", a stale 'applied' status that filters the book out
  // of the next session, and a successor book from the old selection. Each
  // async handler captures `sessionRef.current` before awaiting and drops its
  // UI updates if handleClose (or unmount) has moved it on since.
  const sessionRef = useRef(0);
  const mountedRef = useRef(true);
  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      sessionRef.current += 1;
    };
  }, []);
  const isStale = (session: number) => session !== sessionRef.current;
  // An apply or undo that lands after the dialog closed still changed the
  // book on the server, and handleClose already decided whether to refresh
  // before it landed. Refresh the list here, unless the page itself is gone.
  const refreshAfterStaleWrite = () => {
    if (mountedRef.current) onComplete();
  };

  const handleToggleSkipApplied = (checked: boolean) => {
    setSkipApplied(checked);
    setCurrentBookId(null); // Reset to first book when filter changes
  };

  const isSessionApplied = (id: string) => bookStatuses.get(id) === 'applied';
  // The session's work set. With "Skip applied" on, books the server already
  // reports as matched are excluded up front. The `books` prop is never
  // refreshed while the dialog is open, so this alone cannot see applies made
  // in this session.
  const pool = skipApplied ? books.filter((b) => b.metadata_review_status !== 'matched') : books;
  // Session-aware: a book applied in this dialog leaves the list as well.
  const filteredBooks = skipApplied ? pool.filter((b) => !isSessionApplied(b.id)) : pool;
  const foundIndex =
    currentBookId === null ? -1 : filteredBooks.findIndex((b) => b.id === currentBookId);
  const currentIndex = foundIndex >= 0 ? foundIndex : 0;
  const currentBook = filteredBooks[currentIndex];
  const appliedCount = [...bookStatuses.values()].filter((s) => s === 'applied').length;
  const skippedCount = [...bookStatuses.values()].filter((s) => s === 'skipped').length;
  // Books handled so far out of the work set. Measured against `pool`, not the
  // shrinking filteredBooks, so progress cannot run past 100%.
  const poolDoneCount = pool.filter(
    (b) => bookStatuses.has(b.id) && bookStatuses.get(b.id) !== 'pending'
  ).length;
  const alreadyAppliedCount = books.filter(
    (b) => b.metadata_review_status === 'matched' || isSessionApplied(b.id)
  ).length;

  // Search when the current book changes
  const doSearch = useCallback(
    async (searchQuery: string, author?: string, narrator?: string, series?: string) => {
      if (!currentBook?.id) return;
      const session = sessionRef.current;
      setLoading(true);
      setResults([]);
      setExpandedCard(null);
      setSelectedFields(new Set());
      try {
        const resp = await api.searchMetadataForBook(
          currentBook.id,
          searchQuery,
          author || undefined,
          narrator || undefined,
          series || undefined
        );
        if (session !== sessionRef.current) return;
        setResults(resp.results || []);
      } catch {
        if (session !== sessionRef.current) return;
        setResults([]);
      } finally {
        if (session === sessionRef.current) setLoading(false);
      }
    },
    [currentBook?.id]
  );

  // Auto-search when navigating to a new book
  useEffect(() => {
    if (open && currentBook) {
      const q = currentBook.title || '';
      const a = currentBook.author || '';
      const n = currentBook.narrator || '';
      const s = currentBook.series || '';
      setQuery(q);
      setAuthorQuery(a);
      setNarratorQuery(n);
      setSeriesQuery(s);
      setShowAdvanced(false);
      // Search with author + narrator (series can over-constrain results)
      doSearch(q, a, n);
    }
    // Keyed on the book itself, not its index: an undo that re-admits an
    // earlier book shifts the index without changing the book, and must not
    // wipe what the user has typed into the search fields.
  }, [open, currentBook, doSearch]);

  // Eager-load the current book's files so the "N File(s)" control shows the
  // real count without a click. One request per navigation — cheap because the
  // dialog shows a single book at a time.
  useEffect(() => {
    if (!open || !currentBook?.id) return;
    const controller = new AbortController();
    setFiles([]);
    setFilesExpanded(false);
    setFilesLoading(true);
    api
      .getBookFiles(currentBook.id, { signal: controller.signal })
      .then((res) => setFiles(res.files || []))
      .catch(() => setFiles([]))
      .finally(() => setFilesLoading(false));
    return () => controller.abort();
  }, [open, currentBook?.id]);

  // Synthetic fallback so the Files control is never blank: if the API returns
  // nothing, show "1 File" using the book's own path/size.
  const displayFiles: BookFile[] =
    files.length > 0
      ? files
      : currentBook?.file_path
        ? [
            {
              id: `fallback-${currentBook.id}`,
              book_id: currentBook.id,
              file_path: currentBook.file_path,
              file_size: currentBook.file_size_bytes ?? undefined,
              format: currentBook.format ?? undefined,
              missing: false,
              created_at: '',
              updated_at: '',
            },
          ]
        : [];
  const fileCount = Math.max(displayFiles.length, 1);

  const handleSearch = () => doSearch(query, authorQuery, narratorQuery, seriesQuery);

  // Shared by "Apply all" and "Apply selected". `fields` undefined applies the
  // whole candidate. Every UI write after the await is dropped when the dialog
  // was closed (or unmounted) in the meantime; see sessionRef.
  const applyCandidate = async (
    candidate: MetadataCandidate,
    fields: string[] | undefined,
    successMessage: string
  ) => {
    const session = sessionRef.current;
    setApplying(true);
    const bookId = currentBook.id;
    const bookTitle = currentBook.title;
    try {
      await api.applyMetadataCandidate(bookId, candidate, fields, writeToFiles);
      if (isStale(session)) {
        refreshAfterStaleWrite();
        return;
      }
      toast(successMessage, 'success', {
        label: 'Undo',
        onClick: async () => {
          try {
            await api.undoLastApply(bookId);
            toast(`Undid metadata apply for "${bookTitle}"`, 'info');
            if (isStale(session)) {
              refreshAfterStaleWrite();
              return;
            }
            setBookStatuses((prev) => new Map(prev).set(bookId, 'pending'));
            setAppliedStack((prev) => prev.filter((b) => b.id !== bookId));
          } catch {
            /* ignore */
          }
        },
      });
      setBookStatuses((prev) => new Map(prev).set(bookId, 'applied'));
      setAppliedStack((prev) => [...prev, { id: bookId, title: bookTitle }]);
      // With "Skip applied" on, the book leaves the list on this render.
      advanceFrom(bookId, skipApplied);
    } catch (err) {
      if (isStale(session)) return;
      toast(err instanceof Error ? err.message : 'Failed to apply metadata', 'error');
    } finally {
      // handleClose resets `applying` itself, so a stale request must not
      // clear the flag for an apply started in the next session.
      if (!isStale(session)) setApplying(false);
    }
  };

  const handleApplyAll = (candidate: MetadataCandidate) =>
    applyCandidate(
      candidate,
      undefined,
      `Applied metadata to "${currentBook.title}" from ${candidate.source}`
    );

  const handleApplySelected = async (candidate: MetadataCandidate) => {
    if (selectedFields.size === 0) {
      toast('Select at least one field to apply', 'warning');
      return;
    }
    await applyCandidate(
      candidate,
      Array.from(selectedFields),
      `Applied selected fields to "${currentBook.title}"`
    );
  };

  // Undo the most-recently applied book in this session. Works after the
  // success banner is gone and after navigating to other books, because it
  // keys off the applied stack rather than the current book.
  const handleUndoLastApplied = async () => {
    const last = appliedStack[appliedStack.length - 1];
    if (!last) return;
    const session = sessionRef.current;
    setUndoing(true);
    try {
      await api.undoLastApply(last.id);
      if (isStale(session)) {
        refreshAfterStaleWrite();
        return;
      }
      toast(`Undid metadata apply for "${last.title}"`, 'success');
      setBookStatuses((prev) => new Map(prev).set(last.id, 'pending'));
      setAppliedStack((prev) => prev.slice(0, -1));
    } catch (err) {
      if (isStale(session)) return;
      toast(err instanceof Error ? err.message : 'Failed to undo', 'error');
    } finally {
      if (!isStale(session)) setUndoing(false);
    }
  };

  const handleUndoCurrentBook = async () => {
    const session = sessionRef.current;
    const bookId = currentBook.id;
    const bookTitle = currentBook.title;
    setUndoing(true);
    try {
      const resp = await api.undoLastApply(bookId);
      if (isStale(session)) {
        refreshAfterStaleWrite();
        return;
      }
      toast(`Undid ${resp.undone_fields.length} field(s) for "${bookTitle}"`, 'success');
      setBookStatuses((prev) => new Map(prev).set(bookId, 'pending'));
      setAppliedStack((prev) => prev.filter((b) => b.id !== bookId));
    } catch (err) {
      if (isStale(session)) return;
      toast(err instanceof Error ? err.message : 'Failed to undo', 'error');
    } finally {
      if (!isStale(session)) setUndoing(false);
    }
  };

  const handleSkip = () => {
    setBookStatuses((prev) => new Map(prev).set(currentBook.id, 'skipped'));
    advanceFrom(currentBook.id, false);
  };

  const handleMarkNoMatch = async () => {
    const session = sessionRef.current;
    const bookId = currentBook.id;
    try {
      await api.markNoMatch(bookId);
      if (isStale(session)) return;
      setBookStatuses((prev) => new Map(prev).set(bookId, 'skipped'));
      advanceFrom(bookId, false);
    } catch {
      if (isStale(session)) return;
      toast('Failed to mark as no match', 'error');
    }
  };

  // Move the wizard off `bookId` after it was applied / skipped. `leavesList`
  // says the book is about to drop out of filteredBooks; then the successor is
  // the book after it or, if it was last, the one before it. The successor is
  // taken from the list as it stood when the action started, which is the
  // list the user was looking at. If the user navigated elsewhere while the
  // request was in flight (Previous/Next stay enabled), their position wins.
  const advanceFrom = (bookId: string, leavesList: boolean) => {
    const idx = filteredBooks.findIndex((b) => b.id === bookId);
    if (idx < 0) return;
    const next = filteredBooks[idx + 1] ?? (leavesList ? filteredBooks[idx - 1] : undefined);
    if (!next) return; // last book and it stays: remain on it
    setCurrentBookId((prev) => {
      const shown = filteredBooks.some((b) => b.id === prev)
        ? prev
        : (filteredBooks[0]?.id ?? null);
      return shown === bookId ? next.id : prev;
    });
    setSourceFilter(null); // reset filter for next book
  };

  const goToIndex = (index: number) => {
    const target = filteredBooks[index];
    if (target) setCurrentBookId(target.id);
  };

  const handleClose = () => {
    if (appliedCount > 0) {
      onComplete();
    }
    // Detach every request still in flight from this session (see sessionRef).
    // Their busy flags are cleared here because they will no longer clear them.
    sessionRef.current += 1;
    setApplying(false);
    setUndoing(false);
    setLoading(false);
    setCurrentBookId(null);
    setBookStatuses(new Map());
    setAppliedStack([]);
    onClose();
  };

  const toggleField = (field: string) => {
    setSelectedFields((prev) => {
      const next = new Set(prev);
      if (next.has(field)) next.delete(field);
      else next.add(field);
      return next;
    });
  };
  void toggleField; // retained for non-range callers/tests

  // Plain click toggles; shift-click selects the whole visible range from the
  // last-clicked field (file-manager semantics). See fieldRangeSelect.ts.
  const handleFieldClick = (field: string, shiftKey: boolean, visibleFields: string[]) => {
    setSelectedFields((prev) => {
      const r = applyFieldClick(prev, field, shiftKey, fieldAnchorRef.current, visibleFields);
      fieldAnchorRef.current = r.anchor;
      return r.next;
    });
  };

  if (!open || books.length === 0) return null;

  // All books filtered out — show message instead of closing
  if (filteredBooks.length === 0) {
    return (
      <Dialog open={open} onClose={handleClose} maxWidth="sm" fullWidth>
        <DialogTitle>Search Metadata</DialogTitle>
        <DialogContent>
          <Typography
            sx={{
              color: 'text.secondary',
              py: 3,
              textAlign: 'center',
            }}
          >
            All {books.length} book(s) have metadata applied.
            <br />
            <Button size="small" onClick={() => handleToggleSkipApplied(false)} sx={{ mt: 1 }}>
              Show all books
            </Button>
          </Typography>
        </DialogContent>
        <DialogActions>
          {/* Reachable by applying the last remaining book, so the session's
              undo must stay available here. Undoing re-admits the book. */}
          {appliedStack.length > 0 && (
            <Button
              color="warning"
              startIcon={<UndoIcon />}
              onClick={handleUndoLastApplied}
              disabled={undoing}
              size="small"
              variant="contained"
            >
              {undoing ? 'Undoing…' : `Undo Last (${appliedStack.length})`}
            </Button>
          )}
          <Button onClick={handleClose} variant="outlined">
            Close
          </Button>
        </DialogActions>
      </Dialog>
    );
  }

  const progress = pool.length > 0 ? Math.min(100, (poolDoneCount / pool.length) * 100) : 0;
  const status = bookStatuses.get(currentBook?.id);

  return (
    <Dialog open={open} onClose={handleClose} maxWidth="md" fullWidth>
      <DialogTitle sx={{ pb: 1 }}>
        <Box sx={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
          <Typography variant="h6">
            Search Metadata — Book {currentIndex + 1} of {filteredBooks.length}
            {skipApplied && alreadyAppliedCount > 0 ? ` (${alreadyAppliedCount} filtered)` : ''}
          </Typography>
          <Stack
            direction="row"
            spacing={1}
            sx={{
              alignItems: 'center',
            }}
          >
            {appliedCount > 0 && (
              <Chip
                icon={<CheckCircleIcon />}
                label={`${appliedCount} applied`}
                color="success"
                size="small"
              />
            )}
            {skippedCount > 0 && (
              <Chip label={`${skippedCount} skipped`} size="small" variant="outlined" />
            )}
          </Stack>
        </Box>
        <LinearProgress variant="determinate" value={progress} sx={{ mt: 1, borderRadius: 1 }} />
      </DialogTitle>

      <DialogContent>
        {/* Current book info — click to enlarge cover */}
        <Box
          sx={{
            p: 1.5,
            mb: 2,
            border: 1,
            borderColor: 'divider',
            borderRadius: 1,
            bgcolor: 'action.hover',
            cursor: currentBook.cover_url ? 'pointer' : 'default',
            '&:hover': currentBook.cover_url
              ? { borderColor: 'primary.main', bgcolor: 'action.selected' }
              : {},
          }}
          onClick={() => {
            if (currentBook.cover_url) setPreviewCover(currentBook.cover_url);
          }}
        >
          <Stack
            direction="row"
            spacing={2}
            sx={{
              alignItems: 'flex-start',
            }}
          >
            {currentBook.cover_url && (
              <Avatar src={currentBook.cover_url} variant="rounded" sx={{ width: 48, height: 64 }}>
                {currentBook.title?.[0]}
              </Avatar>
            )}
            <Box sx={{ flex: 1, minWidth: 0 }}>
              <Typography
                variant="subtitle1"
                noWrap
                sx={{
                  fontWeight: 'bold',
                }}
              >
                {currentBook.title || 'Untitled'}
              </Typography>
              <Typography
                variant="body2"
                sx={{
                  color: 'text.secondary',
                }}
              >
                {currentBook.author || 'Unknown author'}
                {currentBook.narrator ? ` — Narrated by ${currentBook.narrator}` : ''}
              </Typography>
              <Stack
                direction="row"
                spacing={1}
                sx={{
                  flexWrap: 'wrap',
                  mt: 0.5,
                }}
              >
                {currentBook.format && (
                  <Chip
                    label={currentBook.format.toUpperCase()}
                    size="small"
                    color="primary"
                    variant="outlined"
                  />
                )}
                {currentBook.duration_seconds != null && currentBook.duration_seconds > 0 && (
                  <Chip
                    label={formatDuration(currentBook.duration_seconds)}
                    size="small"
                    variant="outlined"
                  />
                )}
                {currentBook.series && (
                  <Chip
                    label={`${currentBook.series}${currentBook.series_number ? ` #${currentBook.series_number}` : ''}`}
                    size="small"
                    color="info"
                    variant="outlined"
                  />
                )}
                {currentBook.file_size_bytes != null && currentBook.file_size_bytes > 0 && (
                  <Chip
                    label={formatFileSize(currentBook.file_size_bytes)}
                    size="small"
                    variant="outlined"
                  />
                )}
                {currentBook.cover_url ? (
                  <Chip label="Has Cover" size="small" color="success" variant="outlined" />
                ) : (
                  <Chip label="No Cover" size="small" color="warning" variant="outlined" />
                )}
                {currentBook.language && (
                  <Chip label={currentBook.language} size="small" variant="outlined" />
                )}
              </Stack>
              {currentBook.file_path && (
                <Typography
                  variant="caption"
                  sx={{
                    color: 'text.secondary',
                    display: 'block',
                    mt: 0.5,
                    wordBreak: 'break-all',
                  }}
                >
                  File: {currentBook.file_path}
                </Typography>
              )}
              {currentBook.original_filename &&
                currentBook.original_filename !== currentBook.file_path?.split('/').pop() && (
                  <Typography
                    variant="caption"
                    sx={{
                      color: 'text.secondary',
                      display: 'block',
                      wordBreak: 'break-all',
                    }}
                  >
                    Original: {currentBook.original_filename}
                  </Typography>
                )}
              {currentBook.itunes_path && (
                <Typography
                  variant="caption"
                  sx={{
                    color: 'info.main',
                    display: 'block',
                    wordBreak: 'break-all',
                  }}
                >
                  iTunes: {currentBook.itunes_path}
                </Typography>
              )}

              {/* Always-visible Files control — shows even for single-file books
                  so the user can confirm the file set. Mirrors the Library page's
                  expandable "N Files" list. Stop click propagation so expanding
                  the list doesn't trigger the card's cover-preview handler. */}
              <Box sx={{ mt: 0.75 }} onClick={(e) => e.stopPropagation()}>
                <Chip
                  icon={filesLoading ? <CircularProgress size={14} /> : <FolderOpenIcon />}
                  label={
                    filesLoading
                      ? 'Loading files…'
                      : `${fileCount} File${fileCount === 1 ? '' : 's'}`
                  }
                  size="small"
                  variant="outlined"
                  clickable
                  onClick={() => setFilesExpanded((v) => !v)}
                  deleteIcon={filesExpanded ? <ExpandLessIcon /> : <ExpandMoreIcon />}
                  onDelete={() => setFilesExpanded((v) => !v)}
                />
                <Collapse in={filesExpanded}>
                  <List dense disablePadding sx={{ mt: 0.5, maxHeight: 200, overflow: 'auto' }}>
                    {displayFiles.map((f) => {
                      const meta = [
                        f.format,
                        f.file_size != null && f.file_size > 0 ? formatFileSize(f.file_size) : null,
                        f.duration != null && f.duration > 0 ? formatDuration(f.duration) : null,
                      ]
                        .filter(Boolean)
                        .join(' · ');
                      return (
                        <ListItem key={f.id} disableGutters sx={{ display: 'block', py: 0.25 }}>
                          <Tooltip title={f.file_path} placement="bottom-start">
                            <Typography
                              variant="caption"
                              sx={{
                                fontFamily: 'monospace',
                                display: 'block',
                                color: f.missing ? 'error.main' : 'text.primary',
                                wordBreak: 'break-all',
                              }}
                            >
                              {basename(f.file_path)}
                            </Typography>
                          </Tooltip>
                          {meta && (
                            <Typography
                              variant="caption"
                              sx={{
                                color: 'text.secondary',
                              }}
                            >
                              {meta}
                            </Typography>
                          )}
                        </ListItem>
                      );
                    })}
                  </List>
                </Collapse>
              </Box>
            </Box>
            {status === 'applied' && <Chip label="Applied" color="success" size="small" />}
            {status === 'skipped' && <Chip label="Skipped" size="small" variant="outlined" />}
          </Stack>
        </Box>

        {/* Search */}
        <TextField
          fullWidth
          size="small"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter') handleSearch();
          }}
          placeholder="Search by title, ISBN, or ASIN..."
          sx={{ mb: 1 }}
          slotProps={{
            input: {
              startAdornment: (
                <InputAdornment position="start">
                  <SearchIcon />
                </InputAdornment>
              ),
              endAdornment: (
                <InputAdornment position="end">
                  <IconButton onClick={handleSearch} disabled={loading} size="small">
                    <SearchIcon />
                  </IconButton>
                </InputAdornment>
              ),
            },
          }}
        />
        <Button
          size="small"
          onClick={() => setShowAdvanced(!showAdvanced)}
          endIcon={showAdvanced ? <ExpandLessIcon /> : <ExpandMoreIcon />}
          sx={{ mb: 1, textTransform: 'none' }}
        >
          Advanced
        </Button>
        <Collapse in={showAdvanced}>
          <Stack spacing={1.5} sx={{ mb: 2 }}>
            <TextField
              fullWidth
              size="small"
              value={authorQuery}
              onChange={(e) => setAuthorQuery(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter') handleSearch();
              }}
              placeholder="Author name (narrows results)"
              label="Author"
            />
            <TextField
              fullWidth
              size="small"
              value={narratorQuery}
              onChange={(e) => setNarratorQuery(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter') handleSearch();
              }}
              placeholder="Narrator name (boosts matching results)"
              label="Narrator"
            />
            <TextField
              fullWidth
              size="small"
              value={seriesQuery}
              onChange={(e) => setSeriesQuery(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter') handleSearch();
              }}
              placeholder="Series name (boosts matching series)"
              label="Series"
            />
          </Stack>
        </Collapse>

        {/* Toggles and undo */}
        <Stack
          direction="row"
          spacing={2}
          sx={{
            alignItems: 'center',
            mb: 1,
          }}
        >
          <Tooltip title="Write applied metadata to audio file tags (MP3/M4B/M4A)">
            <FormControlLabel
              control={
                <Switch
                  checked={writeToFiles}
                  onChange={(e) => setWriteToFiles(e.target.checked)}
                  size="small"
                />
              }
              label={<Typography variant="body2">Write to files</Typography>}
            />
          </Tooltip>
          <Tooltip
            title={`Skip books that already have metadata applied${alreadyAppliedCount > 0 ? ` (${alreadyAppliedCount} books)` : ''}`}
          >
            <FormControlLabel
              control={
                <Switch
                  checked={skipApplied}
                  onChange={(e) => handleToggleSkipApplied(e.target.checked)}
                  size="small"
                />
              }
              label={<Typography variant="body2">Skip applied</Typography>}
            />
          </Tooltip>
          {status === 'applied' && (
            <Button
              size="small"
              color="warning"
              startIcon={<UndoIcon />}
              onClick={handleUndoCurrentBook}
              disabled={undoing}
            >
              {undoing ? 'Undoing...' : 'Undo'}
            </Button>
          )}
        </Stack>

        {/* Results */}
        {loading && (
          <Box sx={{ display: 'flex', justifyContent: 'center', py: 3 }}>
            <CircularProgress />
          </Box>
        )}

        {!loading && results.length === 0 && (
          <Typography
            sx={{
              color: 'text.secondary',
              py: 3,
              textAlign: 'center',
            }}
          >
            No results found. Try a different query or paste an Audible ASIN.
          </Typography>
        )}

        {/* Source filter + sort */}
        {!loading &&
          results.length > 0 &&
          (() => {
            const sources = Array.from(new Set(results.map((r) => r.source)));
            return (
              <Stack
                direction="row"
                spacing={0.5}
                sx={{
                  alignItems: 'center',
                  flexWrap: 'wrap',
                  mb: 1,
                }}
              >
                <Chip
                  label="All"
                  size="small"
                  variant={sourceFilter === null ? 'filled' : 'outlined'}
                  onClick={() => setSourceFilter(null)}
                />
                {sources.map((src) => (
                  <Chip
                    key={src}
                    label={`${src} (${results.filter((r) => r.source === src).length})`}
                    size="small"
                    color={SOURCE_COLORS[src] || 'default'}
                    variant={sourceFilter === src ? 'filled' : 'outlined'}
                    onClick={() => setSourceFilter(sourceFilter === src ? null : src)}
                  />
                ))}
                <Box sx={{ flex: 1 }} />
                <Chip
                  label={sortResults === 'score' ? 'Sort: Score' : 'Sort: Source'}
                  size="small"
                  variant="outlined"
                  onClick={() => setSortResults(sortResults === 'score' ? 'source' : 'score')}
                />
              </Stack>
            );
          })()}

        <Stack spacing={1.5} sx={{ maxHeight: '50vh', overflow: 'auto' }}>
          {results
            .filter((c) => !sourceFilter || c.source === sourceFilter)
            .sort((a, b) =>
              sortResults === 'source' ? a.source.localeCompare(b.source) : b.score - a.score
            )
            .map((candidate, idx) => (
              <Box key={idx} sx={{ border: 1, borderColor: 'divider', borderRadius: 1, p: 1.5 }}>
                <Stack
                  direction="row"
                  spacing={2}
                  sx={{
                    alignItems: 'flex-start',
                  }}
                >
                  <Avatar
                    src={candidate.cover_url}
                    variant="rounded"
                    sx={{
                      width: 50,
                      height: 65,
                      cursor: candidate.cover_url ? 'pointer' : 'default',
                      '&:hover': candidate.cover_url ? { opacity: 0.8 } : {},
                    }}
                    onClick={() => {
                      if (candidate.cover_url) setPreviewCover(candidate.cover_url);
                    }}
                  >
                    {candidate.title?.[0] || '?'}
                  </Avatar>
                  <Box sx={{ flex: 1, minWidth: 0 }}>
                    <Typography
                      variant="body1"
                      noWrap
                      sx={{
                        fontWeight: 'bold',
                      }}
                    >
                      {candidate.title}
                    </Typography>
                    <Typography
                      variant="body2"
                      sx={{
                        color: 'text.secondary',
                      }}
                    >
                      {candidate.author}
                      {candidate.year ? ` (${candidate.year})` : ''}
                    </Typography>
                    {candidate.series && (
                      <Typography
                        variant="body2"
                        sx={{
                          color: 'text.secondary',
                        }}
                      >
                        Series: {candidate.series}
                        {candidate.series_position ? ` · Book ${candidate.series_position}` : ''}
                      </Typography>
                    )}
                    {candidate.narrator && (
                      <Typography
                        variant="body2"
                        sx={{
                          color: 'text.secondary',
                        }}
                      >
                        Narrator: {candidate.narrator}
                      </Typography>
                    )}
                    <Stack direction="row" spacing={0.5} sx={{ mt: 0.5 }}>
                      <Chip
                        label={candidate.source}
                        size="small"
                        color={SOURCE_COLORS[candidate.source] || 'default'}
                      />
                      <Chip
                        label={`${Math.round(candidate.score * 100)}%`}
                        size="small"
                        variant="outlined"
                      />
                      {candidate.narrator && (
                        <Chip
                          icon={<HeadphonesIcon />}
                          label="Audiobook"
                          size="small"
                          color="info"
                          variant="outlined"
                        />
                      )}
                    </Stack>
                  </Box>
                  <Button
                    variant="contained"
                    size="small"
                    onClick={() => handleApplyAll(candidate)}
                    disabled={applying || bookStatuses.get(currentBook.id) === 'applied'}
                    sx={bookStatuses.get(currentBook.id) === 'applied' ? { opacity: 0.5 } : {}}
                    startIcon={
                      bookStatuses.get(currentBook.id) === 'applied' ? (
                        <CheckCircleIcon />
                      ) : undefined
                    }
                  >
                    {bookStatuses.get(currentBook.id) === 'applied' ? 'Applied' : 'Apply'}
                  </Button>
                </Stack>

                {/* Field selector */}
                <Box sx={{ mt: 0.5 }}>
                  <Button
                    size="small"
                    onClick={() => setExpandedCard(expandedCard === idx ? null : idx)}
                    endIcon={expandedCard === idx ? <ExpandLessIcon /> : <ExpandMoreIcon />}
                  >
                    Select fields...
                  </Button>
                  <Collapse in={expandedCard === idx}>
                    <Box sx={{ mt: 0.5, pl: 1 }}>
                      {(() => {
                        const visibleFields = FIELD_OPTIONS.filter((f) => {
                          const v = candidate[f as keyof MetadataCandidate];
                          return v !== undefined && v !== null && v !== '';
                        });
                        return visibleFields.map((field) => {
                          const value = candidate[field as keyof MetadataCandidate];
                          return (
                            <FormControlLabel
                              key={field}
                              control={
                                <Checkbox
                                  checked={selectedFields.has(field)}
                                  onClick={(e) => {
                                    e.preventDefault();
                                    handleFieldClick(field, e.shiftKey, visibleFields);
                                  }}
                                  onChange={() => {}}
                                  size="small"
                                />
                              }
                              label={
                                <Typography variant="body2">
                                  {FIELD_LABELS[field] || field}: {String(value)}
                                </Typography>
                              }
                            />
                          );
                        });
                      })()}
                      <Box sx={{ mt: 0.5 }}>
                        <Button
                          variant="outlined"
                          size="small"
                          onClick={() => handleApplySelected(candidate)}
                          disabled={
                            applying ||
                            selectedFields.size === 0 ||
                            bookStatuses.get(currentBook.id) === 'applied'
                          }
                          sx={
                            bookStatuses.get(currentBook.id) === 'applied' ? { opacity: 0.5 } : {}
                          }
                        >
                          {bookStatuses.get(currentBook.id) === 'applied'
                            ? 'Applied'
                            : 'Apply Selected'}
                        </Button>
                      </Box>
                    </Box>
                  </Collapse>
                </Box>
              </Box>
            ))}
        </Stack>
      </DialogContent>

      <DialogActions sx={{ justifyContent: 'space-between', px: 3, py: 2 }}>
        <Stack direction="row" spacing={1}>
          <Button color="warning" onClick={handleMarkNoMatch} disabled={applying} size="small">
            No Match
          </Button>
          <Button onClick={handleSkip} startIcon={<SkipNextIcon />} size="small">
            Skip
          </Button>
          {status === 'applied' && (
            <Button
              color="warning"
              startIcon={<UndoIcon />}
              onClick={handleUndoCurrentBook}
              disabled={undoing}
              size="small"
              variant="outlined"
            >
              Undo
            </Button>
          )}
          {/* Persistent undo for the last applied book — remains usable after
              the success banner is gone and after navigating to other books. */}
          {appliedStack.length > 0 && (
            <Tooltip title={`Undo last applied: "${appliedStack[appliedStack.length - 1].title}"`}>
              <Button
                color="warning"
                startIcon={<UndoIcon />}
                onClick={handleUndoLastApplied}
                disabled={undoing}
                size="small"
                variant="contained"
              >
                {undoing ? 'Undoing…' : `Undo Last (${appliedStack.length})`}
              </Button>
            </Tooltip>
          )}
        </Stack>
        <Stack direction="row" spacing={1}>
          <Button
            onClick={() => goToIndex(currentIndex - 1)}
            disabled={currentIndex === 0}
            startIcon={<NavigateBeforeIcon />}
            size="small"
          >
            Previous
          </Button>
          <Button
            onClick={() => goToIndex(currentIndex + 1)}
            disabled={currentIndex >= filteredBooks.length - 1}
            endIcon={<NavigateNextIcon />}
            size="small"
          >
            Next
          </Button>
          <Button onClick={handleClose} variant="outlined">
            {poolDoneCount >= pool.length ? 'Done' : 'Close'}
          </Button>
        </Stack>
      </DialogActions>

      {/* Cover Preview */}
      <Dialog open={!!previewCover} onClose={() => setPreviewCover(null)} maxWidth="sm">
        <Box
          component="img"
          src={previewCover ?? ''}
          alt="Cover preview"
          onClick={() => setPreviewCover(null)}
          sx={{ maxWidth: '100%', maxHeight: '80vh', cursor: 'pointer', display: 'block' }}
        />
      </Dialog>
    </Dialog>
  );
}
