// file: web/src/components/dedup/BandFilterBar.tsx
// version: 1.0.1
// guid: b1a2c3d4-e5f6-7890-abcd-ba1234567890
// last-edited: 2026-08-19

// BandFilterBar renders four band chips (CERTAIN, HIGH, MEDIUM, REVIEW) with
// per-band candidate counts fetched from /api/v1/dedup/stats. Each chip is
// clickable to filter the candidate table to that band. The "All" chip clears
// the band filter.

import { Box, Chip, Skeleton, Stack, Tooltip } from '@mui/material';
import type { DedupBand } from '../../services/api';

// Band display configuration.
const BAND_CONFIG: Record<
  DedupBand,
  { label: string; color: 'error' | 'warning' | 'info' | 'default'; description: string }
> = {
  CERTAIN: {
    label: 'Certain',
    color: 'error',
    description: 'Score ≥ 97 — auto-merge eligible',
  },
  HIGH: {
    label: 'High',
    color: 'warning',
    description: 'Score 90–97 — suggest merge',
  },
  MEDIUM: {
    label: 'Medium',
    color: 'info',
    description: 'Score 75–90 — review queue',
  },
  REVIEW: {
    label: 'Review',
    color: 'default',
    description: 'Score 60–75 — manual / LLM review',
  },
};

const BAND_ORDER: DedupBand[] = ['CERTAIN', 'HIGH', 'MEDIUM', 'REVIEW'];

export interface BandCounts {
  CERTAIN: number;
  HIGH: number;
  MEDIUM: number;
  REVIEW: number;
  total: number;
}

interface BandFilterBarProps {
  /** Currently selected band filter; null means "all". */
  selected: DedupBand | null;
  /** Counts per band (from /api/v1/dedup/stats or cached). */
  counts: BandCounts | null;
  /** Whether counts are still loading. */
  loading?: boolean;
  /** Called when a band chip is clicked; null clears the filter. */
  onChange: (band: DedupBand | null) => void;
}

export function BandFilterBar({ selected, counts, loading, onChange }: BandFilterBarProps) {
  return (
    <Stack
      direction="row"
      spacing={1}
      useFlexGap
      data-testid="band-filter-bar"
      sx={{
        flexWrap: 'wrap',
        mb: 2,
      }}
    >
      {/* All chip */}
      <Chip
        label={`All${counts != null ? ` (${counts.total})` : ''}`}
        size="small"
        variant={selected == null ? 'filled' : 'outlined'}
        onClick={() => onChange(null)}
        sx={{ cursor: 'pointer' }}
      />

      {BAND_ORDER.map((band) => {
        const cfg = BAND_CONFIG[band];
        const count = counts?.[band] ?? 0;
        return (
          <Tooltip key={band} title={cfg.description}>
            <Box component="span">
              {loading ? (
                <Skeleton variant="rounded" width={80} height={24} />
              ) : (
                <Chip
                  data-testid={`band-chip-${band}`}
                  label={`${cfg.label} (${count})`}
                  size="small"
                  color={cfg.color}
                  variant={selected === band ? 'filled' : 'outlined'}
                  onClick={() => onChange(selected === band ? null : band)}
                  sx={{ cursor: 'pointer' }}
                />
              )}
            </Box>
          </Tooltip>
        );
      })}
    </Stack>
  );
}

export { BAND_ORDER, BAND_CONFIG };
