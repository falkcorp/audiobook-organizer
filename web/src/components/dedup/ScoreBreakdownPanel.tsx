// file: web/src/components/dedup/ScoreBreakdownPanel.tsx
// version: 1.1.0
// guid: d3c4e5f6-a7b8-9012-cdef-dc3456789012
// last-edited: 2026-08-10
// ScoreBreakdownPanel renders a stacked bar visualization of the per-signal
// score contributions for a dedup candidate. Signal contributions are
// computed client-side from the stored signal values and weights.

import { Box, Chip, Stack, Tooltip, Typography } from '@mui/material';
import type { DedupScoreBreakdown, DedupSignal } from '../../services/api';

// Human-friendly label map for signal kinds.
const SIGNAL_LABELS: Record<string, string> = {
  exact_file: 'Exact file hash',
  exact_acoustid: 'Exact AcoustID',
  isbn_asin: 'ISBN/ASIN',
  lsh_acoustid: 'LSH AcoustID',
  embedding_high: 'Embedding (high)',
  metadata_hash: 'Metadata hash',
  metadata_fuzzy: 'Metadata fuzzy',
  embedding_med: 'Embedding (medium)',
  duration: 'Duration match',
  folder_path: 'Folder path',
};

const SIGNAL_COLORS: Record<string, string> = {
  exact_file: '#d32f2f',
  exact_acoustid: '#c62828',
  isbn_asin: '#f57c00',
  lsh_acoustid: '#e65100',
  embedding_high: '#1565c0',
  metadata_hash: '#558b2f',
  metadata_fuzzy: '#33691e',
  embedding_med: '#0277bd',
  duration: '#6a1b9a',
  folder_path: '#4a148c',
};

interface ScoreBreakdownPanelProps {
  breakdown: DedupScoreBreakdown;
}

/** Compute each signal's contribution share (0–1) relative to total weight. */
function computeContributions(signals: DedupSignal[]): Array<DedupSignal & { share: number }> {
  const totalWeight = signals.reduce((sum, s) => sum + Math.max(0, s.weight), 0);
  return signals.map((s) => ({
    ...s,
    share: totalWeight > 0 ? Math.max(0, s.weight) / totalWeight : 0,
  }));
}

export function ScoreBreakdownPanel({ breakdown }: ScoreBreakdownPanelProps) {
  if (!breakdown.signals || breakdown.signals.length === 0) {
    return (
      <Box sx={{ p: 1 }}>
        <Typography variant="body2" color="text.secondary" fontStyle="italic">
          {breakdown.skipped_reason ?? 'No signal data available (pre-pipeline candidate).'}
        </Typography>
      </Box>
    );
  }

  const withShares = computeContributions(breakdown.signals);

  return (
    <Box data-testid="score-breakdown-panel">
      {/* Score + band header */}
      <Stack direction="row" spacing={1} alignItems="center" sx={{ mb: 1.5 }}>
        <Typography variant="subtitle2" fontWeight={700}>
          Score: {breakdown.score.toFixed(1)}
        </Typography>
        <Chip
          label={breakdown.band}
          size="small"
          color={
            breakdown.band === 'CERTAIN'
              ? 'error'
              : breakdown.band === 'HIGH'
              ? 'warning'
              : breakdown.band === 'MEDIUM'
              ? 'info'
              : 'default'
          }
        />
        {breakdown.formula && (
          <Typography variant="caption" color="text.disabled" sx={{ ml: 'auto' }}>
            {breakdown.formula}
          </Typography>
        )}
      </Stack>

      {/* Stacked bar */}
      <Tooltip
        title={
          <Box>
            {withShares.map((s) => (
              <Typography key={s.kind} variant="caption" display="block">
                {SIGNAL_LABELS[s.kind] ?? s.kind}: {(s.share * 100).toFixed(1)}%
              </Typography>
            ))}
          </Box>
        }
        placement="bottom"
      >
        <Box
          sx={{
            display: 'flex',
            height: 16,
            borderRadius: 1,
            overflow: 'hidden',
            mb: 1.5,
            bgcolor: 'action.disabledBackground',
          }}
          data-testid="score-stacked-bar"
        >
          {withShares.map((s) => (
            <Box
              key={s.kind}
              sx={[{
                width: `${s.share * 100}%`,
                bgcolor: SIGNAL_COLORS[s.kind] ?? '#9e9e9e'
              }, s.share > 0 ? {
                minWidth: 2
              } : {
                minWidth: 0
              }]}
            />
          ))}
        </Box>
      </Tooltip>

      {/* Signal rows */}
      <Stack spacing={0.75}>
        {withShares.map((s) => (
          <Tooltip
            key={s.kind}
            title={s.evidence || s.kind}
            placement="left"
          >
            <Stack direction="row" spacing={1} alignItems="center">
              <Box
                sx={{
                  width: 10,
                  height: 10,
                  borderRadius: '2px',
                  bgcolor: SIGNAL_COLORS[s.kind] ?? '#9e9e9e',
                  flexShrink: 0,
                }}
              />
              <Typography variant="caption" sx={{ flex: 1, minWidth: 0 }} noWrap>
                {SIGNAL_LABELS[s.kind] ?? s.kind}
              </Typography>
              <Typography
                variant="caption"
                color="text.secondary"
                sx={{ fontVariantNumeric: 'tabular-nums', flexShrink: 0 }}
              >
                {(s.value * 100).toFixed(0)}%
              </Typography>
              <Typography
                variant="caption"
                color="text.disabled"
                sx={{ fontVariantNumeric: 'tabular-nums', flexShrink: 0, minWidth: 36, textAlign: 'right' }}
              >
                w={s.weight.toFixed(2)}
              </Typography>
            </Stack>
          </Tooltip>
        ))}
      </Stack>
    </Box>
  );
}
