// file: web/src/components/audiobooks/BulkMetadataSearchDialog.tsx
// version: 1.12.1
// guid: d4e5f6a7-b8c9-0d1e-2f3a-4b5c6d7e8f9a
// last-edited: 2026-10-10

import { useCallback, useEffect, useState, useRef } from 'react';
import { applyFieldClick } from './fieldRangeSelect';
import {
  Alert,
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
import WarningAmberIcon from '@mui/icons-material/WarningAmber';
import NavigateBeforeIcon from '@mui/icons-material/NavigateBefore';
import NavigateNextIcon from '@mui/icons-material/NavigateNext';
import SkipNextIcon from '@mui/icons-material/SkipNext';
import CheckCircleIcon from '@mui/icons-material/CheckCircle';
import type { Audiobook } from '../../types';
import type { BookFile, MetadataCandidate } from '../../services/api';
import * as api from '../../services/api';
import { rankScoreOf } from './rankScore';
import {
  METADATA_APPLY_FIELD_LABELS,
  candidateApplyFieldValue,
} from '../../config/metadataApplyFields';
import {
  candidateFields,
  sameCandidate,
  stagedFieldCount,
  submitStagedApplies,
  type StagedPick,
} from './stagedMetadataApply';

interface BulkMetadataSearchDialogProps {
  open: boolean;
  books: Audiobook[];
  onClose: () => void;
  // Session end: the user closed the wizard with picks staged, which are now
  // being applied in the background. The parent reloads the list and clears
  // its selection.
  onComplete: () => void;
  // Reload the library list and nothing else. Called when the background
  // applies of a closed session settle (or an Undo from their toast lands), by
  // which time the selection may belong to a session reopened on other books,
  // so it must not be touched.
  onLibraryChanged: () => void;
  toast: (
    message: string,
    severity?: 'success' | 'error' | 'warning' | 'info',
    action?: { label: string; onClick: () => void }
  ) => void;
}

const SOURCE_COLORS: Record<string, 'primary' | 'secondary' | 'success' | 'warning' | 'info'> = {
  openlibrary: 'primary',
  google_books: 'secondary',
  audible: 'success',
  goodreads: 'warning',
  manual: 'info',
};

type BookStatus = 'pending' | 'skipped';

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
  onLibraryChanged,
  toast,
}: BulkMetadataSearchDialogProps) {
  // The wizard's position is tracked by book id, not by list index, so a
  // filter change or a late list update cannot silently move it to another
  // book. null means "the first book in the list".
  const [currentBookId, setCurrentBookId] = useState<string | null>(null);
  const [query, setQuery] = useState('');
  const [authorQuery, setAuthorQuery] = useState('');
  const [narratorQuery, setNarratorQuery] = useState('');
  const [seriesQuery, setSeriesQuery] = useState('');
  const [showAdvanced, setShowAdvanced] = useState(false);
  const [results, setResults] = useState<MetadataCandidate[]>([]);
  const [loading, setLoading] = useState(false);
  const [previewCover, setPreviewCover] = useState<string | null>(null);
  const [expandedCard, setExpandedCard] = useState<number | null>(null);
  // Field ticks belong to ONE candidate: ticking a field on another card
  // starts a fresh selection for it, so "Stage selected" never sends one
  // card's ticks with another card's candidate.
  const [fieldSelection, setFieldSelection] = useState<{
    candidate: MetadataCandidate | null;
    fields: Set<string>;
  }>({ candidate: null, fields: new Set() });
  // The picks this session applies when the window closes, one per book (an
  // apply carries one candidate, and two queued applies of one book would
  // refuse each other). Picking never disables anything: a new pick for a
  // book replaces its staged one, Unstage drops it, Discard drops them all.
  const [staged, setStaged] = useState<Map<string, { book: Audiobook; pick: StagedPick }>>(
    new Map()
  );
  // A pick waiting for the user to confirm it over an ASIN conflict the search
  // flagged (the candidate names another ASIN than the book carries).
  // Confirming stages it with the override; a conflict the search did not
  // flag is caught by the server at submit and offered again from the toast.
  const [asinOverride, setAsinOverride] = useState<{
    book: Audiobook;
    candidate: MetadataCandidate;
    fields?: string[];
    bookAsin: string;
    candidateAsin: string;
    detail: string;
  } | null>(null);
  // Anchor for shift-click range selection over the visible field rows.
  const fieldAnchorRef = useRef<string | null>(null);
  const [bookStatuses, setBookStatuses] = useState<Map<string, BookStatus>>(new Map());
  const [writeToFiles, setWriteToFiles] = useState(true);
  // On by default: the wizard is a queue of books still needing metadata, so
  // books the server already reports as matched are hidden. Books staged in
  // this session stay listed (marked Staged) so a pick can be revised.
  const [skipApplied, setSkipApplied] = useState(true);
  const [sourceFilter, setSourceFilter] = useState<string | null>(null);
  const [sortResults, setSortResults] = useState<'score' | 'source'>('score');
  // Files list for the current book — always shown so the user can confirm
  // the file set even for single-file books. Eager-loaded on book change.
  const [files, setFiles] = useState<BookFile[]>([]);
  const [filesLoading, setFilesLoading] = useState(false);
  const [filesExpanded, setFilesExpanded] = useState(false);
  // The parent keeps this component mounted and only toggles `open`, so its
  // state outlives a close. A search or No Match request still in flight when
  // the user closes the dialog would otherwise write into that state after
  // handleClose reset it (stale results, a successor book from the old
  // selection). Each async handler captures `sessionRef.current` before
  // awaiting and drops its UI updates if handleClose (or unmount) has moved it
  // on since.
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
  // The background applies of a closed session (and an Undo clicked on their
  // toast) land after handleClose already refreshed. Reload the list here,
  // unless the page itself is gone. Never onComplete: it also clears the
  // parent's selection, and by now the dialog may have been reopened on new
  // books, so clearing it would empty the new session's books mid-use.
  const refreshAfterStaleWrite = () => {
    if (mountedRef.current) onLibraryChanged();
  };

  const handleToggleSkipApplied = (checked: boolean) => {
    setSkipApplied(checked);
    setCurrentBookId(null); // Reset to first book when filter changes
  };

  // The session's work set. With "Skip applied" on, books the server already
  // reports as matched are excluded up front. Staged books stay in the list so
  // their pick can be revised; picking moves the wizard on instead.
  const pool = skipApplied ? books.filter((b) => b.metadata_review_status !== 'matched') : books;
  const filteredBooks = pool;
  const foundIndex =
    currentBookId === null ? -1 : filteredBooks.findIndex((b) => b.id === currentBookId);
  const currentIndex = foundIndex >= 0 ? foundIndex : 0;
  const currentBook = filteredBooks[currentIndex];
  const stagedCount = staged.size;
  const skippedCount = [...bookStatuses.entries()].filter(
    ([id, s]) => s === 'skipped' && !staged.has(id)
  ).length;
  // Books handled so far (staged or skipped) out of the work set.
  const poolDoneCount = pool.filter(
    (b) => staged.has(b.id) || bookStatuses.get(b.id) === 'skipped'
  ).length;
  const alreadyAppliedCount = books.filter((b) => b.metadata_review_status === 'matched').length;
  const currentStaged = currentBook ? staged.get(currentBook.id) : undefined;

  // Search when the current book changes
  const doSearch = useCallback(
    async (searchQuery: string, author?: string, narrator?: string, series?: string) => {
      if (!currentBook?.id) return;
      const session = sessionRef.current;
      setLoading(true);
      setResults([]);
      setExpandedCard(null);
      setFieldSelection({ candidate: null, fields: new Set() });
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
      // Colours rows by the stored `missing` flag only; skip the per-file stat.
      .getBookFiles(currentBook.id, { signal: controller.signal, skipDiskCheck: true })
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

  // stagePick records `candidate` (narrowed to `fields`, undefined = all) as
  // the book's pick, replacing any earlier one, and moves the wizard on to the
  // next book. Nothing is sent: the dialog applies every staged pick when it
  // closes.
  const stagePick = (book: Audiobook, pick: StagedPick) => {
    setStaged((prev) => new Map(prev).set(book.id, { book, pick }));
    setBookStatuses((prev) => {
      if (!prev.has(book.id)) return prev;
      const next = new Map(prev);
      next.delete(book.id);
      return next;
    });
    advanceFrom(book.id, false);
  };

  // requestStage asks for confirmation first when the search flagged the
  // candidate's ASIN as conflicting with the book's.
  const requestStage = (candidate: MetadataCandidate, fields: string[] | undefined) => {
    const book = currentBook;
    const check = candidate.apply_check;
    if (check?.asin_conflict && check.book_asin) {
      setAsinOverride({
        book,
        candidate,
        fields,
        bookAsin: check.book_asin,
        candidateAsin: candidate.asin ?? '',
        detail: check.detail ?? '',
      });
      return;
    }
    stagePick(book, { candidate, fields });
  };

  const confirmAsinOverride = () => {
    if (!asinOverride) return;
    const { book, candidate, fields, bookAsin } = asinOverride;
    setAsinOverride(null);
    stagePick(book, { candidate, fields, overrideAsin: bookAsin });
  };

  const handlePickAll = (candidate: MetadataCandidate) => requestStage(candidate, undefined);

  const handleStageSelected = (candidate: MetadataCandidate) => {
    const fields = fieldSelection.candidate === candidate ? Array.from(fieldSelection.fields) : [];
    if (fields.length === 0) {
      toast('Select at least one field to stage', 'warning');
      return;
    }
    requestStage(candidate, fields);
  };

  const unstage = (bookId: string) => {
    setStaged((prev) => {
      if (!prev.has(bookId)) return prev;
      const next = new Map(prev);
      next.delete(bookId);
      return next;
    });
  };

  // Skip moves on. It never drops a staged pick: a book with one stays staged.
  const handleSkip = () => {
    if (!staged.has(currentBook.id)) {
      setBookStatuses((prev) => new Map(prev).set(currentBook.id, 'skipped'));
    }
    advanceFrom(currentBook.id, false);
  };

  const handleMarkNoMatch = async () => {
    const session = sessionRef.current;
    const bookId = currentBook.id;
    try {
      await api.markNoMatch(bookId);
      if (isStale(session)) return;
      // The newer decision wins: a book marked no match drops its staged pick.
      unstage(bookId);
      setBookStatuses((prev) => new Map(prev).set(bookId, 'skipped'));
      advanceFrom(bookId, false);
    } catch {
      if (isStale(session)) return;
      toast('Failed to mark as no match', 'error');
    }
  };

  // Move the wizard off `bookId` after it was staged / skipped. `leavesList`
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

  // resetSession detaches every request still in flight from this session
  // (see sessionRef) and clears the wizard for the next open.
  const resetSession = () => {
    sessionRef.current += 1;
    setLoading(false);
    setCurrentBookId(null);
    setBookStatuses(new Map());
    setStaged(new Map());
    setAsinOverride(null);
    setFieldSelection({ candidate: null, fields: new Set() });
  };

  // Closing applies every staged pick (Escape, backdrop and the footer button
  // alike): the owner asked for the picks to go in when the window closes, not
  // one per click. The applies are handed off and NOT awaited, so the dialog
  // is gone at once; each runs as a background operation and one toast reports
  // the batch. writeToFiles is read here, at close, for every book.
  const handleClose = () => {
    const entries = [...staged.values()];
    if (entries.length > 0) {
      void submitStagedApplies({
        entries: entries.map(({ book, pick }) => ({
          book: { id: book.id, title: book.title },
          pick,
        })),
        writeToFiles,
        toast,
        onDone: refreshAfterStaleWrite,
      });
      onComplete();
    }
    resetSession();
    onClose();
  };

  // Discard drops every staged pick and closes WITHOUT applying anything.
  const handleDiscardAndClose = () => {
    resetSession();
    onClose();
  };

  // Plain click toggles; shift-click selects the whole visible range from the
  // last-clicked field (file-manager semantics). See fieldRangeSelect.ts. A
  // click on another candidate's fields starts a fresh selection for it.
  const handleFieldClick = (
    candidate: MetadataCandidate,
    field: string,
    shiftKey: boolean,
    visibleFields: string[]
  ) => {
    setFieldSelection((prev) => {
      const same = prev.candidate === candidate;
      const r = applyFieldClick(
        same ? prev.fields : new Set<string>(),
        field,
        shiftKey,
        same ? fieldAnchorRef.current : null,
        visibleFields
      );
      fieldAnchorRef.current = r.anchor;
      return { candidate, fields: r.next };
    });
  };

  const applyCloseLabel = `Apply ${stagedCount} book${stagedCount === 1 ? '' : 's'} & close`;

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
          {stagedCount > 0 ? (
            <>
              <Button onClick={handleDiscardAndClose}>Discard all &amp; close</Button>
              <Button onClick={handleClose} variant="contained">
                {applyCloseLabel}
              </Button>
            </>
          ) : (
            <Button onClick={handleClose} variant="outlined">
              Close
            </Button>
          )}
        </DialogActions>
      </Dialog>
    );
  }

  const progress = pool.length > 0 ? Math.min(100, (poolDoneCount / pool.length) * 100) : 0;
  const status = currentStaged ? 'staged' : bookStatuses.get(currentBook?.id);

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
            {stagedCount > 0 && (
              <Chip
                icon={<CheckCircleIcon />}
                label={`${stagedCount} staged`}
                color="success"
                size="small"
                data-testid="bulk-staged-count"
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
            {status === 'staged' && <Chip label="Staged" color="success" size="small" />}
            {status === 'skipped' && <Chip label="Skipped" size="small" variant="outlined" />}
          </Stack>
        </Box>

        {currentStaged && (
          <Alert
            severity="success"
            sx={{ mb: 1.5 }}
            data-testid="bulk-staged-pick"
            action={
              <Button color="inherit" size="small" onClick={() => unstage(currentBook.id)}>
                Unstage
              </Button>
            }
          >
            Staged: {stagedFieldCount(currentStaged.pick)} field
            {stagedFieldCount(currentStaged.pick) === 1 ? '' : 's'} from{' '}
            {currentStaged.pick.candidate.source} &mdash; &ldquo;
            {currentStaged.pick.candidate.title}&rdquo;
            {currentStaged.pick.overrideAsin ? ' (over the ASIN conflict)' : ''}. Applied in the
            background when you close this window; pick another result to replace it.
          </Alert>
        )}

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
              sortResults === 'source' ? a.source.localeCompare(b.source) : rankScoreOf(b) - rankScoreOf(a)
            )
            .map((candidate, idx) => {
              const isStaged =
                !!currentStaged && sameCandidate(currentStaged.pick.candidate, candidate);
              const pickedAll = isStaged && !currentStaged?.pick.fields;
              const ticked =
                fieldSelection.candidate === candidate ? fieldSelection.fields : new Set<string>();
              return (
                <Box
                  key={idx}
                  data-staged={isStaged ? 'true' : undefined}
                  sx={{
                    border: isStaged ? 2 : 1,
                    borderColor: isStaged ? 'success.main' : 'divider',
                    borderRadius: 1,
                    p: 1.5,
                  }}
                >
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
                        {candidate.apply_check?.asin_conflict && (
                          <Tooltip title={candidate.apply_check.detail ?? ''}>
                            <Chip
                              icon={<WarningAmberIcon />}
                              label="ASIN conflict"
                              size="small"
                              color="error"
                            />
                          </Tooltip>
                        )}
                        {candidate.apply_check?.identity_stale &&
                          !candidate.apply_check?.asin_conflict && (
                            <Tooltip title={candidate.apply_check.detail ?? ''}>
                              <Chip
                                icon={<WarningAmberIcon />}
                                label="Fetched for another ASIN"
                                size="small"
                                color="warning"
                                variant="outlined"
                              />
                            </Tooltip>
                          )}
                      </Stack>
                    </Box>
                    {/* Never disabled: a pick only stages; picking again replaces it. */}
                    <Button
                      variant={pickedAll ? 'outlined' : 'contained'}
                      color={pickedAll ? 'success' : 'primary'}
                      size="small"
                      onClick={() => handlePickAll(candidate)}
                      startIcon={pickedAll ? <CheckCircleIcon /> : undefined}
                    >
                      {pickedAll ? 'Picked' : 'Pick'}
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
                          const visibleFields = candidateFields(candidate);
                          return visibleFields.map((field) => {
                            const value = candidateApplyFieldValue(candidate, field);
                            return (
                              <FormControlLabel
                                key={field}
                                control={
                                  <Checkbox
                                    checked={ticked.has(field)}
                                    onClick={(e) => {
                                      e.preventDefault();
                                      handleFieldClick(candidate, field, e.shiftKey, visibleFields);
                                    }}
                                    onChange={() => {}}
                                    size="small"
                                  />
                                }
                                label={
                                  <Typography variant="body2">
                                    {METADATA_APPLY_FIELD_LABELS[field]}: {value}
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
                            onClick={() => handleStageSelected(candidate)}
                            disabled={ticked.size === 0}
                          >
                            {isStaged && currentStaged?.pick.fields
                              ? 'Re-stage selected'
                              : 'Stage selected'}
                          </Button>
                        </Box>
                      </Box>
                    </Collapse>
                  </Box>
                </Box>
              );
            })}
        </Stack>
      </DialogContent>

      <DialogActions sx={{ justifyContent: 'space-between', px: 3, py: 2 }}>
        <Stack direction="row" spacing={1}>
          <Button color="warning" onClick={handleMarkNoMatch} size="small">
            No Match
          </Button>
          <Button onClick={handleSkip} startIcon={<SkipNextIcon />} size="small">
            Skip
          </Button>
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
          {stagedCount > 0 ? (
            <>
              <Button onClick={handleDiscardAndClose}>Discard all &amp; close</Button>
              <Button onClick={handleClose} variant="contained">
                {applyCloseLabel}
              </Button>
            </>
          ) : (
            <Button onClick={handleClose} variant="outlined">
              {poolDoneCount >= pool.length ? 'Done' : 'Close'}
            </Button>
          )}
        </Stack>
      </DialogActions>

      {/* ASIN conflict: applying replaces the book's record with another. */}
      <Dialog open={!!asinOverride} onClose={() => setAsinOverride(null)} maxWidth="xs">
        <DialogTitle>Pick over an ASIN conflict?</DialogTitle>
        <DialogContent>
          <Typography variant="body2" sx={{ mb: 1 }}>
            This candidate&apos;s ASIN
            {asinOverride?.candidateAsin ? ` (${asinOverride.candidateAsin})` : ''} is not the
            book&apos;s ({asinOverride?.bookAsin}). Applying it puts another record&apos;s metadata
            on this book. It is staged and applied when you close the window.
          </Typography>
          {asinOverride?.detail && (
            <Typography variant="caption" sx={{ color: 'text.secondary' }}>
              {asinOverride.detail}
            </Typography>
          )}
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setAsinOverride(null)}>Cancel</Button>
          <Button color="error" variant="contained" onClick={confirmAsinOverride}>
            Pick anyway
          </Button>
        </DialogActions>
      </Dialog>

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
