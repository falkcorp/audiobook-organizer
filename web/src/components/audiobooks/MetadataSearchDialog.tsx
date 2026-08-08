// file: web/src/components/audiobooks/MetadataSearchDialog.tsx
// version: 1.9.1
// guid: 8a9b0c1d-2e3f-4a5b-6c7d-8e9f0a1b2c3d

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
  Stack,
  Switch,
  TextField,
  Tooltip,
  Typography,
} from '@mui/material';
import SearchIcon from '@mui/icons-material/Search';
import RefreshIcon from '@mui/icons-material/Refresh';
import ExpandMoreIcon from '@mui/icons-material/ExpandMore';
import ExpandLessIcon from '@mui/icons-material/ExpandLess';
import HeadphonesIcon from '@mui/icons-material/Headphones';
import WarningAmberIcon from '@mui/icons-material/WarningAmber';
import type { Book, MetadataCandidate } from '../../services/api';
import * as api from '../../services/api';

interface MetadataSearchDialogProps {
  open: boolean;
  book: Book;
  onClose: () => void;
  onApplied: (updatedBook: Book) => void;
  toast: (message: string, severity?: 'success' | 'error' | 'warning' | 'info', action?: { label: string; onClick: () => void }) => void;
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

function humanizeField(field: string): string {
  return FIELD_LABELS[field] || field.replace(/_/g, ' ').replace(/\b\w/g, (c) => c.toUpperCase());
}

const SOURCE_COLORS: Record<string, 'primary' | 'secondary' | 'success' | 'warning' | 'info'> = {
  openlibrary: 'primary',
  google_books: 'secondary',
  audible: 'success',
  goodreads: 'warning',
  manual: 'info',
};

export function MetadataSearchDialog({
  open,
  book,
  onClose,
  onApplied,
  toast,
}: MetadataSearchDialogProps) {
  const [query, setQuery] = useState('');
  const [authorQuery, setAuthorQuery] = useState('');
  const [narratorQuery, setNarratorQuery] = useState('');
  const [seriesQuery, setSeriesQuery] = useState('');
  const [showAdvanced, setShowAdvanced] = useState(false);
  const [results, setResults] = useState<MetadataCandidate[]>([]);
  const [loading, setLoading] = useState(false);
  const [expandedCard, setExpandedCard] = useState<number | null>(null);
  const [selectedFields, setSelectedFields] = useState<Set<string>>(new Set());
  const [applying, setApplying] = useState(false);
  const [sourcesTried, setSourcesTried] = useState<string[]>([]);
  const [sourcesFailed, setSourcesFailed] = useState<Record<string, string>>({});
  const [cacheBadge, setCacheBadge] = useState<{ fromCache: boolean; isFresh: boolean; fetchedAt?: string } | null>(null);
  const [previewCover, setPreviewCover] = useState<string | null>(null);
  const [sourceFilter, setSourceFilter] = useState<string | null>(null);
  const [sortResults, setSortResults] = useState<'score' | 'source'>('score');
  const [writeToFiles, setWriteToFiles] = useState(true);
  const [useRerank, setUseRerank] = useState(false);

  // Auto-populate query and search on open
  useEffect(() => {
    if (open && book) {
      const q = book.title || '';
      const a = book.author_name || '';
      const n = book.narrator || '';
      const s = book.series_name || '';
      setQuery(q);
      setAuthorQuery(a);
      setNarratorQuery(n);
      setSeriesQuery(s);
      setShowAdvanced(false);
      setResults([]);
      setExpandedCard(null);
      setSelectedFields(new Set());
      // Search with author + narrator (series can over-constrain results)
      doSearch(q, a, n);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, book?.id]);

  const doSearch = useCallback(
    async (searchQuery: string, author?: string, narrator?: string, series?: string, refresh?: boolean) => {
      if (!book?.id) return;
      setLoading(true);
      try {
        const resp = await api.searchMetadataForBook(
          book.id,
          searchQuery,
          author || undefined,
          narrator || undefined,
          series || undefined,
          useRerank || undefined,
          refresh
        );
        setResults(resp.results || []);
        setSourcesTried(resp.sources_tried || []);
        setSourcesFailed(resp.sources_failed || {});
        // METADATA-CACHED-MATCHER: surface cache provenance so the user
        // knows whether they're looking at a cached or freshly-fetched
        // set. from_cache=undefined for legacy alt-query searches that
        // never touch the cache.
        if (resp.from_cache !== undefined) {
          setCacheBadge({
            fromCache: !!resp.from_cache,
            isFresh: resp.is_fresh !== false,
            fetchedAt: resp.fetched_at,
          });
        } else {
          setCacheBadge(null);
        }
      } catch (err) {
        toast(
          err instanceof Error ? err.message : 'Search failed',
          'error'
        );
        setResults([]);
      } finally {
        setLoading(false);
      }
    },
    [book?.id, toast, useRerank]
  );

  const handleSearch = () => {
    doSearch(query, authorQuery, narratorQuery, seriesQuery);
  };
  // METADATA-CACHED-MATCHER: bypass the persistent cache so a user
  // explicitly asking for fresh metadata re-runs every source.
  const handleRefresh = () => {
    doSearch(query, authorQuery, narratorQuery, seriesQuery, true);
  };

  const handleKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === 'Enter') {
      handleSearch();
    }
  };

  const handleApplyAll = async (candidate: MetadataCandidate) => {
    setApplying(true);
    const bookId = book.id;
    try {
      const resp = await api.applyMetadataCandidate(bookId, candidate, undefined, writeToFiles);
      onApplied(resp.book);
      onClose();
      toast(`Metadata applied from ${resp.source}`, 'success', {
        label: 'Undo',
        onClick: async () => {
          try {
            await api.undoLastApply(bookId);
            toast('Metadata apply undone', 'info');
          } catch { /* ignore */ }
        },
      });
    } catch (err) {
      toast(
        err instanceof Error ? err.message : 'Failed to apply metadata',
        'error'
      );
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
    const bookId = book.id;
    try {
      const resp = await api.applyMetadataCandidate(
        bookId,
        candidate,
        Array.from(selectedFields),
        writeToFiles
      );
      onApplied(resp.book);
      onClose();
      toast(`Selected fields applied from ${resp.source}`, 'success', {
        label: 'Undo',
        onClick: async () => {
          try {
            await api.undoLastApply(bookId);
            toast('Metadata apply undone', 'info');
          } catch { /* ignore */ }
        },
      });
    } catch (err) {
      toast(
        err instanceof Error ? err.message : 'Failed to apply metadata',
        'error'
      );
    } finally {
      setApplying(false);
    }
  };

  const handleMarkNoMatch = async () => {
    try {
      await api.markNoMatch(book.id);
      toast('Marked as no match found', 'info');
      onClose();
    } catch (err) {
      toast(
        err instanceof Error ? err.message : 'Failed to mark as no match',
        'error'
      );
    }
  };

  const toggleField = (field: string) => {
    setSelectedFields((prev) => {
      const next = new Set(prev);
      if (next.has(field)) {
        next.delete(field);
      } else {
        next.add(field);
      }
      return next;
    });
  };

  return (
    <Dialog open={open} onClose={onClose} maxWidth="md" fullWidth>
      <DialogTitle>Search Metadata</DialogTitle>
      <DialogContent>
        {/* Book info bar — click to enlarge cover */}
        <Box
          sx={{
            p: 1.5, mb: 2, border: 1, borderColor: 'divider', borderRadius: 1, bgcolor: 'action.hover',
            cursor: book.cover_url ? 'pointer' : 'default',
            '&:hover': book.cover_url ? { borderColor: 'primary.main', bgcolor: 'action.selected' } : {},
          }}
          onClick={() => { if (book.cover_url) setPreviewCover(book.cover_url); }}
        >
          <Stack direction="row" spacing={2} alignItems="flex-start">
            {book.cover_url && (
              <Avatar src={book.cover_url} variant="rounded" sx={{ width: 48, height: 64 }}>
                {book.title?.[0]}
              </Avatar>
            )}
            <Box sx={{ flex: 1, minWidth: 0 }}>
              <Typography variant="subtitle1" fontWeight="bold" noWrap>
                {book.title || 'Untitled'}
              </Typography>
              <Typography variant="body2" color="text.secondary">
                {book.author_name || 'Unknown author'}
                {book.narrator ? ` — Narrated by ${book.narrator}` : ''}
              </Typography>
              <Stack direction="row" spacing={1} flexWrap="wrap" sx={{ mt: 0.5 }}>
                {book.duration != null && book.duration > 0 && (
                  <Chip
                    label={`${Math.floor(book.duration / 3600)}h ${Math.floor((book.duration % 3600) / 60)}m`}
                    size="small"
                    variant="outlined"
                  />
                )}
                {book.format && (
                  <Chip label={book.format.toUpperCase()} size="small" variant="outlined" />
                )}
                {book.series_name && (
                  <Chip
                    label={`${book.series_name}${book.series_position ? ` #${book.series_position}` : ''}`}
                    size="small"
                    color="info"
                    variant="outlined"
                  />
                )}
                {book.file_size != null && book.file_size > 0 && (
                  <Chip
                    label={book.file_size >= 1073741824 ? `${(book.file_size / 1073741824).toFixed(1)} GB` : `${(book.file_size / 1048576).toFixed(0)} MB`}
                    size="small"
                    variant="outlined"
                  />
                )}
                {book.language && (
                  <Chip label={book.language} size="small" variant="outlined" />
                )}
              </Stack>
              {book.file_path && (
                <Typography variant="caption" color="text.secondary" noWrap sx={{ display: 'block', mt: 0.5 }}>
                  {book.file_path.split('/').slice(-2).join('/')}
                </Typography>
              )}
            </Box>
          </Stack>
        </Box>

        <TextField
          fullWidth
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          onKeyDown={handleKeyDown}
          placeholder="Search by title, ISBN, or Audible ASIN..."
          sx={{ mt: 1, mb: 1 }}
          InputProps={{
            startAdornment: (
              <InputAdornment position="start">
                <SearchIcon />
              </InputAdornment>
            ),
            endAdornment: (
              <InputAdornment position="end">
                <IconButton
                  onClick={handleRefresh}
                  disabled={loading}
                  title="Bypass cache — re-fetch from all metadata sources"
                >
                  <RefreshIcon />
                </IconButton>
                <IconButton onClick={handleSearch} disabled={loading}>
                  <SearchIcon />
                </IconButton>
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
          Advanced Search
        </Button>
        <Collapse in={showAdvanced}>
          <Stack spacing={1.5} sx={{ mb: 2 }}>
            <TextField
              fullWidth
              size="small"
              value={authorQuery}
              onChange={(e) => setAuthorQuery(e.target.value)}
              onKeyDown={handleKeyDown}
              placeholder="Author name (narrows results)"
              label="Author"
            />
            <TextField
              fullWidth
              size="small"
              value={narratorQuery}
              onChange={(e) => setNarratorQuery(e.target.value)}
              onKeyDown={handleKeyDown}
              placeholder="Narrator name (boosts matching results)"
              label="Narrator"
            />
            <TextField
              fullWidth
              size="small"
              value={seriesQuery}
              onChange={(e) => setSeriesQuery(e.target.value)}
              onKeyDown={handleKeyDown}
              placeholder="Series name (boosts matching series)"
              label="Series"
            />
            <FormControlLabel
              control={
                <Switch
                  size="small"
                  checked={useRerank}
                  onChange={(e) => setUseRerank(e.target.checked)}
                />
              }
              label="AI rerank (higher quality, ~$0.003/search)"
              sx={{ ml: 0 }}
            />
          </Stack>
        </Collapse>

        <Tooltip title="Write applied metadata to audio file tags (MP3/M4B/M4A)">
          <FormControlLabel
            control={<Switch checked={writeToFiles} onChange={(e) => setWriteToFiles(e.target.checked)} size="small" />}
            label={<Typography variant="body2">Write to files</Typography>}
            sx={{ mb: 1 }}
          />
        </Tooltip>

        {loading && (
          <Box sx={{ display: 'flex', justifyContent: 'center', py: 4 }}>
            <CircularProgress />
          </Box>
        )}

        {!loading && results.length === 0 && (
          <Typography color="text.secondary" sx={{ py: 4, textAlign: 'center' }}>
            No results found. Try a different search query, or paste an Audible ASIN (e.g. B0XXXXXXXXX).
          </Typography>
        )}

        {!loading && cacheBadge && (
          <Box sx={{ mb: 1 }}>
            <Chip
              size="small"
              color={cacheBadge.fromCache ? (cacheBadge.isFresh ? 'success' : 'warning') : 'info'}
              label={
                cacheBadge.fromCache
                  ? cacheBadge.isFresh
                    ? `Cached${cacheBadge.fetchedAt ? ` (${new Date(cacheBadge.fetchedAt).toLocaleString()})` : ''}`
                    : `Cached — stale${cacheBadge.fetchedAt ? ` (${new Date(cacheBadge.fetchedAt).toLocaleString()})` : ''}, click Refresh for fresh results`
                  : 'Fresh from sources'
              }
              title={cacheBadge.fromCache ? 'Click the Refresh icon to re-fetch from sources.' : 'Just fetched from every metadata source.'}
            />
          </Box>
        )}
        {!loading && Object.keys(sourcesFailed).length > 0 && (
          <Box sx={{ mb: 2, p: 1.5, bgcolor: 'warning.main', borderRadius: 1, opacity: 0.85 }}>
            <Stack direction="row" spacing={1} alignItems="center" sx={{ mb: 0.5 }}>
              <WarningAmberIcon fontSize="small" sx={{ color: 'warning.contrastText' }} />
              <Typography variant="body2" fontWeight="bold" sx={{ color: 'warning.contrastText' }}>
                {Object.keys(sourcesFailed).length} of {sourcesTried.length} sources failed
              </Typography>
            </Stack>
            {Object.entries(sourcesFailed).map(([source, error]) => (
              <Typography key={source} variant="caption" sx={{ display: 'block', color: 'warning.contrastText' }}>
                {source}: {error}
              </Typography>
            ))}
          </Box>
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

        <Stack spacing={2}>
          {results
            .filter((c) => !sourceFilter || c.source === sourceFilter)
            .sort((a, b) => sortResults === 'source' ? a.source.localeCompare(b.source) : b.score - a.score)
            .map((candidate, idx) => (
            <Box
              key={idx}
              sx={{
                border: 1,
                borderColor: 'divider',
                borderRadius: 1,
                p: 2,
              }}
            >
              <Stack direction="row" spacing={2} alignItems="flex-start">
                <Avatar
                  src={candidate.cover_url}
                  variant="rounded"
                  sx={{ width: 60, height: 80, cursor: candidate.cover_url ? 'pointer' : 'default', '&:hover': candidate.cover_url ? { opacity: 0.8 } : {} }}
                  onClick={() => { if (candidate.cover_url) setPreviewCover(candidate.cover_url); }}
                >
                  {candidate.title?.[0] || '?'}
                </Avatar>
                <Box sx={{ flex: 1, minWidth: 0 }}>
                  <Typography variant="subtitle1" fontWeight="bold" noWrap>
                    {candidate.title}
                  </Typography>
                  <Typography variant="body2" color="text.secondary">
                    {candidate.author}
                    {candidate.year ? ` (${candidate.year})` : ''}
                  </Typography>
                  {candidate.series && (
                    <Typography variant="body2" color="text.secondary">
                      Series: {candidate.series}
                      {candidate.series_position
                        ? ` · Book ${candidate.series_position}`
                        : ''}
                    </Typography>
                  )}
                  {candidate.narrator && (
                    <Typography variant="body2" color="text.secondary">
                      Narrator: {candidate.narrator}
                    </Typography>
                  )}
                  <Stack direction="row" spacing={1} sx={{ mt: 1 }}>
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
                  disabled={applying}
                >
                  Apply
                </Button>
              </Stack>

              {/* Expandable field selector */}
              <Box sx={{ mt: 1 }}>
                <Button
                  size="small"
                  onClick={() =>
                    setExpandedCard(expandedCard === idx ? null : idx)
                  }
                  endIcon={
                    expandedCard === idx ? (
                      <ExpandLessIcon />
                    ) : (
                      <ExpandMoreIcon />
                    )
                  }
                >
                  Select fields...
                </Button>
                <Collapse in={expandedCard === idx}>
                  <Box sx={{ mt: 1, pl: 1 }}>
                    {FIELD_OPTIONS.map((field) => {
                      const value =
                        candidate[field as keyof MetadataCandidate];
                      if (value === undefined || value === null || value === '')
                        return null;
                      return (
                        <FormControlLabel
                          key={field}
                          control={
                            <Checkbox
                              checked={selectedFields.has(field)}
                              onChange={() => toggleField(field)}
                              size="small"
                            />
                          }
                          label={`${humanizeField(field)}: ${value}`}
                        />
                      );
                    })}
                    <Box sx={{ mt: 1 }}>
                      <Button
                        variant="outlined"
                        size="small"
                        onClick={() => handleApplySelected(candidate)}
                        disabled={applying || selectedFields.size === 0}
                      >
                        Apply Selected
                      </Button>
                    </Box>
                  </Box>
                </Collapse>
              </Box>
            </Box>
          ))}
        </Stack>
      </DialogContent>
      <DialogActions>
        <Button
          color="warning"
          onClick={handleMarkNoMatch}
          disabled={applying}
        >
          No Match Found
        </Button>
        <Button onClick={onClose}>Cancel</Button>
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
