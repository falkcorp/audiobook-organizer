// file: web/src/modules/books/pages/BooksSettingsDialogs.tsx
// version: 0.1.0
// guid: a74a9839-9a7b-40b2-9dcb-5d63472fd95b
// last-edited: 2026-11-21

// Every dialog the Settings page can open: folder browsers, scans, backups, import and the unsaved-changes prompt.

import {
  Box,
  Typography,
  TextField,
  Button,
  Switch,
  FormControlLabel,
  Alert,
  Dialog,
  DialogTitle,
  DialogContent,
  DialogActions,
  List,
  ListItem,
  ListItemText,
} from '@mui/material';
import { ServerFileBrowser } from '../../../components/common/ServerFileBrowser';
import { FolderOpen as FolderOpenIcon } from '@mui/icons-material';
import { useBooksSettings } from './booksSettingsContext';

export function BooksSettingsDialogs() {
  const {
    addFolderDialogOpen,
    blocker,
    browserOpen,
    cancelScanTarget,
    deleteBackupInProgress,
    deleteBackupTarget,
    handleAddImportFolder,
    handleBrowserCancel,
    handleBrowserConfirm,
    handleBrowserSelect,
    handleCancelNavigation,
    handleConfirmCancelScan,
    handleConfirmDeleteBackup,
    handleConfirmImport,
    handleConfirmRestore,
    handleDiscardNavigation,
    handleFolderBrowserSelect,
    handleSaveAndNavigate,
    importDialogOpen,
    importFileName,
    importInProgress,
    importKeys,
    newFolderPath,
    restoreDialogOpen,
    restoreInProgress,
    restoreTarget,
    restoreVerify,
    scanErrorTarget,
    selectedPath,
    signInGate,
    setAddFolderDialogOpen,
    setCancelScanTarget,
    setDeleteBackupTarget,
    setImportDialogOpen,
    setNewFolderPath,
    setRestoreDialogOpen,
    setRestoreVerify,
    setScanErrorTarget,
    setShowFolderBrowser,
    settings,
    showFolderBrowser,
  } = useBooksSettings();

  return (
    <>
      {/* Library Path Browser Dialog */}
      <Dialog open={browserOpen} onClose={handleBrowserCancel} maxWidth="md" fullWidth>
        <DialogTitle>Browse Server Filesystem</DialogTitle>
        <DialogContent>
          <Typography
            variant="body2"
            gutterBottom
            sx={{
              color: 'text.secondary',
            }}
          >
            Select the library path where organized audiobooks will be stored.
          </Typography>
          <Box sx={{ mt: 2 }}>
            <ServerFileBrowser
              initialPath={selectedPath || settings.libraryPath}
              onSelect={handleBrowserSelect}
              showFiles={false}
              allowDirSelect={true}
              allowFileSelect={false}
            />
          </Box>
          {selectedPath && (
            <Alert severity="info" sx={{ mt: 2 }}>
              <Typography variant="body2">
                <strong>Selected:</strong> {selectedPath}
              </Typography>
            </Alert>
          )}
        </DialogContent>
        <DialogActions>
          <Button onClick={handleBrowserCancel}>Cancel</Button>
          <Button onClick={handleBrowserConfirm} variant="contained" disabled={!selectedPath}>
            Select Folder
          </Button>
        </DialogActions>
      </Dialog>

      {/* Import Folder Dialog */}
      <Dialog
        open={addFolderDialogOpen}
        onClose={() => setAddFolderDialogOpen(false)}
        maxWidth="md"
        fullWidth
      >
        <DialogTitle>Add Import Path (Watch Location)</DialogTitle>
        <DialogContent>
          {signInGate.notice && <Box sx={{ mb: 2 }}>{signInGate.notice}</Box>}
          <Alert severity="info" sx={{ mb: 2 }}>
            <strong>Import paths</strong> are separate from your main library. They are watched for
            new audiobook files that will be scanned, organized, and moved to your library path.
          </Alert>

          {!showFolderBrowser ? (
            <Box>
              <TextField
                autoFocus
                fullWidth
                label="Folder Path"
                value={newFolderPath}
                onChange={(e) => setNewFolderPath(e.target.value)}
                placeholder="/path/to/downloads"
                sx={{ mt: 1 }}
              />
              <Button
                startIcon={<FolderOpenIcon />}
                onClick={() => setShowFolderBrowser(true)}
                sx={{ mt: 2 }}
              >
                Browse Server Filesystem
              </Button>
            </Box>
          ) : (
            <Box>
              <Button onClick={() => setShowFolderBrowser(false)} sx={{ mb: 2 }}>
                ← Back to Manual Entry
              </Button>
              <ServerFileBrowser
                initialPath={newFolderPath || '/'}
                onSelect={handleFolderBrowserSelect}
                showFiles={false}
                allowDirSelect={true}
                allowFileSelect={false}
              />
              {newFolderPath && (
                <Alert severity="success" sx={{ mt: 2 }}>
                  <Typography variant="body2">
                    <strong>Selected:</strong> {newFolderPath}
                  </Typography>
                </Alert>
              )}
            </Box>
          )}
        </DialogContent>
        <DialogActions>
          <Button
            onClick={() => {
              setAddFolderDialogOpen(false);
              setShowFolderBrowser(false);
            }}
          >
            Cancel
          </Button>
          <Button
            onClick={handleAddImportFolder}
            variant="contained"
            disabled={!newFolderPath.trim() || signInGate.blocked}
          >
            Add Path
          </Button>
        </DialogActions>
      </Dialog>

      <Dialog open={Boolean(cancelScanTarget)} onClose={() => setCancelScanTarget(null)}>
        <DialogTitle>Cancel Scan</DialogTitle>
        <DialogContent>
          <Typography variant="body2" gutterBottom>
            Cancel scan for <strong>{cancelScanTarget?.path || 'this folder'}</strong>?
          </Typography>
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setCancelScanTarget(null)}>Keep Scanning</Button>
          <Button color="error" variant="contained" onClick={handleConfirmCancelScan}>
            Cancel Scan
          </Button>
        </DialogActions>
      </Dialog>

      <Dialog open={Boolean(scanErrorTarget)} onClose={() => setScanErrorTarget(null)}>
        <DialogTitle>Scan Errors</DialogTitle>
        <DialogContent>
          <Typography variant="body2" gutterBottom>
            Errors while scanning <strong>{scanErrorTarget?.path || 'this folder'}</strong>:
          </Typography>
          {scanErrorTarget?.errors?.length ? (
            <List dense>
              {scanErrorTarget.errors.map((error, index) => (
                <ListItem key={`${error}-${index}`}>
                  <ListItemText primary={error} />
                </ListItem>
              ))}
            </List>
          ) : (
            <Typography
              variant="body2"
              sx={{
                color: 'text.secondary',
              }}
            >
              No errors recorded.
            </Typography>
          )}
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setScanErrorTarget(null)}>Close</Button>
        </DialogActions>
      </Dialog>

      <Dialog open={restoreDialogOpen} onClose={() => setRestoreDialogOpen(false)}>
        <DialogTitle>Restore Backup</DialogTitle>
        <DialogContent>
          {signInGate.notice && <Box sx={{ mb: 2 }}>{signInGate.notice}</Box>}
          <Alert severity="warning" sx={{ mb: 2 }}>
            The server restarts to replace the current database with the selected backup. The
            current database is moved aside, not deleted.
          </Alert>
          <Typography variant="body2" gutterBottom>
            Restore from <strong>{restoreTarget?.filename || 'selected backup'}</strong>?
          </Typography>
          <FormControlLabel
            control={
              <Switch
                checked={restoreVerify}
                onChange={(e) => setRestoreVerify(e.target.checked)}
              />
            }
            label="Verify backup before restore"
          />
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setRestoreDialogOpen(false)} disabled={restoreInProgress}>
            Cancel
          </Button>
          <Button variant="contained" onClick={handleConfirmRestore}
            disabled={restoreInProgress || signInGate.blocked}>
            {restoreInProgress ? 'Restoring...' : 'Restore'}
          </Button>
        </DialogActions>
      </Dialog>

      <Dialog open={Boolean(deleteBackupTarget)} onClose={() => setDeleteBackupTarget(null)}>
        <DialogTitle>Delete Backup</DialogTitle>
        <DialogContent>
          <Typography variant="body2">
            Delete <strong>{deleteBackupTarget?.filename || 'this backup'}</strong>?
          </Typography>
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setDeleteBackupTarget(null)} disabled={deleteBackupInProgress}>
            Cancel
          </Button>
          <Button
            variant="contained"
            color="error"
            onClick={handleConfirmDeleteBackup}
            disabled={deleteBackupInProgress}
          >
            {deleteBackupInProgress ? 'Deleting...' : 'Delete'}
          </Button>
        </DialogActions>
      </Dialog>

      <Dialog
        open={importDialogOpen}
        onClose={() => setImportDialogOpen(false)}
        maxWidth="sm"
        fullWidth
      >
        <DialogTitle>Import Settings</DialogTitle>
        <DialogContent>
          <Alert severity="warning" sx={{ mb: 2 }}>
            Importing settings will overwrite your current configuration.
          </Alert>
          <Typography variant="body2" gutterBottom>
            Import from <strong>{importFileName || 'selected file'}</strong>?
          </Typography>
          {importKeys.length > 0 && (
            <List dense>
              {importKeys.slice(0, 12).map((key) => (
                <ListItem key={key}>
                  <ListItemText primary={key} />
                </ListItem>
              ))}
              {importKeys.length > 12 && (
                <ListItem>
                  <ListItemText primary={`+${importKeys.length - 12} more fields`} />
                </ListItem>
              )}
            </List>
          )}
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setImportDialogOpen(false)} disabled={importInProgress}>
            Cancel
          </Button>
          <Button variant="contained" onClick={handleConfirmImport} disabled={importInProgress}>
            {importInProgress ? 'Importing...' : 'Import Settings'}
          </Button>
        </DialogActions>
      </Dialog>

      <Dialog open={blocker.state === 'blocked'} onClose={handleCancelNavigation}>
        <DialogTitle>Unsaved Changes</DialogTitle>
        <DialogContent>
          <Typography variant="body2" gutterBottom>
            You have unsaved changes. Save before leaving?
          </Typography>
        </DialogContent>
        <DialogActions>
          <Button onClick={handleCancelNavigation}>Cancel</Button>
          <Button onClick={handleDiscardNavigation}>Discard</Button>
          <Button variant="contained" onClick={handleSaveAndNavigate}>
            Save
          </Button>
        </DialogActions>
      </Dialog>
    </>
  );
}
