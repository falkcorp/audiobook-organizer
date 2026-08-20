// file: web/src/components/audiobooks/AudiobookGrid.tsx
// version: 1.8.2
// guid: 9b0c1d2e-3f4a-5b6c-7d8e-9f0a1b2c3d4e
// last-edited: 2026-08-19

import React from 'react';
import { Grid, Box, Typography } from '@mui/material';
import { AudiobookCard } from './AudiobookCard';
import { LoadingWithCancel } from './LoadingWithCancel';
import type { Audiobook } from '../../types';
import type { ColumnDefinition } from '../../config/columnDefinitions';

interface AudiobookGridProps {
  audiobooks: Audiobook[];
  loading?: boolean;
  onCancelLoad?: () => void;
  onEdit?: (audiobook: Audiobook) => void;
  onDelete?: (audiobook: Audiobook) => void;
  onClick?: (audiobook: Audiobook) => void;
  onVersionManage?: (audiobook: Audiobook) => void;
  onFetchMetadata?: (audiobook: Audiobook) => void;
  onParseWithAI?: (audiobook: Audiobook) => void;
  selectedIds?: Set<string>;
  onToggleSelect?: (audiobook: Audiobook, event?: React.MouseEvent) => void;
  columns?: ColumnDefinition[];
  visibleColumnIds?: string[];
}

export const AudiobookGrid: React.FC<AudiobookGridProps> = ({
  audiobooks,
  loading = false,
  onCancelLoad,
  onEdit,
  onDelete,
  onClick,
  onVersionManage,
  onFetchMetadata,
  onParseWithAI,
  selectedIds,
  onToggleSelect,
  columns,
  visibleColumnIds,
}) => {
  if (loading) {
    return <LoadingWithCancel onCancel={onCancelLoad} />;
  }

  if (audiobooks.length === 0) {
    return (
      <Box
        sx={{
          display: 'flex',
          justifyContent: 'center',
          alignItems: 'center',
          minHeight: '400px',
          flexDirection: 'column',
          gap: 2,
        }}
      >
        <Typography
          variant="h6"
          sx={{
            color: 'text.secondary',
          }}
        >
          No audiobooks found
        </Typography>
        <Typography
          variant="body2"
          sx={{
            color: 'text.secondary',
          }}
        >
          Try adjusting your filters or add audiobooks to your library
        </Typography>
      </Box>
    );
  }

  return (
    <Grid container spacing={3}>
      {audiobooks.map((audiobook) => (
        <Grid
          key={audiobook.id}
          sx={{ contentVisibility: 'auto', containIntrinsicSize: '1px 420px' }}
          size={{
            xs: 12,
            sm: 6,
            md: 4,
            lg: 3,
            xl: 2,
          }}
        >
          <AudiobookCard
            audiobook={audiobook}
            onEdit={onEdit}
            onDelete={onDelete}
            onClick={onClick}
            onVersionManage={onVersionManage}
            onFetchMetadata={onFetchMetadata}
            onParseWithAI={onParseWithAI}
            selectable={Boolean(onToggleSelect)}
            selected={selectedIds?.has(audiobook.id) || false}
            onToggleSelect={onToggleSelect}
            columns={columns}
            visibleColumnIds={visibleColumnIds}
          />
        </Grid>
      ))}
    </Grid>
  );
};
