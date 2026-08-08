// file: web/src/components/common/FilterTagBar.tsx
// version: 1.0.1
// guid: 7c4f8d12-3b6e-4a5c-9d1e-8f2a3b4c5d6e
// last-edited: 2026-08-07

import { Box, Button, Chip, Stack, Typography } from '@mui/material';
import CloseIcon from '@mui/icons-material/Close';

// Avoid importing the full ChipPropsColorOverrides surface — these are
// the MUI palette names we actually use across the app for filter chips.
export type FilterChipColor =
  | 'default'
  | 'primary'
  | 'secondary'
  | 'error'
  | 'info'
  | 'success'
  | 'warning';

export interface FilterTag {
  /** Stable id for React keying and dedupe; usually `${field}:${value}`. */
  id: string;
  /** Human-readable label shown on the chip. */
  label: string;
  /** Optional MUI palette color; default `'default'`. */
  color?: FilterChipColor;
  /** Removes the filter when the chip's X is clicked. */
  onRemove: () => void;
}

interface FilterTagBarProps {
  tags: FilterTag[];
  /** Optional "Clear all" handler — when omitted, the button is hidden. */
  onClearAll?: () => void;
  /** Hide the prefix label "Filters:" (use when the surrounding layout already labels it). */
  hideLabel?: boolean;
  /** Override the prefix label text (default "Filters:"). */
  label?: string;
}

/**
 * EC2-style active-filter strip. Renders an array of currently-applied
 * filters as removable Chips with X icons and an optional "Clear all"
 * button. Renders nothing when `tags` is empty so it doesn't reserve
 * empty visual space.
 *
 * Pattern: pages own filter state and derive a FilterTag[] from it on
 * each render. Clicking the X calls `onRemove`, which the page wires
 * to the appropriate setState (e.g. `setStatusFilter('')`). This keeps
 * the component stateless — there is one source of truth (the page's
 * filter state) and the bar is a pure projection of it.
 */
export function FilterTagBar({
  tags,
  onClearAll,
  hideLabel,
  label = 'Filters:',
}: FilterTagBarProps) {
  if (tags.length === 0) {
    return null;
  }

  const showClearAll = onClearAll && tags.length >= 2;

  return (
    <Box
      sx={{
        mb: 2,
        py: 1,
        px: 1.5,
        borderRadius: 1,
        bgcolor: 'action.hover',
        display: 'flex',
        alignItems: 'center',
        gap: 1,
        flexWrap: 'wrap',
      }}
    >
      {!hideLabel && (
        <Typography variant="caption" color="text.secondary" sx={{ fontWeight: 600 }}>
          {label}
        </Typography>
      )}
      <Stack direction="row" spacing={0.75} flexWrap="wrap" useFlexGap sx={{ flex: 1 }}>
        {tags.map((tag) => (
          <Chip
            key={tag.id}
            label={tag.label}
            size="small"
            color={tag.color ?? 'default'}
            onDelete={tag.onRemove}
            deleteIcon={<CloseIcon />}
          />
        ))}
      </Stack>
      {showClearAll && (
        <Button
          size="small"
          onClick={onClearAll}
          sx={{ textTransform: 'none', fontSize: '0.75rem' }}
        >
          Clear all
        </Button>
      )}
    </Box>
  );
}
