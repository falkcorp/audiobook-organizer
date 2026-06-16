// file: web/src/components/wizard/WelcomeWizard.tsx
// version: 1.5.0
// guid: 8b9c0d1e-2f3a-4b5c-6d7e-8f9a0b1c2d3e
// last-edited: 2026-06-15

import { useState, useEffect } from 'react';
import {
  Dialog,
  DialogTitle,
  DialogContent,
  DialogActions,
  Button,
  Stepper,
  Step,
  StepLabel,
  Typography,
  TextField,
  Box,
  Alert,
  InputAdornment,
  Chip,
  CircularProgress,
  FormControl,
  FormControlLabel,
  FormLabel,
  Radio,
  RadioGroup,
  Stack,
} from '@mui/material';
import {
  Folder as FolderIcon,
  Settings as SettingsIcon,
  CheckCircle as CheckCircleIcon,
} from '@mui/icons-material';
import { ServerFileBrowser } from '../common/ServerFileBrowser';
import { ToolsPanel } from '../tools/ToolsPanel';
import * as api from '../../services/api';
import { STORAGE_KEYS } from '../../lib/storageKeys';

interface WelcomeWizardProps {
  open: boolean;
  onComplete: () => void;
}

/**
 * WelcomeWizard component for first-run setup
 *
 * Guides users through initial configuration:
 * 1. Set library path (where organized books go)
 * 2. Optional OpenAI API key setup
 * 3. Add import/download folder paths
 */
export function WelcomeWizard({ open, onComplete }: WelcomeWizardProps) {
  const [activeStep, setActiveStep] = useState(0);
  const [libraryPath, setLibraryPath] = useState('');
  const [homePath, setHomePath] = useState('');
  const [libraryPathSelection, setLibraryPathSelection] = useState('');
  const [showLibraryBrowser, setShowLibraryBrowser] = useState(false);
  const [openaiKey, setOpenaiKey] = useState('');
  const [testingKey, setTestingKey] = useState(false);
  const [keyTestResult, setKeyTestResult] = useState<
    'success' | 'error' | null
  >(null);
  const [importFolders, setImportFolders] = useState<string[]>([]);
  const [importFolderSelection, setImportFolderSelection] = useState('');
  const [showImportBrowser, setShowImportBrowser] = useState(false);
  const [saving, setSaving] = useState(false);
  const [iTunesPath, setITunesPath] = useState('');
  const [iTunesPathSelection, setITunesPathSelection] = useState('');
  const [showITunesBrowser, setShowITunesBrowser] = useState(false);
  const [iTunesValidating, setITunesValidating] = useState(false);
  const [iTunesValidation, setITunesValidation] = useState<{
    valid: boolean;
    audiobook_count?: number;
    files_found?: number;
    error?: string;
  } | null>(null);
  const [iTunesEnabled, setITunesEnabled] = useState(false);
  const [iTunesImportMode, setITunesImportMode] = useState<'import' | 'organize'>('import');
  const [pathMappings, setPathMappings] = useState<api.PathMapping[]>([]);
  const [mappingTests, setMappingTests] = useState<Record<number, api.ITunesTestMappingResponse | null>>({});
  const [testingMapping, setTestingMapping] = useState<number | null>(null);
  const [appVersion, setAppVersion] = useState('');
  const [toolInstallChoice, setToolInstallChoice] = useState<'recommended' | 'custom'>('recommended');

  useEffect(() => {
    api.getAppVersion().then(setAppVersion);
  }, []);

  const steps = [
    'Library Path',
    'Tools',
    'AI Setup (Optional)',
    'iTunes Library',
    'Import Folders',
  ];

  useEffect(() => {
    if (!open) return;
    let cancelled = false;

    const loadHomePath = async () => {
      try {
        const path = await api.getHomeDirectory();
        if (cancelled) return;
        setHomePath(path);
        setLibraryPath((prev) => ((prev || '').trim() ? prev : path));
      } catch (_error) {
        if (cancelled) return;
        setHomePath((prev) => ((prev || '').trim() ? prev : '/'));
        setLibraryPath((prev) => ((prev || '').trim() ? prev : '/'));
      }
    };

    loadHomePath();

    return () => {
      cancelled = true;
    };
  }, [open]);

  const handleNext = () => {
    setActiveStep((prev) => prev + 1);
  };

  const handleBack = () => {
    setActiveStep((prev) => prev - 1);
  };

  const handleOpenLibraryBrowser = () => {
    setLibraryPathSelection(libraryPath || homePath || '/');
    setShowLibraryBrowser(true);
  };

  const handleLibraryPathSelect = (path: string) => {
    setLibraryPathSelection(path);
  };

  const handleConfirmLibraryPath = () => {
    const trimmed = (libraryPathSelection || '').trim();
    if (trimmed) {
      setLibraryPath(trimmed);
    }
    setShowLibraryBrowser(false);
  };

  const handleTestOpenAIKey = async () => {
    if (!(openaiKey || '').trim()) {
      setKeyTestResult('error');
      return;
    }

    setTestingKey(true);
    setKeyTestResult(null);

    try {
      // Test the key by making a simple API call
      const response = await fetch('https://api.openai.com/v1/models', {
        headers: {
          Authorization: `Bearer ${openaiKey}`,
        },
      });

      if (response.ok) {
        setKeyTestResult('success');
      } else {
        setKeyTestResult('error');
      }
    } catch (_error) {
      setKeyTestResult('error');
    } finally {
      setTestingKey(false);
    }
  };

  const handleOpenImportBrowser = () => {
    setImportFolderSelection(homePath || '/');
    setShowImportBrowser(true);
  };

  const handleImportFolderSelect = (path: string) => {
    setImportFolderSelection(path);
  };

  const handleConfirmImportFolder = () => {
    const trimmed = (importFolderSelection || '').trim();
    if (trimmed) {
      setImportFolders((prev) =>
        prev.includes(trimmed) ? prev : [...prev, trimmed]
      );
    }
    setShowImportBrowser(false);
  };

  const handleRemoveImportFolder = (index: number) => {
    setImportFolders(importFolders.filter((_, i) => i !== index));
  };

  const handleOpenITunesBrowser = () => {
    setITunesPathSelection(iTunesPath || homePath || '/');
    setShowITunesBrowser(true);
  };

  const handleConfirmITunesPath = async () => {
    const trimmed = (iTunesPathSelection || '').trim();
    if (!trimmed) return;
    setITunesPath(trimmed);
    setShowITunesBrowser(false);

    // Validate the iTunes library
    setITunesValidating(true);
    setITunesValidation(null);
    try {
      const activeMappings = pathMappings.filter((m) => m.from && m.to);
      const result = await api.validateITunesLibrary({
        library_path: trimmed,
        path_mappings: activeMappings.length > 0 ? activeMappings : undefined,
      });
      setITunesValidation({
        valid: true,
        audiobook_count: result.audiobook_count || result.audiobook_tracks,
        files_found: result.files_found,
      });
      // Auto-populate path mappings from detected prefixes
      if (result.path_prefixes?.length && pathMappings.length === 0) {
        setPathMappings(result.path_prefixes.map((p) => ({ from: p, to: '' })));
      }
      setITunesEnabled(true);
    } catch (error) {
      setITunesValidation({
        valid: false,
        error: error instanceof Error ? error.message : 'Validation failed',
      });
    } finally {
      setITunesValidating(false);
    }
  };

  const handleComplete = async () => {
    setSaving(true);

    try {
      // Step 1: Save library path to config
      await api.updateConfig({
        root_dir: libraryPath,
        playlist_dir: `${libraryPath}/playlists`,
        setup_complete: true,
      });

      // Step 2: Save OpenAI key if provided
      if ((openaiKey || '').trim()) {
        await api.updateConfig({
          openai_api_key: openaiKey,
          enable_ai_parsing: true,
        });
      }

      // Step 3: Add import folders
      for (const path of importFolders) {
        try {
          await api.addImportPath(path, path.split('/').pop() || path);
        } catch (error) {
          console.error(`Failed to add import folder ${path}:`, error);
        }
      }

      // Step 4: Trigger iTunes import if configured
      if (iTunesEnabled && iTunesPath) {
        // Persist iTunes settings so the Settings page can see them
        const activeMappings = pathMappings.filter((m) => m.from && m.to);
        try {
          localStorage.setItem(STORAGE_KEYS.ITUNES_IMPORT_SETTINGS, JSON.stringify({
            libraryPath: iTunesPath,
            importMode: iTunesImportMode,
            preserveLocation: iTunesImportMode === 'import',
            importPlaylists: true,
            skipDuplicates: true,
            pathMappings: activeMappings,
          }));
        } catch {
          // ignore localStorage errors
        }

        try {
          await api.importITunesLibrary({
            library_path: iTunesPath,
            import_mode: iTunesImportMode,
            preserve_location: iTunesImportMode === 'import',
            import_playlists: true,
            skip_duplicates: true,
            path_mappings: activeMappings,
          });
        } catch (error) {
          console.error('Failed to start iTunes import:', error);
        }
      }

      // Mark wizard as completed (store in localStorage for now)
      localStorage.setItem(STORAGE_KEYS.WELCOME_WIZARD_COMPLETED, 'true');

      onComplete();
    } catch (error) {
      console.error('Failed to complete setup:', error);
      alert('Failed to save settings. Please check the console for details.');
    } finally {
      setSaving(false);
    }
  };

  const canProceed = () => {
    switch (activeStep) {
      case 0:
        return (libraryPath || '').trim() !== '';
      case 1:
        return true; // Tools step is optional
      case 2:
        return true; // AI Setup is optional
      case 3:
        return true; // iTunes is optional
      case 4:
        return true; // Import folders are optional
      default:
        return false;
    }
  };

  return (
    <>
      <Dialog open={open} maxWidth="md" fullWidth disableEscapeKeyDown>
        <DialogTitle>
          <Box sx={{ display: 'flex', alignItems: 'center', gap: 1 }}>
            <SettingsIcon />
            <Typography variant="h6">Welcome to Audiobook Organizer</Typography>
            {appVersion && appVersion !== 'unknown' && (
              <Chip label={`v${appVersion}`} size="small" color="primary" variant="outlined" />
            )}
          </Box>
        </DialogTitle>

        <DialogContent>
          <Stepper activeStep={activeStep} sx={{ mb: 4 }}>
            {steps.map((label) => (
              <Step key={label}>
                <StepLabel>{label}</StepLabel>
              </Step>
            ))}
          </Stepper>

          {/* Step 1: Library Path */}
          {activeStep === 0 && (
            <Box>
              <Typography variant="h6" gutterBottom>
                Set Your Library Path
              </Typography>
              <Typography variant="body2" color="text.secondary" paragraph>
                This is where your organized audiobooks will be stored. The app
                will create a structured folder hierarchy here based on your
                naming patterns.
              </Typography>

              <TextField
                fullWidth
                label="Library Path"
                value={libraryPath}
                onChange={(e) => setLibraryPath(e.target.value)}
                placeholder="/path/to/audiobooks/library"
                sx={{ mb: 2 }}
                InputProps={{
                  endAdornment: (
                    <InputAdornment position="end">
                      <Button
                        variant="outlined"
                        size="small"
                        onClick={handleOpenLibraryBrowser}
                      >
                        Browse
                      </Button>
                    </InputAdornment>
                  ),
                }}
              />

              <Alert severity="info">
                Choose a location with plenty of storage space. This will be the
                permanent home for your organized audiobook collection.
              </Alert>
            </Box>
          )}

          {/* Step 2: Tools */}
          {activeStep === 1 && (
            <Box>
              <Typography variant="h6" gutterBottom>
                Set up AI &amp; fingerprinting tools
              </Typography>
              <Typography variant="body2" color="text.secondary" mb={2}>
                These tools power local AI deduplication and audio fingerprint matching.
                Recommended installs everything automatically.
              </Typography>

              <FormControl component="fieldset" sx={{ mb: 2 }}>
                <RadioGroup
                  value={toolInstallChoice}
                  onChange={e => setToolInstallChoice(e.target.value as 'recommended' | 'custom')}
                >
                  <FormControlLabel
                    value="recommended"
                    control={<Radio />}
                    label={
                      <Stack>
                        <Typography>Install recommended tools</Typography>
                        <Typography variant="caption" color="text.secondary">
                          Ollama (local AI, ~5GB) + fpcalc (fingerprinting, ~2MB)
                        </Typography>
                      </Stack>
                    }
                  />
                  <FormControlLabel
                    value="custom"
                    control={<Radio />}
                    label="Let me choose"
                  />
                </RadioGroup>
              </FormControl>

              {toolInstallChoice === 'custom' && (
                <ToolsPanel mode="wizard" />
              )}

              <Button
                variant="text"
                size="small"
                onClick={() => setActiveStep(prev => prev + 1)}
                sx={{ mt: 1 }}
              >
                Skip — I'll set this up later
              </Button>
            </Box>
          )}

          {/* Step 3: OpenAI API Key */}
          {activeStep === 2 && (
            <Box>
              <Typography variant="h6" gutterBottom>
                AI-Powered Metadata (Optional)
              </Typography>
              <Typography variant="body2" color="text.secondary" paragraph>
                Enable AI-powered author name parsing and metadata enhancement
                with OpenAI. This is optional and can be configured later in
                Settings.
              </Typography>

              <TextField
                fullWidth
                label="OpenAI API Key"
                type="password"
                value={openaiKey}
                onChange={(e) => setOpenaiKey(e.target.value)}
                placeholder="sk-..."
                sx={{ mb: 2 }}
                helperText="Get your API key from platform.openai.com"
              />

              <Button
                variant="outlined"
                onClick={handleTestOpenAIKey}
                disabled={!(openaiKey || '').trim() || testingKey}
                startIcon={testingKey ? <CircularProgress size={16} /> : null}
                sx={{ mb: 2 }}
              >
                Test Connection
              </Button>

              {keyTestResult === 'success' && (
                <Alert severity="success" sx={{ mb: 2 }}>
                  API key is valid and working!
                </Alert>
              )}

              {keyTestResult === 'error' && (
                <Alert severity="error" sx={{ mb: 2 }}>
                  Invalid API key or connection failed. Please check your key.
                </Alert>
              )}

              <Alert severity="info">
                You can skip this step and configure it later in Settings if
                needed.
              </Alert>
            </Box>
          )}

          {/* Step 4: iTunes Library */}
          {activeStep === 3 && (
            <Box>
              <Typography variant="h6" gutterBottom>
                Import from iTunes Library
              </Typography>
              <Typography variant="body2" color="text.secondary" paragraph>
                If you have audiobooks in iTunes/Apple Books, you can import them
                automatically. Point to your iTunes Library.xml file to get
                started.
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
                    InputProps={{
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
                    Found {iTunesValidation.audiobook_count} audiobook tracks
                    ({iTunesValidation.files_found} files found). These will be
                    processed in the background when you complete setup.
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

              {iTunesValidation?.valid && iTunesValidation.files_found === 0 && (iTunesValidation.audiobook_count ?? 0) > 0 && (
                <Alert severity="warning" sx={{ mb: 2 }}>
                  No files found on disk. If your iTunes library was created on a
                  different machine (e.g. Windows), use path mapping below to
                  translate the original paths to local paths.
                </Alert>
              )}

              {pathMappings.length > 0 && (
                <Box sx={{ mb: 2 }}>
                  <Typography variant="subtitle2" sx={{ mb: 1 }}>
                    Path Mapping
                  </Typography>
                  <Typography variant="body2" color="text.secondary" sx={{ mb: 1.5 }}>
                    Map each iTunes path prefix to its local equivalent on this server.
                  </Typography>
                  {pathMappings.map((mapping, idx) => (
                    <Box key={idx} sx={{ mb: 2 }}>
                      <Typography variant="body2" sx={{ fontFamily: 'monospace', mb: 0.5, wordBreak: 'break-all' }}>
                        {mapping.from}
                      </Typography>
                      <Stack direction="row" spacing={1} alignItems="flex-start">
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
                              const result = await api.testITunesPathMapping(iTunesPath, mapping.from, mapping.to);
                              setMappingTests((prev) => ({ ...prev, [idx]: result }));
                            } catch {
                              setMappingTests((prev) => ({ ...prev, [idx]: { tested: 0, found: 0, examples: [] } }));
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
                          <Typography variant="body2" color={mappingTests[idx]!.found > 0 ? 'success.main' : 'error.main'}>
                            Found {mappingTests[idx]!.found}/{mappingTests[idx]!.tested} files tested
                          </Typography>
                          {mappingTests[idx]!.examples.map((ex, i) => (
                            <Typography key={i} variant="body2" sx={{ fontFamily: 'monospace', fontSize: '0.75rem', color: 'text.secondary', wordBreak: 'break-all' }}>
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
                This step is optional. You can skip it and import from iTunes
                later via the Library page.
              </Alert>
            </Box>
          )}

          {/* Step 5: Import Folders */}
          {activeStep === 4 && (
            <Box>
              <Typography variant="h6" gutterBottom>
                Add Import Folders
              </Typography>
              <Typography variant="body2" color="text.secondary" paragraph>
                Import folders are watched locations where the scanner looks for
                new audiobooks. Files found here will be scanned and organized
                into your library path.
              </Typography>

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
                You can add import folders later in the Library page if you
                prefer to skip this step.
              </Alert>
            </Box>
          )}
        </DialogContent>

        <DialogActions>
          <Button onClick={handleBack} disabled={activeStep === 0 || saving}>
            Back
          </Button>
          <Box sx={{ flex: 1 }} />
          {activeStep === steps.length - 1 ? (
            <Button
              variant="contained"
              onClick={handleComplete}
              disabled={saving}
              startIcon={
                saving ? <CircularProgress size={16} /> : <CheckCircleIcon />
              }
            >
              {saving ? 'Saving...' : 'Complete Setup'}
            </Button>
          ) : (
            <Button
              variant="contained"
              onClick={handleNext}
              disabled={!canProceed()}
            >
              Next
            </Button>
          )}
        </DialogActions>
      </Dialog>

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
