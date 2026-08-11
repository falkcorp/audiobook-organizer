// file: web/src/components/library/LibraryDialogs.tsx
// version: 1.6.0
// guid: d4e5f6a7-b8c9-0123-def0-234567890123
// last-edited: 2026-08-10
import React from 'react';
import {
  Typography,
  Box,
  Button,
  Stack,
  Alert,
  Dialog,
  DialogTitle,
  DialogContent,
  DialogActions,
  TextField,
  IconButton,
  FormControlLabel,
  Checkbox,
  Collapse,
  LinearProgress,
  CircularProgress,
  List,
  ListItem,
  ListItemText,
  ListItemSecondaryAction,
} from '@mui/material';
import {
  FolderOpen as FolderOpenIcon,
  Delete as DeleteIcon,
  ExpandMore as ExpandMoreIcon,
  Refresh as RefreshIcon,
} from '@mui/icons-material';
import { MetadataEditDialog } from '../audiobooks/MetadataEditDialog';
import { BatchEditDialog } from '../audiobooks/BatchEditDialog';
import { BulkMetadataSearchDialog } from '../audiobooks/BulkMetadataSearchDialog';
import { BulkTagDialog } from '../audiobooks/BulkTagDialog';
import { BulkRatingDialog } from '../audiobooks/BulkRatingDialog';
import { MetadataReviewDialog } from '../audiobooks/MetadataReviewDialog';
import { VersionManagement } from '../audiobooks/VersionManagement';
import AddToPlaylistDialog from '../audiobooks/AddToPlaylistDialog';
import { ServerFileBrowser } from '../common/ServerFileBrowser';
import type { Audiobook } from '../../types';
import * as api from '../../services/api';
import type {
  BulkActionProgress,
  DuplicateAction,
  DuplicateDialogState,
  OrganizeErrorState,
  ImportPath,
} from '../../pages/libraryTypes';
import { getResultLabel } from '../../pages/libraryTypes';

type ToastFn = (message: string, severity?: 'success' | 'error' | 'warning' | 'info') => void;

interface DeleteOptions {
  softDelete: boolean;
  blockHash: boolean;
}

interface LibraryDialogsProps {
  selectedAudiobooks: Audiobook[];
  setSelectedAudiobooks: (books: Audiobook[]) => void;
  hasSelection: boolean;
  selectedHasActive: boolean;
  selectedHasImport: boolean;
  toast: ToastFn;
  loadAudiobooks: () => void;
  refreshTags: () => void;

  // MetadataEditDialog
  editingAudiobook: Audiobook | null;
  setEditingAudiobook: (book: Audiobook | null) => void;
  handleSaveMetadata: (audiobook: Audiobook) => Promise<void>;

  // BatchEditDialog
  batchEditOpen: boolean;
  setBatchEditOpen: (open: boolean) => void;
  handleBatchSave: (updates: Partial<Audiobook>) => Promise<void>;

  // BulkTagDialog
  bulkTagDialogOpen: boolean;
  setBulkTagDialogOpen: (open: boolean) => void;
  availableTags: Array<{ tag: string; count: number }>;

  // BulkRatingDialog
  bulkRatingDialogOpen: boolean;
  setBulkRatingDialogOpen: (open: boolean) => void;

  // Merge dialog
  mergeDialogOpen: boolean;
  setMergeDialogOpen: (open: boolean) => void;
  mergePrimaryId: string;
  setMergePrimaryId: (id: string) => void;
  mergeInProgress: boolean;
  handleMergeAsVersions: () => void;
  combineDialogOpen: boolean;
  setCombineDialogOpen: (open: boolean) => void;
  combineInProgress: boolean;
  combineOverrideTitle: string;
  setCombineOverrideTitle: (v: string) => void;
  combineOverrideAuthor: string;
  setCombineOverrideAuthor: (v: string) => void;
  combineOverrideNarrator: string;
  setCombineOverrideNarrator: (v: string) => void;
  handleCombineIntoOneBook: () => void;

  // Batch delete dialog
  batchDeleteDialogOpen: boolean;
  setBatchDeleteDialogOpen: (open: boolean) => void;
  batchDeleteInProgress: boolean;
  handleBatchDelete: () => void;

  // Bulk organize dialog
  bulkOrganizeDialogOpen: boolean;
  handleCancelBulkOrganize: () => void;
  bulkOrganizeProgress: BulkActionProgress | null;
  bulkOrganizeInProgress: boolean;
  handleBulkOrganize: () => void;

  // Bulk write back dialog
  bulkWriteBackDialogOpen: boolean;
  handleCloseBulkWriteBackDialog: () => void;
  bulkWriteBackRename: boolean;
  setBulkWriteBackRename: (v: boolean) => void;
  bulkWriteBackForce: boolean;
  setBulkWriteBackForce: (v: boolean) => void;
  bulkWriteBackResult: api.BatchWriteBackResponse | null;
  bulkWriteBackInProgress: boolean;
  handleBulkWriteBack: () => void;

  // Bulk save all dialog
  bulkSaveAllDialogOpen: boolean;
  handleCloseBulkSaveAllDialog: () => void;
  bulkSaveAllEstimate: number | null;
  bulkSaveAllRename: boolean;
  setBulkSaveAllRename: (v: boolean) => void;
  bulkSaveAllStarting: boolean;
  handleBulkSaveAll: () => void;

  // Duplicate dialog
  duplicateDialog: DuplicateDialogState | null;
  handleDuplicateAction: (action: DuplicateAction) => void;

  // Organize error dialog
  bulkOrganizeError: OrganizeErrorState | null;
  handleCloseOrganizeError: () => void;
  handleOrganizeRollback: () => void;

  // Import file dialog
  importFileDialogOpen: boolean;
  setImportFileDialogOpen: (open: boolean) => void;
  importFilePath: string;
  setImportFilePath: (path: string) => void;
  handleAddImportFilePath: () => void;
  importFilePaths: string[];
  handleToggleImportFilePath: (path: string) => void;
  handleRemoveImportFilePath: (path: string) => void;
  importFileOrganize: boolean;
  setImportFileOrganize: (v: boolean) => void;
  importFileInProgress: boolean;
  handleImportFile: () => void;

  // Manual import operation dialog
  manualImportDialogOpen: boolean;
  setManualImportDialogOpen: (open: boolean) => void;
  manualImportPath: string;
  setManualImportPath: (path: string) => void;
  manualImportError: string | null;
  manualImportInProgress: boolean;
  manualImportOp: api.OperationV2 | null;
  handleManualPathImport: () => void;

  // Bulk fetch dialog
  bulkFetchDialogOpen: boolean;
  handleCancelBulkFetch: () => void;
  bulkFetchProgress: BulkActionProgress | null;
  bulkFetchInProgress: boolean;
  handleBulkFetchMetadata: () => void;

  // Bulk search dialog
  bulkSearchOpen: boolean;
  setBulkSearchOpen: (open: boolean) => void;

  // Metadata review dialog
  metadataReviewOpen: boolean;
  setMetadataReviewOpen: (open: boolean) => void;

  // Version management
  versionManagingAudiobook: Audiobook | null;
  versionManagementOpen: boolean;
  handleVersionManagementClose: () => void;
  handleVersionUpdate: () => void;

  // Delete dialog
  deleteDialogOpen: boolean;
  handleCloseDeleteDialog: () => void;
  bookPendingDelete: Audiobook | null;
  deleteOptions: DeleteOptions;
  setDeleteOptions: React.Dispatch<React.SetStateAction<DeleteOptions>>;
  deleteInProgress: boolean;
  handleConfirmDelete: () => void;

  // Purge dialog
  purgeDialogOpen: boolean;
  setPurgeDialogOpen: (open: boolean) => void;
  purgeDeleteFiles: boolean;
  setPurgeDeleteFiles: (v: boolean) => void;
  softDeletedCount: number;
  purgeInProgress: boolean;
  handleConfirmPurge: () => void;

  // Add path dialog
  addPathDialogOpen: boolean;
  setAddPathDialogOpen: (open: boolean) => void;
  showServerBrowser: boolean;
  setShowServerBrowser: (v: boolean) => void;
  newImportPath: string;
  setNewImportPath: (path: string) => void;
  handleAddImportPath: () => void;
  handleServerBrowserSelect: (path: string, isDir: boolean) => void;

  // Import paths list
  importPaths: ImportPath[];
  importPathsExpanded: boolean;
  setImportPathsExpanded: (v: boolean) => void;
  scanningAll: boolean;
  handleScanAll: () => void;
  scanningPathId: string | null;
  handleScanImportPath: (id: number) => void;
  removingPathId: string | null;
  handleRemoveImportPath: (id: number) => void;

  // Playlist dialog
  batchPlaylistOpen: boolean;
  setBatchPlaylistOpen: (open: boolean) => void;
}

export const LibraryDialogs = ({
  selectedAudiobooks,
  setSelectedAudiobooks,
  hasSelection,
  selectedHasActive,
  selectedHasImport,
  toast,
  loadAudiobooks,
  refreshTags,
  editingAudiobook,
  setEditingAudiobook,
  handleSaveMetadata,
  batchEditOpen,
  setBatchEditOpen,
  handleBatchSave,
  bulkTagDialogOpen,
  setBulkTagDialogOpen,
  availableTags,
  bulkRatingDialogOpen,
  setBulkRatingDialogOpen,
  mergeDialogOpen,
  setMergeDialogOpen,
  mergePrimaryId,
  setMergePrimaryId,
  mergeInProgress,
  handleMergeAsVersions,
  combineDialogOpen,
  setCombineDialogOpen,
  combineInProgress,
  combineOverrideTitle,
  setCombineOverrideTitle,
  combineOverrideAuthor,
  setCombineOverrideAuthor,
  combineOverrideNarrator,
  setCombineOverrideNarrator,
  handleCombineIntoOneBook,
  batchDeleteDialogOpen,
  setBatchDeleteDialogOpen,
  batchDeleteInProgress,
  handleBatchDelete,
  bulkOrganizeDialogOpen,
  handleCancelBulkOrganize,
  bulkOrganizeProgress,
  bulkOrganizeInProgress,
  handleBulkOrganize,
  bulkWriteBackDialogOpen,
  handleCloseBulkWriteBackDialog,
  bulkWriteBackRename,
  setBulkWriteBackRename,
  bulkWriteBackForce,
  setBulkWriteBackForce,
  bulkWriteBackResult,
  bulkWriteBackInProgress,
  handleBulkWriteBack,
  bulkSaveAllDialogOpen,
  handleCloseBulkSaveAllDialog,
  bulkSaveAllEstimate,
  bulkSaveAllRename,
  setBulkSaveAllRename,
  bulkSaveAllStarting,
  handleBulkSaveAll,
  duplicateDialog,
  handleDuplicateAction,
  bulkOrganizeError,
  handleCloseOrganizeError,
  handleOrganizeRollback,
  importFileDialogOpen,
  setImportFileDialogOpen,
  importFilePath,
  setImportFilePath,
  handleAddImportFilePath,
  importFilePaths,
  handleToggleImportFilePath,
  handleRemoveImportFilePath,
  importFileOrganize,
  setImportFileOrganize,
  importFileInProgress,
  handleImportFile,
  manualImportDialogOpen,
  setManualImportDialogOpen,
  manualImportPath,
  setManualImportPath,
  manualImportError,
  manualImportInProgress,
  manualImportOp,
  handleManualPathImport,
  bulkFetchDialogOpen,
  handleCancelBulkFetch,
  bulkFetchProgress,
  bulkFetchInProgress,
  handleBulkFetchMetadata,
  bulkSearchOpen,
  setBulkSearchOpen,
  metadataReviewOpen,
  setMetadataReviewOpen,
  versionManagingAudiobook,
  versionManagementOpen,
  handleVersionManagementClose,
  handleVersionUpdate,
  deleteDialogOpen,
  handleCloseDeleteDialog,
  bookPendingDelete,
  deleteOptions,
  setDeleteOptions,
  deleteInProgress,
  handleConfirmDelete,
  purgeDialogOpen,
  setPurgeDialogOpen,
  purgeDeleteFiles,
  setPurgeDeleteFiles,
  softDeletedCount,
  purgeInProgress,
  handleConfirmPurge,
  addPathDialogOpen,
  setAddPathDialogOpen,
  showServerBrowser,
  setShowServerBrowser,
  newImportPath,
  setNewImportPath,
  handleAddImportPath,
  handleServerBrowserSelect,
  importPaths,
  importPathsExpanded,
  setImportPathsExpanded,
  scanningAll,
  handleScanAll,
  scanningPathId,
  handleScanImportPath,
  removingPathId,
  handleRemoveImportPath,
  batchPlaylistOpen,
  setBatchPlaylistOpen,
}: LibraryDialogsProps) => (
  <>
    <MetadataEditDialog
      open={!!editingAudiobook}
      audiobook={editingAudiobook}
      onClose={() => setEditingAudiobook(null)}
      onSave={handleSaveMetadata}
    />

    <BatchEditDialog
      open={batchEditOpen}
      audiobooks={selectedAudiobooks}
      onClose={() => setBatchEditOpen(false)}
      onSave={handleBatchSave}
      onSavePerBook={async (bookId, updates) => {
        await api.updateBook(bookId, updates as Record<string, unknown> & Partial<api.Book>);
      }}
    />

    <BulkTagDialog
      open={bulkTagDialogOpen}
      onClose={() => setBulkTagDialogOpen(false)}
      bookIds={selectedAudiobooks.map((b) => b.id)}
      allTags={availableTags.map((t) => t.tag)}
      onComplete={() => {
        refreshTags();
        loadAudiobooks();
      }}
    />

    <BulkRatingDialog
      open={bulkRatingDialogOpen}
      onClose={() => setBulkRatingDialogOpen(false)}
      bookIds={selectedAudiobooks.map((b) => b.id)}
      onComplete={() => loadAudiobooks()}
    />

    <Dialog open={mergeDialogOpen} onClose={() => setMergeDialogOpen(false)} maxWidth="sm" fullWidth>
      <DialogTitle>Merge as Versions</DialogTitle>
      <DialogContent>
        <Typography variant="body2" gutterBottom>
          Merge {selectedAudiobooks.length} books into a version group. Pick which book to keep as the primary version:
        </Typography>
        <Box sx={{ mt: 1 }}>
          {selectedAudiobooks.map((book) => (
            <FormControlLabel
              key={book.id}
              control={
                <Checkbox
                  checked={mergePrimaryId === book.id}
                  onChange={() => setMergePrimaryId(book.id)}
                />
              }
              label={
                <Typography variant="body2">
                  <strong>{book.title}</strong>
                  {book.author ? ` by ${book.author}` : ''}
                  {book.file_path ? ` (${book.file_path.split('/').pop()})` : ''}
                </Typography>
              }
            />
          ))}
        </Box>
        <Alert severity="info" sx={{ mt: 1 }}>
          Non-primary versions will be linked as alternate formats. Files stay in place.
        </Alert>
      </DialogContent>
      <DialogActions>
        <Button onClick={() => setMergeDialogOpen(false)} disabled={mergeInProgress}>
          Cancel
        </Button>
        <Button
          variant="contained"
          onClick={handleMergeAsVersions}
          disabled={mergeInProgress || !mergePrimaryId}
        >
          {mergeInProgress ? 'Merging...' : 'Merge'}
        </Button>
      </DialogActions>
    </Dialog>

    <Dialog open={combineDialogOpen} onClose={() => setCombineDialogOpen(false)} maxWidth="sm" fullWidth>
      <DialogTitle>Combine into One Book</DialogTitle>
      <DialogContent>
        <Typography variant="body2" gutterBottom>
          Combine {selectedAudiobooks.length} books into a single multi-file book. Pick which book to keep as the survivor — its metadata is kept and all other files move onto it:
        </Typography>
        <Box sx={{ mt: 1 }}>
          {selectedAudiobooks.map((book) => (
            <FormControlLabel
              key={book.id}
              control={
                <Checkbox
                  checked={mergePrimaryId === book.id}
                  onChange={() => setMergePrimaryId(book.id)}
                />
              }
              label={
                <Typography variant="body2">
                  <strong>{book.title}</strong>
                  {book.author ? ` by ${book.author}` : ''}
                  {book.file_path ? ` (${book.file_path.split('/').pop()})` : ''}
                </Typography>
              }
            />
          ))}
        </Box>
        <Alert severity="warning" sx={{ mt: 1 }}>
          The other {Math.max(selectedAudiobooks.length - 1, 0)} entries will be deleted and their files attached to the survivor. Unlike &quot;Merge as Versions&quot;, this produces ONE book, not a version group. Files stay in place on disk.
        </Alert>
        <Typography variant="body2" sx={{ mt: 2, mb: 0.5, color: 'text.secondary' }}>
          Override metadata (optional — leave blank to keep the survivor&apos;s existing values):
        </Typography>
        <Stack spacing={1.5}>
          <TextField
            label="Title"
            size="small"
            fullWidth
            value={combineOverrideTitle}
            onChange={(e) => setCombineOverrideTitle(e.target.value)}
            placeholder="e.g. Salem's Lot"
          />
          <TextField
            label="Author"
            size="small"
            fullWidth
            value={combineOverrideAuthor}
            onChange={(e) => setCombineOverrideAuthor(e.target.value)}
            placeholder="e.g. Stephen King"
          />
          <TextField
            label="Narrator"
            size="small"
            fullWidth
            value={combineOverrideNarrator}
            onChange={(e) => setCombineOverrideNarrator(e.target.value)}
            placeholder="e.g. Ron McClarty"
          />
        </Stack>
      </DialogContent>
      <DialogActions>
        <Button onClick={() => setCombineDialogOpen(false)} disabled={combineInProgress}>
          Cancel
        </Button>
        <Button
          variant="contained"
          onClick={handleCombineIntoOneBook}
          disabled={combineInProgress || !mergePrimaryId}
        >
          {combineInProgress ? 'Combining...' : 'Combine'}
        </Button>
      </DialogActions>
    </Dialog>

    <Dialog open={batchDeleteDialogOpen} onClose={() => setBatchDeleteDialogOpen(false)}>
      <DialogTitle>Delete Selected Audiobooks</DialogTitle>
      <DialogContent>
        <Typography variant="body1" gutterBottom>
          Are you sure you want to soft delete {selectedAudiobooks.length} selected audiobooks?
        </Typography>
        <Alert severity="warning">
          Selected books will be hidden from the library and can be restored from the
          soft-deleted list.
        </Alert>
      </DialogContent>
      <DialogActions>
        <Button
          onClick={() => setBatchDeleteDialogOpen(false)}
          disabled={batchDeleteInProgress}
        >
          Cancel
        </Button>
        <Button
          variant="contained"
          color="secondary"
          onClick={handleBatchDelete}
          disabled={batchDeleteInProgress}
        >
          {batchDeleteInProgress ? 'Deleting...' : 'Delete Selected'}
        </Button>
      </DialogActions>
    </Dialog>

    <Dialog open={bulkOrganizeDialogOpen} onClose={handleCancelBulkOrganize}>
      <DialogTitle>Organize Selected Audiobooks</DialogTitle>
      <DialogContent>
        <Typography variant="body1" gutterBottom>
          Organize {selectedAudiobooks.length} selected books.
        </Typography>
        {bulkOrganizeProgress && (
          <Box sx={{ mt: 2 }}>
            <Typography variant="body2" gutterBottom>
              Organized {bulkOrganizeProgress.completed} of {bulkOrganizeProgress.total}
            </Typography>
            <LinearProgress
              variant="determinate"
              value={
                bulkOrganizeProgress.total > 0
                  ? (bulkOrganizeProgress.completed / bulkOrganizeProgress.total) * 100
                  : 0
              }
            />
            {bulkOrganizeProgress.results.length > 0 && (
              <List dense sx={{ mt: 2 }}>
                {bulkOrganizeProgress.results.map((result) => (
                  <ListItem key={result.book_id}>
                    <ListItemText
                      primary={result.title || result.book_id}
                      secondary={getResultLabel(result)}
                    />
                  </ListItem>
                ))}
              </List>
            )}
          </Box>
        )}
      </DialogContent>
      <DialogActions>
        <Button onClick={handleCancelBulkOrganize}>
          {bulkOrganizeInProgress ? 'Cancel' : 'Close'}
        </Button>
        <Button
          variant="contained"
          onClick={handleBulkOrganize}
          disabled={bulkOrganizeInProgress || !selectedHasImport}
        >
          {bulkOrganizeInProgress ? 'Organizing\u2026' : 'Organize Selected'}
        </Button>
      </DialogActions>
    </Dialog>

    <Dialog open={bulkWriteBackDialogOpen} onClose={handleCloseBulkWriteBackDialog}>
      <DialogTitle>Save Selected to Files</DialogTitle>
      <DialogContent>
        <Typography variant="body1" gutterBottom>
          Write current database metadata to {selectedAudiobooks.length} selected books.
        </Typography>
        <FormControlLabel
          control={
            <Checkbox
              checked={bulkWriteBackRename}
              onChange={(event) => setBulkWriteBackRename(event.target.checked)}
              disabled={bulkWriteBackInProgress}
            />
          }
          label="Organize files after write"
        />
        <FormControlLabel
          control={
            <Checkbox
              checked={bulkWriteBackForce}
              onChange={(event) => setBulkWriteBackForce(event.target.checked)}
              disabled={bulkWriteBackInProgress}
            />
          }
          label="Force rewrite (skip change detection)"
        />
        {bulkWriteBackResult && (
          <Box sx={{ mt: 2 }}>
            <Alert
              severity={(bulkWriteBackResult.failed ?? 0) > 0 ? 'warning' : 'success'}
              sx={{ mb: 2 }}
            >
              Wrote {bulkWriteBackResult.written ?? 0} books to files, updated{' '}
              {bulkWriteBackResult.written_files ?? 0} file
              {(bulkWriteBackResult.written_files ?? 0) === 1 ? '' : 's'}
              {(bulkWriteBackResult.renamed ?? 0) > 0
                ? `, renamed ${bulkWriteBackResult.renamed}`
                : ''}
              {(bulkWriteBackResult.failed ?? 0) > 0 ? `, ${bulkWriteBackResult.failed} failed` : ''}.
            </Alert>
            {(bulkWriteBackResult.errors ?? []).length > 0 && (
              <List dense>
                {(bulkWriteBackResult.errors ?? []).map((error) => (
                  <ListItem key={`${error.book_id}-${error.error}`}>
                    <ListItemText primary={error.book_id} secondary={error.error} />
                  </ListItem>
                ))}
              </List>
            )}
          </Box>
        )}
      </DialogContent>
      <DialogActions>
        <Button onClick={handleCloseBulkWriteBackDialog} disabled={bulkWriteBackInProgress}>
          Close
        </Button>
        <Button
          variant="contained"
          onClick={handleBulkWriteBack}
          disabled={bulkWriteBackInProgress || !selectedHasActive}
        >
          {bulkWriteBackInProgress ? 'Saving\u2026' : 'Save to Files'}
        </Button>
      </DialogActions>
    </Dialog>

    <Dialog open={bulkSaveAllDialogOpen} onClose={handleCloseBulkSaveAllDialog}>
      <DialogTitle>Save All to Files</DialogTitle>
      <DialogContent>
        <Typography variant="body1" gutterBottom>
          This will write metadata tags and rename files for all organized and imported
          books in your library.
        </Typography>
        {bulkSaveAllEstimate !== null ? (
          <Alert severity="info" sx={{ mb: 2 }}>
            {bulkSaveAllEstimate} book{bulkSaveAllEstimate === 1 ? '' : 's'} will be
            processed. Books in protected paths (iTunes) will be skipped.
          </Alert>
        ) : (
          <Box sx={{ display: 'flex', justifyContent: 'center', my: 2 }}>
            <CircularProgress size={24} />
          </Box>
        )}
        <FormControlLabel
          control={
            <Checkbox
              checked={bulkSaveAllRename}
              onChange={(event) => setBulkSaveAllRename(event.target.checked)}
              disabled={bulkSaveAllStarting}
            />
          }
          label="Organize files after write"
        />
      </DialogContent>
      <DialogActions>
        <Button onClick={handleCloseBulkSaveAllDialog} disabled={bulkSaveAllStarting}>
          Cancel
        </Button>
        <Button
          variant="contained"
          onClick={handleBulkSaveAll}
          disabled={
            bulkSaveAllStarting || bulkSaveAllEstimate === null || bulkSaveAllEstimate === 0
          }
        >
          {bulkSaveAllStarting ? 'Starting...' : 'Save All to Files'}
        </Button>
      </DialogActions>
    </Dialog>

    <Dialog open={Boolean(duplicateDialog)} onClose={() => handleDuplicateAction('skip')}>
      <DialogTitle>Duplicate File Detected</DialogTitle>
      <DialogContent>
        <Typography variant="body2" gutterBottom>
          The file for <strong>{duplicateDialog?.duplicate.title || 'this audiobook'}</strong>{' '}
          matches an existing audiobook.
        </Typography>
        <Typography variant="body2" color="text.secondary">
          Existing: {duplicateDialog?.existing.title || 'Unknown audiobook'}
        </Typography>
      </DialogContent>
      <DialogActions>
        <Button onClick={() => handleDuplicateAction('skip')}>Skip</Button>
        <Button onClick={() => handleDuplicateAction('link')}>Link as Version</Button>
        <Button
          color="warning"
          variant="contained"
          onClick={() => handleDuplicateAction('replace')}
        >
          Replace
        </Button>
      </DialogActions>
    </Dialog>

    <Dialog open={Boolean(bulkOrganizeError)} onClose={handleCloseOrganizeError}>
      <DialogTitle>Organize Error</DialogTitle>
      <DialogContent>
        <Typography variant="body2" gutterBottom>
          {bulkOrganizeError?.message || 'Organize operation failed.'}
        </Typography>
      </DialogContent>
      <DialogActions>
        <Button onClick={handleCloseOrganizeError}>Close</Button>
        <Button color="warning" variant="contained" onClick={handleOrganizeRollback}>
          Rollback
        </Button>
      </DialogActions>
    </Dialog>

    <Dialog
      open={importFileDialogOpen}
      onClose={() => setImportFileDialogOpen(false)}
      maxWidth="md"
      fullWidth
    >
      <DialogTitle>Import Audiobook File</DialogTitle>
      <DialogContent>
        <Alert severity="info" sx={{ mb: 2 }}>
          Select a file on the server to import into the library. Use the organize toggle to
          immediately move it into the library layout.
        </Alert>
        <Stack direction={{ xs: 'column', sm: 'row' }} spacing={2} sx={{ mb: 2 }}>
          <TextField
            fullWidth
            label="Import file path"
            value={importFilePath}
            onChange={(e) => setImportFilePath(e.target.value)}
            placeholder="/path/to/audiobook.m4b"
          />
          <Button
            variant="outlined"
            onClick={handleAddImportFilePath}
            disabled={!importFilePath.trim()}
            sx={{ minWidth: 140 }}
          >
            Add to list
          </Button>
        </Stack>
        <ServerFileBrowser
          initialPath="/"
          showFiles
          allowDirSelect={false}
          allowFileSelect
          onSelect={(path, isDir) => {
            if (!isDir) {
              handleToggleImportFilePath(path);
            }
          }}
        />
        <Typography variant="caption" color="text.secondary" sx={{ display: 'block', mt: 1 }}>
          Click files to add or remove them from the import list.
        </Typography>
        {importFilePaths.length > 0 && (
          <Box sx={{ mt: 2 }}>
            <Typography variant="subtitle2" gutterBottom>
              Selected Files ({importFilePaths.length}):
            </Typography>
            <List dense>
              {importFilePaths.map((path) => (
                <ListItem key={path}>
                  <ListItemText primary={path} />
                  <ListItemSecondaryAction>
                    <IconButton
                      edge="end"
                      aria-label="remove"
                      onClick={() => handleRemoveImportFilePath(path)}
                    >
                      <DeleteIcon />
                    </IconButton>
                  </ListItemSecondaryAction>
                </ListItem>
              ))}
            </List>
          </Box>
        )}
        <FormControlLabel
          control={
            <Checkbox
              checked={importFileOrganize}
              onChange={(e) => setImportFileOrganize(e.target.checked)}
            />
          }
          label="Organize into library after import"
        />
      </DialogContent>
      <DialogActions>
        <Button onClick={() => setImportFileDialogOpen(false)} disabled={importFileInProgress}>
          Cancel
        </Button>
        <Button
          variant="contained"
          onClick={handleImportFile}
          disabled={
            importFileInProgress || (!importFilePath.trim() && importFilePaths.length === 0)
          }
        >
          {importFileInProgress ? 'Importing\u2026' : 'Import'}
        </Button>
      </DialogActions>
    </Dialog>

    <Dialog
      open={manualImportDialogOpen}
      onClose={() => {
        if (!manualImportInProgress) {
          setManualImportDialogOpen(false);
        }
      }}
      maxWidth="sm"
      fullWidth
    >
      <DialogTitle>Manual import</DialogTitle>
      <DialogContent>
        <TextField
          fullWidth
          autoFocus
          margin="dense"
          label="Absolute path"
          value={manualImportPath}
          onChange={(e) => setManualImportPath(e.target.value)}
          placeholder="/path/to/audiobook-or-folder"
          error={Boolean(manualImportError)}
          helperText={manualImportError || 'Import one server path using the library.import operation.'}
          disabled={manualImportInProgress}
        />
        {manualImportInProgress && (
          <Box sx={{ mt: 2 }}>
            <Stack direction="row" spacing={1} alignItems="center" sx={{ mb: 1 }}>
              <CircularProgress size={18} />
              <Typography variant="body2" color="text.secondary">
                {manualImportOp?.progress_message ||
                  manualImportOp?.current_phase ||
                  'Waiting for progress...'}
              </Typography>
            </Stack>
            <LinearProgress
              variant={(manualImportOp?.progress_total ?? 0) > 0 ? 'determinate' : 'indeterminate'}
              value={
                (manualImportOp?.progress_total ?? 0) > 0
                  ? Math.round(
                      ((manualImportOp?.progress_current ?? 0) /
                        (manualImportOp?.progress_total ?? 1)) *
                        100
                    )
                  : 0
              }
            />
            {(manualImportOp?.progress_total ?? 0) > 0 && (
              <Typography variant="caption" color="text.secondary" sx={{ mt: 0.5, display: 'block' }}>
                {Math.round(
                  ((manualImportOp?.progress_current ?? 0) /
                    (manualImportOp?.progress_total ?? 1)) *
                    100
                )}
                % ({manualImportOp?.progress_current ?? 0}/{manualImportOp?.progress_total ?? 0})
              </Typography>
            )}
          </Box>
        )}
      </DialogContent>
      <DialogActions>
        <Button
          onClick={() => setManualImportDialogOpen(false)}
          disabled={manualImportInProgress}
        >
          Cancel
        </Button>
        <Button
          variant="contained"
          onClick={handleManualPathImport}
          disabled={manualImportInProgress || !manualImportPath.trim()}
        >
          {manualImportInProgress ? 'Importing...' : 'Start import'}
        </Button>
      </DialogActions>
    </Dialog>

    <Dialog open={bulkFetchDialogOpen} onClose={handleCancelBulkFetch}>
      <DialogTitle>Bulk Fetch Metadata</DialogTitle>
      <DialogContent>
        <Typography variant="body1" gutterBottom>
          Fetch metadata for {selectedAudiobooks.length} selected books.
        </Typography>
        {bulkFetchProgress && (
          <Box sx={{ mt: 2 }}>
            <Typography variant="body2" gutterBottom>
              {bulkFetchProgress.completed} / {bulkFetchProgress.total} completed
            </Typography>
            <LinearProgress
              variant="determinate"
              value={
                bulkFetchProgress.total > 0
                  ? (bulkFetchProgress.completed / bulkFetchProgress.total) * 100
                  : 0
              }
            />
            {bulkFetchProgress.results.length > 0 && (
              <List dense sx={{ mt: 2 }}>
                {bulkFetchProgress.results.map((result) => (
                  <ListItem key={result.book_id}>
                    <ListItemText
                      primary={result.title || result.book_id}
                      secondary={getResultLabel(result)}
                    />
                  </ListItem>
                ))}
              </List>
            )}
          </Box>
        )}
      </DialogContent>
      <DialogActions>
        <Button onClick={handleCancelBulkFetch}>
          {bulkFetchInProgress ? 'Cancel' : 'Close'}
        </Button>
        <Button
          variant="contained"
          onClick={handleBulkFetchMetadata}
          disabled={bulkFetchInProgress || !hasSelection}
        >
          {bulkFetchInProgress ? 'Fetching\u2026' : 'Fetch Metadata'}
        </Button>
      </DialogActions>
    </Dialog>

    <BulkMetadataSearchDialog
      open={bulkSearchOpen}
      books={selectedAudiobooks}
      onClose={() => setBulkSearchOpen(false)}
      onComplete={() => {
        loadAudiobooks();
        setSelectedAudiobooks([]);
      }}
      toast={toast}
    />

    {/* Always render the review dialog so its internal state
        (loaded results, row states, scroll position) survives
        across close/reopen cycles. Only toggle the `open` prop.
        The dialog ignores backdrop clicks and Escape — the
        user must click the x button or Done to close, which
        prevents accidentally blowing away a long review
        session. Reopening is instant because the data is
        already loaded. */}
    <MetadataReviewDialog
      open={metadataReviewOpen}
      onClose={() => setMetadataReviewOpen(false)}
      onComplete={() => {
        loadAudiobooks();
        setSelectedAudiobooks([]);
      }}
      toast={toast}
    />

    <VersionManagement
      audiobookId={versionManagingAudiobook?.id || ''}
      open={versionManagementOpen}
      onClose={handleVersionManagementClose}
      onUpdate={handleVersionUpdate}
    />

    <Dialog open={deleteDialogOpen} onClose={handleCloseDeleteDialog}>
      <DialogTitle>Delete Audiobook</DialogTitle>
      <DialogContent>
        <Typography variant="body1" gutterBottom>
          {bookPendingDelete
            ? `Are you sure you want to delete "${bookPendingDelete.title}"?`
            : 'Are you sure you want to delete this audiobook?'}
        </Typography>
        <FormControlLabel
          control={
            <Checkbox
              checked={deleteOptions.softDelete}
              onChange={(e) =>
                setDeleteOptions((prev) => ({
                  ...prev,
                  softDelete: e.target.checked,
                }))
              }
            />
          }
          label="Soft delete (hide from library, keep for purge)"
        />
        <FormControlLabel
          control={
            <Checkbox
              checked={deleteOptions.blockHash}
              onChange={(e) =>
                setDeleteOptions((prev) => ({
                  ...prev,
                  blockHash: e.target.checked,
                }))
              }
            />
          }
          label="Prevent reimporting this file (block hash)"
        />
        <Alert severity="warning" sx={{ mt: 2 }}>
          Soft deleting keeps the record for auditing and purging. Use purge to permanently
          remove it later.
        </Alert>
      </DialogContent>
      <DialogActions>
        <Button onClick={handleCloseDeleteDialog}>Cancel</Button>
        <Button
          onClick={handleConfirmDelete}
          color="error"
          variant="contained"
          disabled={deleteInProgress}
        >
          {deleteInProgress
            ? 'Deleting...'
            : deleteOptions.softDelete
              ? 'Soft Delete'
              : 'Delete'}
        </Button>
      </DialogActions>
    </Dialog>

    <Dialog
      open={purgeDialogOpen}
      onClose={() => {
        setPurgeDialogOpen(false);
        setPurgeDeleteFiles(false);
      }}
    >
      <DialogTitle>Purge Soft-Deleted Books</DialogTitle>
      <DialogContent>
        <Typography variant="body1" gutterBottom>
          {softDeletedCount === 0
            ? 'There are no soft-deleted books to purge.'
            : `This will permanently remove ${softDeletedCount} soft-deleted ${
                softDeletedCount === 1 ? 'book' : 'books'
              } from the library.`}
        </Typography>
        <FormControlLabel
          control={
            <Checkbox
              checked={purgeDeleteFiles}
              onChange={(e) => setPurgeDeleteFiles(e.target.checked)}
            />
          }
          label="Also delete files from disk (if they still exist)"
        />
        <Alert severity="warning" sx={{ mt: 2 }}>
          This cannot be undone. Purge removes the records entirely and deletes files when
          selected.
        </Alert>
      </DialogContent>
      <DialogActions>
        <Button
          onClick={() => {
            setPurgeDialogOpen(false);
            setPurgeDeleteFiles(false);
          }}
        >
          Cancel
        </Button>
        <Button
          onClick={handleConfirmPurge}
          color="error"
          variant="contained"
          disabled={purgeInProgress || softDeletedCount === 0}
        >
          {purgeInProgress ? 'Purging...' : 'Purge Now'}
        </Button>
      </DialogActions>
    </Dialog>

    {/* Import Path Management Dialog */}
    <Dialog
      open={addPathDialogOpen}
      onClose={() => setAddPathDialogOpen(false)}
      maxWidth="md"
      fullWidth
    >
      <DialogTitle>Add Import Folder (Watch Location)</DialogTitle>
      <DialogContent>
        <Alert severity="info" sx={{ mb: 2 }}>
          <strong>Import folders</strong> are watch locations where the scanner looks for new
          audiobooks. Files discovered here will be copied and organized into your main library
          path (configured in Settings).
        </Alert>

        {!showServerBrowser ? (
          <Box>
            <TextField
              autoFocus
              fullWidth
              label="Import Path"
              value={newImportPath}
              onChange={(e) => setNewImportPath(e.target.value)}
              placeholder="/path/to/downloads"
              sx={{ mt: 1 }}
            />
            <Button
              startIcon={<FolderOpenIcon />}
              onClick={() => setShowServerBrowser(true)}
              sx={{ mt: 2 }}
            >
              Browse Server Filesystem
            </Button>
          </Box>
        ) : (
          <Box>
            <Button onClick={() => setShowServerBrowser(false)} sx={{ mb: 2 }}>
              \u2190 Back to Manual Entry
            </Button>
            <ServerFileBrowser
              initialPath={newImportPath || '/'}
              onSelect={handleServerBrowserSelect}
              showFiles={false}
              allowDirSelect={true}
              allowFileSelect={false}
            />
            {newImportPath && (
              <Alert severity="success" sx={{ mt: 2 }}>
                <Typography variant="body2">
                  <strong>Selected:</strong> {newImportPath}
                </Typography>
              </Alert>
            )}
          </Box>
        )}
      </DialogContent>
      <DialogActions>
        <Button
          onClick={() => {
            setAddPathDialogOpen(false);
            setShowServerBrowser(false);
          }}
        >
          Cancel
        </Button>
        <Button
          onClick={handleAddImportPath}
          variant="contained"
          disabled={!newImportPath.trim()}
        >
          Add Path
        </Button>
      </DialogActions>
    </Dialog>

    {/* Import Paths List */}
    {importPaths.length > 0 && (
      <Box sx={{ mt: 2 }}>
        <Box
          sx={{
            p: 2,
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'space-between',
            cursor: 'pointer',
            bgcolor: 'background.paper',
            borderRadius: 1,
          }}
          onClick={() => setImportPathsExpanded(!importPathsExpanded)}
        >
          <Typography variant="h6">Import Paths ({importPaths.length})</Typography>
          <Stack direction="row" spacing={1} alignItems="center">
            <Button
              size="small"
              variant="outlined"
              startIcon={scanningAll ? <CircularProgress size={16} /> : undefined}
              onClick={(e) => {
                e.stopPropagation();
                handleScanAll();
              }}
              disabled={
                scanningAll ||
                importPaths.length === 0 ||
                importPaths.some((p) => p.status === 'scanning')
              }
            >
              {scanningAll ? 'Scanning...' : 'Scan All'}
            </Button>
            <IconButton
              size="small"
              onClick={(e) => {
                e.stopPropagation();
                setImportPathsExpanded(!importPathsExpanded);
              }}
            >
              <ExpandMoreIcon
                sx={[{
                  transition: 'transform 0.3s'
                }, importPathsExpanded ? {
                  transform: 'rotate(180deg)'
                } : {
                  transform: 'rotate(0deg)'
                }]}
              />
            </IconButton>
          </Stack>
        </Box>
        <Collapse in={importPathsExpanded}>
          <List>
            {importPaths.map((path) => (
              <ListItem key={path.id}>
                <ListItemText
                  primary={path.path}
                  secondary={
                    path.status === 'scanning'
                      ? 'Scanning...'
                      : `${path.book_count} books found`
                  }
                />
                <ListItemSecondaryAction>
                  <IconButton
                    edge="end"
                    onClick={() => handleScanImportPath(path.id)}
                    disabled={
                      path.status === 'scanning' || scanningPathId === path.id.toString()
                    }
                    sx={{ mr: 1 }}
                  >
                    {scanningPathId === path.id.toString() ? (
                      <CircularProgress size={20} />
                    ) : (
                      <RefreshIcon />
                    )}
                  </IconButton>
                  <IconButton
                    edge="end"
                    onClick={() => handleRemoveImportPath(path.id)}
                    disabled={removingPathId === path.id.toString()}
                  >
                    {removingPathId === path.id.toString() ? (
                      <CircularProgress size={20} />
                    ) : (
                      <DeleteIcon />
                    )}
                  </IconButton>
                </ListItemSecondaryAction>
              </ListItem>
            ))}
          </List>
        </Collapse>
      </Box>
    )}

    <AddToPlaylistDialog
      open={batchPlaylistOpen}
      onClose={() => setBatchPlaylistOpen(false)}
      bookIds={selectedAudiobooks.map((b) => b.id)}
    />
  </>
);
