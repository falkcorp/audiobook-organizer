// file: web/src/components/settings/PathsSettingsTab.tsx
// version: 1.0.2
// guid: 8c9d7e6f-5a4b-3c2d-1e0f-9a8b7c6d5e4f
// last-edited: 2026-08-19

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
}

export function PathsSettingsTab(props: PathsSettingsTabProps) {
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
