// file: web/src/modules/books/pages/ITunesStep.tsx
// version: 0.1.0
// guid: e3299d4f-51ea-402c-8af0-379aac91a745
// last-edited: 2026-11-21

// F9-04 slice 5: books' first-run setup step "iTunes Library" (07 §7.11 SetupContribution.steps), moved verbatim from the WelcomeWizard.

import {
  Button,
  Typography,
  TextField,
  Box,
  Alert,
  InputAdornment,
  CircularProgress,
  FormControl,
  FormControlLabel,
  FormLabel,
  Radio,
  RadioGroup,
  Stack,
} from '@mui/material';
import * as api from '../../../services/api';
import { useBooksSetup } from './booksSetupContext';

export function ITunesStep() {
  const { iTunesPath, setITunesPath, iTunesPathSelection, setITunesPathSelection, iTunesValidating, setITunesValidating, iTunesValidation, setITunesValidation, setITunesEnabled, iTunesImportMode, setITunesImportMode, pathMappings, setPathMappings, mappingTests, setMappingTests, testingMapping, setTestingMapping, handleOpenITunesBrowser, handleConfirmITunesPath } = useBooksSetup();
  return (
    <Box>
      <Typography variant="h6" gutterBottom>
        Import from iTunes Library
      </Typography>
      <Typography
        variant="body2"
        sx={{
          color: 'text.secondary',
          marginBottom: '16px',
        }}
      >
        If you have audiobooks in iTunes/Apple Books, you can import them automatically.
        Point to your iTunes Library.xml file to get started.
      </Typography>

      {!iTunesPath ? (
        <Box sx={{ mb: 2 }}>
          <TextField
            fullWidth
            size="small"
            label="iTunes Library.xml path"
            placeholder="/path/to/iTunes Library.xml"
            value={iTunesPathSelection}
            onChange={(e) => setITunesPathSelection(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter' && iTunesPathSelection.trim()) {
                handleConfirmITunesPath();
              }
            }}
            sx={{ mb: 1 }}
            slotProps={{
              input: {
                endAdornment: (
                  <InputAdornment position="end">
                    <Button size="small" onClick={handleOpenITunesBrowser}>
                      Browse
                    </Button>
                    <Button
                      size="small"
                      variant="contained"
                      disabled={!iTunesPathSelection.trim() || iTunesValidating}
                      onClick={handleConfirmITunesPath}
                      sx={{ ml: 1 }}
                    >
                      Use
                    </Button>
                  </InputAdornment>
                ),
              },
            }}
          />
        </Box>
      ) : (
        <Box sx={{ mb: 2 }}>
          <Box
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
            <Typography variant="body2">{iTunesPath}</Typography>
            <Button
              size="small"
              color="error"
              onClick={() => {
                setITunesPath('');
                setITunesValidation(null);
                setITunesEnabled(false);
              }}
            >
              Remove
            </Button>
          </Box>
        </Box>
      )}

      {iTunesValidating && (
        <Box sx={{ display: 'flex', alignItems: 'center', gap: 1, mb: 2 }}>
          <CircularProgress size={16} />
          <Typography variant="body2">Validating iTunes library...</Typography>
        </Box>
      )}

      {iTunesValidation?.valid && (
        <>
          <Alert severity="success" sx={{ mb: 2 }}>
            Found {iTunesValidation.audiobook_count} audiobook tracks (
            {iTunesValidation.files_found} files found). These will be processed in the
            background when you complete setup.
          </Alert>

          <FormControl component="fieldset" sx={{ mb: 2 }}>
            <FormLabel component="legend">What should happen after import?</FormLabel>
            <RadioGroup
              value={iTunesImportMode}
              onChange={(e) => setITunesImportMode(e.target.value as 'import' | 'organize')}
            >
              <FormControlLabel
                value="import"
                control={<Radio />}
                label="Import metadata only (keep files where they are)"
              />
              <FormControlLabel
                value="organize"
                control={<Radio />}
                label="Import metadata and organize files into library"
              />
            </RadioGroup>
          </FormControl>
        </>
      )}

      {iTunesValidation && !iTunesValidation.valid && (
        <Alert severity="error" sx={{ mb: 2 }}>
          {iTunesValidation.error || 'Invalid iTunes library file.'}
        </Alert>
      )}

      {iTunesValidation?.valid &&
        iTunesValidation.files_found === 0 &&
        (iTunesValidation.audiobook_count ?? 0) > 0 && (
          <Alert severity="warning" sx={{ mb: 2 }}>
            No files found on disk. If your iTunes library was created on a different
            machine (e.g. Windows), use path mapping below to translate the original paths
            to local paths.
          </Alert>
        )}

      {pathMappings.length > 0 && (
        <Box sx={{ mb: 2 }}>
          <Typography variant="subtitle2" sx={{ mb: 1 }}>
            Path Mapping
          </Typography>
          <Typography
            variant="body2"
            sx={{
              color: 'text.secondary',
              mb: 1.5,
            }}
          >
            Map each iTunes path prefix to its local equivalent on this server.
          </Typography>
          {pathMappings.map((mapping, idx) => (
            <Box key={idx} sx={{ mb: 2 }}>
              <Typography
                variant="body2"
                sx={{ fontFamily: 'monospace', mb: 0.5, wordBreak: 'break-all' }}
              >
                {mapping.from}
              </Typography>
              <Stack
                direction="row"
                spacing={1}
                sx={{
                  alignItems: 'flex-start',
                }}
              >
                <TextField
                  fullWidth
                  size="small"
                  label="Local path"
                  placeholder="/local/path/to/media"
                  value={mapping.to}
                  onChange={(e) => {
                    const updated = [...pathMappings];
                    updated[idx] = { ...updated[idx], to: e.target.value };
                    setPathMappings(updated);
                    setMappingTests((prev) => ({ ...prev, [idx]: null }));
                  }}
                />
                <Button
                  size="small"
                  variant="outlined"
                  disabled={!mapping.to || testingMapping === idx}
                  sx={{ whiteSpace: 'nowrap', minWidth: 'auto' }}
                  onClick={async () => {
                    setTestingMapping(idx);
                    try {
                      const result = await api.testITunesPathMapping(
                        iTunesPath,
                        mapping.from,
                        mapping.to
                      );
                      setMappingTests((prev) => ({ ...prev, [idx]: result }));
                    } catch {
                      setMappingTests((prev) => ({
                        ...prev,
                        [idx]: { tested: 0, found: 0, examples: [] },
                      }));
                    } finally {
                      setTestingMapping(null);
                    }
                  }}
                >
                  {testingMapping === idx ? 'Testing...' : 'Test'}
                </Button>
              </Stack>
              {mappingTests[idx] && (
                <Box sx={{ mt: 0.5, pl: 1 }}>
                  <Typography
                    variant="body2"
                    color={mappingTests[idx]!.found > 0 ? 'success.main' : 'error.main'}
                  >
                    Found {mappingTests[idx]!.found}/{mappingTests[idx]!.tested} files
                    tested
                  </Typography>
                  {mappingTests[idx]!.examples.map((ex, i) => (
                    <Typography
                      key={i}
                      variant="body2"
                      sx={{
                        fontFamily: 'monospace',
                        fontSize: '0.75rem',
                        color: 'text.secondary',
                        wordBreak: 'break-all',
                      }}
                    >
                      {ex.title}: {ex.path}
                    </Typography>
                  ))}
                </Box>
              )}
            </Box>
          ))}
          {pathMappings.some((m) => m.to) && (
            <Button
              size="small"
              variant="outlined"
              onClick={async () => {
                setITunesValidating(true);
                setITunesValidation(null);
                try {
                  const result = await api.validateITunesLibrary({
                    library_path: iTunesPath,
                    path_mappings: pathMappings.filter((m) => m.from && m.to),
                  });
                  setITunesValidation({
                    valid: true,
                    audiobook_count: result.audiobook_count || result.audiobook_tracks,
                    files_found: result.files_found,
                  });
                  setITunesEnabled(true);
                } catch (err) {
                  setITunesValidation({
                    valid: false,
                    error: err instanceof Error ? err.message : 'Validation failed',
                  });
                } finally {
                  setITunesValidating(false);
                }
              }}
            >
              Re-validate with path mapping
            </Button>
          )}
        </Box>
      )}

      <Alert severity="info">
        This step is optional. You can skip it and import from iTunes later via the Library
        page.
      </Alert>
    </Box>
  );
}
