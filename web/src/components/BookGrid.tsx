// file: web/src/components/BookGrid.tsx
// version: 1.4.0
// guid: 6e7f8a9b-0c1d-2e3f-4a5b-6c7d8e9f0a1b
// last-edited: 2026-07-11

import React from 'react';
import {
  Box,
  Pagination,
} from '@mui/material';
import { AudiobookGrid } from './audiobooks/AudiobookGrid';
import { AudiobookList } from './audiobooks/AudiobookList';
import { ViewMode } from './audiobooks/SearchBar';
import type { Audiobook, FilterOptions, SortOrder as SortOrderType } from '../types';
import { SortOrder } from '../types';
import type { ColumnDefinition } from '../config/columnDefinitions';

interface BookGridProps {
  audiobooks: Audiobook[];
  loading: boolean;
  onCancelLoad?: () => void;
  viewMode: ViewMode;
  page: number;
  totalPages: number;
  itemsPerPage: number;
  onPageChange: (page: number) => void;
  onEdit?: (audiobook: Audiobook) => void;
  onDelete?: (audiobook: Audiobook) => void;
  onClick?: (audiobook: Audiobook) => void;
  onVersionManage?: (audiobook: Audiobook) => void;
  onFetchMetadata?: (audiobook: Audiobook) => void;
  onParseWithAI?: (audiobook: Audiobook) => void;
  selectedIds?: Set<string>;
  onToggleSelect?: (audiobook: Audiobook, event?: React.MouseEvent) => void;
  columns?: ColumnDefinition[];
  columnWidths?: Record<string, number>;
  sortBy?: string;
  sortOrder?: SortOrderType;
  onSortChange?: (sortKey: string, order: 'asc' | 'desc') => void;
  onColumnResize?: (columnId: string, width: number) => void;
  onSelectAll?: () => void;
  visibleColumnIds?: string[];
  onToggleColumn?: (columnId: string) => void;
  onFiltersChange?: (filters: FilterOptions) => void;
}

export const BookGrid: React.FC<BookGridProps> = ({
  audiobooks,
  loading,
  onCancelLoad,
  viewMode,
  page,
  totalPages,
  itemsPerPage,
  onPageChange,
  onEdit,
  onDelete,
  onClick,
  onVersionManage,
  onFetchMetadata,
  onParseWithAI,
  selectedIds,
  onToggleSelect,
  columns,
  columnWidths,
  sortBy,
  sortOrder,
  onSortChange,
  onColumnResize,
  onSelectAll,
  visibleColumnIds,
  onToggleColumn,
  onFiltersChange,
}) => {
  return (
    <>
      {itemsPerPage > 50 && totalPages > 1 && (
        <Box sx={{ display: 'flex', justifyContent: 'center', mb: 1 }}>
          <Pagination
            count={totalPages}
            page={page}
            onChange={(_, value) => onPageChange(value)}
            color="primary"
            siblingCount={3}
            size="small"
          />
        </Box>
      )}
      {viewMode === 'grid' ? (
        <AudiobookGrid
          audiobooks={audiobooks}
          loading={loading}
          onCancelLoad={onCancelLoad}
          onEdit={onEdit}
          onDelete={onDelete}
          onClick={onClick}
          onVersionManage={onVersionManage}
          onFetchMetadata={onFetchMetadata}
          onParseWithAI={onParseWithAI}
          selectedIds={selectedIds}
          onToggleSelect={onToggleSelect}
          columns={columns}
          visibleColumnIds={visibleColumnIds}
        />
      ) : (
        <AudiobookList
          audiobooks={audiobooks}
          loading={loading}
          onCancelLoad={onCancelLoad}
          onEdit={onEdit}
          onDelete={onDelete}
          onClick={onClick}
          selectedIds={selectedIds}
          onToggleSelect={onToggleSelect}
          onSelectAll={onSelectAll}
          columns={columns}
          columnWidths={columnWidths}
          sortBy={sortBy}
          sortOrder={sortOrder === SortOrder.Ascending ? 'asc' : 'desc'}
          onSortChange={onSortChange}
          onColumnResize={onColumnResize}
          visibleColumnIds={visibleColumnIds}
          onToggleColumn={onToggleColumn}
          onFiltersChange={onFiltersChange}
        />
      )}
    </>
  );
};
