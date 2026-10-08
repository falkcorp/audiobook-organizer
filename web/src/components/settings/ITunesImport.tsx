// file: web/src/components/settings/ITunesImport.tsx
// version: 1.24.0
// guid: 4eb9b74d-7192-497b-849a-092833ae63a4
// last-edited: 2026-10-08
import { useEffect, useRef, useState } from 'react';
import {
  Alert,
  AlertTitle,
  Box,
  Button,
  Card,
  CardContent,
  CardHeader,
  Checkbox,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  FormControl,
  FormControlLabel,
  FormLabel,
  LinearProgress,
  List,
  ListItem,
  ListItemText,
  Paper,
  Radio,
  RadioGroup,
  Stack,
  TextField,
  Tooltip,
  Typography,
} from '@mui/material';
import AddIcon from '@mui/icons-material/Add';
import DeleteIcon from '@mui/icons-material/Delete';
import FolderOpenIcon from '@mui/icons-material/FolderOpen';
import CloudUploadIcon from '@mui/icons-material/CloudUpload';
import CheckCircleIcon from '@mui/icons-material/CheckCircle';
import IconButton from '@mui/material/IconButton';
import { ServerFileBrowser } from '../common/ServerFileBrowser';
import {
  cancelOperation,
  getConfig,
  getITunesImportStatus,
  getITunesLibraryStatus,
  getITunesLinkedBookCount,
  importITunesLibrary,
  updateConfig,
  type ITunesImportRequest,
  type ITunesImportStatus,
  type ITunesValidateResponse,
  type PathMapping,
  validateITunesLibrary,
} from '../../services/api';
import { useOperationsStore } from '../../stores/useOperationsStore';
import { isTerminal } from '../../utils/operationPolling';
import { useToast } from '../toast/ToastProvider';
import { STORAGE_KEYS } from '../../lib/storageKeys';

interface ITunesImportSettings {
  libraryPath: string;
  itlPath: string;
  importMode: 'organized' | 'import' | 'organize';
  preserveLocation: boolean;
  importPlaylists: boolean;
  skipDuplicates: boolean;
  pathMappings: PathMapping[];
}

const defaultSettings: ITunesImportSettings = {
  libraryPath: '',
  itlPath: '',
  importMode: 'import',
  preserveLocation: false,
  importPlaylists: true,
  skipDuplicates: true,
  pathMappings: [],
};

/**
 * ITunesImport provides a guided workflow to validate and import iTunes
 * Library.xml metadata into the audiobook organizer.
 */
export function ITunesImport() {
  const { toast } = useToast();
  const [settings, setSettings] = useState<ITunesImportSettings>(() => {
    try {
      const saved = localStorage.getItem(STORAGE_KEYS.ITUNES_IMPORT_SETTINGS);
      if (saved) {
        return { ...defaultSettings, ...JSON.parse(saved) };
      }
    } catch {
      // ignore
    }
    return defaultSettings;
  });
  // Persist settings to localStorage
  useEffect(() => {
    try {
      localStorage.setItem(STORAGE_KEYS.ITUNES_IMPORT_SETTINGS, JSON.stringify(settings));
    } catch {
      // ignore
    }
  }, [settings]);

  const [validationResult, setValidationResult] = useState<ITunesValidateResponse | null>(null);
  const [validating, setValidating] = useState(false);
  const [importing, setImporting] = useState(false);
  const [importStatus, setImportStatus] = useState<ITunesImportStatus | null>(null);
  const [showMissingFiles, setShowMissingFiles] = useState(false);
  const [libraryChanged, setLibraryChanged] = useState(false);
  // Open while asking whether to import again over books already linked.
  const [reimportConfirmOpen, setReimportConfirmOpen] = useState(false);
  const [checkingPriorImport, setCheckingPriorImport] = useState(false);
  const pollTimeoutRef = useRef<number | null>(null);
  const pollingUnmountedRef = useRef(false);

  useEffect(() => {
    return () => {
      pollingUnmountedRef.current = true;
      if (pollTimeoutRef.current) {
        window.clearTimeout(pollTimeoutRef.current);
      }
    };
  }, []);

  // Pre-fill library path from server config when the user hasn't entered
  // one in localStorage.
  useEffect(() => {
    getConfig()
      .then((cfg) => {
        if (cfg.itunes_library_read_path) {
          setSettings((prev) => {
            if (!prev.libraryPath) {
              return { ...prev, libraryPath: cfg.itunes_library_read_path! };
            }
            return prev;
          });
        }
      })
      .catch(() => {
        /* ignore — the field just stays empty */
      });
  }, []); // eslint-disable-line react-hooks/exhaustive-deps

  // Poll library status when a library path is configured
  useEffect(() => {
    const itunesPath = settings.libraryPath;
    if (!itunesPath) return;

    let isMounted = true;

    const checkStatus = async () => {
      try {
        const status = await getITunesLibraryStatus(itunesPath);
        if (isMounted) {
          setLibraryChanged(status.changed_since_import === true);
        }
      } catch {
        // Silently ignore — library status is non-critical
      }
    };

    checkStatus();
    const interval = setInterval(checkStatus, 30000);
    return () => {
      isMounted = false;
      clearInterval(interval);
    };
  }, [settings.libraryPath]);

  // Detect an already-running iTunes import on mount — read from v2 store (UOS-13).
  useEffect(() => {
    let cancelled = false;
    const detectRunningImport = async () => {
      const ops = useOperationsStore.getState().activeOperations;
      if (cancelled) return;
      // Match on def_id, not type. The store is fed from operations v2, where
      // `type` is the def_id's TAIL segment ("itunes.import" → "import"), so
      // the old `op.type === 'itunes_import'` compared against a v1 type string
      // that this store never produces and silently never matched.
      const running = ops.find((op) => op.def_id === 'itunes.import' && !isTerminal(op.status));
      if (running) {
        setImporting(true);
        await pollImportStatus(running.id);
      }
    };
    detectRunningImport();
    return () => {
      cancelled = true;
    };
  }, []); // eslint-disable-line react-hooks/exhaustive-deps

  const [browseTarget, setBrowseTarget] = useState<'xml' | 'itl' | null>(null);

  const handleBrowseFile = () => setBrowseTarget('xml');
  const handleBrowseItl = () => setBrowseTarget('itl');

  const handleBrowseSelect = (path: string) => {
    if (browseTarget === 'xml') {
      setSettings((prev) => ({ ...prev, libraryPath: path }));
    } else if (browseTarget === 'itl') {
      setSettings((prev) => ({ ...prev, itlPath: path }));
      updateConfig({ itunes_library_write_path: path }).catch(() => {});
    }
    setBrowseTarget(null);
  };

  const handleValidate = async () => {
    setValidating(true);

    setValidationResult(null);

    try {
      const activeMappings = settings.pathMappings.filter((m) => m.from && m.to);
      const result = await validateITunesLibrary({
        library_path: settings.libraryPath,
        path_mappings: activeMappings.length > 0 ? activeMappings : undefined,
      });
      setValidationResult(result);
      // Auto-populate path mappings from detected prefixes
      if (result.path_prefixes?.length && settings.pathMappings.length === 0) {
        setSettings((prev) => ({
          ...prev,
          pathMappings: result.path_prefixes!.map((p) => ({ from: p, to: '' })),
        }));
      }
    } catch (err) {
      const message = err instanceof Error ? err.message : 'Validation failed';
      toast(message, 'error');
    } finally {
      setValidating(false);
    }
  };

  // Import is the only iTunes action, and it can be run again: each album is
  // matched to an existing book by iTunes ID, then by file path, before a new
  // book is added. When books are already linked to iTunes, the repeat run is
  // confirmed first, because matching cannot catch everything.
  const handleImportClick = async () => {
    setCheckingPriorImport(true);
    let linkedCount: number;
    try {
      linkedCount = await getITunesLinkedBookCount();
    } catch {
      // Could not tell whether a previous import exists: warn rather than
      // skip the warning.
      linkedCount = 1;
    } finally {
      setCheckingPriorImport(false);
    }
    if (linkedCount > 0) {
      setReimportConfirmOpen(true);
      return;
    }
    await startImport();
  };

  const startImport = async () => {
    setImporting(true);

    setImportStatus(null);

    try {
      const request: ITunesImportRequest = {
        library_path: settings.libraryPath,
        import_mode: settings.importMode,
        preserve_location: settings.preserveLocation,
        import_playlists: settings.importPlaylists,
        skip_duplicates: settings.skipDuplicates,
        path_mappings: settings.pathMappings.filter((m) => m.from && m.to),
      };

      const result = await importITunesLibrary(request);
      useOperationsStore.getState().startPolling(result.operation_id, 'itunes_import');
      await pollImportStatus(result.operation_id);
    } catch (err) {
      const message = err instanceof Error ? err.message : 'Import failed';
      toast(message, 'error');
      setImporting(false);
    }
  };


  const pollImportStatus = async (operationId: string) => {
    const poll = async () => {
      try {
        const status = await getITunesImportStatus(operationId);
        if (!pollingUnmountedRef.current) {
          setImportStatus(status);
        }

        // Must cover every terminal status, not just completed/failed: the
        // panel offers a Cancel button, and an interrupted server leaves the op
        // at one of the interrupted_* statuses. Either one used to fall through
        // and re-arm the 2s timer forever on an op that had already finished.
        if (isTerminal(status.status)) {
          if (!pollingUnmountedRef.current) {
            setImporting(false);
          }
          return;
        }

        if (!pollingUnmountedRef.current) {
          pollTimeoutRef.current = window.setTimeout(poll, 2000);
        }
      } catch (err) {
        const message = err instanceof Error ? err.message : 'Failed to get import status';
        if (!pollingUnmountedRef.current) {
          toast(message, 'error');
          setImporting(false);
        }
      }
    };

    await poll();
  };

  return (
    <Card>
      <CardHeader title="iTunes Library Import" />
      <CardContent>
        <Typography
          variant="body2"
          gutterBottom
          sx={{
            color: 'text.secondary',
          }}
        >
          Import your iTunes Library.xml with play counts, ratings, and bookmarks preserved.
        </Typography>

        {libraryChanged && (
          <Alert severity="warning" sx={{ mt: 2 }}>
            iTunes library has been modified since last import. Consider re-importing to pick up
            changes.
          </Alert>
        )}

        <Box sx={{ mt: 3 }}>
          <TextField
            label="iTunes Library Path"
            value={settings.libraryPath}
            onChange={(event) =>
              setSettings((prev) => ({
                ...prev,
                libraryPath: event.target.value,
              }))
            }
            fullWidth
            placeholder="/Users/username/Music/iTunes/iTunes Music Library.xml"
            helperText="Path to iTunes Library.xml or Music Library.xml"
            slotProps={{
              input: {
                endAdornment: (
                  <Button startIcon={<FolderOpenIcon />} onClick={handleBrowseFile}>
                    Browse
                  </Button>
                ),
              },
            }}
          />
        </Box>

        <Box sx={{ mt: 2 }}>
          <TextField
            label="iTunes Library ITL Path (optional)"
            value={settings.itlPath}
            onChange={(event) => {
              const itlPath = event.target.value;
              setSettings((prev) => ({ ...prev, itlPath }));
              updateConfig({ itunes_library_write_path: itlPath }).catch(() => {});
            }}
            fullWidth
            placeholder="/path/to/iTunes Library.itl"
            helperText="Path to the iTunes Library.itl binary file. Read only: used for the PID integrity check and the library download. Nothing writes to it."
            slotProps={{
              input: {
                endAdornment: (
                  <Button startIcon={<FolderOpenIcon />} onClick={handleBrowseItl}>
                    Browse
                  </Button>
                ),
              },
            }}
          />
        </Box>

        <Box sx={{ mt: 3 }}>
          <Typography variant="subtitle2" gutterBottom>
            Path Mapping (for cross-platform imports)
          </Typography>
          <Typography
            variant="body2"
            sx={{
              color: 'text.secondary',
              mb: 1,
            }}
          >
            Map iTunes file prefixes to local paths. Validate to auto-detect, or add manually.
          </Typography>
          {settings.pathMappings.map((mapping, idx) => (
            <Box key={idx} sx={{ display: 'flex', gap: 1, mb: 1.5, alignItems: 'flex-start' }}>
              <Box sx={{ flex: 1 }}>
                <TextField
                  fullWidth
                  size="small"
                  label="From prefix"
                  placeholder="file://localhost/W:/itunes/iTunes%20Media"
                  value={mapping.from}
                  onChange={(e) => {
                    const updated = [...settings.pathMappings];
                    updated[idx] = { ...updated[idx], from: e.target.value };
                    setSettings((prev) => ({ ...prev, pathMappings: updated }));
                  }}
                />
              </Box>
              <Box sx={{ flex: 1 }}>
                <TextField
                  fullWidth
                  size="small"
                  label="To local path"
                  placeholder="/local/path/to/media"
                  value={mapping.to}
                  onChange={(e) => {
                    const updated = [...settings.pathMappings];
                    updated[idx] = { ...updated[idx], to: e.target.value };
                    setSettings((prev) => ({ ...prev, pathMappings: updated }));
                  }}
                />
              </Box>
              <IconButton
                size="small"
                color="error"
                onClick={() => {
                  setSettings((prev) => ({
                    ...prev,
                    pathMappings: prev.pathMappings.filter((_, i) => i !== idx),
                  }));
                }}
              >
                <DeleteIcon fontSize="small" />
              </IconButton>
            </Box>
          ))}
          <Button
            size="small"
            startIcon={<AddIcon />}
            onClick={() =>
              setSettings((prev) => ({
                ...prev,
                pathMappings: [...prev.pathMappings, { from: '', to: '' }],
              }))
            }
          >
            Add Mapping
          </Button>
        </Box>

        <Box sx={{ mt: 3 }}>
          <FormControl component="fieldset">
            <FormLabel component="legend">Import Mode</FormLabel>
            <RadioGroup
              value={settings.importMode}
              onChange={(event) =>
                setSettings((prev) => ({
                  ...prev,
                  importMode: event.target.value as ITunesImportSettings['importMode'],
                }))
              }
            >
              <Tooltip
                title="Import all metadata (titles, authors, play counts, ratings, bookmarks) but mark files as already in their final location. Use this if your iTunes library folder structure is how you want it."
                placement="right"
                arrow
              >
                <FormControlLabel
                  value="organized"
                  control={<Radio />}
                  label="Files already organized"
                />
              </Tooltip>
              <Tooltip
                title="Import all metadata into the database but leave files where they are. You can organize them later from the Library page. Good for previewing what will be imported before moving anything."
                placement="right"
                arrow
              >
                <FormControlLabel value="import" control={<Radio />} label="Import metadata only" />
              </Tooltip>
              <Tooltip
                title="Import all metadata AND move/rename files into the organized folder structure (Author/Series/Title). Files are copied to the root directory. Skips files that are already organized."
                placement="right"
                arrow
              >
                <FormControlLabel
                  value="organize"
                  control={<Radio />}
                  label="Import and organize now"
                />
              </Tooltip>
            </RadioGroup>
          </FormControl>

          <Box sx={{ mt: 2 }}>
            <Tooltip
              title="Don't move files during organize — only update the database with their current locations."
              placement="right"
              arrow
            >
              <FormControlLabel
                control={
                  <Checkbox
                    checked={settings.preserveLocation}
                    onChange={(event) =>
                      setSettings((prev) => ({
                        ...prev,
                        preserveLocation: event.target.checked,
                      }))
                    }
                  />
                }
                label="Preserve original file locations"
              />
            </Tooltip>
          </Box>

          <Box>
            <Tooltip
              title="Convert iTunes playlist memberships into tags on each audiobook."
              placement="right"
              arrow
            >
              <FormControlLabel
                control={
                  <Checkbox
                    checked={settings.importPlaylists}
                    onChange={(event) =>
                      setSettings((prev) => ({
                        ...prev,
                        importPlaylists: event.target.checked,
                      }))
                    }
                  />
                }
                label="Import playlists as tags"
              />
            </Tooltip>
          </Box>

          <Box>
            <Tooltip
              title="Skip audiobooks that already exist in the library (matched by file path or file hash). Uncheck to re-import and overwrite existing entries."
              placement="right"
              arrow
            >
              <FormControlLabel
                control={
                  <Checkbox
                    checked={settings.skipDuplicates}
                    onChange={(event) =>
                      setSettings((prev) => ({
                        ...prev,
                        skipDuplicates: event.target.checked,
                      }))
                    }
                  />
                }
                label="Skip duplicates already in library"
              />
            </Tooltip>
          </Box>
        </Box>

        <Box sx={{ mt: 3 }}>
          <Button
            variant="outlined"
            onClick={handleValidate}
            disabled={!settings.libraryPath || validating || importing}
            startIcon={validating ? undefined : <CheckCircleIcon />}
          >
            {validating ? 'Validating...' : 'Validate Import'}
          </Button>
        </Box>

        {validationResult && (
          <Alert
            severity={validationResult.files_missing > 0 ? 'warning' : 'success'}
            sx={{ mt: 2 }}
          >
            <AlertTitle>Validation Results</AlertTitle>
            <Typography variant="body2">
              Found{' '}
              <strong>
                {validationResult.audiobook_count || validationResult.audiobook_tracks}
              </strong>{' '}
              audiobooks ({validationResult.audiobook_tracks} tracks across{' '}
              {validationResult.files_found} files found,
              {` ${validationResult.files_missing} missing`})
            </Typography>
            {validationResult.duplicate_count > 0 && (
              <Typography variant="body2" sx={{ mt: 1 }}>
                {validationResult.duplicate_count} potential duplicates detected
              </Typography>
            )}
            <Typography variant="body2" sx={{ mt: 1 }}>
              Estimated import time: {validationResult.estimated_import_time}
            </Typography>
            {validationResult.files_missing > 0 && (
              <Button size="small" onClick={() => setShowMissingFiles(true)} sx={{ mt: 1 }}>
                View Missing Files
              </Button>
            )}
          </Alert>
        )}

        <Box sx={{ mt: 3 }}>
          <Button
            variant="contained"
            onClick={handleImportClick}
            disabled={!validationResult || importing || checkingPriorImport}
            startIcon={importing ? undefined : <CloudUploadIcon />}
          >
            {importing ? 'Importing...' : 'Import iTunes library'}
          </Button>
        </Box>

        {importStatus && (
          <Paper variant="outlined" sx={{ mt: 3, p: 2 }}>
            <Stack
              direction="row"
              sx={{
                justifyContent: 'space-between',
                alignItems: 'center',
                mb: 1,
              }}
            >
              <Typography variant="subtitle2">Import Progress</Typography>
              {importing && (
                <Button
                  size="small"
                  color="error"
                  variant="outlined"
                  onClick={async () => {
                    try {
                      await cancelOperation(importStatus.operation_id);
                      setImporting(false);
                    } catch {
                      toast('Failed to cancel import', 'error');
                    }
                  }}
                >
                  Cancel Import
                </Button>
              )}
            </Stack>

            <LinearProgress
              variant="determinate"
              value={importStatus.progress}
              sx={{ height: 8, borderRadius: 1, mb: 1 }}
            />

            <Stack
              direction="row"
              sx={{
                justifyContent: 'space-between',
                alignItems: 'center',
              }}
            >
              <Typography
                variant="body2"
                sx={{
                  color: 'text.secondary',
                }}
              >
                {importStatus.progress}% complete
                {importStatus.processed !== undefined && importStatus.total_books !== undefined && (
                  <>
                    {' '}
                    &mdash; {importStatus.processed} / {importStatus.total_books} books
                  </>
                )}
              </Typography>
              <Stack direction="row" spacing={1}>
                {importStatus.linked !== undefined && importStatus.linked > 0 && (
                  <Typography
                    variant="caption"
                    sx={{
                      color: 'info.main',
                    }}
                  >
                    {importStatus.linked} linked
                  </Typography>
                )}
                {importStatus.imported !== undefined && importStatus.imported > 0 && (
                  <Typography
                    variant="caption"
                    sx={{
                      color: 'success.main',
                    }}
                  >
                    {importStatus.imported} added
                  </Typography>
                )}
                {importStatus.skipped !== undefined && importStatus.skipped > 0 && (
                  <Typography
                    variant="caption"
                    sx={{
                      color: 'text.secondary',
                    }}
                  >
                    {importStatus.skipped} skipped
                  </Typography>
                )}
                {importStatus.failed !== undefined && importStatus.failed > 0 && (
                  <Typography
                    variant="caption"
                    sx={{
                      color: 'error.main',
                    }}
                  >
                    {importStatus.failed} failed
                  </Typography>
                )}
              </Stack>
            </Stack>

            {/* Show current item from message */}
            {importStatus.message && (
              <Typography
                variant="caption"
                noWrap
                title={importStatus.message}
                sx={{
                  color: 'text.secondary',
                  display: 'block',
                  mt: 0.5,
                }}
              >
                {importStatus.message}
              </Typography>
            )}

            {importStatus.status === 'completed' && (
              <Alert severity="success" sx={{ mt: 2 }}>
                <AlertTitle>Import Complete</AlertTitle>
                <Typography variant="body2" data-testid="itunes-import-result">
                  Linked <strong>{importStatus.linked ?? 0}</strong>, added{' '}
                  <strong>{importStatus.imported ?? 0}</strong>, skipped{' '}
                  <strong>{importStatus.skipped ?? 0}</strong>
                  {importStatus.failed !== undefined && importStatus.failed > 0
                    ? `, ${importStatus.failed} failed`
                    : ''}
                </Typography>
              </Alert>
            )}

            {importStatus.status === 'failed' && (
              <Alert severity="error" sx={{ mt: 2 }}>
                <AlertTitle>Import Failed</AlertTitle>
                <Typography variant="body2">{importStatus.message}</Typography>
              </Alert>
            )}
          </Paper>
        )}

        <Dialog
          open={showMissingFiles}
          onClose={() => setShowMissingFiles(false)}
          maxWidth="md"
          fullWidth
        >
          <DialogTitle>Missing Files ({validationResult?.files_missing ?? 0} total)</DialogTitle>
          <DialogContent>
            {(validationResult?.files_missing ?? 0) > 100 && (
              <Alert severity="info" sx={{ mb: 2 }}>
                Showing first 100 of {validationResult?.files_missing} missing files. If these paths
                are from a different OS, use the Path Mapping fields above to translate them to
                local paths.
              </Alert>
            )}
            <List dense>
              {validationResult?.missing_paths?.map((path) => (
                <ListItem key={path}>
                  <ListItemText
                    primary={path}
                    slotProps={{
                      primary: { variant: 'body2', noWrap: true },
                    }}
                  />
                </ListItem>
              ))}
            </List>
          </DialogContent>
          <DialogActions>
            <Button onClick={() => setShowMissingFiles(false)}>Close</Button>
          </DialogActions>
        </Dialog>

        <Dialog
          open={reimportConfirmOpen}
          onClose={() => setReimportConfirmOpen(false)}
          aria-labelledby="itunes-reimport-title"
        >
          <DialogTitle id="itunes-reimport-title">Import iTunes library again?</DialogTitle>
          <DialogContent>
            <Typography>
              You&apos;ve imported from iTunes before. We match each album to your existing books
              by iTunes ID, then by file path. Albums iTunes has re-created with new IDs, or whose
              files moved, can&apos;t be matched and will be added as new books, so you may see
              duplicates. Nothing in iTunes is changed, and no files are moved.
            </Typography>
          </DialogContent>
          <DialogActions>
            <Button onClick={() => setReimportConfirmOpen(false)}>Cancel</Button>
            <Button
              variant="contained"
              onClick={() => {
                setReimportConfirmOpen(false);
                void startImport();
              }}
            >
              Import anyway
            </Button>
          </DialogActions>
        </Dialog>

        {/* File browser dialog for selecting XML/ITL paths */}
        <Dialog
          open={browseTarget !== null}
          onClose={() => setBrowseTarget(null)}
          maxWidth="md"
          fullWidth
        >
          <DialogTitle>
            {browseTarget === 'itl'
              ? 'Select iTunes Library ITL File'
              : 'Select iTunes Library XML File'}
          </DialogTitle>
          <DialogContent sx={{ height: 500, p: 0 }}>
            <ServerFileBrowser
              initialPath={
                browseTarget === 'itl' ? settings.itlPath || '/' : settings.libraryPath || '/'
              }
              showFiles
              allowFileSelect
              allowDirSelect={false}
              onSelect={(path) => handleBrowseSelect(path)}
            />
          </DialogContent>
          <DialogActions>
            <Button onClick={() => setBrowseTarget(null)}>Cancel</Button>
          </DialogActions>
        </Dialog>
      </CardContent>
    </Card>
  );
}
