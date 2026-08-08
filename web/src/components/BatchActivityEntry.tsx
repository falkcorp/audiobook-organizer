// file: web/src/components/BatchActivityEntry.tsx
// version: 1.1.1
// guid: 7e3a1f9c-4b82-4d5e-a6c8-2f0d8e7b3a91

import React, { useState } from 'react';
import {
  Box,
  Chip,
  Collapse,
  IconButton,
  TableCell,
  TableRow,
  Tooltip,
  Typography,
  useMediaQuery,
  useTheme,
} from '@mui/material';
import ExpandMoreIcon from '@mui/icons-material/ExpandMore';
import ChevronRightIcon from '@mui/icons-material/ChevronRight';
import type { ActivityEntry } from '../services/activityApi';

// ---- Local types -------------------------------------------------------

interface BatchItem {
  name: string;
  count: number;
  detail?: string;
}

interface BatchDetails {
  batched: true;
  batch_key: string;
  items: BatchItem[];
  window_start: string;
  window_end: string;
  original_count: number;
  truncated?: boolean;
}

// ---- Props -------------------------------------------------------------

interface BatchActivityEntryProps {
  /** The ActivityEntry row emitted by the backend with details.batched === true */
  entry: ActivityEntry;
  /** Hex color string for the tier indicator dot */
  tierColor: string;
}

// ---- Helpers -----------------------------------------------------------

function formatTimestamp(ts: string): string {
  try {
    return new Date(ts).toLocaleString(undefined, {
      month: 'short',
      day: '2-digit',
      hour: '2-digit',
      minute: '2-digit',
      second: '2-digit',
    });
  } catch {
    return ts;
  }
}

// ---- Component ---------------------------------------------------------

export function BatchActivityEntry({ entry, tierColor }: BatchActivityEntryProps) {
  const [expanded, setExpanded] = useState(false);
  const theme = useTheme();
  const isMobile = useMediaQuery(theme.breakpoints.down('sm'));
  const details = entry.details as unknown as BatchDetails;

  // Safely cap rendered items at 200
  const visibleItems = details?.items?.slice(0, 200) ?? [];
  const hiddenCount =
    details?.original_count != null && details.original_count > visibleItems.length
      ? details.original_count - visibleItems.length
      : 0;

  return (
    <React.Fragment>
      {/* ---- Header row ---- */}
      <TableRow
        hover
        sx={{ cursor: 'pointer', bgcolor: 'rgba(25, 118, 210, 0.04)' }}
        onClick={() => setExpanded((e) => !e)}
      >
        {/* 1. Expand/collapse icon */}
        <TableCell sx={{ whiteSpace: 'nowrap', color: 'text.secondary', fontSize: '0.75rem' }}>
          <Box sx={{ display: 'flex', alignItems: 'center', gap: 0.5 }}>
            <IconButton
              size="small"
              sx={{ p: 0 }}
              aria-label={expanded ? 'Collapse batch' : 'Expand batch'}
              onClick={(e) => { e.stopPropagation(); setExpanded((v) => !v); }}
            >
              {expanded ? (
                <ExpandMoreIcon fontSize="small" />
              ) : (
                <ChevronRightIcon fontSize="small" />
              )}
            </IconButton>
            {/* Tier colored dot */}
            <Box
              component="span"
              sx={{
                display: 'inline-block',
                width: 8,
                height: 8,
                borderRadius: '50%',
                bgcolor: tierColor,
                flexShrink: 0,
              }}
            />
            {/* Timestamp with staleness tooltip */}
            <Tooltip title="Batch entries may be up to 15 seconds old" placement="top">
              <span>{formatTimestamp(entry.timestamp)}</span>
            </Tooltip>
          </Box>
        </TableCell>

        {/* 2. Level chip placeholder — batched entries show a "batch" chip */}
        <TableCell>
          <Chip
            size="small"
            label="batch"
            sx={{ bgcolor: tierColor, color: 'white', opacity: 0.85 }}
          />
        </TableCell>

        {/* 3. Source chip */}
        <TableCell>
          {entry.source ? (
            <Chip size="small" label={entry.source} variant="outlined" />
          ) : null}
        </TableCell>

        {/* 4. Summary (spans wider column) */}
        <TableCell sx={isMobile ? { wordBreak: 'break-word', minWidth: 0 } : {}}>
          <Typography variant="body2" noWrap={!isMobile} title={entry.summary}>
            {entry.summary}
          </Typography>
        </TableCell>

        {/* 5. Item count chip + type info (desktop source column) */}
        <TableCell>
          <Chip
            size="small"
            label={`${details?.original_count ?? visibleItems.length} items`}
            variant="outlined"
          />
        </TableCell>

        {/* 6. Tags column — empty for batch rows */}
        <TableCell />

        {/* 7. Action column — empty for batch rows */}
        <TableCell />
      </TableRow>

      {/* ---- Expanded detail row ---- */}
      <TableRow>
        <TableCell
          colSpan={7}
          sx={{
            py: 0,
            borderBottom: expanded ? undefined : 'none',
          }}
        >
          <Collapse in={expanded} timeout="auto" unmountOnExit>
            <Box sx={{ py: 1, pl: 4 }}>
              {visibleItems.map((item, idx) => (
                <Typography
                  key={idx}
                  variant="body2"
                  sx={{ py: 0.25 }}
                >
                  {idx + 1}.{' '}
                  <strong>{item.name}</strong>
                  {' — '}
                  {item.count === 1 ? '1 item' : `${item.count} items`}
                  {item.detail ? ` (${item.detail})` : ''}
                </Typography>
              ))}
              {(details?.truncated || hiddenCount > 0) && (
                <Typography
                  variant="caption"
                  color="text.secondary"
                  sx={{ pt: 0.5, display: 'block' }}
                >
                  … and {hiddenCount > 0 ? hiddenCount : (details?.original_count ?? 0) - visibleItems.length} more not shown
                </Typography>
              )}
            </Box>
          </Collapse>
        </TableCell>
      </TableRow>
    </React.Fragment>
  );
}
