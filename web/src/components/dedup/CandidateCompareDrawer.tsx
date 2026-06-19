// file: web/src/components/dedup/CandidateCompareDrawer.tsx
// version: 1.2.0
// guid: a6f7b8c9-d0e1-2345-fabc-af6789012345
// last-edited: 2026-06-19

// CandidateCompareDrawer is a right-side Drawer that shows a full side-by-side
// comparison of the two books in a dedup candidate, plus the score breakdown.
// It fetches the breakdown data on open via GET /api/v1/dedup/candidates/:id/breakdown.
// Memory-leak discipline: AbortController on fetch, cleaned up on close/unmount.

import { useEffect, useRef, useState } from 'react';
import {
  Alert,
  Box,
  Button,
  CircularProgress,
  Divider,
  Drawer,
  IconButton,
  Stack,
  Tab,
  Tabs,
  Tooltip,
  Typography,
} from '@mui/material';
import CloseIcon from '@mui/icons-material/Close';
import MergeIcon from '@mui/icons-material/MergeType';
import VisibilityOffIcon from '@mui/icons-material/VisibilityOff';
import OpenInNewIcon from '@mui/icons-material/OpenInNew';
import { useNavigate } from 'react-router-dom';
import * as api from '../../services/api';
import type { AcoustIDCompareResponse, DedupCandidateBreakdownResponse } from '../../services/api';
import { ScoreBadgeRow } from './ScoreBadgeRow';
import { ScoreBreakdownPanel } from './ScoreBreakdownPanel';
import { FileInfoCompare } from './FileInfoCompare';
import { AudioSamplePair } from './AudioSamplePair';
import { FingerprintPair } from './FingerprintCanvas';

interface CandidateCompareDrawerProps {
  /** Candidate ID to load breakdown for, or null to close. */
  candidateId: number | null;
  onClose: () => void;
  /** Called after a successful merge action. */
  onMerged?: (candidateId: number, keepId?: string) => void;
  /** Called after a dismiss action. */
  onDismissed?: (candidateId: number) => void;
}

export function CandidateCompareDrawer({
  candidateId,
  onClose,
  onMerged,
  onDismissed,
}: CandidateCompareDrawerProps) {
  const navigate = useNavigate();
  const [data, setData] = useState<DedupCandidateBreakdownResponse | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [actionLoading, setActionLoading] = useState<string | null>(null);
  const [activeTab, setActiveTab] = useState(0);
  const [fpData, setFpData] = useState<AcoustIDCompareResponse | null>(null);
  const [fpLoading, setFpLoading] = useState(false);
  const abortRef = useRef<AbortController | null>(null);
  const fpAbortRef = useRef<AbortController | null>(null);

  // Fetch breakdown whenever candidateId changes.
  useEffect(() => {
    if (candidateId == null) {
      setData(null);
      setError(null);
      return;
    }

    // Cancel any prior in-flight request.
    abortRef.current?.abort();
    const ctrl = new AbortController();
    abortRef.current = ctrl;

    setLoading(true);
    setError(null);
    setData(null);
    setActiveTab(0);
    setFpData(null);

    api
      .getDedupCandidateBreakdown(candidateId, ctrl.signal)
      .then((resp: DedupCandidateBreakdownResponse) => {
        if (!ctrl.signal.aborted) {
          setData(resp);
        }
      })
      .catch((err: unknown) => {
        if (!ctrl.signal.aborted) {
          setError(err instanceof Error ? err.message : 'Failed to load breakdown');
        }
      })
      .finally(() => {
        if (!ctrl.signal.aborted) {
          setLoading(false);
        }
      });

    return () => {
      ctrl.abort();
    };
  }, [candidateId]);

  // Cleanup on unmount.
  useEffect(() => {
    return () => {
      abortRef.current?.abort();
      fpAbortRef.current?.abort();
    };
  }, []);

  // Lazy-load fingerprint comparison when the Fingerprint tab (index 2) is first opened.
  useEffect(() => {
    const bA = data?.book_a;
    const bB = data?.book_b;
    if (activeTab !== 2 || !bA || !bB || fpData || fpLoading) return;
    fpAbortRef.current?.abort();
    const ctrl = new AbortController();
    fpAbortRef.current = ctrl;
    setFpLoading(true);
    api
      .compareAcoustID(bA.id, bB.id)
      .then((resp) => { if (!ctrl.signal.aborted) setFpData(resp); })
      .catch(() => { /* non-critical: fingerprint view stays empty */ })
      .finally(() => { if (!ctrl.signal.aborted) setFpLoading(false); });
    return () => ctrl.abort();
  }, [activeTab, data, fpData, fpLoading]);

  const handleMerge = async (keepId?: string) => {
    if (!candidateId) return;
    const key = keepId ? `merge:${keepId}` : 'merge';
    setActionLoading(key);
    try {
      await api.mergeDedupCandidate(candidateId, keepId);
      onMerged?.(candidateId, keepId);
      onClose();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Merge failed');
    } finally {
      setActionLoading(null);
    }
  };

  const handleDismiss = async () => {
    if (!candidateId) return;
    setActionLoading('dismiss');
    try {
      await api.dismissDedupCandidate(candidateId);
      onDismissed?.(candidateId);
      onClose();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Dismiss failed');
    } finally {
      setActionLoading(null);
    }
  };

  const candidate = data?.candidate;
  const bookA = data?.book_a;
  const bookB = data?.book_b;

  return (
    <Drawer
      anchor="right"
      open={candidateId != null}
      onClose={onClose}
      PaperProps={{ sx: { width: { xs: '100%', sm: 640, md: 780 }, p: 0 } }}
      data-testid="candidate-compare-drawer"
    >
      {/* Header */}
      <Box
        sx={{
          display: 'flex',
          alignItems: 'center',
          px: 2,
          py: 1.5,
          borderBottom: 1,
          borderColor: 'divider',
          gap: 1,
        }}
      >
        <Typography variant="subtitle1" fontWeight={600} sx={{ flex: 1 }}>
          Candidate #{candidateId}
        </Typography>
        {candidate && (
          <ScoreBadgeRow
            band={candidate.band}
            score={candidate.score}
            layer={candidate.layer}
          />
        )}
        <Tooltip title="Close">
          <IconButton size="small" onClick={onClose} aria-label="close drawer">
            <CloseIcon />
          </IconButton>
        </Tooltip>
      </Box>

      {/* Content */}
      <Box sx={{ flex: 1, overflow: 'auto', p: 2 }}>
        {loading && (
          <Box sx={{ display: 'flex', justifyContent: 'center', py: 4 }}>
            <CircularProgress />
          </Box>
        )}
        {error && (
          <Alert severity="error" onClose={() => setError(null)} sx={{ mb: 2 }}>
            {error}
          </Alert>
        )}

        {data && !loading && (
          <>
            {/* Action bar */}
            <Stack direction="row" spacing={1} sx={{ mb: 2 }} flexWrap="wrap" useFlexGap>
              {candidate?.status === 'pending' && (
                <>
                  <Tooltip title="Merge — auto-pick primary">
                    <span>
                      <Button
                        variant="contained"
                        color="primary"
                        size="small"
                        startIcon={
                          actionLoading === 'merge' ? (
                            <CircularProgress size={14} />
                          ) : (
                            <MergeIcon />
                          )
                        }
                        disabled={actionLoading != null}
                        onClick={() => handleMerge()}
                        data-testid="drawer-merge-btn"
                      >
                        Merge
                      </Button>
                    </span>
                  </Tooltip>
                  {bookA && (
                    <Tooltip title={`Keep "${bookA.title}" as primary`}>
                      <span>
                        <Button
                          variant="outlined"
                          size="small"
                          disabled={actionLoading != null}
                          onClick={() => handleMerge(bookA.id)}
                        >
                          Keep A
                        </Button>
                      </span>
                    </Tooltip>
                  )}
                  {bookB && (
                    <Tooltip title={`Keep "${bookB.title}" as primary`}>
                      <span>
                        <Button
                          variant="outlined"
                          size="small"
                          disabled={actionLoading != null}
                          onClick={() => handleMerge(bookB.id)}
                        >
                          Keep B
                        </Button>
                      </span>
                    </Tooltip>
                  )}
                  <Button
                    variant="outlined"
                    color="inherit"
                    size="small"
                    startIcon={
                      actionLoading === 'dismiss' ? (
                        <CircularProgress size={14} />
                      ) : (
                        <VisibilityOffIcon />
                      )
                    }
                    disabled={actionLoading != null}
                    onClick={handleDismiss}
                    data-testid="drawer-dismiss-btn"
                  >
                    Dismiss
                  </Button>
                  {bookA && bookB && (
                    <AudioSamplePair
                      bookA={bookA}
                      bookB={bookB}
                      onKeep={(winnerId: string | undefined) => handleMerge(winnerId)}
                    />
                  )}
                </>
              )}
              {/* Deep-link buttons to book detail pages */}
              {bookA && (
                <Tooltip title={`Open "${bookA.title}" in library`}>
                  <IconButton
                    size="small"
                    onClick={() => navigate(`/library/${bookA.id}`)}
                    aria-label={`Open book A: ${bookA.title}`}
                  >
                    <OpenInNewIcon fontSize="small" />
                  </IconButton>
                </Tooltip>
              )}
            </Stack>

            <Divider sx={{ mb: 2 }} />

            {/* Tabs: Files | Score Breakdown */}
            <Tabs
              value={activeTab}
              onChange={(_: unknown, v: number) => setActiveTab(v)}
              sx={{ mb: 2, borderBottom: 1, borderColor: 'divider' }}
            >
              <Tab label="Files" data-testid="drawer-tab-files" />
              <Tab label="Score Breakdown" data-testid="drawer-tab-breakdown" />
              <Tab label="Fingerprint" data-testid="drawer-tab-fingerprint" />
            </Tabs>

            {activeTab === 0 && bookA && bookB && (
              <FileInfoCompare bookA={bookA} bookB={bookB} />
            )}
            {activeTab === 0 && (!bookA || !bookB) && (
              <Typography color="text.secondary" variant="body2">
                Book details unavailable.
              </Typography>
            )}

            {activeTab === 1 && candidate?.score_breakdown && (
              <ScoreBreakdownPanel breakdown={candidate.score_breakdown} />
            )}
            {activeTab === 1 && !candidate?.score_breakdown && (
              <Typography color="text.secondary" variant="body2" fontStyle="italic">
                No score breakdown available (pre-T015 candidate — run rescore to backfill).
              </Typography>
            )}

            {activeTab === 2 && (
              <Box sx={{ display: 'flex', flexDirection: 'column', alignItems: 'center', gap: 2, pt: 1 }}>
                {fpLoading && <CircularProgress size={32} />}
                {!fpLoading && fpData && fpData.segment_scores.length > 0 && (
                  <FingerprintPair
                    hashA={fpData.segment_scores[0]?.hash_a ?? ''}
                    hashB={fpData.segment_scores[0]?.hash_b ?? ''}
                    width={220}
                    rows={64}
                  />
                )}
                {!fpLoading && !fpData && (
                  <Typography color="text.secondary" variant="body2" fontStyle="italic">
                    No fingerprint data — run acoustid.scan to generate fingerprints first.
                  </Typography>
                )}
              </Box>
            )}
          </>
        )}
      </Box>
    </Drawer>
  );
}
