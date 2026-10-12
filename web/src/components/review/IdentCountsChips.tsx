// file: web/src/components/review/IdentCountsChips.tsx
// version: 1.0.0
// guid: 37a831ee-09bb-4ffc-9928-cdb1e49ca3ca
// last-edited: 2026-10-11

/**
 * The identification state counts (`ident_counts` in the review summary), as
 * links into the Library: every count opens the Library with the matching
 * `ident:` search applied, so the number shown here and the "Select all N
 * items" the Library shows for it are the same count (both are the server's
 * identification index over the Library's default view).
 *
 * While the index is being built (`ready` false) the counts are zeros that
 * mean nothing, so a single "counting" chip is shown instead of them.
 */

import { Chip, Stack, Tooltip, Typography } from '@mui/material';
import { Link as RouterLink } from 'react-router-dom';
import type { IdentCounts } from '../../services/api';

/** One chip per `ident:` value, in the order the rail shows them. */
export const IDENT_CHIPS: ReadonlyArray<{
  value: keyof Omit<IdentCounts, 'ready'>;
  label: string;
  help: string;
}> = [
  {
    value: 'missing',
    label: 'missing metadata',
    help: 'Never asked about, or asked and nothing found (unasked + asked empty). Fetched suggestions waiting for review do not count as missing.',
  },
  {
    value: 'unasked',
    label: 'never asked',
    help: 'No provider lookup on record (no candidate cache row).',
  },
  {
    value: 'asked_empty',
    label: 'asked, nothing found',
    help: 'Looked up; the providers had no candidate.',
  },
  {
    value: 'has_candidates',
    label: 'with candidates',
    help: 'Candidates fetched that nobody has applied yet.',
  },
  {
    value: 'applied',
    label: 'applied',
    help: 'Metadata applied (review status matched or audio confirmed).',
  },
  { value: 'held', label: 'held', help: 'Ruled "no match".' },
];

/** The Library URL that lists the books an `ident:` value selects. */
export function identLibraryHref(value: string): string {
  return `/library?search=${encodeURIComponent(`ident:${value}`)}`;
}

export function IdentCountsChips({ counts }: { counts: IdentCounts }) {
  return (
    <Stack spacing={0.5} data-testid="ident-counts">
      <Typography variant="caption" color="text.secondary">
        Identification (Library default view)
      </Typography>
      <Stack direction="row" spacing={0.5} useFlexGap sx={{ flexWrap: 'wrap' }}>
        {!counts.ready ? (
          <Tooltip title="The identification index is being built; the counts appear when it finishes.">
            <Chip
              size="small"
              variant="outlined"
              label="counting…"
              data-testid="ident-chip-building"
            />
          </Tooltip>
        ) : (
          IDENT_CHIPS.map(({ value, label, help }) => {
            const n = counts[value] ?? 0;
            return (
              <Tooltip key={value} title={`${help} Click to list them in the Library.`}>
                <Chip
                  size="small"
                  variant="outlined"
                  color={value === 'missing' ? 'warning' : 'default'}
                  clickable
                  component={RouterLink}
                  to={identLibraryHref(value)}
                  label={`${n.toLocaleString()} ${label}`}
                  aria-label={`Open the ${n.toLocaleString()} ${label} books in the Library (ident:${value})`}
                  data-testid={`ident-chip-${value}`}
                />
              </Tooltip>
            );
          })
        )}
      </Stack>
    </Stack>
  );
}
