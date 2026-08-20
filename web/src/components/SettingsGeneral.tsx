// file: web/src/components/SettingsGeneral.tsx
// version: 1.0.4
// guid: 72ebd6f3-7436-4f24-8233-205c50dd05fb
// last-edited: 2026-08-19

import { Dispatch, SetStateAction } from 'react';
import {
  Box,
  Typography,
  TextField,
  Button,
  Grid,
  Switch,
  FormControlLabel,
  Alert,
  Divider,
  MenuItem,
  InputAdornment,
  IconButton,
  List,
  ListItem,
  ListItemIcon,
  ListItemText,
  Chip,
  CircularProgress,
  Stack,
} from '@mui/material';
import {
  FolderOpen as FolderOpenIcon,
  Folder as FolderIcon,
  Add as AddIcon,
  Delete as DeleteIcon,
} from '@mui/icons-material';
import * as api from '../services/api';

interface SettingsState {
  libraryPath: string;
  organizationStrategy: string;
  scanOnStartup: boolean;
  autoOrganize: boolean;
  folderNamingPattern: string;
  fileNamingPattern: string;
  createBackups: boolean;
  supportedExtensions: string[];
  excludePatterns: string[];
  enableDiskQuota: boolean;
  diskQuotaPercent: number;
  enableUserQuotas: boolean;
  defaultUserQuotaGB: number;
  autoFetchMetadata: boolean;
  enableAIParsing: boolean;
  metadataLLMScoringEnabled: boolean;
  openaiApiKey: string;
  metadataSources: any[];
  language: string;
  concurrentScans: number;
  memoryLimitType: string;
  cacheSize: number;
  cacheInvalidateOnBookUpdate: boolean;
  metadataFetchCacheTTLDays: number;
  memoryLimitPercent: number;
  memoryLimitMB: number;
  logLevel: string;
  logFormat: string;
  enableJsonLogging: boolean;
  purgeSoftDeletedAfterDays: number;
  purgeSoftDeletedDeleteFiles: boolean;
  autoUpdateEnabled: boolean;
  autoUpdateChannel: string;
  autoUpdateCheckMinutes: number;
  autoUpdateWindowStart: number;
  autoUpdateWindowEnd: number;
  maintenanceWindowEnabled: boolean;
  maintenanceWindowStart: number;
  maintenanceWindowEnd: number;
  autoRenameOnApply: boolean;
  autoWriteTagsOnApply: boolean;
  verifyAfterWrite: boolean;
  protectedPaths: string;
}

interface ScanStatus {
  status: 'scanning' | 'complete' | 'error' | 'cancelled';
  scanned: number;
  total: number;
  operationId?: string;
  errors?: string[];
}

interface SettingsGeneralProps {
  settings: SettingsState;
  setSettings: Dispatch<SetStateAction<SettingsState>>;
  libraryPathError: string | null;
  handleChange: (field: string, value: string | boolean | number | string[]) => void;
  handleBrowseLibraryPath: () => void;
  extensionsInput: string;
  setExtensionsInput: (value: string) => void;
  extensionsError: string | null;
  handleAddExtension: () => void;
  handleRemoveExtension: (extension: string) => void;
  excludePatternInput: string;
  setExcludePatternInput: (value: string) => void;
  excludePatternError: string | null;
  handleAddExcludePattern: () => void;
  handleRemoveExcludePattern: (pattern: string) => void;
  importPaths?: api.ImportPath[];
  scanStatuses?: Record<number, ScanStatus>;
  handleViewScanErrors?: (folder: api.ImportPath, status: ScanStatus) => void;
  handleRequestCancelScan?: (folder: api.ImportPath) => void;
  handleScanImportFolder?: (folder: api.ImportPath) => void;
  handleRemoveImportFolder?: (id: number) => void;
  setAddFolderDialogOpen?: (value: boolean) => void;
  backupNotice: { severity: 'success' | 'error' | 'info'; message: string } | null;
  createBackupInProgress: boolean;
  handleCreateBackup: () => void;
  backupsLoading: boolean;
  backups: api.BackupInfo[];
  handleRequestRestore: (backup: api.BackupInfo) => void;
  handleRequestDeleteBackup: (backup: api.BackupInfo) => void;
}

export function SettingsGeneral(props: SettingsGeneralProps) {
  const exampleNoSeries = {
    title: 'To Kill a Mockingbird',
    author: 'Harper Lee',
    narrator: 'Sissy Spacek',
    series: '',
    series_number: '',
    print_year: 1960,
    audiobook_release_year: 2014,
    year: 1960,
    publisher: 'Harper Audio',
    edition: 'Unabridged',
    language: 'English',
    isbn13: '9780061808128',
    isbn10: '0061808121',
    track_number: 3,
    total_tracks: 50,
    bitrate: '320kbps',
    codec: 'AAC',
    quality: '320kbps AAC',
  };

  const exampleWithSeries = {
    title: 'The Secret of the Old Clock',
    author: 'Carolyn Keene',
    narrator: 'Laura Linney',
    series: 'Nancy Drew Mystery Stories',
    series_number: '1',
    print_year: 1930,
    audiobook_release_year: 2018,
    year: 1930,
    publisher: 'Listening Library',
    edition: 'Unabridged',
    language: 'English',
    isbn13: '9781524780123',
    isbn10: '1524780120',
    track_number: 1,
    total_tracks: 12,
    bitrate: '128kbps',
    codec: 'MP3',
    quality: '128kbps MP3',
  };

  const generateExample = (
    pattern: string,
    exampleData: typeof exampleNoSeries,
    isFolder: boolean = false
  ) => {
    let result = pattern;
    const replacements: Record<string, string> = {
      '{title}': exampleData.title,
      '{author}': exampleData.author,
      '{narrator}': exampleData.narrator,
      '{series}': exampleData.series || '',
      '{series_number}': exampleData.series_number || '',
      '{print_year}': exampleData.print_year.toString(),
      '{audiobook_release_year}': exampleData.audiobook_release_year.toString(),
      '{year}': exampleData.year.toString(),
      '{publisher}': exampleData.publisher,
      '{edition}': exampleData.edition,
      '{language}': exampleData.language,
      '{isbn13}': exampleData.isbn13,
      '{isbn10}': exampleData.isbn10,
      '{track_number}': exampleData.track_number.toString().padStart(2, '0'),
      '{total_tracks}': exampleData.total_tracks.toString(),
      '{bitrate}': exampleData.bitrate || '',
      '{codec}': exampleData.codec || '',
      '{quality}': exampleData.quality || '',
    };

    Object.entries(replacements).forEach(([key, value]) => {
      result = result.split(key).join(value);
    });

    if (isFolder) {
      result = result
        .split('/')
        .filter((segment) => segment.trim() !== '')
        .join('/');
      return result + '/';
    }

    return result + '.m4b';
  };

  return (
    <Grid container spacing={3}>
      <Grid size={12}>
        <Typography variant="h6" gutterBottom>
          Library Settings
        </Typography>
        <Divider sx={{ mb: 2 }} />
      </Grid>

      <Grid size={12}>
        <TextField
          fullWidth
          label="Library Path"
          value={props.settings.libraryPath}
          onChange={(e) => props.handleChange('libraryPath', e.target.value)}
          error={Boolean(props.libraryPathError)}
          helperText={
            props.libraryPathError ||
            'Main library directory where organized audiobooks are ' +
              'stored. Import paths are configured in File Manager.'
          }
          slotProps={{
            input: {
              endAdornment: (
                <InputAdornment position="end">
                  <Button
                    variant="outlined"
                    size="small"
                    startIcon={<FolderOpenIcon />}
                    onClick={props.handleBrowseLibraryPath}
                  >
                    Browse Server
                  </Button>
                </InputAdornment>
              ),
            },
          }}
        />
        <Alert severity="info" sx={{ mt: 1 }}>
          <Typography variant="caption">
            <strong>Library vs Import Paths:</strong> The library path is where organized audiobooks
            live. Import paths (configured in File Manager) are watched for new files to import into
            the library.
          </Typography>
        </Alert>
      </Grid>

      <Grid size={12}>
        <TextField
          fullWidth
          select
          label="File Organization Strategy"
          value={props.settings.organizationStrategy}
          onChange={(e) => props.handleChange('organizationStrategy', e.target.value)}
          helperText="How files are organized into the library"
        >
          <MenuItem value="auto">Auto (tries Reflink → Hard Link → Copy)</MenuItem>
          <MenuItem value="reflink">Reflink (CoW - fastest, space-efficient)</MenuItem>
          <MenuItem value="hardlink">Hard Link (fast, space-efficient)</MenuItem>
          <MenuItem value="symlink">Symbolic Link (fast, but fragile)</MenuItem>
          <MenuItem value="copy">Copy (slow, uses double space, safest)</MenuItem>
        </TextField>
        <Typography
          variant="caption"
          sx={{
            color: 'text.secondary',
            mt: 1,
            display: 'block',
          }}
        >
          Auto mode tries methods in order of efficiency: Reflink (CoW clone) → Hard Link → Copy as
          fallback.
        </Typography>
      </Grid>

      <Grid size={12}>
        <FormControlLabel
          control={
            <Switch
              checked={props.settings.scanOnStartup}
              onChange={(e) => props.handleChange('scanOnStartup', e.target.checked)}
            />
          }
          label="Scan library on startup"
        />
      </Grid>

      <Grid size={12}>
        <FormControlLabel
          control={
            <Switch
              checked={props.settings.autoOrganize}
              onChange={(e) => props.handleChange('autoOrganize', e.target.checked)}
            />
          }
          label="Automatically organize audiobooks"
        />
      </Grid>

      <Grid size={12}>
        <Typography variant="h6" gutterBottom sx={{ mt: 2 }}>
          Scan Settings
        </Typography>
        <Divider sx={{ mb: 2 }} />
      </Grid>

      <Grid
        size={{
          xs: 12,
          md: 6,
        }}
      >
        <Typography variant="subtitle2" gutterBottom>
          Supported Extensions
        </Typography>
        <Stack spacing={1}>
          <Stack direction="row" spacing={1}>
            <TextField
              fullWidth
              size="small"
              label="Add extension"
              placeholder=".m4b"
              value={props.extensionsInput}
              onChange={(e) => props.setExtensionsInput(e.target.value)}
              error={Boolean(props.extensionsError)}
            />
            <Button variant="outlined" onClick={props.handleAddExtension}>
              Add
            </Button>
          </Stack>
          {props.extensionsError && <Alert severity="error">{props.extensionsError}</Alert>}
          <Stack
            direction="row"
            spacing={1}
            sx={{
              flexWrap: 'wrap',
            }}
          >
            {props.settings.supportedExtensions.map((extension) => (
              <Chip
                key={extension}
                label={extension}
                onDelete={() => props.handleRemoveExtension(extension)}
              />
            ))}
          </Stack>
        </Stack>
      </Grid>

      <Grid
        size={{
          xs: 12,
          md: 6,
        }}
      >
        <Typography variant="subtitle2" gutterBottom>
          Exclude Patterns
        </Typography>
        <Stack spacing={1}>
          <Stack direction="row" spacing={1}>
            <TextField
              fullWidth
              size="small"
              label="Add exclude pattern"
              placeholder="*_preview.m4b"
              value={props.excludePatternInput}
              onChange={(e) => props.setExcludePatternInput(e.target.value)}
              error={Boolean(props.excludePatternError)}
            />
            <Button variant="outlined" onClick={props.handleAddExcludePattern}>
              Add
            </Button>
          </Stack>
          {props.excludePatternError && <Alert severity="error">{props.excludePatternError}</Alert>}
          <Stack
            direction="row"
            spacing={1}
            sx={{
              flexWrap: 'wrap',
            }}
          >
            {props.settings.excludePatterns.map((pattern) => (
              <Chip
                key={pattern}
                label={pattern}
                onDelete={() => props.handleRemoveExcludePattern(pattern)}
              />
            ))}
          </Stack>
        </Stack>
      </Grid>

      <Grid size={12}>
        <TextField
          fullWidth
          label="Folder Naming Pattern"
          value={props.settings.folderNamingPattern}
          onChange={(e) => props.handleChange('folderNamingPattern', e.target.value)}
          helperText={
            'Available: {title}, {author}, {series}, {series_number}, ' +
            '{print_year}, {audiobook_release_year}, {year}, ' +
            '{publisher}, {edition}, {narrator}, {language}, ' +
            '{isbn10}, {isbn13}, {track_number}, {total_tracks}.'
          }
        />
        <Alert severity="info" sx={{ mt: 1, mb: 1 }}>
          <Typography variant="caption">
            <strong>Smart Path Handling:</strong> Empty fields (like {'{series}'}) are automatically
            removed from paths. If a book has no series, that segment disappears gracefully—no
            duplicate slashes or empty folders.
          </Typography>
        </Alert>
        <Box
          sx={{
            mt: 1,
            p: 2,
            bgcolor: 'action.hover',
            border: 1,
            borderColor: 'divider',
            borderRadius: 1,
          }}
        >
          <Typography
            variant="caption"
            sx={{
              color: 'text.secondary',
              wordBreak: 'break-word',
              display: 'block',
              fontWeight: 'bold',
              mb: 0.5,
            }}
          >
            With Series:
          </Typography>
          <Typography
            variant="caption"
            sx={{
              color: 'text.secondary',
              wordBreak: 'break-word',
              display: 'block',
              mb: 1,
            }}
          >
            {generateExample(props.settings.folderNamingPattern, exampleWithSeries, true)}
          </Typography>
          <Typography
            variant="caption"
            sx={{
              color: 'text.secondary',
              wordBreak: 'break-word',
              display: 'block',
              fontWeight: 'bold',
              mb: 0.5,
            }}
          >
            Without Series:
          </Typography>
          <Typography
            variant="caption"
            sx={{
              color: 'text.secondary',
              wordBreak: 'break-word',
              display: 'block',
            }}
          >
            {generateExample(props.settings.folderNamingPattern, exampleNoSeries, true)}
          </Typography>
        </Box>
      </Grid>

      <Grid size={12}>
        <TextField
          fullWidth
          label="File Naming Pattern"
          value={props.settings.fileNamingPattern}
          onChange={(e) => props.handleChange('fileNamingPattern', e.target.value)}
          helperText={
            'Pattern for individual audiobook files. All folder fields ' +
            'plus {track_number}, {total_tracks}, {bitrate}, {codec}, ' +
            '{quality} (parsed from media)'
          }
        />
        <Box
          sx={{
            mt: 1,
            p: 2,
            bgcolor: 'action.hover',
            border: 1,
            borderColor: 'divider',
            borderRadius: 1,
          }}
        >
          <Typography
            variant="caption"
            sx={{
              color: 'text.secondary',
              wordBreak: 'break-word',
              display: 'block',
              fontWeight: 'bold',
              mb: 0.5,
            }}
          >
            With Series (single file):
          </Typography>
          <Typography
            variant="caption"
            sx={{
              color: 'text.secondary',
              wordBreak: 'break-word',
              display: 'block',
              mb: 1,
            }}
          >
            {generateExample(props.settings.fileNamingPattern, exampleWithSeries, false)}
          </Typography>
          <Typography
            variant="caption"
            sx={{
              color: 'text.secondary',
              wordBreak: 'break-word',
              display: 'block',
              fontWeight: 'bold',
              mb: 0.5,
            }}
          >
            Without Series (multi-file):
          </Typography>
          <Typography
            variant="caption"
            sx={{
              color: 'text.secondary',
              display: 'block',
              wordBreak: 'break-word',
            }}
          >
            {generateExample(props.settings.fileNamingPattern, exampleNoSeries, false).replace(
              '.m4b',
              ' 03 of 50.mp3'
            )}
          </Typography>
        </Box>
        <Alert severity="info" sx={{ mt: 1 }}>
          <Typography variant="caption">
            <strong>Multi-file audiobooks:</strong> For books with multiple files (e.g., 50 MP3s),
            the system automatically appends track numbers. Pattern detection: Uses hyphens if
            pattern contains "-", underscores if "_", otherwise spaces. Example: "Title -
            Narrator-03-of-50.mp3" or "Title Narrator 03 of 50.mp3"
            <br />
            <strong>Override:</strong> Include {'{track_number}'} and {'{total_tracks}'} in your
            pattern to control exact formatting. Example: "{'{title}'} - Part {'{track_number}'} of{' '}
            {'{total_tracks}'}" → "To Kill a Mockingbird - Part 03 of 50.m4b"
          </Typography>
        </Alert>
      </Grid>

      {/* Smart Apply Pipeline Section */}
      <Grid size={12}>
        <Divider sx={{ my: 2 }} />
        <Typography variant="h6" gutterBottom>
          Smart Apply Pipeline
        </Typography>
        <Typography
          variant="body2"
          sx={{
            color: 'text.secondary',
            mb: 2,
          }}
        >
          Controls how metadata is applied to files when using the smart apply pipeline. Where files
          land on disk is set by the folder and file naming patterns above — this pipeline uses
          those same two patterns, so a book never moves twice.
        </Typography>
      </Grid>

      <Grid
        size={{
          xs: 12,
          sm: 4,
        }}
      >
        <FormControlLabel
          control={
            <Switch
              checked={props.settings.autoRenameOnApply}
              onChange={(e) =>
                props.setSettings((prev) => ({
                  ...prev,
                  autoRenameOnApply: e.target.checked,
                }))
              }
            />
          }
          label="Auto-rename files on apply"
        />
      </Grid>

      <Grid
        size={{
          xs: 12,
          sm: 4,
        }}
      >
        <FormControlLabel
          control={
            <Switch
              checked={props.settings.autoWriteTagsOnApply}
              onChange={(e) =>
                props.setSettings((prev) => ({
                  ...prev,
                  autoWriteTagsOnApply: e.target.checked,
                }))
              }
            />
          }
          label="Auto-write tags on apply"
        />
      </Grid>

      <Grid
        size={{
          xs: 12,
          sm: 4,
        }}
      >
        <FormControlLabel
          control={
            <Switch
              checked={props.settings.verifyAfterWrite}
              onChange={(e) =>
                props.setSettings((prev) => ({
                  ...prev,
                  verifyAfterWrite: e.target.checked,
                }))
              }
            />
          }
          label="Verify files after write"
        />
      </Grid>

      {/* Import Paths Section */}
      {props.importPaths && (
        <>
          <Grid size={12}>
            <Typography variant="h6" gutterBottom sx={{ mt: 2 }}>
              Import Paths (Watch Locations)
            </Typography>
            <Divider sx={{ mb: 2 }} />
          </Grid>

          <Grid size={12}>
            <Alert severity="info" sx={{ mb: 2 }}>
              <strong>Import Paths</strong> are watched for new audiobook files. Files found here
              are scanned and imported into the main library path where they are organized.
            </Alert>

            <Box>
              {props.importPaths.length === 0 ? (
                <Alert severity="warning" sx={{ mb: 2 }}>
                  No import folders configured. Add folders to automatically import audiobooks from
                  specific locations.
                </Alert>
              ) : (
                <List>
                  {props.importPaths.map((folder) => {
                    const scanStatus = props.scanStatuses![folder.id];
                    const errorCount = scanStatus?.errors?.length || 0;
                    const isScanning = scanStatus?.status === 'scanning';
                    let secondaryText = `${folder.book_count || 0} books`;
                    if (scanStatus) {
                      if (scanStatus.status === 'scanning') {
                        secondaryText = `Scanning... Scanned ${scanStatus.scanned} files`;
                      } else if (scanStatus.status === 'complete') {
                        if (errorCount > 0) {
                          secondaryText =
                            'Scan complete. Found ' +
                            scanStatus.scanned +
                            ' audiobooks, ' +
                            errorCount +
                            ' errors.';
                        } else {
                          secondaryText =
                            'Scan complete. Found ' + scanStatus.scanned + ' audiobooks.';
                        }
                      } else if (scanStatus.status === 'cancelled') {
                        secondaryText =
                          'Scan cancelled. Processed ' + scanStatus.scanned + ' files.';
                      } else if (scanStatus.status === 'error') {
                        secondaryText =
                          errorCount > 0 ? `Scan failed. ${errorCount} errors.` : 'Scan failed.';
                      }
                    }

                    return (
                      <ListItem
                        key={folder.id}
                        secondaryAction={
                          <Stack direction="row" spacing={1}>
                            {scanStatus && errorCount > 0 && (
                              <Button
                                size="small"
                                onClick={() => props.handleViewScanErrors!(folder, scanStatus)}
                              >
                                View Errors
                              </Button>
                            )}
                            {isScanning && (
                              <Button
                                size="small"
                                color="error"
                                variant="outlined"
                                onClick={() => props.handleRequestCancelScan!(folder)}
                              >
                                Cancel Scan
                              </Button>
                            )}
                            <Button
                              size="small"
                              variant="outlined"
                              onClick={() => props.handleScanImportFolder!(folder)}
                              disabled={isScanning}
                            >
                              {isScanning ? 'Scanning...' : 'Scan'}
                            </Button>
                            <IconButton
                              edge="end"
                              onClick={() => props.handleRemoveImportFolder!(folder.id)}
                            >
                              <DeleteIcon />
                            </IconButton>
                          </Stack>
                        }
                      >
                        <ListItemIcon>
                          <FolderIcon />
                        </ListItemIcon>
                        <ListItemText primary={folder.path} secondary={secondaryText} />
                      </ListItem>
                    );
                  })}
                </List>
              )}

              <Button
                variant="contained"
                startIcon={<AddIcon />}
                onClick={() => props.setAddFolderDialogOpen!(true)}
                sx={{ mt: 2 }}
              >
                Add Import Path
              </Button>
            </Box>
          </Grid>
        </>
      )}

      <Grid size={12}>
        <FormControlLabel
          control={
            <Switch
              checked={props.settings.createBackups}
              onChange={(e) => props.handleChange('createBackups', e.target.checked)}
            />
          }
          label="Create backups before modifying files"
        />
      </Grid>

      <Grid size={12}>
        <Typography variant="h6" gutterBottom sx={{ mt: 2 }}>
          Backups
        </Typography>
        <Divider sx={{ mb: 2 }} />
      </Grid>

      <Grid size={12}>
        {props.backupNotice && (
          <Alert severity={props.backupNotice.severity} sx={{ mb: 2 }}>
            {props.backupNotice.message}
          </Alert>
        )}
        <Stack
          direction="row"
          spacing={2}
          sx={{
            alignItems: 'center',
            mb: 2,
          }}
        >
          <Button
            variant="contained"
            onClick={props.handleCreateBackup}
            disabled={props.createBackupInProgress}
          >
            {props.createBackupInProgress ? 'Creating...' : 'Create Backup'}
          </Button>
          {props.createBackupInProgress && <CircularProgress size={20} />}
        </Stack>
        {props.backupsLoading ? (
          <Stack
            direction="row"
            spacing={1}
            sx={{
              alignItems: 'center',
            }}
          >
            <CircularProgress size={18} />
            <Typography variant="body2">Loading backups...</Typography>
          </Stack>
        ) : props.backups.length === 0 ? (
          <Alert severity="info">No backups available yet.</Alert>
        ) : (
          <List>
            {props.backups.map((backup) => (
              <ListItem key={backup.filename}>
                <ListItemIcon>
                  <FolderIcon />
                </ListItemIcon>
                <ListItemText
                  primary={
                    <Stack
                      direction="row"
                      spacing={1}
                      sx={{
                        alignItems: 'center',
                      }}
                    >
                      <Typography variant="body2">{backup.filename}</Typography>
                      {(backup.auto || backup.trigger === 'schedule') && (
                        <Chip label="Auto" size="small" color="info" />
                      )}
                    </Stack>
                  }
                  secondary={`${(backup.size / (1024 * 1024)).toFixed(2)} MB • ${new Date(
                    backup.created_at
                  ).toLocaleString()}`}
                />
                <Stack direction="row" spacing={1}>
                  <Button
                    size="small"
                    component="a"
                    href={`/api/v1/backup/${backup.filename}`}
                    download
                  >
                    Download
                  </Button>
                  <Button
                    size="small"
                    variant="outlined"
                    onClick={() => props.handleRequestRestore(backup)}
                  >
                    Restore
                  </Button>
                  <Button
                    size="small"
                    color="error"
                    variant="outlined"
                    onClick={() => props.handleRequestDeleteBackup(backup)}
                  >
                    Delete
                  </Button>
                </Stack>
              </ListItem>
            ))}
          </List>
        )}
      </Grid>

      <Grid size={12}>
        <Typography variant="h6" gutterBottom sx={{ mt: 2 }}>
          Storage Quotas
        </Typography>
        <Divider sx={{ mb: 2 }} />
      </Grid>

      <Grid size={12}>
        <FormControlLabel
          control={
            <Switch
              checked={props.settings.enableDiskQuota}
              onChange={(e) => props.handleChange('enableDiskQuota', e.target.checked)}
            />
          }
          label="Enable disk quota limit"
        />
      </Grid>

      {props.settings.enableDiskQuota && (
        <Grid
          size={{
            xs: 12,
            sm: 6,
          }}
        >
          <TextField
            fullWidth
            type="number"
            label="Maximum Disk Usage"
            value={props.settings.diskQuotaPercent}
            onChange={(e) => props.handleChange('diskQuotaPercent', parseInt(e.target.value) || 0)}
            helperText={'Maximum percentage of disk space the library can use'}
            slotProps={{
              input: {
                endAdornment: <InputAdornment position="end">%</InputAdornment>,
              },

              htmlInput: { min: 1, max: 100 },
            }}
          />
        </Grid>
      )}

      <Grid size={12}>
        <FormControlLabel
          control={
            <Switch
              checked={props.settings.enableUserQuotas}
              onChange={(e) => props.handleChange('enableUserQuotas', e.target.checked)}
            />
          }
          label="Enable per-user storage quotas (multi-user mode)"
        />
      </Grid>

      {props.settings.enableUserQuotas && (
        <Grid
          size={{
            xs: 12,
            sm: 6,
          }}
        >
          <TextField
            fullWidth
            type="number"
            label="Default User Quota"
            value={props.settings.defaultUserQuotaGB}
            onChange={(e) =>
              props.handleChange('defaultUserQuotaGB', parseInt(e.target.value) || 0)
            }
            helperText="Storage limit per user"
            slotProps={{
              input: {
                endAdornment: <InputAdornment position="end">GB</InputAdornment>,
              },

              htmlInput: { min: 1, max: 10000 },
            }}
          />
        </Grid>
      )}
    </Grid>
  );
}
