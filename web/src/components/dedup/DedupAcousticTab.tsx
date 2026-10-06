// file: web/src/components/dedup/DedupAcousticTab.tsx
// version: 1.6.0
// guid: c3d4e5f6-a7b8-9012-cdef-012345678902
// last-edited: 2026-10-06
import { useState, useEffect, useCallback, useMemo, useRef } from 'react';
import { useNavigate, Link as RouterLink } from 'react-router-dom';
import {
  Box,
  Typography,
  Paper,
  Button,
  Alert,
  Chip,
  Divider,
  IconButton,
  Tooltip,
  Stack,
  LinearProgress,
  Checkbox,
  TablePagination,
  Avatar,
  Link,
  TextField,
  Table,
  TableContainer,
  TableHead,
  TableRow,
  TableCell,
  TableBody,
  Dialog,
  DialogTitle,
  DialogContent,
  DialogContentText,
  DialogActions,
} from '@mui/material';
import RefreshIcon from '@mui/icons-material/Refresh';
import FingerprintIcon from '@mui/icons-material/Fingerprint';
import GraphicEqIcon from '@mui/icons-material/GraphicEq';
import * as api from '../../services/api';
import type { Book, DedupCandidate } from '../../services/api';
import { CoverLightbox } from '../CoverLightbox';
import { fetchBookCached } from './DedupEmbeddingTab';
import { useRowSelection } from '../../hooks/useRowSelection';
import { useServerMatchingCount } from '../../hooks/useServerMatchingCount';
import { SelectAllMatchingBanner } from '../common/SelectAllMatchingBanner';

type AcousticBulkAction = 'dismiss' | 'keep-a' | 'keep-b';
const ACOUSTIC_LIST_PARAMS = { layer: 'acoustid' } as const;
/**
 * The filter a cross-page Acoustic action sends to the filter-scoped bulk
 * endpoints: every PENDING acoustic book candidate. The server re-evaluates
 * it, applies bulk-link's review-queue-only guard (pinned, same-path and
 * chain-linking pairs are refused) and re-checks each pair's status right
 * before acting, so nothing a person decided or pinned is overturned.
 */
const ACOUSTIC_BULK_FILTER = { entity_type: 'book', status: 'pending', layer: 'acoustid' } as const;
/** Ceiling the server applies (bulk_apply_max_items default); stated in the dialog. */
const BULK_APPLY_DEFAULT_CAP = 5000;
const BULK_LABEL: Record<AcousticBulkAction, string> = {
  'keep-a': 'Keep A',
  'keep-b': 'Keep B',
  dismiss: 'Dismiss',
};

// ULID pattern: 26-character alphanumeric (0-9, A-Z only)
const ULID_PATTERN = /^[0-9A-Z]{26}$/;

// ---- Acoustic Compare Panel ----
// Manual two-book fingerprint comparison tool.
function formatDuration(seconds: number): string {
  if (!seconds) return '';
  const h = Math.floor(seconds / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  if (h > 0) return `${h}h ${m}m`;
  return `${m}m`;
}

function bookCoverSrc(book: Book): string {
  if (!book.cover_url) return '';
  return book.cover_url.startsWith('/api/')
    ? book.cover_url
    : `/api/v1/covers/proxy?url=${encodeURIComponent(book.cover_url)}`;
}

// Helper function to render book metadata (reusable in AcousticComparePanel)
export function AcousticBookMetadata({ book, filePath }: { book: Book; filePath?: string }) {
  const navigate = useNavigate();
  return (
    <Box sx={{ minWidth: 0 }}>
      <Typography
        variant="body2"
        onClick={() => navigate(`/library/${book.id}`)}
        noWrap
        sx={{
          fontWeight: 600,
          cursor: 'pointer',
          '&:hover': { textDecoration: 'underline' },
        }}
      >
        {book.title || <em style={{ opacity: 0.5 }}>Untitled</em>}
      </Typography>
      {book.author_name && (
        <Typography
          variant="caption"
          noWrap
          sx={{
            color: 'text.secondary',
          }}
        >
          {book.author_name}
        </Typography>
      )}
      {book.series_name && (
        <Typography
          variant="caption"
          noWrap
          sx={{
            color: 'text.secondary',
            display: 'block',
          }}
        >
          {book.series_name}
          {book.series_position ? ` · Book ${book.series_position}` : ''}
        </Typography>
      )}
      <Stack
        direction="row"
        spacing={0.5}
        useFlexGap
        sx={{
          flexWrap: 'wrap',
          mt: 0.5,
        }}
      >
        {book.format && <Chip label={book.format.toUpperCase()} size="small" />}
        {book.duration && (
          <Chip label={formatDuration(book.duration)} size="small" variant="outlined" />
        )}
      </Stack>
      {filePath && (
        <Typography
          variant="caption"
          sx={{
            color: 'text.secondary',
            display: 'block',
            mt: 0.5,
            wordBreak: 'break-all',
            fontSize: '0.65rem',
            fontFamily: 'monospace',
          }}
        >
          {filePath}
        </Typography>
      )}
    </Box>
  );
}

// Legacy function for backward compatibility (if used elsewhere)
export function AcousticBookCard({ book, label }: { book: Book; label: string }) {
  return (
    <Box sx={{ flex: 1, minWidth: 0 }}>
      <Typography
        variant="caption"
        sx={{
          color: 'text.secondary',
          fontWeight: 600,
          textTransform: 'uppercase',
          letterSpacing: 0.5,
        }}
      >
        {label}
      </Typography>
      <Stack
        direction="row"
        spacing={1.5}
        sx={{
          alignItems: 'flex-start',
          mt: 0.5,
        }}
      >
        <Avatar
          src={bookCoverSrc(book)}
          variant="rounded"
          sx={{ width: 56, height: 72, flexShrink: 0, bgcolor: 'action.selected' }}
        >
          <GraphicEqIcon />
        </Avatar>
        <AcousticBookMetadata book={book} filePath={book.file_path} />
      </Stack>
    </Box>
  );
}

interface AcousticComparePanelProps {
  initialA?: string;
  initialB?: string;
}

function AcousticComparePanel({ initialA = '', initialB = '' }: AcousticComparePanelProps) {
  const [bookAID, setBookAID] = useState(initialA);
  const [bookBID, setBookBID] = useState(initialB);
  const [result, setResult] = useState<api.AcoustIDCompareResponse | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [lightboxOpen, setLightboxOpen] = useState(false);
  const [lightboxSrc, setLightboxSrc] = useState<string | null>(null);
  const [idAError, setIdAError] = useState<string | null>(null);
  const [idBError, setIdBError] = useState<string | null>(null);

  const handleOpenCoverLightbox = (src: string | null) => {
    setLightboxSrc(src);
    setLightboxOpen(true);
  };

  const handleCloseLightbox = () => {
    setLightboxOpen(false);
    setLightboxSrc(null);
  };

  useEffect(() => {
    if (initialA) setBookAID(initialA);
    if (initialB) setBookBID(initialB);
  }, [initialA, initialB]);

  // Validate ULID format
  const validateBookID = (id: string): string | null => {
    const trimmed = id.trim();
    if (!trimmed) return 'Book ID is required';
    if (!ULID_PATTERN.test(trimmed)) {
      return 'Invalid book ID format. Must be 26-character alphanumeric (0-9, A-Z only).';
    }
    return null;
  };

  const handleCompare = async () => {
    // Validate both IDs
    const aError = validateBookID(bookAID);
    const bError = validateBookID(bookBID);

    setIdAError(aError);
    setIdBError(bError);

    if (aError || bError) return;

    setLoading(true);
    setError(null);
    setResult(null);
    try {
      const resp = await api.compareAcoustID(bookAID.trim(), bookBID.trim());
      setResult(resp);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Comparison failed');
    } finally {
      setLoading(false);
    }
  };

  const segLabels: Record<string, string> = {
    seg0: 'Intro',
    seg1: 'Body 1',
    seg2: 'Body 2',
    seg3: 'Body 3',
    seg4: 'Body 4',
    seg5: 'Body 5',
    seg6: 'Outro',
  };

  const hasAnySegments = result ? result.segment_scores.some((s) => s.hash_a || s.hash_b) : false;

  const handleBookAIDChange = (e: React.ChangeEvent<HTMLInputElement>) => {
    setBookAID(e.target.value);
    setIdAError(null);
  };

  const handleBookBIDChange = (e: React.ChangeEvent<HTMLInputElement>) => {
    setBookBID(e.target.value);
    setIdBError(null);
  };

  return (
    <Paper sx={{ p: 2 }}>
      <Typography variant="subtitle1" sx={{ mb: 1.5, fontWeight: 600 }}>
        Fingerprint Comparison
      </Typography>
      <Stack
        direction="row"
        spacing={2}
        sx={{
          alignItems: 'flex-start',
          mb: 2,
        }}
      >
        <TextField
          label="Book A ID"
          size="small"
          value={bookAID}
          onChange={handleBookAIDChange}
          error={idAError !== null}
          helperText={idAError}
          sx={{ flex: 1 }}
          placeholder="Paste book ID…"
        />
        <TextField
          label="Book B ID"
          size="small"
          value={bookBID}
          onChange={handleBookBIDChange}
          error={idBError !== null}
          helperText={idBError}
          sx={{ flex: 1 }}
          placeholder="Paste book ID…"
        />
        <Button
          variant="contained"
          onClick={handleCompare}
          disabled={loading || !bookAID.trim() || !bookBID.trim()}
          sx={{ mt: 0.5 }}
        >
          {loading ? 'Comparing…' : 'Compare'}
        </Button>
      </Stack>

      {error && (
        <Alert severity="error" sx={{ mb: 1 }}>
          {error}
        </Alert>
      )}

      {result && (
        <Box>
          {/* Cover images and metadata side by side */}
          <Stack direction="row" spacing={3} sx={{ mb: 3 }}>
            {/* Book A */}
            <Box
              sx={{
                flex: 1,
                display: 'flex',
                flexDirection: 'column',
                alignItems: 'center',
                gap: 2,
              }}
            >
              <Typography
                variant="caption"
                sx={{
                  color: 'text.secondary',
                  fontWeight: 600,
                  textTransform: 'uppercase',
                  letterSpacing: 0.5,
                }}
              >
                Book A
              </Typography>
              {/* Cover image (clickable) */}
              <Box
                onClick={() => handleOpenCoverLightbox(bookCoverSrc(result.book_a as Book))}
                sx={{
                  width: 180,
                  height: 240,
                  borderRadius: 1,
                  overflow: 'hidden',
                  cursor: result.book_a?.cover_url ? 'pointer' : 'default',
                  bgcolor: 'action.disabledBackground',
                  display: 'flex',
                  alignItems: 'center',
                  justifyContent: 'center',
                  '&:hover': result.book_a?.cover_url ? { opacity: 0.8, boxShadow: 3 } : {},
                  transition: 'all 0.2s',
                }}
              >
                {result.book_a?.cover_url ? (
                  <img
                    src={bookCoverSrc(result.book_a as Book)}
                    alt={result.book_a?.title}
                    style={{ width: '100%', height: '100%', objectFit: 'cover' }}
                  />
                ) : (
                  <GraphicEqIcon sx={{ fontSize: 60, opacity: 0.3 }} />
                )}
              </Box>
              {/* Metadata */}
              <AcousticBookMetadata
                book={result.book_a as Book}
                filePath={(result.book_a as any)?.file_path}
              />
            </Box>

            <Divider orientation="vertical" flexItem />

            {/* Book B (same structure as Book A) */}
            <Box
              sx={{
                flex: 1,
                display: 'flex',
                flexDirection: 'column',
                alignItems: 'center',
                gap: 2,
              }}
            >
              <Typography
                variant="caption"
                sx={{
                  color: 'text.secondary',
                  fontWeight: 600,
                  textTransform: 'uppercase',
                  letterSpacing: 0.5,
                }}
              >
                Book B
              </Typography>
              <Box
                onClick={() => handleOpenCoverLightbox(bookCoverSrc(result.book_b as Book))}
                sx={{
                  width: 180,
                  height: 240,
                  borderRadius: 1,
                  overflow: 'hidden',
                  cursor: result.book_b?.cover_url ? 'pointer' : 'default',
                  bgcolor: 'action.disabledBackground',
                  display: 'flex',
                  alignItems: 'center',
                  justifyContent: 'center',
                  '&:hover': result.book_b?.cover_url ? { opacity: 0.8, boxShadow: 3 } : {},
                  transition: 'all 0.2s',
                }}
              >
                {result.book_b?.cover_url ? (
                  <img
                    src={bookCoverSrc(result.book_b as Book)}
                    alt={result.book_b?.title}
                    style={{ width: '100%', height: '100%', objectFit: 'cover' }}
                  />
                ) : (
                  <GraphicEqIcon sx={{ fontSize: 60, opacity: 0.3 }} />
                )}
              </Box>
              <AcousticBookMetadata
                book={result.book_b as Book}
                filePath={(result.book_b as any)?.file_path}
              />
            </Box>
          </Stack>

          {/* Lightbox modal */}
          <CoverLightbox open={lightboxOpen} src={lightboxSrc} onClose={handleCloseLightbox} />

          {/* Similarity score */}
          <Stack
            direction="row"
            spacing={1}
            sx={{
              alignItems: 'center',
              mb: 2,
            }}
          >
            <Chip
              label={
                hasAnySegments
                  ? `${Math.round(result.overall_score * 100)}% match`
                  : 'No fingerprint data'
              }
              color={
                !hasAnySegments
                  ? 'default'
                  : result.overall_score >= 0.85
                    ? 'error'
                    : result.overall_score >= 0.6
                      ? 'warning'
                      : 'default'
              }
              icon={<GraphicEqIcon />}
            />
            {!hasAnySegments && (
              <Typography
                variant="caption"
                sx={{
                  color: 'text.secondary',
                }}
              >
                Run "Fingerprint Books" first to populate segment data
              </Typography>
            )}
          </Stack>

          {/* Segment table */}
          <TableContainer>
            <Table size="small">
              <TableHead>
                <TableRow>
                  <TableCell>Segment</TableCell>
                  <TableCell>Book A fingerprint</TableCell>
                  <TableCell>Book B fingerprint</TableCell>
                  <TableCell align="center">Match</TableCell>
                </TableRow>
              </TableHead>
              <TableBody>
                {result.segment_scores.map((seg) => (
                  <TableRow
                    key={seg.segment}
                    sx={{
                      bgcolor: seg.match
                        ? 'success.light'
                        : seg.hash_a && seg.hash_b
                          ? 'error.light'
                          : undefined,
                      opacity: 0.9,
                    }}
                  >
                    <TableCell>
                      <strong>{segLabels[seg.segment] ?? seg.segment}</strong>
                    </TableCell>
                    <TableCell sx={{ fontFamily: 'monospace', fontSize: '0.7rem' }}>
                      {seg.hash_a ? (
                        seg.hash_a.slice(0, 16) + '…'
                      ) : (
                        <em style={{ opacity: 0.4 }}>not fingerprinted</em>
                      )}
                    </TableCell>
                    <TableCell sx={{ fontFamily: 'monospace', fontSize: '0.7rem' }}>
                      {seg.hash_b ? (
                        seg.hash_b.slice(0, 16) + '…'
                      ) : (
                        <em style={{ opacity: 0.4 }}>not fingerprinted</em>
                      )}
                    </TableCell>
                    <TableCell align="center">
                      {!seg.hash_a || !seg.hash_b ? (
                        <Chip label="n/a" size="small" variant="outlined" />
                      ) : seg.match ? (
                        <Chip label="✓ match" size="small" color="success" />
                      ) : (
                        <Chip label="✗ differ" size="small" color="error" />
                      )}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </TableContainer>
        </Box>
      )}
    </Paper>
  );
}

// metadataQuality scores a Book's metadata completeness (0–10).
// Higher = more complete / reliable source.
function metadataQuality(book: Book | undefined): number {
  if (!book) return 0;
  let score = 0;
  const title = book.title ?? '';
  // Title sanity: not empty, not literal "TITLE", not looks like a ULID/UUID
  const isGarbageTitle =
    !title || title.toUpperCase() === 'TITLE' || /^[0-9A-Z]{26}$/.test(title.trim());
  if (!isGarbageTitle) score += 2;
  if (book.asin) score += 3;
  if (book.isbn13 || book.isbn) score += 2;
  if (book.cover_url) score += 1;
  if (book.narrator) score += 0.5;
  if (book.description) score += 0.5;
  if (book.publisher) score += 0.5;
  return score;
}

function qualityChip(score: number) {
  if (score >= 6)
    return <Chip label="Rich metadata" size="small" color="success" variant="outlined" />;
  if (score >= 3)
    return <Chip label="Partial metadata" size="small" color="warning" variant="outlined" />;
  return <Chip label="Poor metadata" size="small" color="error" variant="outlined" />;
}

// ---- Acoustic Dedup Tab ----
export function AcousticDedupTab() {
  const [candidates, setCandidates] = useState<DedupCandidate[]>([]);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(true);
  const [scanning, setScanning] = useState(false);
  const [fingerprinting, setFingerprinting] = useState(false);
  const [statusMsg, setStatusMsg] = useState<string | null>(null);
  const [statusSeverity, setStatusSeverity] = useState<'info' | 'error'>('info');
  const [page, setPage] = useState(0);
  // Bigger default than 25 and exposes 50/100/250 because 12K candidates at
  // 25/page is 512 clicks — the user understandably refuses to triage that
  // way. Bulk Keep-A / Keep-B / Dismiss act on the selection, which can be
  // the page or (via "Select all N matching") every acoustic candidate.
  const [rowsPerPage, setRowsPerPage] = useState(100);
  const [bookCache, setBookCache] = useState<Map<string, Book>>(new Map());
  const [loadError, setLoadError] = useState<string | null>(null);
  const [bulkBusy, setBulkBusy] = useState(false);
  const [bulkProgress, setBulkProgress] = useState<string | null>(null);
  // A cross-page action awaiting confirmation, with the PENDING count it will
  // be confirmed against (sent as expected_total).
  const [confirmBulk, setConfirmBulk] = useState<{
    action: AcousticBulkAction;
    pending: number;
  } | null>(null);
  // Ids the last cross-page dismiss rejected, for Undo.
  const [undoIds, setUndoIds] = useState<number[] | null>(null);
  const [purging, setPurging] = useState(false);
  const [resolving, setResolving] = useState<Set<number>>(new Set());
  const [compareA, setCompareA] = useState('');
  const [compareB, setCompareB] = useState('');
  const comparePanelRef = useCallback((el: HTMLDivElement | null) => {
    if (el) el.scrollIntoView({ behavior: 'smooth', block: 'start' });
  }, []);
  const [showComparePanel, setShowComparePanel] = useState(false);
  const timeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const isUnmountedRef = useRef(false);

  const loadCandidates = useCallback(async () => {
    setLoading(true);
    setLoadError(null);
    try {
      const resp = await api.getDedupCandidates({
        ...ACOUSTIC_LIST_PARAMS,
        limit: rowsPerPage,
        offset: page * rowsPerPage,
      });
      const cands = resp.candidates || [];
      setCandidates(cands);
      setTotal(resp.total || 0);

      const ids = new Set<string>();
      for (const c of cands) {
        ids.add(c.entity_a_id);
        ids.add(c.entity_b_id);
      }
      const cache = new Map<string, Book>();
      await Promise.all(
        Array.from(ids).map(async (id) => {
          try {
            const book = await fetchBookCached(id);
            if (book) cache.set(id, book);
          } catch {
            /* ignore */
          }
        })
      );
      setBookCache(cache);
    } catch (err) {
      // A failed load must not read as "no candidates": say it failed and
      // drop the stale page so nothing below it can be acted on.
      setLoadError(err instanceof Error ? err.message : 'Failed to load acoustic candidates');
      setCandidates([]);
      setTotal(0);
    } finally {
      setLoading(false);
    }
  }, [page, rowsPerPage]);

  useEffect(() => {
    loadCandidates();
  }, [loadCandidates]);

  // Selection: header checkbox = this page, banner = every acoustic candidate,
  // shift-click = a range. Page size is the reset key; a page turn clears an
  // explicit selection (its rows leave the screen) but keeps "all matching".
  // The acoustic list shows every status (it sends none), but only PENDING
  // pairs are actionable: dismissing a merged pair would flip it and record a
  // "not a duplicate" label for a real duplicate. Decided rows are therefore
  // not selectable, and a cross-page selection skips them (and says so).
  const pageKeys = useMemo(() => candidates.map((c) => c.id), [candidates]);
  const decidedIds = useMemo(
    () => new Set(candidates.filter((c) => c.status !== 'pending').map((c) => c.id)),
    [candidates]
  );
  const isDecided = useCallback((id: number) => decidedIds.has(id), [decidedIds]);
  // "N matching" on the banner and the bulk bar is the SERVER's pending count
  // (what the confirm dialog shows and the bulk endpoints act on), not the
  // list's total, which counts every status. Fetched only while the page is
  // fully selected or all-matching is on; the list total stands in until then.
  const fetchPendingCount = useCallback(
    (signal: AbortSignal) => api.countBulkDedupCandidates({ ...ACOUSTIC_BULK_FILTER }, { signal }),
    []
  );
  const matchingCount = useServerMatchingCount(fetchPendingCount, candidates);
  const selection = useRowSelection<number>({
    pageKeys,
    totalMatching: matchingCount.totalOr(total),
    resetKey: String(rowsPerPage),
    isDisabled: isDecided,
  });
  matchingCount.sync(selection.pageFullySelected || selection.allMatching);
  const matchingCountState =
    matchingCount.count.state === 'ready'
      ? undefined
      : matchingCount.count.state === 'error'
        ? ('approximate' as const)
        : ('counting' as const);
  const selectedLabel = !selection.allMatching
    ? selection.selectedCount.toLocaleString()
    : matchingCountState === 'counting'
      ? '…'
      : `${matchingCountState === 'approximate' ? '~' : ''}${selection.selectedCount.toLocaleString()}`;

  // Cleanup timeout on unmount
  useEffect(() => {
    return () => {
      isUnmountedRef.current = true;
      if (timeoutRef.current) {
        clearTimeout(timeoutRef.current);
        timeoutRef.current = null;
      }
    };
  }, []);

  const handleFingerprint = async () => {
    setFingerprinting(true);
    setStatusMsg(null);
    try {
      const op = await api.triggerFingerprintBackfill('missing');
      setStatusMsg(`Fingerprinting queued — see bell icon for progress (op ${op.id.slice(-6)})`);
    } catch (err) {
      setStatusMsg(err instanceof Error ? err.message : 'Fingerprint job failed to start');
    } finally {
      setFingerprinting(false);
    }
  };

  const handleScan = async () => {
    setScanning(true);
    setStatusMsg(null);
    try {
      const op = await api.triggerDedupAcoustID();
      setStatusMsg(`Duplicate scan queued — see bell icon for progress (op ${op.id.slice(-6)})`);
      if (timeoutRef.current) clearTimeout(timeoutRef.current);
      timeoutRef.current = setTimeout(() => {
        if (!isUnmountedRef.current) {
          loadCandidates();
        }
      }, 5000);
    } catch (err) {
      setStatusMsg(err instanceof Error ? err.message : 'Scan failed to start');
    } finally {
      setScanning(false);
    }
  };

  const handleMerge = async (candidateId: number, keepId?: string) => {
    // The sync /dedup/candidates/:id/link endpoint (formerly /merge; the old
    // path is a deprecated alias) performs the link, updates candidate
    // status, publishes the event, and cleans up orphan candidates (PR
    // #1167). Previously we also fired /audiobooks/link (formerly /merge,
    // async) here, which caused a race + UI flicker + spurious 409 from
    // the sync call when the async one won. (B1)
    //
    // keepId, when provided, tells the backend which side of the pair to
    // keep as the merge primary. Without it the backend auto-selects by
    // format/bitrate/size — which historically ignored the user's
    // Keep A / Keep B click.
    setResolving((s) => new Set(s).add(candidateId));
    try {
      await api.linkDedupCandidate(candidateId, keepId);
      setCandidates((prev) => prev.filter((c) => c.id !== candidateId));
    } catch (err) {
      setStatusSeverity('error');
      setStatusMsg(err instanceof Error ? err.message : 'Merge failed');
    } finally {
      setResolving((s) => {
        const next = new Set(s);
        next.delete(candidateId);
        return next;
      });
    }
  };

  const handleDismiss = async (candidateId: number) => {
    setResolving((s) => new Set(s).add(candidateId));
    try {
      await api.rejectDedupCandidate(candidateId);
      setCandidates((prev) => prev.filter((c) => c.id !== candidateId));
    } catch (err) {
      setStatusSeverity('error');
      setStatusMsg(err instanceof Error ? err.message : 'Dismiss failed');
    } finally {
      setResolving((s) => {
        const next = new Set(s);
        next.delete(candidateId);
        return next;
      });
    }
  };

  // One-shot cleanup of pending candidates that are no longer real duplicates:
  // chapter files of one multi-file book (same parent directory), books in the
  // same version group, distinct numbered series volumes. Runs server-side via
  // Engine.PurgeStaleCandidates; no rescan required.
  const handlePurgeStale = async () => {
    setPurging(true);
    setStatusMsg(null);
    try {
      const { op_id } = await api.purgeStaleCandidates();
      setStatusMsg(
        op_id
          ? `Cleanup queued — see bell for progress (op ${op_id.slice(-6)}).`
          : 'Cleanup queued — see bell for progress.'
      );
      if (timeoutRef.current) clearTimeout(timeoutRef.current);
      timeoutRef.current = setTimeout(() => {
        if (!isUnmountedRef.current) loadCandidates();
      }, 3000);
    } catch (err) {
      setStatusMsg(err instanceof Error ? err.message : 'Cleanup failed');
    } finally {
      setPurging(false);
    }
  };

  // Nuke every stored AcoustID fingerprint + drop acoustid candidates + force
  // a full rescan. Use when prior fingerprints are suspected bad (e.g. the
  // "AQAAAA" sentinel pollution that made every book a 100% match against
  // one anchor). Heavy: 5–10 minute clear, then a multi-hour rescan.
  // Online AcoustID lookup — sends every fingerprint to acoustid.org's
  // /v2/lookup and stores the top MusicBrainz recording match. Rate-
  // limited (~3 req/sec) so this takes hours on a large library.
  // Audiobook hit rate is modest (5–15%); the value is the "free wins"
  // when a chapter happens to be in MusicBrainz.
  const [onlineLookingUp, setOnlineLookingUp] = useState(false);
  const handleAcoustIDOnline = async () => {
    setOnlineLookingUp(true);
    setStatusMsg(null);
    try {
      const op = await api.triggerAcoustIDOnlineLookup();
      setStatusMsg(`AcoustID.org lookup queued — see bell (op ${op.id.slice(-6)}).`);
    } catch (err) {
      setStatusMsg(err instanceof Error ? err.message : 'AcoustID online lookup failed to start');
    } finally {
      setOnlineLookingUp(false);
    }
  };

  // AcoustID API key form. Loads the masked value from /api/v1/config so
  // the user can see "•••• …xyz" without re-entering it, then PUTs the
  // new value when they save. The server stores it in the settings DB
  // and the lookup-online op reads from config.AppConfig.AcoustIDAPIKey
  // before falling back to the env var.
  const [acoustidKey, setAcoustidKey] = useState('');
  const [acoustidKeyMask, setAcoustidKeyMask] = useState('');
  const [acoustidKeySaving, setAcoustidKeySaving] = useState(false);
  useEffect(() => {
    let cancelled = false;
    api
      .getConfig()
      .then((cfg) => {
        if (!cancelled) setAcoustidKeyMask(cfg.acoustid_api_key || '');
      })
      .catch(() => {
        /* leave blank */
      });
    return () => {
      cancelled = true;
    };
  }, []);
  const handleSaveAcoustIDKey = async () => {
    if (!acoustidKey.trim()) return;
    setAcoustidKeySaving(true);
    setStatusMsg(null);
    try {
      const cfg = await api.updateConfig({ acoustid_api_key: acoustidKey.trim() });
      setAcoustidKeyMask(cfg.acoustid_api_key || '');
      setAcoustidKey('');
      setStatusMsg('AcoustID API key saved.');
    } catch (err) {
      setStatusMsg(err instanceof Error ? err.message : 'Failed to save AcoustID API key');
    } finally {
      setAcoustidKeySaving(false);
    }
  };

  const [resetting, setResetting] = useState(false);
  const handleResetAcoustID = async () => {
    if (
      !window.confirm(
        'This clears EVERY stored AcoustID fingerprint and re-enqueues a full library rescan (multi-hour). Continue?'
      )
    )
      return;
    setResetting(true);
    setStatusMsg(null);
    try {
      const { reset_op_id, rescan_op_id } = await api.resetAcoustIDFingerprints();
      setStatusMsg(
        `Reset queued (op ${reset_op_id.slice(-6)}); rescan will follow (op ${rescan_op_id.slice(-6) || 'pending'}). Watch the bell.`
      );
    } catch (err) {
      setStatusMsg(err instanceof Error ? err.message : 'Reset failed');
    } finally {
      setResetting(false);
    }
  };

  // Bulk Keep A / Keep B / Dismiss over THIS PAGE's ticked rows. Each row was
  // ticked by hand, so each goes through the single-pair endpoints like a
  // per-row button. Links run one at a time -- each rewrites book rows and two
  // in flight can touch the same book; dismisses run 5 at a time.
  const bulkApply = async (action: AcousticBulkAction) => {
    if (selection.selectedCount === 0) return;
    setBulkBusy(true);
    setStatusMsg(null);
    setUndoIds(null);
    const rows = candidates.filter((c) => selection.selected.has(c.id));
    // Selected ids with no row behind them (decided and gone since) are
    // failures, not silently "processed".
    const failed: number[] = [...selection.selected].filter((id) => !rows.some((c) => c.id === id));
    const missing = failed.length;
    const concurrency = action === 'dismiss' ? 5 : 1;
    let done = 0;
    for (let i = 0; i < rows.length; i += concurrency) {
      const batch = rows.slice(i, i + concurrency);
      await Promise.all(
        batch.map(async (c) => {
          try {
            if (action === 'dismiss') await api.rejectDedupCandidate(c.id);
            else if (action === 'keep-a') await api.linkDedupCandidate(c.id, c.entity_a_id);
            else await api.linkDedupCandidate(c.id, c.entity_b_id);
          } catch {
            failed.push(c.id);
          }
        })
      );
      done += batch.length;
      setBulkProgress(
        `${BULK_LABEL[action]}: ${done.toLocaleString()} / ${rows.length.toLocaleString()}…`
      );
    }
    const attempted = rows.length + missing;
    const ok = attempted - failed.length;
    // Keep only the failures on this page selected, so a retry is one click.
    selection.replace(failed.filter((id) => candidates.some((c) => c.id === id)));
    setBulkBusy(false);
    setBulkProgress(null);
    setStatusSeverity(failed.length === 0 ? 'info' : 'error');
    setStatusMsg(
      failed.length === 0
        ? `${BULK_LABEL[action]}: ${ok.toLocaleString()} candidate(s) processed`
        : `${BULK_LABEL[action]}: ${ok.toLocaleString()} ok, ${failed.length.toLocaleString()} failed of ${attempted.toLocaleString()}`
    );
    await loadCandidates();
  };

  // Every pending acoustic candidate ("Select all N matching"): one request to
  // the filter-scoped endpoint, which re-evaluates the filter, refuses a count
  // that moved since the confirmation (409), applies the review-queue-only
  // guard and re-checks each pair before acting. Nothing is resolved here.
  const bulkApplyAll = async (action: AcousticBulkAction, pending: number) => {
    setBulkBusy(true);
    setStatusMsg(null);
    setUndoIds(null);
    setBulkProgress(
      `${BULK_LABEL[action]}: working on ${pending.toLocaleString()} pending candidates…`
    );
    const filter = { ...ACOUSTIC_BULK_FILTER, expected_total: pending };
    try {
      if (action === 'dismiss') {
        const r = await api.bulkRejectDedupCandidates(filter);
        setStatusSeverity(r.failed === 0 ? 'info' : 'error');
        setStatusMsg(
          `Dismiss: ${r.rejected.toLocaleString()} dismissed, ${r.failed.toLocaleString()} refused or failed of ${r.attempted.toLocaleString()}`
        );
        setUndoIds(r.rejected_ids && r.rejected_ids.length > 0 ? r.rejected_ids : null);
      } else {
        const r = await api.bulkLinkDedupCandidates({
          ...filter,
          keep_side: action === 'keep-a' ? 'a' : 'b',
        });
        setStatusSeverity(r.failed === 0 ? 'info' : 'error');
        setStatusMsg(
          `${BULK_LABEL[action]}: ${r.merged.toLocaleString()} linked, ${r.failed.toLocaleString()} refused or failed of ${r.attempted.toLocaleString()}` +
            (r.failures && r.failures.length > 0
              ? ` (e.g. #${r.failures[0].candidate_id}: ${r.failures[0].reason})`
              : '')
        );
      }
      selection.clear();
    } catch (err) {
      const moved = api.filterChangedOf(err);
      setStatusSeverity(moved ? 'info' : 'error');
      setStatusMsg(
        moved
          ? `The list changed — ${moved.matched.toLocaleString()} pending candidates now match, not ${moved.expected.toLocaleString()}. Nothing was changed; confirm again.`
          : err instanceof Error
            ? err.message
            : 'Bulk action failed'
      );
    } finally {
      setBulkBusy(false);
      setBulkProgress(null);
    }
    await loadCandidates();
  };

  const undoBulkDismiss = async () => {
    if (!undoIds) return;
    const ids = undoIds;
    setUndoIds(null);
    try {
      const r = await api.revertBulkRejectDedupCandidates(ids);
      setStatusSeverity(r.failed === 0 ? 'info' : 'error');
      setStatusMsg(
        `Undo: ${r.reverted.toLocaleString()} back to pending` +
          (r.failed > 0 ? `, ${r.failed.toLocaleString()} changed since and left alone` : '')
      );
    } catch (err) {
      setStatusSeverity('error');
      setStatusMsg(err instanceof Error ? err.message : 'Undo failed');
    }
    await loadCandidates();
  };

  /**
   * Cross-page actions always confirm, against the PENDING count (the list
   * shows every status; only pending pairs are acted on). Page selections act
   * on the ticked rows directly.
   */
  const requestBulk = async (action: AcousticBulkAction) => {
    if (!selection.allMatching) {
      void bulkApply(action);
      return;
    }
    try {
      // The server's bulk count (same function bulk-link / bulk-reject
      // re-evaluate, dead rows excluded), sent back as expected_total. The
      // list's total is a paging hint and would 409 on every dead row.
      const pending = await api.countBulkDedupCandidates({ ...ACOUSTIC_BULK_FILTER });
      setConfirmBulk({ action, pending });
    } catch (err) {
      setStatusSeverity('error');
      setStatusMsg(err instanceof Error ? err.message : 'Could not count the pending candidates');
    }
  };

  const simPct = (c: DedupCandidate) =>
    c.similarity != null ? `${Math.round(c.similarity * 100)}%` : '—';

  const bookTitle = (id: string) => {
    const b = bookCache.get(id);
    if (!b) return <em style={{ opacity: 0.5 }}>{id.slice(-8)}</em>;
    const title = b.title;
    const isGarbage =
      !title || title.toUpperCase() === 'TITLE' || /^[0-9A-Z]{26}$/.test(title.trim());
    if (isGarbage) return <em style={{ color: 'orange' }}>{title || '(no title)'}</em>;
    return title;
  };

  // Renders the title + file path for a candidate cell. Title opens the book
  // detail page in a new tab so reviewers don't lose their position in the
  // dedup list. File path lives directly under the title so reviewers can
  // disambiguate when titles are missing, identical, or wrong — the case the
  // user has been screaming about. If the book row 404s out of the backend
  // (merged/deleted/orphaned candidate) the cell shows a clear "(missing)"
  // marker and a Dismiss-orphan action is implied via the row's Dismiss
  // button.
  const renderBookCell = (id: string) => {
    const b = bookCache.get(id);
    const missing = !b;
    const path = b?.file_path ?? '';
    return (
      <Stack spacing={0.25} sx={{ minWidth: 0 }}>
        {missing ? (
          <Typography variant="body2" sx={{ color: 'error.main', fontStyle: 'italic' }}>
            (missing book — {id.slice(-8)})
          </Typography>
        ) : (
          // SPA navigation via react-router Link (NOT target="_blank"). The
          // new-tab version forced a full bundle reload, which kicked the
          // SSE connection — causing "Client unregistered" + HTTP/3 TLS
          // handshake EOF noise every click. In-app nav is instant and
          // preserves the SSE. Ctrl/Cmd-click still opens in a new tab if
          // the user wants to keep their place in the candidate list.
          <Link
            component={RouterLink}
            to={`/library/${id}`}
            underline="hover"
            sx={{
              color: 'primary.main',
              fontWeight: 500,
              fontSize: '0.95rem',
              textTransform: 'none',
              textAlign: 'left',
              display: 'block',
              whiteSpace: 'normal',
              wordBreak: 'break-word',
            }}
            onClick={(e) => e.stopPropagation()}
          >
            {bookTitle(id)}
          </Link>
        )}
        {path && (
          <Tooltip title={path} placement="bottom-start">
            <Typography
              variant="caption"
              sx={{
                color: 'text.secondary',
                fontFamily: 'monospace',
                fontSize: '0.72rem',
                lineHeight: 1.2,
                wordBreak: 'break-all',
                opacity: 0.75,
              }}
            >
              {path}
            </Typography>
          </Tooltip>
        )}
      </Stack>
    );
  };

  return (
    <Box>
      <Stack
        direction="row"
        spacing={2}
        useFlexGap
        sx={{
          alignItems: 'center',
          flexWrap: 'wrap',
          mb: 1,
        }}
      >
        <Typography variant="h6">Acoustic Duplicates</Typography>

        <Tooltip title="Read every audio file and compute 7-segment chromaprint fingerprints. Required before duplicate scanning. Runs overnight; safe to trigger manually for new files.">
          <Button
            variant="outlined"
            startIcon={<FingerprintIcon />}
            onClick={handleFingerprint}
            disabled={fingerprinting}
          >
            {fingerprinting ? 'Queuing…' : 'Fingerprint Books'}
          </Button>
        </Tooltip>

        <Tooltip title="Compare already-stored fingerprints across all books to find audio-level duplicate pairs. Fast — no file I/O.">
          <Button
            variant="outlined"
            startIcon={<GraphicEqIcon />}
            onClick={handleScan}
            disabled={scanning}
          >
            {scanning ? 'Queuing…' : 'Find Acoustic Duplicates'}
          </Button>
        </Tooltip>

        <Tooltip title="Delete pending candidates that are no longer valid duplicates: chapter files of one multi-file book, same-version-group books, distinct series volumes. Fast — no rescan.">
          <Button variant="outlined" color="warning" onClick={handlePurgeStale} disabled={purging}>
            {purging ? 'Cleaning…' : 'Cleanup Stale (same-folder, etc)'}
          </Button>
        </Tooltip>

        <Tooltip title="Nuke every stored AcoustID fingerprint and force a full rescan. Use when stored fingerprints are suspected bad (e.g. every book matching one anchor at 100%). Multi-hour.">
          <Button
            variant="outlined"
            color="error"
            onClick={handleResetAcoustID}
            disabled={resetting}
          >
            {resetting ? 'Queuing…' : 'Reset & Rescan All AcoustID'}
          </Button>
        </Tooltip>

        <Tooltip title="Send every file's whole-file chromaprint to acoustid.org's /v2/lookup and store the top MusicBrainz recording_id (score ≥ 0.85). Requires ACOUSTID_API_KEY. Rate-limited to ~3 req/sec; takes hours over a full library. Audiobook coverage in AcoustID's DB is sparse — expect a 5–15% hit rate.">
          <Button
            variant="outlined"
            color="info"
            onClick={handleAcoustIDOnline}
            disabled={onlineLookingUp}
          >
            {onlineLookingUp ? 'Queuing…' : 'Look Up on AcoustID.org'}
          </Button>
        </Tooltip>

        <IconButton onClick={() => loadCandidates()} size="small" title="Refresh">
          <RefreshIcon />
        </IconButton>
      </Stack>

      <Typography
        variant="caption"
        sx={{
          color: 'text.secondary',
          mb: 2,
          display: 'block',
        }}
      >
        Workflow: <strong>Fingerprint Books</strong> (reads audio, ~hours) →{' '}
        <strong>Find Acoustic Duplicates</strong> (compares hashes, seconds). Merge direction:
        prefer the book with richer metadata (ASIN/ISBN → cover → sane title).
      </Typography>

      {/* AcoustID online lookup — API key form. Saved to the settings
          DB via PUT /api/v1/config; the server-side op reads from
          AppConfig.AcoustIDAPIKey before falling back to env. */}
      <Paper variant="outlined" sx={{ mb: 2, p: 1.5 }}>
        <Stack
          direction="row"
          spacing={1}
          useFlexGap
          sx={{
            alignItems: 'center',
            flexWrap: 'wrap',
          }}
        >
          <Typography variant="body2" sx={{ fontWeight: 500, minWidth: 0 }}>
            AcoustID.org API key
          </Typography>
          <TextField
            size="small"
            type="password"
            placeholder={
              acoustidKeyMask ? `Saved: ${acoustidKeyMask}` : 'Get a free key at acoustid.org/login'
            }
            value={acoustidKey}
            onChange={(e) => setAcoustidKey(e.target.value)}
            sx={{ flex: 1, minWidth: 280 }}
            slotProps={{
              htmlInput: { autoComplete: 'off' },
            }}
          />
          <Button
            variant="outlined"
            size="small"
            onClick={handleSaveAcoustIDKey}
            disabled={acoustidKeySaving || !acoustidKey.trim()}
          >
            {acoustidKeySaving ? 'Saving…' : 'Save'}
          </Button>
        </Stack>
        <Typography
          variant="caption"
          sx={{
            color: 'text.secondary',
            mt: 0.5,
            display: 'block',
          }}
        >
          Required for "Look Up on AcoustID.org". Stored in the settings database (masked when read
          back). Falls back to ACOUSTID_API_KEY env var if unset.
        </Typography>
      </Paper>

      {statusMsg && (
        <Alert
          severity={statusSeverity}
          sx={{ mb: 2 }}
          onClose={() => {
            setStatusMsg(null);
            setStatusSeverity('info');
          }}
          action={
            undoIds ? (
              <Button
                size="small"
                color="inherit"
                onClick={() => void undoBulkDismiss()}
                data-testid="acoustic-undo-dismiss"
              >
                Undo
              </Button>
            ) : undefined
          }
        >
          {statusMsg}
        </Alert>
      )}

      {loading ? (
        <LinearProgress />
      ) : loadError ? (
        <Alert
          severity="error"
          data-testid="acoustic-load-error"
          action={
            <Button size="small" onClick={() => void loadCandidates()}>
              Retry
            </Button>
          }
        >
          {loadError}
        </Alert>
      ) : candidates.length === 0 ? (
        <Alert severity="info">
          No acoustic duplicate candidates found. Run "Fingerprint Books" then "Find Acoustic
          Duplicates".
        </Alert>
      ) : (
        <Paper>
          {/* Bulk action toolbar — visible whenever any row is selected. */}
          <Box sx={{ px: 2 }}>
            <SelectAllMatchingBanner
              selection={selection}
              pageCount={candidates.length}
              totalMatching={matchingCount.totalOr(total)}
              countState={matchingCountState}
              noun="candidates"
              testIdPrefix="acoustic-select-all"
            />
          </Box>
          {selection.selectedCount > 0 && (
            <Stack
              direction="row"
              spacing={1}
              sx={{
                alignItems: 'center',
                px: 2,
                py: 1,
                bgcolor: 'action.selected',
                borderBottom: '1px solid',
                borderColor: 'divider',
              }}
            >
              <Typography variant="body2" sx={{ fontWeight: 600 }}>
                {selectedLabel} selected
                {selection.allMatching ? ' (every page)' : ''}
              </Typography>
              {bulkProgress && (
                <Typography
                  variant="caption"
                  color="text.secondary"
                  data-testid="acoustic-bulk-progress"
                >
                  {bulkProgress}
                </Typography>
              )}
              <Box sx={{ flexGrow: 1 }} />
              <Button
                size="small"
                variant="outlined"
                disabled={bulkBusy}
                onClick={() => void requestBulk('keep-a')}
              >
                Keep A on {selectedLabel}
              </Button>
              <Button
                size="small"
                variant="outlined"
                disabled={bulkBusy}
                onClick={() => void requestBulk('keep-b')}
              >
                Keep B on {selectedLabel}
              </Button>
              <Button
                size="small"
                variant="outlined"
                color="warning"
                disabled={bulkBusy}
                onClick={() => void requestBulk('dismiss')}
              >
                Dismiss {selectedLabel}
              </Button>
              <Button size="small" variant="text" disabled={bulkBusy} onClick={selection.clear}>
                Clear
              </Button>
            </Stack>
          )}
          <TableContainer>
            <Table size="small">
              <TableHead>
                <TableRow>
                  <TableCell padding="checkbox">
                    <Checkbox
                      size="small"
                      indeterminate={selection.header.indeterminate}
                      checked={selection.header.checked}
                      disabled={selection.header.disabled}
                      onChange={selection.togglePage}
                      slotProps={{
                        input: { 'aria-label': `Select all ${candidates.length} on this page` },
                      }}
                    />
                  </TableCell>
                  <TableCell>Book A</TableCell>
                  <TableCell>Book B</TableCell>
                  <TableCell align="center">Similarity</TableCell>
                  <TableCell>Actions</TableCell>
                </TableRow>
              </TableHead>
              <TableBody>
                {candidates.map((c) => {
                  const bookA = bookCache.get(c.entity_a_id);
                  const bookB = bookCache.get(c.entity_b_id);
                  const qA = metadataQuality(bookA);
                  const qB = metadataQuality(bookB);
                  const recommendA = qA > qB;
                  const recommendB = qB > qA;
                  const busy = resolving.has(c.id);
                  const selected = selection.isSelected(c.id);
                  return (
                    <TableRow
                      key={c.id}
                      hover
                      selected={selected}
                      sx={[
                        busy
                          ? {
                              opacity: 0.5,
                            }
                          : {
                              opacity: 1,
                            },
                      ]}
                    >
                      <TableCell padding="checkbox">
                        <Checkbox
                          size="small"
                          {...selection.checkboxProps(c.id)}
                          slotProps={{ input: { 'aria-label': `Select candidate ${c.id}` } }}
                        />
                      </TableCell>
                      <TableCell sx={{ verticalAlign: 'top', minWidth: 280 }}>
                        <Stack spacing={0.5}>
                          {renderBookCell(c.entity_a_id)}
                          <Stack
                            direction="row"
                            spacing={0.5}
                            useFlexGap
                            sx={{
                              flexWrap: 'wrap',
                            }}
                          >
                            {qualityChip(qA)}
                            {recommendA && (
                              <Chip label="★ Recommended keep" size="small" color="primary" />
                            )}
                          </Stack>
                        </Stack>
                      </TableCell>
                      <TableCell sx={{ verticalAlign: 'top', minWidth: 280 }}>
                        <Stack spacing={0.5}>
                          {renderBookCell(c.entity_b_id)}
                          <Stack
                            direction="row"
                            spacing={0.5}
                            useFlexGap
                            sx={{
                              flexWrap: 'wrap',
                            }}
                          >
                            {qualityChip(qB)}
                            {recommendB && (
                              <Chip label="★ Recommended keep" size="small" color="primary" />
                            )}
                          </Stack>
                        </Stack>
                      </TableCell>
                      <TableCell align="center">
                        <Chip
                          label={simPct(c)}
                          size="small"
                          color={(c.similarity ?? 0) >= 0.9 ? 'error' : 'warning'}
                        />
                      </TableCell>
                      <TableCell>
                        <Stack
                          direction="row"
                          spacing={0.5}
                          useFlexGap
                          sx={{
                            flexWrap: 'wrap',
                          }}
                        >
                          <Tooltip title="Keep Book A, merge B into it">
                            <Button
                              size="small"
                              variant={recommendA ? 'contained' : 'outlined'}
                              color="primary"
                              disabled={busy}
                              onClick={() => handleMerge(c.id, c.entity_a_id)}
                            >
                              Keep A
                            </Button>
                          </Tooltip>
                          <Tooltip title="Keep Book B, merge A into it">
                            <Button
                              size="small"
                              variant={recommendB ? 'contained' : 'outlined'}
                              color="primary"
                              disabled={busy}
                              onClick={() => handleMerge(c.id, c.entity_b_id)}
                            >
                              Keep B
                            </Button>
                          </Tooltip>
                          <Tooltip title="Compare fingerprint segments side-by-side">
                            <Button
                              size="small"
                              variant="outlined"
                              startIcon={<GraphicEqIcon />}
                              onClick={() => {
                                setCompareA(c.entity_a_id);
                                setCompareB(c.entity_b_id);
                                setShowComparePanel(true);
                              }}
                            >
                              Compare
                            </Button>
                          </Tooltip>
                          <Tooltip title="Not a duplicate — dismiss">
                            <Button
                              size="small"
                              variant="text"
                              color="inherit"
                              disabled={busy}
                              onClick={() => handleDismiss(c.id)}
                            >
                              Dismiss
                            </Button>
                          </Tooltip>
                        </Stack>
                      </TableCell>
                    </TableRow>
                  );
                })}
              </TableBody>
            </Table>
          </TableContainer>
          <TablePagination
            component="div"
            count={total}
            page={page}
            onPageChange={(_, p) => {
              setPage(p);
              // An explicit selection's rows leave the screen; "all matching" does not.
              if (!selection.allMatching) selection.clear();
            }}
            rowsPerPage={rowsPerPage}
            onRowsPerPageChange={(e) => {
              setRowsPerPage(parseInt(e.target.value, 10));
              setPage(0);
              // rowsPerPage is the selection's resetKey, so it clears itself.
            }}
            rowsPerPageOptions={[25, 50, 100, 250]}
          />
        </Paper>
      )}

      <Dialog
        open={confirmBulk !== null}
        onClose={() => setConfirmBulk(null)}
        data-testid="acoustic-bulk-confirm"
      >
        <DialogTitle>
          {confirmBulk ? BULK_LABEL[confirmBulk.action] : ''} on all{' '}
          {(confirmBulk?.pending ?? 0).toLocaleString()} pending candidates?
        </DialogTitle>
        <DialogContent>
          <DialogContentText>
            {confirmBulk?.action === 'dismiss'
              ? 'Every pending acoustic candidate, on every page, is dismissed as not a duplicate. The result offers Undo.'
              : 'Every pending acoustic candidate, on every page, is linked into a version group keeping the chosen side. This cannot be undone.'}{' '}
            Pairs already merged or dismissed are not touched. The server skips hand-pinned pairs,
            pairs at the same path and links that would chain them together, and reports each one.
            If the number of pending candidates changes before this runs, nothing is changed and you
            are asked again.
            {(confirmBulk?.pending ?? 0) > BULK_APPLY_DEFAULT_CAP &&
              ` ${(confirmBulk?.pending ?? 0).toLocaleString()} is over the server's bulk limit (bulk_apply_max_items, ${BULK_APPLY_DEFAULT_CAP.toLocaleString()} by default), so it will be refused.`}
          </DialogContentText>
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setConfirmBulk(null)}>Cancel</Button>
          <Button
            color="warning"
            variant="contained"
            disabled={!confirmBulk || confirmBulk.pending === 0}
            data-testid="acoustic-bulk-confirm-btn"
            onClick={() => {
              const c = confirmBulk;
              setConfirmBulk(null);
              if (c) void bulkApplyAll(c.action, c.pending);
            }}
          >
            {confirmBulk ? BULK_LABEL[confirmBulk.action] : ''}{' '}
            {(confirmBulk?.pending ?? 0).toLocaleString()}
          </Button>
        </DialogActions>
      </Dialog>

      <Box sx={{ mt: 3 }} ref={showComparePanel ? comparePanelRef : undefined}>
        <AcousticComparePanel initialA={compareA} initialB={compareB} />
      </Box>
    </Box>
  );
}
