// file: web/src/components/audiobooks/BulkMetadataSearchDialog.tsx
// version: 1.4.0
// guid: d4e5f6a7-b8c9-0d1e-2f3a-4b5c6d7e8f9a
// last-edited: 2026-06-21

import { useCallback, useEffect, useState } from 'react';
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
import SearchIcon from '@mui/icons-material/Search.js';
import FolderOpenIcon from '@mui/icons-material/FolderOpen.js';
import ExpandMoreIcon from '@mui/icons-material/ExpandMore.js';
import ExpandLessIcon from '@mui/icons-material/ExpandLess.js';
import HeadphonesIcon from '@mui/icons-material/Headphones.js';
import NavigateBeforeIcon from '@mui/icons-material/NavigateBefore.js';
import NavigateNextIcon from '@mui/icons-material/NavigateNext.js';
import SkipNextIcon from '@mui/icons-material/SkipNext.js';
import CheckCircleIcon from '@mui/icons-material/CheckCircle.js';
import UndoIcon from '@mui/icons-material/Undo.js';
import type { Audiobook } from '../../types';
import type { BookFile, MetadataCandidate } from '../../services/api';
import * as api from '../../services/api';

interface BulkMetadataSearchDialogProps {
  open: boolean;
  books: Audiobook[];
  onClose: () => void;
  onComplete: () => void;
  toast: (message: string, severity?: 'success' | 'error' | 'warning' | 'info', action?: { label: string; onClick: () => void }) => void;
}

const FIELD_OPTIONS = [
  'title', 'author', 'narrator', 'series', 'series_position',
  'year', 'publisher', 'isbn', 'cover_url', 'description', 'language',
] as const;

const FIELD_LABELS: Record<string, string> = {
  title: 'Title', author: 'Author', narrator: 'Narrator', series: 'Series',
  series_position: 'Series Position', year: 'Year', publisher: 'Publisher',
  isbn: 'ISBN', cover_url: 'Cover Image', description: 'Description', language: 'Language',
};

const SOURCE_COLORS: Record<string, 'primary' | 'secondary' | 'success' | 'warning' | 'info'> = {
  openlibrary: 'primary', google_books: 'secondary', audible: 'success', goodreads: 'warning', manual: 'info',
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

export function BulkMetadataSearchDialog({ open, books, onClose, onComplete, toast }: BulkMetadataSearchDialogProps) {
  const [currentIndex, setCurrentIndex] = useState(0);
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
  const [bookStatuses, setBookStatuses] = useState<Map<string, BookStatus>>(new Map());
  const [writeToFiles, setWriteToFiles] = useState(true);
  const [undoing, setUndoing] = useState(false);
  const [skipApplied, setSkipApplied] = useState(false);
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

  const handleToggleSkipApplied = (checked: boolean) => {
    setSkipApplied(checked);
    setCurrentIndex(0); // Reset to first book when filter changes
  };

  // Filter books based on skipApplied toggle
  const filteredBooks = skipApplied
    ? books.filter((b) => b.metadata_review_status !== 'matched')
    : books;
  const currentBook = filteredBooks[currentIndex];
  const appliedCount = [...bookStatuses.values()].filter((s) => s === 'applied').length;
  const skippedCount = [...bookStatuses.values()].filter((s) => s === 'skipped').length;
  const alreadyAppliedCount = books.filter((b) => b.metadata_review_status === 'matched').length;

  // Search when the current book changes
  const doSearch = useCallback(async (searchQuery: string, author?: string, narrator?: string, series?: string) => {
    if (!currentBook?.id) return;
    setLoading(true);
    setResults([]);
    setExpandedCard(null);
    setSelectedFields(new Set());
    try {
      const resp = await api.searchMetadataForBook(currentBook.id, searchQuery, author || undefined, narrator || undefined, series || undefined);
      setResults(resp.results || []);
    } catch {
      setResults([]);
    } finally {
      setLoading(false);
    }
  }, [currentBook?.id]);

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
  }, [open, currentIndex, currentBook, doSearch]);

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
  const displayFiles: BookFile[] = files.length > 0
    ? files
    : currentBook?.file_path
      ? [{
          id: `fallback-${currentBook.id}`,
          book_id: currentBook.id,
          file_path: currentBook.file_path,
          file_size: currentBook.file_size_bytes ?? undefined,
          format: currentBook.format ?? undefined,
          missing: false,
          created_at: '',
          updated_at: '',
        }]
      : [];
  const fileCount = Math.max(displayFiles.length, 1);

  const handleSearch = () => doSearch(query, authorQuery, narratorQuery, seriesQuery);

  const handleApplyAll = async (candidate: MetadataCandidate) => {
    setApplying(true);
    const bookId = currentBook.id;
    const bookTitle = currentBook.title;
    try {
      await api.applyMetadataCandidate(bookId, candidate, undefined, writeToFiles);
      toast(`Applied metadata to "${bookTitle}" from ${candidate.source}`, 'success', {
        label: 'Undo',
        onClick: async () => {
          try {
            await api.undoLastApply(bookId);
            toast(`Undid metadata apply for "${bookTitle}"`, 'info');
            setBookStatuses((prev) => new Map(prev).set(bookId, 'pending'));
            setAppliedStack((prev) => prev.filter((b) => b.id !== bookId));
          } catch { /* ignore */ }
        },
      });
      setBookStatuses((prev) => new Map(prev).set(bookId, 'applied'));
      setAppliedStack((prev) => [...prev, { id: bookId, title: bookTitle }]);
      advanceToNext();
    } catch (err) {
      toast(err instanceof Error ? err.message : 'Failed to apply metadata', 'error');
    } finally {
      setApplying(false);
    }
  };

  const handleApplySelected = async (candidate: MetadataCandidate) => {
    if (selectedFields.size === 0) {
      toast('Select at least one field to apply', 'warning');
      return;
    }
    setApplying(true);
    const bookId = currentBook.id;
    const bookTitle = currentBook.title;
    try {
      await api.applyMetadataCandidate(bookId, candidate, Array.from(selectedFields), writeToFiles);
      toast(`Applied selected fields to "${bookTitle}"`, 'success', {
        label: 'Undo',
        onClick: async () => {
          try {
            await api.undoLastApply(bookId);
            toast(`Undid metadata apply for "${bookTitle}"`, 'info');
            setBookStatuses((prev) => new Map(prev).set(bookId, 'pending'));
            setAppliedStack((prev) => prev.filter((b) => b.id !== bookId));
          } catch { /* ignore */ }
        },
      });
      setBookStatuses((prev) => new Map(prev).set(bookId, 'applied'));
      setAppliedStack((prev) => [...prev, { id: bookId, title: bookTitle }]);
      advanceToNext();
    } catch (err) {
      toast(err instanceof Error ? err.message : 'Failed to apply metadata', 'error');
    } finally {
      setApplying(false);
    }
  };

  // Undo the most-recently applied book in this session. Works after the
  // success banner is gone and after navigating to other books, because it
  // keys off the applied stack rather than the current book.
  const handleUndoLastApplied = async () => {
    const last = appliedStack[appliedStack.length - 1];
    if (!last) return;
    setUndoing(true);
    try {
      await api.undoLastApply(last.id);
      toast(`Undid metadata apply for "${last.title}"`, 'success');
      setBookStatuses((prev) => new Map(prev).set(last.id, 'pending'));
      setAppliedStack((prev) => prev.slice(0, -1));
    } catch (err) {
      toast(err instanceof Error ? err.message : 'Failed to undo', 'error');
    } finally {
      setUndoing(false);
    }
  };

  const handleUndoCurrentBook = async () => {
    setUndoing(true);
    try {
      const resp = await api.undoLastApply(currentBook.id);
      toast(`Undid ${resp.undone_fields.length} field(s) for "${currentBook.title}"`, 'success');
      setBookStatuses((prev) => new Map(prev).set(currentBook.id, 'pending'));
      setAppliedStack((prev) => prev.filter((b) => b.id !== currentBook.id));
    } catch (err) {
      toast(err instanceof Error ? err.message : 'Failed to undo', 'error');
    } finally {
      setUndoing(false);
    }
  };

  const handleSkip = () => {
    setBookStatuses((prev) => new Map(prev).set(currentBook.id, 'skipped'));
    advanceToNext();
  };

  const handleMarkNoMatch = async () => {
    try {
      await api.markNoMatch(currentBook.id);
      setBookStatuses((prev) => new Map(prev).set(currentBook.id, 'skipped'));
      advanceToNext();
    } catch {
      toast('Failed to mark as no match', 'error');
    }
  };

  const advanceToNext = () => {
    if (currentIndex < filteredBooks.length - 1) {
      setCurrentIndex((i) => i + 1);
      setSourceFilter(null); // reset filter for next book
    }
  };

  const handleClose = () => {
    if (appliedCount > 0) {
      onComplete();
    }
    setCurrentIndex(0);
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

  if (!open || books.length === 0) return null;

  // All books filtered out — show message instead of closing
  if (filteredBooks.length === 0) {
    return (
      <Dialog open={open} onClose={handleClose} maxWidth="sm" fullWidth>
        <DialogTitle>Search Metadata</DialogTitle>
        <DialogContent>
          <Typography color="text.secondary" sx={{ py: 3, textAlign: 'center' }}>
            All {books.length} book(s) already have metadata applied.
            <br />
            <Button size="small" onClick={() => handleToggleSkipApplied(false)} sx={{ mt: 1 }}>
              Show all books
            </Button>
          </Typography>
        </DialogContent>
        <DialogActions>
          <Button onClick={handleClose} variant="outlined">Close</Button>
        </DialogActions>
      </Dialog>
    );
  }

  const progress = ((appliedCount + skippedCount) / filteredBooks.length) * 100;
  const status = bookStatuses.get(currentBook?.id);

  return (
    <Dialog open={open} onClose={handleClose} maxWidth="md" fullWidth>
      <DialogTitle sx={{ pb: 1 }}>
        <Box sx={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
          <Typography variant="h6">
            Search Metadata — Book {currentIndex + 1} of {filteredBooks.length}{skipApplied && alreadyAppliedCount > 0 ? ` (${alreadyAppliedCount} filtered)` : ''}
          </Typography>
          <Stack direction="row" spacing={1} alignItems="center">
            {appliedCount > 0 && (
              <Chip icon={<CheckCircleIcon />} label={`${appliedCount} applied`} color="success" size="small" />
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
            p: 1.5, mb: 2, border: 1, borderColor: 'divider', borderRadius: 1, bgcolor: 'action.hover',
            cursor: currentBook.cover_url ? 'pointer' : 'default',
            '&:hover': currentBook.cover_url ? { borderColor: 'primary.main', bgcolor: 'action.selected' } : {},
          }}
          onClick={() => { if (currentBook.cover_url) setPreviewCover(currentBook.cover_url); }}
        >
          <Stack direction="row" spacing={2} alignItems="flex-start">
            {currentBook.cover_url && (
              <Avatar
                src={currentBook.cover_url}
                variant="rounded"
                sx={{ width: 48, height: 64 }}
              >
                {currentBook.title?.[0]}
              </Avatar>
            )}
            <Box sx={{ flex: 1, minWidth: 0 }}>
              <Typography variant="subtitle1" fontWeight="bold" noWrap>
                {currentBook.title || 'Untitled'}
              </Typography>
              <Typography variant="body2" color="text.secondary">
                {currentBook.author || 'Unknown author'}
                {currentBook.narrator ? ` — Narrated by ${currentBook.narrator}` : ''}
              </Typography>
              <Stack direction="row" spacing={1} flexWrap="wrap" sx={{ mt: 0.5 }}>
                {currentBook.format && (
                  <Chip label={currentBook.format.toUpperCase()} size="small" color="primary" variant="outlined" />
                )}
                {currentBook.duration_seconds != null && currentBook.duration_seconds > 0 && (
                  <Chip label={formatDuration(currentBook.duration_seconds)} size="small" variant="outlined" />
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
                  <Chip label={formatFileSize(currentBook.file_size_bytes)} size="small" variant="outlined" />
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
                <Typography variant="caption" color="text.secondary" sx={{ display: 'block', mt: 0.5, wordBreak: 'break-all' }}>
                  File: {currentBook.file_path}
                </Typography>
              )}
              {currentBook.original_filename && currentBook.original_filename !== currentBook.file_path?.split('/').pop() && (
                <Typography variant="caption" color="text.secondary" sx={{ display: 'block', wordBreak: 'break-all' }}>
                  Original: {currentBook.original_filename}
                </Typography>
              )}
              {currentBook.itunes_path && (
                <Typography variant="caption" color="info.main" sx={{ display: 'block', wordBreak: 'break-all' }}>
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
                  label={filesLoading ? 'Loading files…' : `${fileCount} File${fileCount === 1 ? '' : 's'}`}
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
                      ].filter(Boolean).join(' · ');
                      return (
                        <ListItem key={f.id} disableGutters sx={{ display: 'block', py: 0.25 }}>
                          <Tooltip title={f.file_path} placement="bottom-start">
                            <Typography
                              variant="caption"
                              sx={{ fontFamily: 'monospace', display: 'block', color: f.missing ? 'error.main' : 'text.primary', wordBreak: 'break-all' }}
                            >
                              {basename(f.file_path)}
                            </Typography>
                          </Tooltip>
                          {meta && (
                            <Typography variant="caption" color="text.secondary">{meta}</Typography>
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
          fullWidth size="small"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          onKeyDown={(e) => { if (e.key === 'Enter') handleSearch(); }}
          placeholder="Search by title, ISBN, or ASIN..."
          sx={{ mb: 1 }}
          InputProps={{
            startAdornment: <InputAdornment position="start"><SearchIcon /></InputAdornment>,
            endAdornment: (
              <InputAdornment position="end">
                <IconButton onClick={handleSearch} disabled={loading} size="small"><SearchIcon /></IconButton>
              </InputAdornment>
            ),
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
              fullWidth size="small"
              value={authorQuery}
              onChange={(e) => setAuthorQuery(e.target.value)}
              onKeyDown={(e) => { if (e.key === 'Enter') handleSearch(); }}
              placeholder="Author name (narrows results)"
              label="Author"
            />
            <TextField
              fullWidth size="small"
              value={narratorQuery}
              onChange={(e) => setNarratorQuery(e.target.value)}
              onKeyDown={(e) => { if (e.key === 'Enter') handleSearch(); }}
              placeholder="Narrator name (boosts matching results)"
              label="Narrator"
            />
            <TextField
              fullWidth size="small"
              value={seriesQuery}
              onChange={(e) => setSeriesQuery(e.target.value)}
              onKeyDown={(e) => { if (e.key === 'Enter') handleSearch(); }}
              placeholder="Series name (boosts matching series)"
              label="Series"
            />
          </Stack>
        </Collapse>

        {/* Toggles and undo */}
        <Stack direction="row" spacing={2} alignItems="center" sx={{ mb: 1 }}>
          <Tooltip title="Write applied metadata to audio file tags (MP3/M4B/M4A)">
            <FormControlLabel
              control={<Switch checked={writeToFiles} onChange={(e) => setWriteToFiles(e.target.checked)} size="small" />}
              label={<Typography variant="body2">Write to files</Typography>}
            />
          </Tooltip>
          <Tooltip title={`Skip books that already have metadata applied${alreadyAppliedCount > 0 ? ` (${alreadyAppliedCount} books)` : ''}`}>
            <FormControlLabel
              control={<Switch checked={skipApplied} onChange={(e) => handleToggleSkipApplied(e.target.checked)} size="small" />}
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
          <Typography color="text.secondary" sx={{ py: 3, textAlign: 'center' }}>
            No results found. Try a different query or paste an Audible ASIN.
          </Typography>
        )}

        {/* Source filter + sort */}
        {!loading && results.length > 0 && (() => {
          const sources = Array.from(new Set(results.map((r) => r.source)));
          return (
            <Stack direction="row" spacing={0.5} alignItems="center" sx={{ mb: 1 }} flexWrap="wrap">
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
            .sort((a, b) => sortResults === 'source' ? a.source.localeCompare(b.source) : b.score - a.score)
            .map((candidate, idx) => (
            <Box key={idx} sx={{ border: 1, borderColor: 'divider', borderRadius: 1, p: 1.5 }}>
              <Stack direction="row" spacing={2} alignItems="flex-start">
                <Avatar
                  src={candidate.cover_url}
                  variant="rounded"
                  sx={{ width: 50, height: 65, cursor: candidate.cover_url ? 'pointer' : 'default', '&:hover': candidate.cover_url ? { opacity: 0.8 } : {} }}
                  onClick={() => { if (candidate.cover_url) setPreviewCover(candidate.cover_url); }}
                >
                  {candidate.title?.[0] || '?'}
                </Avatar>
                <Box sx={{ flex: 1, minWidth: 0 }}>
                  <Typography variant="body1" fontWeight="bold" noWrap>{candidate.title}</Typography>
                  <Typography variant="body2" color="text.secondary">
                    {candidate.author}{candidate.year ? ` (${candidate.year})` : ''}
                  </Typography>
                  {candidate.series && (
                    <Typography variant="body2" color="text.secondary">
                      Series: {candidate.series}{candidate.series_position ? ` · Book ${candidate.series_position}` : ''}
                    </Typography>
                  )}
                  {candidate.narrator && (
                    <Typography variant="body2" color="text.secondary">Narrator: {candidate.narrator}</Typography>
                  )}
                  <Stack direction="row" spacing={0.5} sx={{ mt: 0.5 }}>
                    <Chip label={candidate.source} size="small" color={SOURCE_COLORS[candidate.source] || 'default'} />
                    <Chip label={`${Math.round(candidate.score * 100)}%`} size="small" variant="outlined" />
                    {candidate.narrator && <Chip icon={<HeadphonesIcon />} label="Audiobook" size="small" color="info" variant="outlined" />}
                  </Stack>
                </Box>
                <Button
                  variant="contained"
                  size="small"
                  onClick={() => handleApplyAll(candidate)}
                  disabled={applying || bookStatuses.get(currentBook.id) === 'applied'}
                  sx={bookStatuses.get(currentBook.id) === 'applied' ? { opacity: 0.5 } : {}}
                  startIcon={bookStatuses.get(currentBook.id) === 'applied' ? <CheckCircleIcon /> : undefined}
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
                    {FIELD_OPTIONS.map((field) => {
                      const value = candidate[field as keyof MetadataCandidate];
                      if (value === undefined || value === null || value === '') return null;
                      return (
                        <FormControlLabel
                          key={field}
                          control={<Checkbox checked={selectedFields.has(field)} onChange={() => toggleField(field)} size="small" />}
                          label={<Typography variant="body2">{FIELD_LABELS[field] || field}: {String(value)}</Typography>}
                        />
                      );
                    })}
                    <Box sx={{ mt: 0.5 }}>
                      <Button
                        variant="outlined"
                        size="small"
                        onClick={() => handleApplySelected(candidate)}
                        disabled={applying || selectedFields.size === 0 || bookStatuses.get(currentBook.id) === 'applied'}
                        sx={bookStatuses.get(currentBook.id) === 'applied' ? { opacity: 0.5 } : {}}
                      >
                        {bookStatuses.get(currentBook.id) === 'applied' ? 'Applied' : 'Apply Selected'}
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
            onClick={() => setCurrentIndex((i) => Math.max(0, i - 1))}
            disabled={currentIndex === 0}
            startIcon={<NavigateBeforeIcon />}
            size="small"
          >
            Previous
          </Button>
          <Button
            onClick={() => setCurrentIndex((i) => Math.min(filteredBooks.length - 1, i + 1))}
            disabled={currentIndex >= filteredBooks.length - 1}
            endIcon={<NavigateNextIcon />}
            size="small"
          >
            Next
          </Button>
          <Button onClick={handleClose} variant="outlined">
            {appliedCount + skippedCount >= filteredBooks.length ? 'Done' : 'Close'}
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
