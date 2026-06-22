// file: web/src/components/dedup/DedupAcousticTab.tsx
// version: 1.0.0
// guid: c3d4e5f6-a7b8-9012-cdef-012345678902
// last-edited: 2026-06-22

import { useState, useEffect, useCallback, useRef } from 'react';
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
  TableHead,
  TableRow,
  TableCell,
  TableBody,
} from '@mui/material';
import RefreshIcon from '@mui/icons-material/Refresh';
import FingerprintIcon from '@mui/icons-material/Fingerprint';
import GraphicEqIcon from '@mui/icons-material/GraphicEq';
import * as api from '../../services/api';
import type { Book, DedupCandidate } from '../../services/api';
import { CoverLightbox } from '../CoverLightbox';
import { fetchBookCached } from './DedupEmbeddingTab';

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
        fontWeight={600}
        sx={{ cursor: 'pointer', '&:hover': { textDecoration: 'underline' } }}
        onClick={() => navigate(`/library/${book.id}`)}
        noWrap
      >
        {book.title || <em style={{ opacity: 0.5 }}>Untitled</em>}
      </Typography>
      {book.author_name && (
        <Typography variant="caption" color="text.secondary" noWrap>
          {book.author_name}
        </Typography>
      )}
      {book.series_name && (
        <Typography variant="caption" color="text.secondary" noWrap display="block">
          {book.series_name}{book.series_position ? ` · Book ${book.series_position}` : ''}
        </Typography>
      )}
      <Stack direction="row" spacing={0.5} sx={{ mt: 0.5 }} flexWrap="wrap" useFlexGap>
        {book.format && <Chip label={book.format.toUpperCase()} size="small" />}
        {book.duration && <Chip label={formatDuration(book.duration)} size="small" variant="outlined" />}
      </Stack>
      {filePath && (
        <Typography
          variant="caption"
          color="text.secondary"
          sx={{ display: 'block', mt: 0.5, wordBreak: 'break-all', fontSize: '0.65rem', fontFamily: 'monospace' }}
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
      <Typography variant="caption" color="text.secondary" sx={{ fontWeight: 600, textTransform: 'uppercase', letterSpacing: 0.5 }}>
        {label}
      </Typography>
      <Stack direction="row" spacing={1.5} sx={{ mt: 0.5 }} alignItems="flex-start">
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
    seg0: 'Intro', seg1: 'Body 1', seg2: 'Body 2',
    seg3: 'Body 3', seg4: 'Body 4', seg5: 'Body 5', seg6: 'Outro',
  };

  const hasAnySegments = result
    ? result.segment_scores.some((s) => s.hash_a || s.hash_b)
    : false;

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
      <Typography variant="subtitle1" sx={{ mb: 1.5, fontWeight: 600 }}>Fingerprint Comparison</Typography>
      <Stack direction="row" spacing={2} sx={{ mb: 2 }} alignItems="flex-start">
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
        <Button variant="contained" onClick={handleCompare} disabled={loading || !bookAID.trim() || !bookBID.trim()} sx={{ mt: 0.5 }}>
          {loading ? 'Comparing…' : 'Compare'}
        </Button>
      </Stack>

      {error && <Alert severity="error" sx={{ mb: 1 }}>{error}</Alert>}

      {result && (
        <Box>
          {/* Cover images and metadata side by side */}
          <Stack direction="row" spacing={3} sx={{ mb: 3 }}>
            {/* Book A */}
            <Box sx={{ flex: 1, display: 'flex', flexDirection: 'column', alignItems: 'center', gap: 2 }}>
              <Typography variant="caption" color="text.secondary" sx={{ fontWeight: 600, textTransform: 'uppercase', letterSpacing: 0.5 }}>
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
              <AcousticBookMetadata book={result.book_a as Book} filePath={(result.book_a as any)?.file_path} />
            </Box>

            <Divider orientation="vertical" flexItem />

            {/* Book B (same structure as Book A) */}
            <Box sx={{ flex: 1, display: 'flex', flexDirection: 'column', alignItems: 'center', gap: 2 }}>
              <Typography variant="caption" color="text.secondary" sx={{ fontWeight: 600, textTransform: 'uppercase', letterSpacing: 0.5 }}>
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
              <AcousticBookMetadata book={result.book_b as Book} filePath={(result.book_b as any)?.file_path} />
            </Box>
          </Stack>

          {/* Lightbox modal */}
          <CoverLightbox open={lightboxOpen} src={lightboxSrc} onClose={handleCloseLightbox} />

          {/* Similarity score */}
          <Stack direction="row" spacing={1} alignItems="center" sx={{ mb: 2 }}>
            <Chip
              label={hasAnySegments ? `${Math.round(result.overall_score * 100)}% match` : 'No fingerprint data'}
              color={
                !hasAnySegments ? 'default'
                  : result.overall_score >= 0.85 ? 'error'
                  : result.overall_score >= 0.6 ? 'warning'
                  : 'default'
              }
              icon={<GraphicEqIcon />}
            />
            {!hasAnySegments && (
              <Typography variant="caption" color="text.secondary">
                Run "Fingerprint Books" first to populate segment data
              </Typography>
            )}
          </Stack>

          {/* Segment table */}
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
                  <TableCell><strong>{segLabels[seg.segment] ?? seg.segment}</strong></TableCell>
                  <TableCell sx={{ fontFamily: 'monospace', fontSize: '0.7rem' }}>
                    {seg.hash_a ? seg.hash_a.slice(0, 16) + '…' : <em style={{ opacity: 0.4 }}>not fingerprinted</em>}
                  </TableCell>
                  <TableCell sx={{ fontFamily: 'monospace', fontSize: '0.7rem' }}>
                    {seg.hash_b ? seg.hash_b.slice(0, 16) + '…' : <em style={{ opacity: 0.4 }}>not fingerprinted</em>}
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
    !title ||
    title.toUpperCase() === 'TITLE' ||
    /^[0-9A-Z]{26}$/.test(title.trim());
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
  if (score >= 6) return <Chip label="Rich metadata" size="small" color="success" variant="outlined" />;
  if (score >= 3) return <Chip label="Partial metadata" size="small" color="warning" variant="outlined" />;
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
  // way. Multiselect bulk Keep-A / Keep-B / Dismiss is a follow-up.
  const [rowsPerPage, setRowsPerPage] = useState(100);
  const [bookCache, setBookCache] = useState<Map<string, Book>>(new Map());
  const [selectedCandIds, setSelectedCandIds] = useState<Set<number>>(new Set());
  const [bulkBusy, setBulkBusy] = useState(false);
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
    try {
      const resp = await api.getDedupCandidates({
        layer: 'acoustid',
        limit: rowsPerPage,
        offset: page * rowsPerPage,
      });
      const cands = resp.candidates || [];
      setCandidates(cands);
      setTotal(resp.total || 0);

      const ids = new Set<string>();
      for (const c of cands) { ids.add(c.entity_a_id); ids.add(c.entity_b_id); }
      const cache = new Map<string, Book>();
      await Promise.all(Array.from(ids).map(async (id) => {
        try {
          const book = await fetchBookCached(id);
          if (book) cache.set(id, book);
        } catch { /* ignore */ }
      }));
      setBookCache(cache);
    } catch {
      // handled by empty state
    } finally {
      setLoading(false);
    }
  }, [page, rowsPerPage]);

  useEffect(() => { loadCandidates(); }, [loadCandidates]);

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
    // The sync /dedup/candidates/:id/merge endpoint performs the merge,
    // updates candidate status, publishes the event, and cleans up orphan
    // candidates (PR #1167). Previously we also fired /audiobooks/merge
    // (async) here, which caused a race + UI flicker + spurious 409 from
    // the sync call when the async one won. (B1)
    //
    // keepId, when provided, tells the backend which side of the pair to
    // keep as the merge primary. Without it the backend auto-selects by
    // format/bitrate/size — which historically ignored the user's
    // Keep A / Keep B click.
    setResolving((s) => new Set(s).add(candidateId));
    try {
      await api.mergeDedupCandidate(candidateId, keepId);
      setCandidates((prev) => prev.filter((c) => c.id !== candidateId));
    } catch (err) {
      setStatusSeverity('error');
      setStatusMsg(err instanceof Error ? err.message : 'Merge failed');
    } finally {
      setResolving((s) => { const next = new Set(s); next.delete(candidateId); return next; });
    }
  };

  const handleDismiss = async (candidateId: number) => {
    setResolving((s) => new Set(s).add(candidateId));
    try {
      await api.dismissDedupCandidate(candidateId);
      setCandidates((prev) => prev.filter((c) => c.id !== candidateId));
    } catch (err) {
      setStatusSeverity('error');
      setStatusMsg(err instanceof Error ? err.message : 'Dismiss failed');
    } finally {
      setResolving((s) => { const next = new Set(s); next.delete(candidateId); return next; });
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
          : 'Cleanup queued — see bell for progress.',
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
    api.getConfig().then((cfg) => {
      if (!cancelled) setAcoustidKeyMask(cfg.acoustid_api_key || '');
    }).catch(() => { /* leave blank */ });
    return () => { cancelled = true; };
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
    if (!window.confirm(
      'This clears EVERY stored AcoustID fingerprint and re-enqueues a full library rescan (multi-hour). Continue?',
    )) return;
    setResetting(true);
    setStatusMsg(null);
    try {
      const { reset_op_id, rescan_op_id } = await api.resetAcoustIDFingerprints();
      setStatusMsg(
        `Reset queued (op ${reset_op_id.slice(-6)}); rescan will follow (op ${rescan_op_id.slice(-6) || 'pending'}). Watch the bell.`,
      );
    } catch (err) {
      setStatusMsg(err instanceof Error ? err.message : 'Reset failed');
    } finally {
      setResetting(false);
    }
  };

  // Bulk dismiss N candidates in parallel (capped concurrency to be polite to
  // the backend). Refreshes the list once at the end instead of per-call so
  // the UI doesn't thrash. Selecting nothing is a no-op.
  const bulkApply = async (
    action: 'dismiss' | 'keep-a' | 'keep-b',
  ) => {
    if (selectedCandIds.size === 0) return;
    setBulkBusy(true);
    const ids = Array.from(selectedCandIds);
    const failed: number[] = [];
    const CONCURRENCY = 5;
    for (let i = 0; i < ids.length; i += CONCURRENCY) {
      const batch = ids.slice(i, i + CONCURRENCY);
      await Promise.all(batch.map(async (id) => {
        const c = candidates.find((x) => x.id === id);
        if (!c) return;
        try {
          if (action === 'dismiss') {
            await api.dismissDedupCandidate(id);
          } else if (action === 'keep-a') {
            await api.mergeDedupCandidate(id, c.entity_a_id);
          } else {
            await api.mergeDedupCandidate(id, c.entity_b_id);
          }
        } catch {
          failed.push(id);
        }
      }));
    }
    setSelectedCandIds(new Set(failed));
    setBulkBusy(false);
    setStatusMsg(failed.length === 0
      ? `Bulk ${action}: ${ids.length} candidate(s) processed`
      : `Bulk ${action}: ${ids.length - failed.length} ok, ${failed.length} failed`);
    await loadCandidates();
  };

  const simPct = (c: DedupCandidate) =>
    c.similarity != null ? `${Math.round(c.similarity * 100)}%` : '—';

  const bookTitle = (id: string) => {
    const b = bookCache.get(id);
    if (!b) return <em style={{ opacity: 0.5 }}>{id.slice(-8)}</em>;
    const title = b.title;
    const isGarbage = !title || title.toUpperCase() === 'TITLE' || /^[0-9A-Z]{26}$/.test(title.trim());
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
      <Stack direction="row" spacing={2} alignItems="center" sx={{ mb: 1 }} flexWrap="wrap" useFlexGap>
        <Typography variant="h6">Acoustic Duplicates</Typography>

        <Tooltip title="Read every audio file and compute 7-segment chromaprint fingerprints. Required before duplicate scanning. Runs overnight; safe to trigger manually for new files.">
          <Button variant="outlined" startIcon={<FingerprintIcon />} onClick={handleFingerprint} disabled={fingerprinting}>
            {fingerprinting ? 'Queuing…' : 'Fingerprint Books'}
          </Button>
        </Tooltip>

        <Tooltip title="Compare already-stored fingerprints across all books to find audio-level duplicate pairs. Fast — no file I/O.">
          <Button variant="outlined" startIcon={<GraphicEqIcon />} onClick={handleScan} disabled={scanning}>
            {scanning ? 'Queuing…' : 'Find Acoustic Duplicates'}
          </Button>
        </Tooltip>

        <Tooltip title="Delete pending candidates that are no longer valid duplicates: chapter files of one multi-file book, same-version-group books, distinct series volumes. Fast — no rescan.">
          <Button variant="outlined" color="warning" onClick={handlePurgeStale} disabled={purging}>
            {purging ? 'Cleaning…' : 'Cleanup Stale (same-folder, etc)'}
          </Button>
        </Tooltip>

        <Tooltip title="Nuke every stored AcoustID fingerprint and force a full rescan. Use when stored fingerprints are suspected bad (e.g. every book matching one anchor at 100%). Multi-hour.">
          <Button variant="outlined" color="error" onClick={handleResetAcoustID} disabled={resetting}>
            {resetting ? 'Queuing…' : 'Reset & Rescan All AcoustID'}
          </Button>
        </Tooltip>

        <Tooltip title="Send every file's whole-file chromaprint to acoustid.org's /v2/lookup and store the top MusicBrainz recording_id (score ≥ 0.85). Requires ACOUSTID_API_KEY. Rate-limited to ~3 req/sec; takes hours over a full library. Audiobook coverage in AcoustID's DB is sparse — expect a 5–15% hit rate.">
          <Button variant="outlined" color="info" onClick={handleAcoustIDOnline} disabled={onlineLookingUp}>
            {onlineLookingUp ? 'Queuing…' : 'Look Up on AcoustID.org'}
          </Button>
        </Tooltip>

        <IconButton onClick={() => loadCandidates()} size="small" title="Refresh"><RefreshIcon /></IconButton>
      </Stack>

      <Typography variant="caption" color="text.secondary" sx={{ mb: 2, display: 'block' }}>
        Workflow: <strong>Fingerprint Books</strong> (reads audio, ~hours) → <strong>Find Acoustic Duplicates</strong> (compares hashes, seconds). Merge direction: prefer the book with richer metadata (ASIN/ISBN → cover → sane title).
      </Typography>

      {/* AcoustID online lookup — API key form. Saved to the settings
          DB via PUT /api/v1/config; the server-side op reads from
          AppConfig.AcoustIDAPIKey before falling back to env. */}
      <Paper variant="outlined" sx={{ mb: 2, p: 1.5 }}>
        <Stack direction="row" spacing={1} alignItems="center" flexWrap="wrap" useFlexGap>
          <Typography variant="body2" sx={{ fontWeight: 500, minWidth: 0 }}>
            AcoustID.org API key
          </Typography>
          <TextField
            size="small"
            type="password"
            placeholder={acoustidKeyMask ? `Saved: ${acoustidKeyMask}` : 'Get a free key at acoustid.org/login'}
            value={acoustidKey}
            onChange={(e) => setAcoustidKey(e.target.value)}
            sx={{ flex: 1, minWidth: 280 }}
            inputProps={{ autoComplete: 'off' }}
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
        <Typography variant="caption" color="text.secondary" sx={{ mt: 0.5, display: 'block' }}>
          Required for "Look Up on AcoustID.org". Stored in the settings database (masked when read back). Falls back to ACOUSTID_API_KEY env var if unset.
        </Typography>
      </Paper>

      {statusMsg && <Alert severity={statusSeverity} sx={{ mb: 2 }} onClose={() => { setStatusMsg(null); setStatusSeverity('info'); }}>{statusMsg}</Alert>}

      {loading ? (
        <LinearProgress />
      ) : candidates.length === 0 ? (
        <Alert severity="info">No acoustic duplicate candidates found. Run "Fingerprint Books" then "Find Acoustic Duplicates".</Alert>
      ) : (
        <Paper>
          {/* Bulk action toolbar — visible whenever any row is selected. */}
          {selectedCandIds.size > 0 && (
            <Stack
              direction="row"
              spacing={1}
              alignItems="center"
              sx={{ px: 2, py: 1, bgcolor: 'action.selected', borderBottom: '1px solid', borderColor: 'divider' }}
            >
              <Typography variant="body2" sx={{ fontWeight: 600 }}>
                {selectedCandIds.size} selected
              </Typography>
              <Box sx={{ flexGrow: 1 }} />
              <Button size="small" variant="outlined" disabled={bulkBusy}
                onClick={() => bulkApply('keep-a')}>
                Keep A on {selectedCandIds.size}
              </Button>
              <Button size="small" variant="outlined" disabled={bulkBusy}
                onClick={() => bulkApply('keep-b')}>
                Keep B on {selectedCandIds.size}
              </Button>
              <Button size="small" variant="outlined" color="warning" disabled={bulkBusy}
                onClick={() => bulkApply('dismiss')}>
                Dismiss {selectedCandIds.size}
              </Button>
              <Button size="small" variant="text" disabled={bulkBusy}
                onClick={() => setSelectedCandIds(new Set())}>
                Clear
              </Button>
            </Stack>
          )}
          <Table size="small">
            <TableHead>
              <TableRow>
                <TableCell padding="checkbox">
                  <Checkbox
                    size="small"
                    indeterminate={selectedCandIds.size > 0 && selectedCandIds.size < candidates.length}
                    checked={candidates.length > 0 && selectedCandIds.size === candidates.length}
                    onChange={(e) => {
                      if (e.target.checked) {
                        setSelectedCandIds(new Set(candidates.map((c) => c.id)));
                      } else {
                        setSelectedCandIds(new Set());
                      }
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
                const selected = selectedCandIds.has(c.id);
                return (
                  <TableRow
                    key={c.id}
                    hover
                    selected={selected}
                    sx={{ opacity: busy ? 0.5 : 1 }}
                  >
                    <TableCell padding="checkbox">
                      <Checkbox
                        size="small"
                        checked={selected}
                        onChange={(e) => {
                          setSelectedCandIds((prev) => {
                            const next = new Set(prev);
                            if (e.target.checked) next.add(c.id);
                            else next.delete(c.id);
                            return next;
                          });
                        }}
                      />
                    </TableCell>
                    <TableCell sx={{ verticalAlign: 'top', minWidth: 280 }}>
                      <Stack spacing={0.5}>
                        {renderBookCell(c.entity_a_id)}
                        <Stack direction="row" spacing={0.5} flexWrap="wrap" useFlexGap>
                          {qualityChip(qA)}
                          {recommendA && <Chip label="★ Recommended keep" size="small" color="primary" />}
                        </Stack>
                      </Stack>
                    </TableCell>
                    <TableCell sx={{ verticalAlign: 'top', minWidth: 280 }}>
                      <Stack spacing={0.5}>
                        {renderBookCell(c.entity_b_id)}
                        <Stack direction="row" spacing={0.5} flexWrap="wrap" useFlexGap>
                          {qualityChip(qB)}
                          {recommendB && <Chip label="★ Recommended keep" size="small" color="primary" />}
                        </Stack>
                      </Stack>
                    </TableCell>
                    <TableCell align="center">
                      <Chip label={simPct(c)} size="small" color={(c.similarity ?? 0) >= 0.9 ? 'error' : 'warning'} />
                    </TableCell>
                    <TableCell>
                      <Stack direction="row" spacing={0.5} flexWrap="wrap" useFlexGap>
                        <Tooltip title="Keep Book A, merge B into it">
                          <Button size="small" variant={recommendA ? 'contained' : 'outlined'} color="primary"
                            disabled={busy} onClick={() => handleMerge(c.id, c.entity_a_id)}>
                            Keep A
                          </Button>
                        </Tooltip>
                        <Tooltip title="Keep Book B, merge A into it">
                          <Button size="small" variant={recommendB ? 'contained' : 'outlined'} color="primary"
                            disabled={busy} onClick={() => handleMerge(c.id, c.entity_b_id)}>
                            Keep B
                          </Button>
                        </Tooltip>
                        <Tooltip title="Compare fingerprint segments side-by-side">
                          <Button size="small" variant="outlined" startIcon={<GraphicEqIcon />}
                            onClick={() => { setCompareA(c.entity_a_id); setCompareB(c.entity_b_id); setShowComparePanel(true); }}>
                            Compare
                          </Button>
                        </Tooltip>
                        <Tooltip title="Not a duplicate — dismiss">
                          <Button size="small" variant="text" color="inherit" disabled={busy}
                            onClick={() => handleDismiss(c.id)}>
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
          <TablePagination
            component="div"
            count={total}
            page={page}
            onPageChange={(_, p) => { setPage(p); setSelectedCandIds(new Set()); }}
            rowsPerPage={rowsPerPage}
            onRowsPerPageChange={(e) => { setRowsPerPage(parseInt(e.target.value, 10)); setPage(0); setSelectedCandIds(new Set()); }}
            rowsPerPageOptions={[25, 50, 100, 250]}
          />
        </Paper>
      )}

      <Box sx={{ mt: 3 }} ref={showComparePanel ? comparePanelRef : undefined}>
        <AcousticComparePanel initialA={compareA} initialB={compareB} />
      </Box>
    </Box>
  );
}
