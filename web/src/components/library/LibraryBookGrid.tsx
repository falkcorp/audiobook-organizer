// file: web/src/components/library/LibraryBookGrid.tsx
// version: 1.5.2
// guid: c3d4e5f6-a7b8-9012-cdef-123456789012
// last-edited: 2026-06-28

import {
  Typography,
  Box,
  Button,
  Stack,
  Chip,
  Paper,
  Alert,
  AlertTitle,
  TextField,
  FormControlLabel,
  Checkbox,
  MenuItem,
  Pagination,
  CircularProgress,
} from '@mui/material';
import {
  Upload as UploadIcon,
  FolderOpen as FolderOpenIcon,
  Add as AddIcon,
  Refresh as RefreshIcon,
} from '@mui/icons-material';
import { FilterPanel } from '../FilterPanel';
import { BookGrid } from '../BookGrid';
import { FilterSidebar } from '../audiobooks/FilterSidebar';
import type { LibrarySoftDeletedSectionProps } from './LibrarySoftDeletedSection';
import { LibrarySoftDeletedSection } from './LibrarySoftDeletedSection';
import type { ColumnDefinition } from '../../config/columnDefinitions';
import type { Audiobook, FilterOptions, SortField, SortOrder } from '../../types';
import type { ViewMode } from '../audiobooks/SearchBar';
import type { ParsedSearch } from '../../utils/searchParser';
import type { ImportPath } from '../../pages/libraryTypes';
import { STORAGE_KEYS } from '../../lib/storageKeys';

interface LibraryBookGridProps {
  audiobooks: Audiobook[];
  loading: boolean;
  searchQuery: string;
  setSearchQuery: (q: string) => void;
  setParsedSearch: (p: ParsedSearch) => void;
  viewMode: ViewMode;
  setViewMode: (m: ViewMode) => void;
  sortBy: SortField;
  handleSortChange: (s: SortField) => void;
  sortOrder: SortOrder;
  setSortOrder: (o: SortOrder) => void;
  setStorageDrawerOpen: (open: boolean) => void;
  importPaths: ImportPath[];
  handleManualImport: () => void;
  setAddPathDialogOpen: (open: boolean) => void;
  handleScanAll: () => void;
  scanningAll: boolean;
  page: number;
  setPage: (n: number) => void;
  totalPages: number;
  totalCount: number;
  itemsPerPage: number;
  setItemsPerPage: (n: number) => void;
  allOnPageSelected: boolean;
  someOnPageSelected: boolean;
  handleToggleSelectAllOnPage: () => void;
  hasSelection: boolean;
  effectiveSelectedCount: number;
  handleClearSelection: () => void;
  showSelectAllBanner: boolean;
  handleSelectAllItems: () => void;
  handleEdit: (book: Audiobook) => void;
  handleDelete: (book: Audiobook) => void;
  handleClick: (book: Audiobook) => void;
  handleVersionManage: (book: Audiobook) => void;
  handleFetchMetadata: (book: Audiobook) => void;
  handleParseWithAI: (book: Audiobook) => void;
  selectedIds: Set<string>;
  handleToggleSelect: (book: Audiobook, event?: React.MouseEvent) => void;
  columnDefs: ColumnDefinition[];
  columnWidths: Record<string, number>;
  handleColumnSortChange: (key: string, order: 'asc' | 'desc') => void;
  resizeColumn: (id: string, width: number) => void;
  visibleColumnIds?: string[];
  onToggleColumn?: (columnId: string) => void;
  softDeletedCount: number;
  softDeletedBooks: Audiobook[];
  softDeletedLoading: boolean;
  softDeletedExpanded: boolean;
  restoringBookId: string | null;
  purgeInProgress: boolean;
  purgingBookId: string | null;
  onToggleSoftDeletedExpanded: () => void;
  loadSoftDeleted: () => void;
  handleRestoreOne: LibrarySoftDeletedSectionProps['onRestoreOne'];
  handlePurgeOne: LibrarySoftDeletedSectionProps['onPurgeOne'];
  filterOpen: boolean;
  setFilterOpen: (open: boolean) => void;
  filters: FilterOptions;
  handleFiltersChange: (f: FilterOptions) => void;
  availableAuthors: string[];
  availableSeries: string[];
  availableGenres: string[];
  availableLanguages: string[];
  availableTags: Array<{ tag: string; count: number }>;
  selectedTags: string[];
  handleTagFilterChange: (tags: string[]) => void;
}

export const LibraryBookGrid = ({
  audiobooks,
  loading,
  searchQuery,
  setSearchQuery,
  setParsedSearch,
  viewMode,
  setViewMode,
  sortBy,
  handleSortChange: _handleSortChange,
  sortOrder,
  setSortOrder: _setSortOrder,
  setStorageDrawerOpen,
  importPaths,
  handleManualImport,
  setAddPathDialogOpen,
  handleScanAll,
  scanningAll,
  page,
  setPage,
  totalPages,
  totalCount,
  itemsPerPage,
  setItemsPerPage,
  allOnPageSelected,
  someOnPageSelected,
  handleToggleSelectAllOnPage,
  hasSelection,
  effectiveSelectedCount,
  handleClearSelection,
  showSelectAllBanner,
  handleSelectAllItems,
  handleEdit,
  handleDelete,
  handleClick,
  handleVersionManage,
  handleFetchMetadata,
  handleParseWithAI,
  selectedIds,
  handleToggleSelect,
  columnDefs,
  columnWidths,
  handleColumnSortChange,
  resizeColumn,
  visibleColumnIds,
  onToggleColumn,
  softDeletedCount,
  softDeletedBooks,
  softDeletedLoading,
  softDeletedExpanded,
  restoringBookId,
  purgeInProgress,
  purgingBookId,
  onToggleSoftDeletedExpanded,
  loadSoftDeleted,
  handleRestoreOne,
  handlePurgeOne,
  filterOpen,
  setFilterOpen,
  filters,
  handleFiltersChange,
  availableAuthors,
  availableSeries,
  availableGenres,
  availableLanguages,
  availableTags,
  selectedTags,
  handleTagFilterChange,
}: LibraryBookGridProps) => (
  <>
    {audiobooks.length === 0 && !loading && !searchQuery ? (
      <Paper sx={{ p: 4, textAlign: 'center', bgcolor: 'background.default' }}>
        <FolderOpenIcon sx={{ fontSize: 80, color: 'text.secondary', mb: 2 }} />
        <Alert severity="info" sx={{ textAlign: 'center' }}>
          <AlertTitle>No Audiobooks Found</AlertTitle>
          {importPaths.length === 0 ? (
            <>
              You haven't added any import paths yet. Get started by:
              <ul style={{ marginTop: 8, marginBottom: 0, textAlign: 'left' }}>
                <li>
                  Importing individual audiobook files using the "Import Files" button below
                </li>
                <li>
                  Adding import paths using the "Add Import Path" button below (watches folders
                  for new files)
                </li>
              </ul>
            </>
          ) : (
            <>
              No audiobooks found in your library. Try:
              <ul style={{ marginTop: 8, marginBottom: 0, textAlign: 'left' }}>
                <li>Scanning your import paths using the "Scan All" button below</li>
                <li>Adding more import paths where audiobooks are located</li>
              </ul>
            </>
          )}
        </Alert>
        <Box sx={{ mt: 3 }}>
          <Button
            variant="contained"
            size="large"
            startIcon={<UploadIcon />}
            onClick={handleManualImport}
            sx={{ mr: 2 }}
          >
            Import Files
          </Button>
          <Button
            variant="outlined"
            size="large"
            startIcon={<AddIcon />}
            onClick={() => setAddPathDialogOpen(true)}
            sx={{ mr: 2 }}
          >
            Add Import Path
          </Button>
          {importPaths.length > 0 && (
            <Button
              variant="outlined"
              size="large"
              startIcon={scanningAll ? <CircularProgress size={20} /> : <RefreshIcon />}
              onClick={handleScanAll}
              disabled={scanningAll}
            >
              {scanningAll ? 'Scanning...' : 'Scan All'}
            </Button>
          )}
        </Box>
      </Paper>
    ) : (
      <Stack spacing={1.5}>
        <FilterPanel
          searchQuery={searchQuery}
          onSearchChange={setSearchQuery}
          onParsedSearchChange={setParsedSearch}
          viewMode={viewMode}
          onViewModeChange={setViewMode}
          onLibraryInfoClick={() => setStorageDrawerOpen(true)}
        />

        {/* Select All bar — always visible */}
        <Stack direction="row" spacing={1} alignItems="center" sx={{ px: 0.5, mt: -0.5, mb: -1 }}>
          <FormControlLabel
            control={
              <Checkbox
                checked={allOnPageSelected}
                indeterminate={someOnPageSelected && !allOnPageSelected}
                onChange={handleToggleSelectAllOnPage}
                size="small"
              />
            }
            label={<Typography variant="body2" color="text.secondary">Select All</Typography>}
          />
          {hasSelection && (
            <>
              <Chip label={`${effectiveSelectedCount.toLocaleString()} selected`} size="small" color="primary" />
              <Button size="small" variant="text" onClick={handleClearSelection}>Deselect</Button>
            </>
          )}
        </Stack>

        {/* Gmail-style "Select all X items" banner */}
        {showSelectAllBanner && (
          <Box
            sx={{
              py: 0.75,
              px: 2,
              bgcolor: 'action.selected',
              borderRadius: 1,
              textAlign: 'center',
              mb: 0.5,
            }}
          >
            <Typography variant="body2" component="span">
              All {audiobooks.length} items on this page are selected.{' '}
            </Typography>
            <Button
              size="small"
              variant="text"
              sx={{ textTransform: 'none', fontWeight: 'bold' }}
              onClick={() => handleSelectAllItems()}
            >
              {`Select all ${totalCount.toLocaleString()} items`}
            </Button>
          </Box>
        )}

        {/* Banner when all items are selected across pages */}
        {effectiveSelectedCount >= totalCount && totalCount > audiobooks.length && (
          <Box
            sx={{
              py: 0.75,
              px: 2,
              bgcolor: 'action.selected',
              borderRadius: 1,
              textAlign: 'center',
              mb: 0.5,
            }}
          >
            <Typography variant="body2" component="span">
              All {totalCount.toLocaleString()} items are selected.{' '}
            </Typography>
            <Button
              size="small"
              variant="text"
              sx={{ textTransform: 'none' }}
              onClick={handleClearSelection}
            >
              Clear selection
            </Button>
          </Box>
        )}

        {audiobooks.length === 0 && !loading && searchQuery ? (
          <Paper sx={{ p: 4, textAlign: 'center' }}>
            <Typography variant="h6" color="text.secondary">
              No results for "{searchQuery}"
            </Typography>
            <Typography variant="body2" color="text.secondary" sx={{ mt: 1 }}>
              Try a different search term or clear the search to see all books.
            </Typography>
            <Button variant="outlined" sx={{ mt: 2 }} onClick={() => setSearchQuery('')}>
              Clear Search
            </Button>
          </Paper>
        ) : (
          <BookGrid
            audiobooks={audiobooks}
            loading={loading}
            viewMode={viewMode}
            page={page}
            totalPages={totalPages}
            itemsPerPage={itemsPerPage}
            onPageChange={setPage}
            onEdit={handleEdit}
            onDelete={handleDelete}
            onClick={handleClick}
            onVersionManage={handleVersionManage}
            onFetchMetadata={handleFetchMetadata}
            onParseWithAI={handleParseWithAI}
            selectedIds={selectedIds}
            onToggleSelect={handleToggleSelect}
            columns={columnDefs}
            columnWidths={columnWidths}
            sortBy={sortBy}
            sortOrder={sortOrder}
            onSortChange={handleColumnSortChange}
            onColumnResize={resizeColumn}
            onSelectAll={handleToggleSelectAllOnPage}
            visibleColumnIds={visibleColumnIds}
            onToggleColumn={onToggleColumn}
            onFiltersChange={handleFiltersChange}
          />
        )}

        {!loading && (
          <Stack
            direction={{ xs: 'column', sm: 'row' }}
            spacing={2}
            alignItems="center"
            justifyContent="center"
            mt={4}
          >
            <TextField
              select
              size="small"
              label="Items per page"
              value={itemsPerPage}
              onChange={(e) => {
                const val = Number(e.target.value);
                setItemsPerPage(val);
                localStorage.setItem(STORAGE_KEYS.LIBRARY_ITEMS_PER_PAGE, String(val));
              }}
              sx={{ minWidth: 150 }}
            >
              <MenuItem value={20}>20</MenuItem>
              <MenuItem value={50}>50</MenuItem>
              <MenuItem value={100}>100</MenuItem>
              <MenuItem value={250}>250</MenuItem>
              <MenuItem value={500}>500</MenuItem>
              <MenuItem value={1000}>1000</MenuItem>
            </TextField>
            {totalPages > 1 && (
              <Pagination
                count={totalPages}
                page={page}
                onChange={(_, value) => setPage(value)}
                color="primary"
                siblingCount={3}
              />
            )}
          </Stack>
        )}
      </Stack>
    )}

    <LibrarySoftDeletedSection
      softDeletedCount={softDeletedCount}
      softDeletedBooks={softDeletedBooks}
      softDeletedLoading={softDeletedLoading}
      softDeletedExpanded={softDeletedExpanded}
      restoringBookId={restoringBookId}
      purgeInProgress={purgeInProgress}
      purgingBookId={purgingBookId}
      onToggleExpanded={onToggleSoftDeletedExpanded}
      onRefresh={loadSoftDeleted}
      onRestoreOne={handleRestoreOne}
      onPurgeOne={handlePurgeOne}
    />

    <FilterSidebar
      open={filterOpen}
      onClose={() => setFilterOpen(false)}
      filters={filters}
      onFiltersChange={handleFiltersChange}
      authors={availableAuthors}
      series={availableSeries}
      genres={availableGenres}
      languages={availableLanguages}
      availableTags={availableTags}
      selectedTags={selectedTags}
      onTagsChange={handleTagFilterChange}
    />
  </>
);
