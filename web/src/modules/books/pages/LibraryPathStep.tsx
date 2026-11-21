// file: web/src/modules/books/pages/LibraryPathStep.tsx
// version: 0.1.0
// guid: 9e1788e9-d6af-4978-902c-bab2c77f3f5f
// last-edited: 2026-11-21

// F9-04 slice 5: books' first-run setup step "Library Path" (07 §7.11 SetupContribution.steps), moved verbatim from the WelcomeWizard.

import {
  Button,
  Typography,
  TextField,
  Box,
  Alert,
  InputAdornment,
} from '@mui/material';
import { useBooksSetup } from './booksSetupContext';

export function LibraryPathStep() {
  const { libraryPath, setLibraryPath, handleOpenLibraryBrowser } = useBooksSetup();
  return (
    <Box>
      <Typography variant="h6" gutterBottom>
        Set Your Library Path
      </Typography>
      <Typography
        variant="body2"
        sx={{
          color: 'text.secondary',
          marginBottom: '16px',
        }}
      >
        This is where your organized audiobooks will be stored. The app will create a
        structured folder hierarchy here based on your naming patterns.
      </Typography>

      <TextField
        fullWidth
        label="Library Path"
        value={libraryPath}
        onChange={(e) => setLibraryPath(e.target.value)}
        placeholder="/path/to/audiobooks/library"
        sx={{ mb: 2 }}
        slotProps={{
          input: {
            endAdornment: (
              <InputAdornment position="end">
                <Button variant="outlined" size="small" onClick={handleOpenLibraryBrowser}>
                  Browse
                </Button>
              </InputAdornment>
            ),
          },
        }}
      />

      <Alert severity="info">
        Choose a location with plenty of storage space. This will be the permanent home for
        your organized audiobook collection.
      </Alert>
    </Box>
  );
}
