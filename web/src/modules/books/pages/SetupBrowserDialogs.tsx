// file: web/src/modules/books/pages/SetupBrowserDialogs.tsx
// version: 0.1.0
// guid: 25f4b9e7-3e42-4275-a8a5-bf53acb4f87e
// last-edited: 2026-11-21

// F9-04 slice 5: the folder and file browser dialogs of books' first-run setup steps, moved verbatim from the WelcomeWizard.

import {
  Dialog,
  DialogTitle,
  DialogContent,
  DialogActions,
  Button,
  Typography,
  TextField,
  Alert,
} from '@mui/material';
import { ServerFileBrowser } from '../../../components/common/ServerFileBrowser';
import { useBooksSetup } from './booksSetupContext';

export function SetupBrowserDialogs() {
  const { libraryPath, homePath, libraryPathSelection, showLibraryBrowser, setShowLibraryBrowser, importFolderSelection, showImportBrowser, setShowImportBrowser, iTunesPathSelection, setITunesPathSelection, showITunesBrowser, setShowITunesBrowser, iTunesValidating, handleLibraryPathSelect, handleConfirmLibraryPath, handleImportFolderSelect, handleConfirmImportFolder, handleConfirmITunesPath } = useBooksSetup();
  return (
    <>
    {/* Library Path Browser Dialog */}
    <Dialog
      open={showLibraryBrowser}
      onClose={() => setShowLibraryBrowser(false)}
      maxWidth="md"
      fullWidth
    >
      <DialogTitle>Select Library Path</DialogTitle>
      <DialogContent>
        <ServerFileBrowser
          onSelect={handleLibraryPathSelect}
          initialPath={libraryPathSelection || libraryPath || homePath || '/'}
          showFiles={false}
          allowDirSelect
          allowFileSelect={false}
        />
        {libraryPathSelection && (
          <Alert severity="info" sx={{ mt: 2 }}>
            <Typography variant="body2">
              <strong>Selected:</strong> {libraryPathSelection}
            </Typography>
          </Alert>
        )}
      </DialogContent>
      <DialogActions>
        <Button onClick={() => setShowLibraryBrowser(false)}>Cancel</Button>
        <Button
          variant="contained"
          onClick={handleConfirmLibraryPath}
          disabled={!(libraryPathSelection || '').trim()}
        >
          Select Folder
        </Button>
      </DialogActions>
    </Dialog>

    {/* iTunes Library Browser Dialog */}
    <Dialog
      open={showITunesBrowser}
      onClose={() => setShowITunesBrowser(false)}
      maxWidth="md"
      fullWidth
    >
      <DialogTitle>Select iTunes Library.xml</DialogTitle>
      <DialogContent>
        <TextField
          fullWidth
          size="small"
          label="Or type the full path"
          placeholder="/path/to/iTunes Library.xml"
          value={iTunesPathSelection}
          onChange={(e) => setITunesPathSelection(e.target.value)}
          sx={{ mb: 2 }}
        />
        <ServerFileBrowser
          onSelect={(path, isDir) => {
            setITunesPathSelection(path);
            if (!isDir && path.toLowerCase().endsWith('.xml')) {
              // Auto-confirm when clicking an XML file
              setITunesPathSelection(path);
            }
          }}
          initialPath={iTunesPathSelection || homePath || '/'}
          showFiles={true}
          allowDirSelect={false}
          allowFileSelect={true}
        />
        {iTunesPathSelection && (
          <Alert severity="info" sx={{ mt: 2 }}>
            <Typography variant="body2">
              <strong>Selected:</strong> {iTunesPathSelection}
            </Typography>
          </Alert>
        )}
      </DialogContent>
      <DialogActions>
        <Button onClick={() => setShowITunesBrowser(false)}>Cancel</Button>
        <Button
          variant="contained"
          onClick={handleConfirmITunesPath}
          disabled={!(iTunesPathSelection || '').trim() || iTunesValidating}
        >
          Use this Library
        </Button>
      </DialogActions>
    </Dialog>

    {/* Import Folder Browser Dialog */}
    <Dialog
      open={showImportBrowser}
      onClose={() => setShowImportBrowser(false)}
      maxWidth="md"
      fullWidth
    >
      <DialogTitle>Select Import Folder</DialogTitle>
      <DialogContent>
        <ServerFileBrowser
          onSelect={handleImportFolderSelect}
          initialPath={importFolderSelection || homePath || '/'}
          showFiles={false}
          allowDirSelect
          allowFileSelect={false}
        />
        {importFolderSelection && (
          <Alert severity="info" sx={{ mt: 2 }}>
            <Typography variant="body2">
              <strong>Selected:</strong> {importFolderSelection}
            </Typography>
          </Alert>
        )}
      </DialogContent>
      <DialogActions>
        <Button onClick={() => setShowImportBrowser(false)}>Cancel</Button>
        <Button
          variant="contained"
          onClick={handleConfirmImportFolder}
          disabled={!(importFolderSelection || '').trim()}
        >
          Add Folder
        </Button>
      </DialogActions>
    </Dialog>
    </>
  );
}
