// file: web/src/components/audiobooks/ReadStatusChip.tsx
// version: 1.0.2
// guid: 7c5d6e1f-8a9b-4a70-b8c5-3d7e0f1b9a99

import React, { useCallback, useEffect, useState } from 'react';
import { Chip, Menu, MenuItem, LinearProgress, Box, Typography } from '@mui/material';
import {
  MenuBook as ReadingIcon,
  CheckCircle as FinishedIcon,
  Cancel as AbandonedIcon,
  RadioButtonUnchecked as UnstartedIcon,
} from '@mui/icons-material';
import {
  type ReadStatus,
  type UserBookState,
  READ_STATUS_LABELS,
  READ_STATUS_COLORS,
  getBookState,
  setBookStatus,
  clearBookStatus,
} from '../../services/readingApi';

interface ReadStatusChipProps {
  bookId: string;
  compact?: boolean;
}

const STATUS_ICONS: Record<ReadStatus, React.ReactNode> = {
  unstarted: <UnstartedIcon fontSize="small" />,
  in_progress: <ReadingIcon fontSize="small" />,
  finished: <FinishedIcon fontSize="small" />,
  abandoned: <AbandonedIcon fontSize="small" />,
};

export default function ReadStatusChip({ bookId, compact }: ReadStatusChipProps) {
  const [state, setState] = useState<UserBookState | null>(null);
  const [anchorEl, setAnchorEl] = useState<null | HTMLElement>(null);

  useEffect(() => {
    getBookState(bookId)
      .then(setState)
      .catch(() => {});
  }, [bookId]);

  const status: ReadStatus = (state?.status as ReadStatus) || 'unstarted';
  const progressPct = state?.progress_pct ?? 0;

  const handleClick = useCallback((e: React.MouseEvent<HTMLElement>) => {
    setAnchorEl(e.currentTarget);
  }, []);

  const handleClose = useCallback(() => setAnchorEl(null), []);

  const handleSelect = useCallback(
    async (newStatus: ReadStatus | 'auto') => {
      setAnchorEl(null);
      try {
        if (newStatus === 'auto') {
          const updated = await clearBookStatus(bookId);
          setState(updated);
        } else {
          const updated = await setBookStatus(bookId, newStatus);
          setState(updated);
        }
      } catch {
        // silently fail — chip will show stale state
      }
    },
    [bookId]
  );

  return (
    <>
      <Chip
        icon={STATUS_ICONS[status] as React.ReactElement}
        label={
          compact
            ? undefined
            : status === 'in_progress'
              ? `${progressPct}%`
              : READ_STATUS_LABELS[status]
        }
        size="small"
        onClick={handleClick}
        sx={{
          bgcolor: READ_STATUS_COLORS[status] + '22',
          color: READ_STATUS_COLORS[status],
          borderColor: READ_STATUS_COLORS[status],
          cursor: 'pointer',
        }}
        variant="outlined"
      />
      {status === 'in_progress' && !compact && (
        <Box sx={{ width: 60, ml: 0.5, display: 'inline-flex', alignItems: 'center' }}>
          <LinearProgress
            variant="determinate"
            value={progressPct}
            sx={{ width: '100%', height: 4, borderRadius: 2 }}
          />
        </Box>
      )}
      <Menu anchorEl={anchorEl} open={Boolean(anchorEl)} onClose={handleClose}>
        {(['unstarted', 'in_progress', 'finished', 'abandoned'] as ReadStatus[]).map((s) => (
          <MenuItem key={s} selected={s === status} onClick={() => handleSelect(s)}>
            <Box sx={{ display: 'flex', alignItems: 'center', gap: 1 }}>
              {STATUS_ICONS[s]}
              <Typography variant="body2">{READ_STATUS_LABELS[s]}</Typography>
            </Box>
          </MenuItem>
        ))}
        {state?.status_manual && (
          <MenuItem onClick={() => handleSelect('auto')}>
            <Typography
              variant="body2"
              sx={{
                color: 'text.secondary',
              }}
            >
              Reset to auto-detected
            </Typography>
          </MenuItem>
        )}
      </Menu>
    </>
  );
}
