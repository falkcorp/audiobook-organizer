// file: web/src/modules/books/pages/ImportFoldersStep.tsx
// version: 0.1.0
// guid: e8dabb19-5690-48d2-82c4-331280cb9757
// last-edited: 2026-11-21

// F9-04 slice 5: books' first-run setup step "Import Folders" (07 §7.11 SetupContribution.steps), moved verbatim from the WelcomeWizard.

import {
  Button,
  Typography,
  Box,
  Alert,
} from '@mui/material';
import { Folder as FolderIcon } from '@mui/icons-material';
import { useBooksSetup } from './booksSetupContext';

export function ImportFoldersStep() {
  const { importFolders, handleOpenImportBrowser, handleRemoveImportFolder, signInGate } =
    useBooksSetup();
  return (
    <Box>
      <Typography variant="h6" gutterBottom>
        Add Import Folders
      </Typography>
      <Typography
        variant="body2"
        sx={{
          color: 'text.secondary',
          marginBottom: '16px',
        }}
      >
        Import folders are watched locations where the scanner looks for new audiobooks.
        Files found here will be scanned and organized into your library path.
      </Typography>

      {signInGate.notice && <Box sx={{ mb: 2 }}>{signInGate.notice}</Box>}

      <Box sx={{ mb: 2 }}>
        <Button
          variant="outlined"
          startIcon={<FolderIcon />}
          onClick={handleOpenImportBrowser}
          sx={{ mb: 2 }}
        >
          Add Import Folder
        </Button>

        {importFolders.length > 0 && (
          <Box>
            <Typography variant="subtitle2" gutterBottom>
              Import Folders ({importFolders.length}):
            </Typography>
            {importFolders.map((folder, index) => (
              <Box
                key={index}
                sx={{
                  display: 'flex',
                  alignItems: 'center',
                  justifyContent: 'space-between',
                  p: 1,
                  mb: 1,
                  bgcolor: 'action.hover',
                  borderRadius: 1,
                }}
              >
                <Typography variant="body2">{folder}</Typography>
                <Button
                  size="small"
                  color="error"
                  onClick={() => handleRemoveImportFolder(index)}
                >
                  Remove
                </Button>
              </Box>
            ))}
          </Box>
        )}
      </Box>

      <Alert severity="info">
        You can add import folders later in the Library page if you prefer to skip this
        step.
      </Alert>
    </Box>
  );
}
