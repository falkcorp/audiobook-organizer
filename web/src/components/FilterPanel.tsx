// file: web/src/components/FilterPanel.tsx
// version: 1.2.2
// guid: 5c7d8e9f-0a1b-2c3d-4e5f-6a7b8c9d0e1f
// last-edited: 2026-08-19

import React from 'react';
import { Box, IconButton, Tooltip } from '@mui/material';
import { Info as InfoIcon } from '@mui/icons-material';
import { SearchBar, ViewMode, type SortOption } from './audiobooks/SearchBar';
import type { ParsedSearch } from '../utils/searchParser';

interface FilterPanelProps {
  searchQuery: string;
  onSearchChange: (value: string) => void;
  onParsedSearchChange?: (parsed: ParsedSearch) => void;
  viewMode: ViewMode;
  onViewModeChange: (mode: ViewMode) => void;
  onLibraryInfoClick: () => void;
  sortBy?: string;
  sortOrder?: 'asc' | 'desc';
  sortOptions?: SortOption[];
  onSortChange?: (sortKey: string, order: 'asc' | 'desc') => void;
}

export const FilterPanel: React.FC<FilterPanelProps> = ({
  searchQuery,
  onSearchChange,
  onParsedSearchChange,
  viewMode,
  onViewModeChange,
  onLibraryInfoClick,
  sortBy,
  sortOrder,
  sortOptions,
  onSortChange,
}) => {
  return (
    <Box
      sx={{
        display: 'flex',
        gap: 1,
        alignItems: 'center',
      }}
    >
      <Box
        sx={{
          flex: 1,
        }}
      >
        <SearchBar
          value={searchQuery}
          onChange={onSearchChange}
          onParsedSearchChange={onParsedSearchChange}
          viewMode={viewMode}
          onViewModeChange={onViewModeChange}
          sortBy={sortBy}
          sortOrder={sortOrder}
          sortOptions={sortOptions}
          onSortChange={onSortChange}
        />
      </Box>
      <Tooltip title="Library info">
        <IconButton onClick={onLibraryInfoClick}>
          <InfoIcon />
        </IconButton>
      </Tooltip>
    </Box>
  );
};
