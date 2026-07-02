// file: web/src/components/library/LibraryToolbar.tsx
// version: 1.5.0
// guid: b2c3d4e5-f6a7-8901-bcde-f12345678901
// last-edited: 2026-07-01

import { useState } from 'react';
import {
  Typography,
  Box,
  Button,
  Stack,
  Chip,
  LinearProgress,
  CircularProgress,
  Drawer,
  Divider,
  Menu,
  MenuItem,
  ListItemText,
  IconButton,
} from '@mui/material';
import {
  FilterList as FilterListIcon,
  Upload as UploadIcon,
  DriveFolderUpload as ManualImportIcon,
  Delete as DeleteSweepIcon,
  Refresh as RefreshIcon,
  Bookmark as BookmarkIcon,
  Edit as EditIcon,
  Close as CloseIcon,
} from '@mui/icons-material';
import { ColumnChooser } from '../audiobooks/ColumnChooser';
import { BatchToolbar } from '../BatchToolbar';
import type { Audiobook } from '../../types';
import * as api from '../../services/api';
import type { SavedFilterPreset } from '../../services/api';

interface LibraryToolbarProps {
  hasSelection: boolean;
  selectedAudiobooks: Audiobook[];
  batchRestoreInProgress: boolean;
  selectedHasActive: boolean;
  selectedHasDeleted: boolean;
  selectedHasImport: boolean;
  organizeRunning: boolean;
  activeScanOp: api.Operation | null;
  activeOrganizeOp: api.Operation | null;
  storageDrawerOpen: boolean;
  systemStatus: api.SystemStatus | null;
  softDeletedCount: number;
  libraryBookCount: number;
  importBookCount: number;
  librarySizeBytes: number;
  importSizeBytes: number;
  visibleColumnIds: string[];
  toggleColumn: (id: string) => void;
  resetColumnsToDefaults: () => void;
  getActiveFilterCount: () => number;
  savedPresets: SavedFilterPreset[];
  onSaveCurrentAsPreset: () => void;
  onApplyPreset: (preset: SavedFilterPreset) => void;
  onRenamePreset: (preset: SavedFilterPreset) => void;
  onDeletePreset: (preset: SavedFilterPreset) => void;
  onBatchEdit: () => void;
  onFetchReview: () => Promise<void>;
  onFetchAllUnmatched: () => Promise<void>;
  onResumeReview: () => Promise<void>;
  onSearchMetadata: () => void;
  onSaveToFiles: () => void;
  onOrganizeSelected: () => void;
  onMergeAsVersions: () => void;
  onCombineIntoOneBook: () => void;
  onTagClick: () => void;
  onRateClick: () => void;
  onDeleteSelected: () => void;
  onRestoreSelected: () => void;
  onManualImport: () => void;
  onManualPathImport: () => void;
  onFilterOpen: () => void;
  onOrganizeLibrary: () => void;
  onFullRescan: () => void;
  onPurgeOpen: () => void;
  onStorageDrawerClose: () => void;
  navigate: (path: string) => void;
}

export const LibraryToolbar = ({
  hasSelection,
  selectedAudiobooks,
  batchRestoreInProgress,
  selectedHasActive,
  selectedHasDeleted,
  selectedHasImport,
  organizeRunning,
  activeScanOp,
  activeOrganizeOp,
  storageDrawerOpen,
  systemStatus,
  softDeletedCount,
  libraryBookCount,
  importBookCount,
  librarySizeBytes,
  importSizeBytes,
  visibleColumnIds,
  toggleColumn,
  resetColumnsToDefaults,
  getActiveFilterCount,
  savedPresets,
  onSaveCurrentAsPreset,
  onApplyPreset,
  onRenamePreset,
  onDeletePreset,
  onBatchEdit,
  onFetchReview,
  onFetchAllUnmatched,
  onResumeReview,
  onSearchMetadata,
  onSaveToFiles,
  onOrganizeSelected,
  onMergeAsVersions,
  onCombineIntoOneBook,
  onTagClick,
  onRateClick,
  onDeleteSelected,
  onRestoreSelected,
  onManualImport,
  onManualPathImport,
  onFilterOpen,
  onOrganizeLibrary,
  onFullRescan,
  onPurgeOpen,
  onStorageDrawerClose,
  navigate,
}: LibraryToolbarProps) => {
  const [presetsMenuAnchor, setPresetsMenuAnchor] = useState<null | HTMLElement>(null);
  const presetsMenuOpen = Boolean(presetsMenuAnchor);
  const closePresetsMenu = () => setPresetsMenuAnchor(null);

  return (
  <>
    {/* Unified toolbar — sticky, shows library actions or batch actions based on selection */}
    <Box
      sx={{
        p: 1.5,
        mb: 2,
        position: 'sticky',
        top: 0,
        zIndex: 10,
        boxShadow: 2,
        bgcolor: 'background.paper',
        borderRadius: 1,
      }}
    >
      {hasSelection ? (
        /* Batch actions mode */
        <BatchToolbar
          selectedCount={selectedAudiobooks.length}
          onBatchEditClick={onBatchEdit}
          onFetchReviewClick={onFetchReview}
          onResumeReviewClick={onResumeReview}
          onSearchMetadataClick={onSearchMetadata}
          onSaveToFilesClick={onSaveToFiles}
          onOrganizeSelectedClick={onOrganizeSelected}
          onMergeAsVersionsClick={onMergeAsVersions}
          onCombineIntoOneBookClick={onCombineIntoOneBook}
          onTagClick={onTagClick}
          onRateClick={onRateClick}
          onDeleteSelectedClick={onDeleteSelected}
          onRestoreSelectedClick={onRestoreSelected}
          batchRestoreInProgress={batchRestoreInProgress}
          selectedHasActive={selectedHasActive}
          selectedHasDeleted={selectedHasDeleted}
          selectedHasImport={selectedHasImport}
          selectedAudiobooksLength={selectedAudiobooks.length}
        />
      ) : (
        /* Library actions mode */
        <Stack direction="row" spacing={1} alignItems="center" flexWrap="wrap">
          <Typography variant="h6" sx={{ mr: 1 }}>Library</Typography>
          <Button startIcon={<UploadIcon />} onClick={onManualImport} variant="contained" size="small">Import Files</Button>
          <Button startIcon={<ManualImportIcon />} onClick={onManualPathImport} variant="outlined" size="small">Manual import</Button>
          <Button startIcon={<FilterListIcon />} onClick={onFilterOpen} variant="outlined" size="small">
            Filters{getActiveFilterCount() > 0 && <Chip label={getActiveFilterCount()} size="small" color="primary" sx={{ ml: 0.5, height: 18 }} />}
          </Button>
          <Button
            startIcon={<BookmarkIcon />}
            onClick={(e) => setPresetsMenuAnchor(e.currentTarget)}
            variant="outlined"
            size="small"
          >
            Presets{savedPresets.length > 0 && <Chip label={savedPresets.length} size="small" color="primary" sx={{ ml: 0.5, height: 18 }} />}
          </Button>
          <Menu anchorEl={presetsMenuAnchor} open={presetsMenuOpen} onClose={closePresetsMenu}>
            <MenuItem
              onClick={() => {
                closePresetsMenu();
                onSaveCurrentAsPreset();
              }}
            >
              Save current filters as preset...
            </MenuItem>
            {savedPresets.length > 0 && <Divider />}
            {savedPresets.map((preset) => (
              <MenuItem
                key={preset.id}
                onClick={() => {
                  closePresetsMenu();
                  onApplyPreset(preset);
                }}
              >
                <ListItemText>{preset.name}</ListItemText>
                <IconButton
                  size="small"
                  aria-label={`Rename ${preset.name}`}
                  onClick={(e) => {
                    e.stopPropagation();
                    closePresetsMenu();
                    onRenamePreset(preset);
                  }}
                >
                  <EditIcon fontSize="inherit" />
                </IconButton>
                <IconButton
                  size="small"
                  aria-label={`Delete ${preset.name}`}
                  onClick={(e) => {
                    e.stopPropagation();
                    closePresetsMenu();
                    onDeletePreset(preset);
                  }}
                >
                  <CloseIcon fontSize="inherit" />
                </IconButton>
              </MenuItem>
            ))}
          </Menu>
          <ColumnChooser visibleColumnIds={visibleColumnIds} onToggleColumn={toggleColumn} onResetDefaults={resetColumnsToDefaults} />
          <Button variant="outlined" size="small" startIcon={organizeRunning ? <CircularProgress size={16} /> : undefined} disabled={organizeRunning} onClick={onOrganizeLibrary}>{organizeRunning ? 'Organizing…' : 'Organize Library'}</Button>
          <Button variant="outlined" size="small" startIcon={activeScanOp !== null ? <CircularProgress size={16} /> : <RefreshIcon />} disabled={activeScanOp !== null} onClick={onFullRescan}>{activeScanOp !== null ? 'Scanning…' : 'Full Rescan'}</Button>
          <Button variant="outlined" size="small" onClick={onFetchAllUnmatched}>Fetch Unmatched</Button>
          <Button variant="contained" size="small" onClick={onResumeReview} title="Open review dialog for books with cached metadata candidates">Review Matches</Button>
          <Button startIcon={<DeleteSweepIcon />} onClick={onPurgeOpen} variant="outlined" size="small" color="secondary" disabled={softDeletedCount === 0}>Purge Deleted{softDeletedCount > 0 ? ` (${softDeletedCount})` : ''}</Button>
        </Stack>
      )}
    </Box>

    {/* Active operations progress (always visible) */}
    {activeOrganizeOp && activeOrganizeOp.status !== 'completed' && (
      <Box sx={{ mb: 1 }}>
        <Stack direction="row" spacing={1} alignItems="center">
          <LinearProgress
            variant="determinate"
            value={activeOrganizeOp.total > 0 ? (activeOrganizeOp.progress / activeOrganizeOp.total) * 100 : 0}
            sx={{ flex: 1, height: 6, borderRadius: 1 }}
          />
          <Typography
            variant="caption"
            color="text.secondary"
            sx={{ cursor: 'pointer', '&:hover': { textDecoration: 'underline' }, whiteSpace: 'nowrap' }}
            onClick={() => navigate(`/activity?op=${activeOrganizeOp.id}`)}
          >
            Organizing: {activeOrganizeOp.progress}/{activeOrganizeOp.total}
          </Typography>
          <Button size="small" variant="text" onClick={() => api.cancelOperation(activeOrganizeOp.id)}>Cancel</Button>
        </Stack>
      </Box>
    )}
    {activeScanOp && activeScanOp.status !== 'completed' && (
      <Box sx={{ mb: 1 }}>
        <Stack direction="row" spacing={1} alignItems="center">
          <LinearProgress
            variant={activeScanOp.total > 0 ? 'determinate' : 'indeterminate'}
            value={activeScanOp.total > 0 ? (activeScanOp.progress / activeScanOp.total) * 100 : 0}
            sx={{ flex: 1, height: 6, borderRadius: 1 }}
          />
          <Typography
            variant="caption"
            color="text.secondary"
            sx={{ cursor: 'pointer', '&:hover': { textDecoration: 'underline' }, whiteSpace: 'nowrap' }}
            onClick={() => navigate(`/activity?op=${activeScanOp.id}`)}
          >
            {activeScanOp.total > 0 ? `Scanning: ${activeScanOp.progress}/${activeScanOp.total}` : 'Scanning...'}
          </Typography>
          <Button size="small" variant="text" onClick={() => api.cancelOperation(activeScanOp.id)}>Cancel</Button>
        </Stack>
      </Box>
    )}

    {/* Storage info drawer */}
    <Drawer anchor="right" open={storageDrawerOpen} onClose={onStorageDrawerClose}>
      <Box sx={{ width: 360, p: 2 }}>
        <Typography variant="h6" gutterBottom>Library Info</Typography>
        <Divider sx={{ mb: 2 }} />
        {systemStatus && (
          <>
            <Typography variant="subtitle2" color="text.secondary" gutterBottom>Storage</Typography>
            <Typography variant="body2">{libraryBookCount} books in Library</Typography>
            <Typography variant="body2">{importBookCount} scanned but not imported</Typography>
            <Typography variant="body2">{systemStatus.import_paths?.folder_count || 0} import paths</Typography>
            <Typography variant="body2" sx={{ mt: 1 }}>
              {(librarySizeBytes / (1024 * 1024 * 1024)).toFixed(1)} GB Library
            </Typography>
            <Typography variant="body2">
              {(importSizeBytes / (1024 * 1024 * 1024)).toFixed(1)} GB Scanned
            </Typography>
            <Divider sx={{ my: 2 }} />
            <Typography variant="subtitle2" color="text.secondary" gutterBottom>Library Path</Typography>
            <Typography variant="body2" sx={{ wordBreak: 'break-all' }}>
              {systemStatus.library.path || 'Not configured'}
            </Typography>
          </>
        )}
      </Box>
    </Drawer>
  </>
  );
};
