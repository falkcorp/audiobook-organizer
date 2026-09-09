// file: web/src/components/settings/PathsSettingsTab.tsx
// version: 1.2.0
// guid: 8c9d7e6f-5a4b-3c2d-1e0f-9a8b7c6d5e4f
// last-edited: 2026-09-09

import { Dispatch, SetStateAction } from 'react';
import {
  Box,
  Typography,
  TextField,
  Button,
  Grid,
  Alert,
  Divider,
  Paper,
  InputAdornment,
  IconButton,
  List,
  ListItem,
  ListItemIcon,
  ListItemText,
  Stack,
  FormControlLabel,
  Switch,
} from '@mui/material';
import {
  FolderOpen as FolderOpenIcon,
  Folder as FolderIcon,
  Add as AddIcon,
  Delete as DeleteIcon,
} from '@mui/icons-material';
import * as api from '../../services/api';
import DelugeSettingsTab from './DelugeSettingsTab';

interface ScanStatus {
  status: 'scanning' | 'complete' | 'error' | 'cancelled';
  scanned: number;
  total: number;
  operationId?: string;
  errors?: string[];
}

interface PathsSettingsTabProps {
  settings: any;
  setSettings: Dispatch<SetStateAction<any>>;
  libraryPathError: string | null;
  handleChange: (field: string, value: string | boolean | number | string[]) => void;
  handleBrowseLibraryPath: () => void;
  importPaths: api.ImportPath[];
  scanStatuses: Record<number, ScanStatus>;
  handleViewScanErrors: (folder: api.ImportPath, status: ScanStatus) => void;
  handleRequestCancelScan: (folder: api.ImportPath) => void;
  handleScanImportFolder: (folder: api.ImportPath) => void;
  handleRemoveImportFolder: (id: number) => void;
  setAddFolderDialogOpen: (value: boolean) => void;
  /**
   * Config keys the server's environment is forcing. A key listed here cannot be
   * changed from here: the environment's value is re-applied over the saved one
   * on every boot, so the control is disabled rather than allowed to accept an
   * edit that would be silently discarded.
   */
  envLocked: string[];
  /**
   * The same keys as `envLocked`, mapped to what is locking each one: an
   * environment variable name, or a command-line flag. Used for the copy that
   * tells the operator where to go and change it.
   */
  settingLocks: Record<string, string>;
  /** Server-computed path an empty Activity Database Path resolves to. */
  activityDbResolvedPath: string;
}

/**
 * describeLock turns a lock mechanism into the sentence fragment that tells the
 * operator where to look. A flag and an environment variable live in different
 * halves of the same unit file, and sending someone to the wrong half is worse
 * than a disabled control with no explanation at all: they delete something that
 * was never the cause and conclude the app is broken.
 */
function describeLock(mechanism: string | undefined): string {
  if (!mechanism) return 'the server configuration';
  return mechanism.startsWith('--')
    ? `the ${mechanism} flag on the server's start-up command`
    : `the ${mechanism} environment variable on the server`;
}

export function PathsSettingsTab(props: PathsSettingsTabProps) {
  // The two activity-database settings lock independently: an operator can pin the
  // path from the unit file without also freezing the move behaviour, and vice versa.
  const pathIsEnvLocked = props.envLocked.includes('activity_db_path');
  const moveIsEnvLocked = props.envLocked.includes('activity_db_move_on_change');
  const dbPathIsLocked = props.envLocked.includes('database_path');
  const dbPathLockedBy = describeLock(props.settingLocks.database_path);

  return (
    <Grid container spacing={3}>
      <Grid size={12}>
        <Typography variant="h6" gutterBottom>
          Path Settings
        </Typography>
        <Divider sx={{ mb: 2 }} />
      </Grid>

      {/* Library Path Section */}
      <Grid size={12}>
        <Typography variant="subtitle1" gutterBottom sx={{ mt: 2, fontWeight: 600 }}>
          Library Path
        </Typography>
        <TextField
          fullWidth
          label="Library Path"
          value={props.settings.libraryPath}
          onChange={(e) => props.handleChange('libraryPath', e.target.value)}
          error={Boolean(props.libraryPathError)}
          helperText={
            props.libraryPathError ||
            'Main library directory where organized audiobooks are stored. Import paths are configured below.'
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
            live. Import paths below are watched for new files to import into the library.
          </Typography>
        </Alert>
      </Grid>

      {/* Main Database Section */}
      <Grid size={12}>
        <Typography variant="subtitle1" gutterBottom sx={{ mt: 2, fontWeight: 600 }}>
          Main Database
        </Typography>
        {dbPathIsLocked && (
          <Alert severity="info" sx={{ mb: 2 }}>
            <Typography variant="body2">
              The database location is set by {dbPathLockedBy}, which takes precedence over
              anything saved here. To manage it from this page, remove it there and restart.
            </Typography>
          </Alert>
        )}
        <TextField
          fullWidth
          label="Database Path"
          value={props.settings.databasePath}
          onChange={(e) => props.handleChange('databasePath', e.target.value)}
          disabled={dbPathIsLocked}
          helperText={
            dbPathIsLocked
              ? `Currently in use: ${props.settings.databasePath} (set by ${props.settingLocks.database_path})`
              : 'Where the book database is stored. Takes effect the next time the server starts.'
          }
        />
        <Alert severity="warning" sx={{ mt: 1 }}>
          <Typography variant="caption">
            <strong>Changing this does not move any data.</strong> The server restarts against
            whatever is at the new path, and starts an empty library if nothing is there. Copy the
            database to the new location first, while the server is stopped. The existing database
            is never deleted, so a wrong path is recoverable by putting the old one back.
          </Typography>
        </Alert>
      </Grid>

      {/* Activity Database Section */}
      <Grid size={12}>
        <Typography variant="subtitle1" gutterBottom sx={{ mt: 2, fontWeight: 600 }}>
          Activity Database
        </Typography>
        {pathIsEnvLocked && (
          <Alert severity="warning" sx={{ mb: 2 }}>
            <Typography variant="body2">
              The activity database location is set by the <code>ACTIVITY_DB_PATH</code> environment
              variable on the server, which overrides anything saved here on every start. To manage
              it from this page, remove that variable from the server&apos;s service configuration
              and restart.
            </Typography>
          </Alert>
        )}
        <TextField
          fullWidth
          label="Activity Database Path"
          value={props.settings.activityDbPath}
          onChange={(e) => props.handleChange('activityDbPath', e.target.value)}
          disabled={pathIsEnvLocked}
          placeholder={props.activityDbResolvedPath}
          helperText={
            pathIsEnvLocked
              ? `Currently in use: ${props.activityDbResolvedPath} (set by the environment)`
              : props.settings.activityDbPath.trim()
                ? 'Full path to the activity-log database file. Clear this field to return to the default location inside the library.'
                : `Using the default: ${props.activityDbResolvedPath || 'a .activity folder inside the library path'}. Enter a path to store it somewhere else.`
          }
        />
        <FormControlLabel
          sx={{ mt: 1 }}
          control={
            <Switch
              checked={props.settings.activityDbMoveOnChange}
              onChange={(e) => props.handleChange('activityDbMoveOnChange', e.target.checked)}
              disabled={moveIsEnvLocked}
            />
          }
          label="Move the existing database when this path changes"
        />
        <Alert severity={props.settings.activityDbMoveOnChange ? 'info' : 'warning'} sx={{ mt: 1 }}>
          <Typography variant="caption">
            {props.settings.activityDbMoveOnChange ? (
              <>
                <strong>On:</strong> changing the path copies the existing database to the new
                location, checks that every row arrived, and only then deletes the original. The
                file can be tens of gigabytes and the copy runs during startup, so the server may
                take a while to come back. If the copy fails for any reason the original is left
                untouched.
              </>
            ) : (
              <>
                <strong>Off:</strong> changing the path starts an empty database at the new
                location. Existing history is not deleted, but it stays at the old path and will no
                longer appear in the activity log.
              </>
            )}
          </Typography>
        </Alert>
      </Grid>

      {/* Import Paths Section */}
      <Grid size={12}>
        <Typography variant="subtitle1" gutterBottom sx={{ mt: 2, fontWeight: 600 }}>
          Import Paths (Watch Locations)
        </Typography>
        <Divider sx={{ mb: 2 }} />
      </Grid>

      <Grid size={12}>
        <Alert severity="info" sx={{ mb: 2 }}>
          <strong>Import Paths</strong> are watched for new audiobook files. Files found here are
          scanned and imported into the main library path where they are organized.
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
                const scanStatus = props.scanStatuses[folder.id];
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
                      secondaryText = 'Scan complete. Found ' + scanStatus.scanned + ' audiobooks.';
                    }
                  } else if (scanStatus.status === 'cancelled') {
                    secondaryText = 'Scan cancelled. Processed ' + scanStatus.scanned + ' files.';
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
                            onClick={() => props.handleViewScanErrors(folder, scanStatus)}
                          >
                            View Errors
                          </Button>
                        )}
                        {isScanning && (
                          <Button
                            size="small"
                            color="error"
                            variant="outlined"
                            onClick={() => props.handleRequestCancelScan(folder)}
                          >
                            Cancel Scan
                          </Button>
                        )}
                        <Button
                          size="small"
                          variant="outlined"
                          onClick={() => props.handleScanImportFolder(folder)}
                          disabled={isScanning}
                        >
                          {isScanning ? 'Scanning...' : 'Scan'}
                        </Button>
                        <IconButton
                          edge="end"
                          onClick={() => props.handleRemoveImportFolder(folder.id)}
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
            onClick={() => props.setAddFolderDialogOpen(true)}
            sx={{ mt: 2 }}
          >
            Add Import Path
          </Button>
        </Box>
      </Grid>

      {/* Protected Paths Section */}
      <Grid size={12}>
        <Paper sx={{ p: 3, mb: 3 }}>
          <Typography
            variant="subtitle1"
            gutterBottom
            sx={{
              fontWeight: 600,
            }}
          >
            Protected Paths
          </Typography>
          <Typography
            variant="body2"
            sx={{
              color: 'text.secondary',
              mb: 2,
            }}
          >
            Paths that the organizer will never move or delete files from. One path per line. These
            are typically your Deluge download directories.
          </Typography>
          <TextField
            multiline
            minRows={3}
            maxRows={10}
            fullWidth
            placeholder={'/mnt/downloads/audiobooks\n/mnt/media/deluge'}
            value={props.settings.protectedPaths}
            onChange={(e) =>
              props.setSettings((prev: any) => ({
                ...prev,
                protectedPaths: e.target.value,
              }))
            }
            size="small"
            label="Protected Paths"
            helperText="Changes are saved with the main Save button."
          />
        </Paper>
      </Grid>

      {/* Deluge Settings */}
      <Grid size={12}>
        <DelugeSettingsTab />
      </Grid>
    </Grid>
  );
}
