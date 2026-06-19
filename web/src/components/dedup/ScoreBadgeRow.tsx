// file: web/src/components/dedup/ScoreBadgeRow.tsx
// version: 1.1.0
// guid: c2b3d4e5-f6a7-8901-bcde-cb2345678901
// last-edited: 2026-06-19

// ScoreBadgeRow renders a compact row of band + score chips for a candidate.
// Used inside the candidate table and the comparison drawer header.

import { Chip, Stack, Tooltip } from '@mui/material';
import type { DedupBand } from '../../services/api';
import { BAND_CONFIG } from './BandFilterBar';

interface ScoreBadgeRowProps {
  band?: DedupBand | string;
  score?: number;
  layer?: string;
  similarity?: number;
}

// Non-exact layers are shown since they're distinctive. "exact" is omitted —
// when everything in view is exact it adds no information and wastes space.
const NOTABLE_LAYER_COLORS: Record<string, 'primary' | 'secondary' | 'default'> = {
  embedding: 'primary',
  llm: 'secondary',
};

export function ScoreBadgeRow({ band, score, layer, similarity }: ScoreBadgeRowProps) {
  const bandCfg = band ? BAND_CONFIG[band as DedupBand] : null;

  // Prefer the composite score; fall back to raw similarity percentage.
  const scoreLabel =
    score != null
      ? `${score.toFixed(0)}%`
      : similarity != null
        ? `${(similarity * 100).toFixed(0)}%`
        : null;

  const scoreTooltip =
    score != null
      ? `Composite score: ${score.toFixed(1)} / 100`
      : similarity != null
        ? `Raw similarity: ${(similarity * 100).toFixed(1)}%`
        : '';

  return (
    <Stack direction="row" spacing={0.5} alignItems="center" flexWrap="wrap" useFlexGap>
      {bandCfg && (
        <Tooltip title={bandCfg.description}>
          <Chip label={bandCfg.label} size="small" color={bandCfg.color} variant="filled" />
        </Tooltip>
      )}
      {band && !bandCfg && (
        <Chip label={String(band)} size="small" variant="outlined" />
      )}
      {scoreLabel && (
        <Tooltip title={scoreTooltip}>
          <Chip label={scoreLabel} size="small" variant="outlined" color="default" />
        </Tooltip>
      )}
      {/* Only show layer when it's something non-obvious (not "exact") */}
      {layer && layer !== 'exact' && layer in NOTABLE_LAYER_COLORS && (
        <Chip
          label={layer}
          size="small"
          color={NOTABLE_LAYER_COLORS[layer]}
          variant="outlined"
        />
      )}
    </Stack>
  );
}
