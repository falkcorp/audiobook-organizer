// file: web/src/pages/FileManager.tsx
// version: 1.3.2
// guid: 4a5b6c7d-8e9f-0a1b-2c3d-4e5f6a7b8c9d

import { useState, useCallback, useRef, useEffect } from 'react';

import {
  Box,
  Typography,
  Button,
  Grid,
  Paper,
  Breadcrumbs,
  Link,
  Dialog,
  DialogTitle,
  DialogContent,
  DialogActions,
  TextField,
  Alert,
  Stack,
} from '@mui/material';
import {
  Add as AddIcon,
  Home as HomeIcon,
  NavigateNext as NavigateNextIcon,
  FolderOpen as FolderOpenIcon,
} from '@mui/icons-material';
import { DirectoryTree, DirectoryNode } from '../components/filemanager/DirectoryTree';
import { ImportPathCard, ImportPath } from '../components/filemanager/ImportPathCard';
import * as api from '../services/api';
import { useToast } from '../components/toast/ToastProvider';

export function FileManager() {
  const [importPaths, setImportPaths] = useState<ImportPath[]>([]);
  const [addFolderOpen, setAddFolderOpen] = useState(false);
  const [newFolderPath, setNewFolderPath] = useState('');
  const [selectedPath, setSelectedPath] = useState<string>('');
  const [directoryTree] = useState<DirectoryNode | null>(null);
  const folderInputRef = useRef<HTMLInputElement>(null);
  const { toast } = useToast();

  useEffect(() => {
    api
      .getImportPaths()
      .then((paths) => {
        setImportPaths(
          paths.map((p) => ({
            id: p.id,
            path: p.path,
            status: 'idle' as const,
            book_count: p.book_count,
          }))
        );
      })
      .catch((err) => console.error('Failed to load import paths:', err));
  }, []);

  const handleAddFolder = async () => {
    if (!newFolderPath.trim()) return;

    try {
      const result = await api.addImportPath(newFolderPath, newFolderPath);
      const newFolder: ImportPath = {
        id: result.id,
        path: result.path,
        status: 'idle',
        book_count: 0,
      };
      setImportPaths((prev) => [...prev, newFolder]);
      setNewFolderPath('');
      setAddFolderOpen(false);
      toast('Import path added.', 'success');
    } catch (error) {
      console.error('Failed to add import path:', error);
      toast('Failed to add import path.', 'error');
    }
  };

  const handleRemoveFolder = useCallback(
    async (folder: ImportPath) => {
      if (!confirm(`Remove import path: ${folder.path}?`)) return;

      try {
        await api.removeImportPath(folder.id);
        setImportPaths((prev) => prev.filter((f) => f.id !== folder.id));
        toast('Import path removed.', 'success');
      } catch (error) {
        console.error('Failed to remove import path:', error);
        toast('Failed to remove import path.', 'error');
      }
    },
    [toast]
  );

  const handleScanFolder = useCallback(
    async (folder: ImportPath) => {
      try {
        setImportPaths((prev) =>
          prev.map((f) =>
            f.id === folder.id ? { ...f, status: 'scanning' as const, progress: 0 } : f
          )
        );
        await api.startScan(folder.path);
        setImportPaths((prev) =>
          prev.map((f) =>
            f.id === folder.id ? { ...f, status: 'complete' as const, progress: 100 } : f
          )
        );
        toast('Scan started.', 'success');
      } catch (error) {
        console.error('Failed to scan import path:', error);
        setImportPaths((prev) =>
          prev.map((f) =>
            f.id === folder.id
              ? { ...f, status: 'error' as const, error_message: 'Scan failed' }
              : f
          )
        );
        toast('Failed to start scan.', 'error');
      }
    },
    [toast]
  );

  const handleLoadChildren = async (path: string): Promise<DirectoryNode[]> => {
    try {
      const result = await api.browseFilesystem(path);
      return (result.items || []).map((item) => ({
        name: item.name,
        path: item.path,
        is_dir: item.is_dir,
        excluded: item.excluded,
        children: item.is_dir ? undefined : [],
      }));
    } catch (error) {
      console.error('Failed to browse filesystem:', error);
      return [];
    }
  };

  const handleSelectDirectory = (path: string) => {
    setSelectedPath(path);
  };

  const pathSegments = selectedPath.split('/').filter(Boolean);

  const handleBrowseFolder = () => {
    folderInputRef.current?.click();
  };

  const handleFolderSelect = async (event: React.ChangeEvent<HTMLInputElement>) => {
    const files = event.target.files;
    if (!files || files.length === 0) return;

    // Get the directory path from the first file
    const firstFile = files[0];
    const webkitPath = (firstFile as File & { webkitRelativePath?: string }).webkitRelativePath;
    if (webkitPath) {
      const folderPath = webkitPath.split('/')[0];
      setNewFolderPath(folderPath);
      setAddFolderOpen(true);
    }
  };

  return (
    <Box>
      <Box
        sx={{
          display: 'flex',
          justifyContent: 'space-between',
          alignItems: 'center',
          mb: 3,
        }}
      >
        <Typography variant="h4">File Manager</Typography>
        <Stack direction="row" spacing={2}>
          <Button variant="outlined" startIcon={<FolderOpenIcon />} onClick={handleBrowseFolder}>
            Browse for Folder
          </Button>
          <Button
            variant="contained"
            startIcon={<AddIcon />}
            onClick={() => setAddFolderOpen(true)}
          >
            Add Import Path
          </Button>
        </Stack>
      </Box>

      {/* Hidden folder input for browsing */}
      <input
        ref={folderInputRef}
        type="file"
        // @ts-expect-error - webkitdirectory is not in TypeScript types but required for directory selection
        webkitdirectory=""
        directory=""
        multiple
        style={{ display: 'none' }}
        onChange={handleFolderSelect}
      />

      <Grid container spacing={3}>
        <Grid size={12}>
          <Typography variant="h6" gutterBottom>
            Import Paths
          </Typography>
          {importPaths.length === 0 ? (
            <Alert severity="info">
              No import paths added yet. Click "Add Import Path" to get started.
            </Alert>
          ) : (
            <Grid container spacing={2}>
              {importPaths.map((folder) => (
                <Grid
                  key={folder.id}
                  size={{
                    xs: 12,
                    md: 6,
                    lg: 4,
                  }}
                >
                  <ImportPathCard
                    importPath={folder}
                    onRemove={handleRemoveFolder}
                    onScan={handleScanFolder}
                  />
                </Grid>
              ))}
            </Grid>
          )}
        </Grid>

        {directoryTree && (
          <Grid size={12}>
            <Paper sx={{ p: 2 }}>
              <Typography variant="h6" gutterBottom>
                Directory Browser
              </Typography>

              <Breadcrumbs separator={<NavigateNextIcon fontSize="small" />} sx={{ mb: 2 }}>
                <Link
                  underline="hover"
                  color="inherit"
                  href="#"
                  onClick={() => handleSelectDirectory('/')}
                  sx={{ display: 'flex', alignItems: 'center' }}
                >
                  <HomeIcon sx={{ mr: 0.5 }} fontSize="small" />
                  Home
                </Link>
                {pathSegments.map((segment, index) => {
                  const path = '/' + pathSegments.slice(0, index + 1).join('/');
                  return (
                    <Link
                      key={path}
                      underline="hover"
                      color="inherit"
                      href="#"
                      onClick={() => handleSelectDirectory(path)}
                    >
                      {segment}
                    </Link>
                  );
                })}
              </Breadcrumbs>

              <DirectoryTree
                root={directoryTree}
                onSelectDirectory={handleSelectDirectory}
                onLoadChildren={handleLoadChildren}
                selectedPath={selectedPath}
              />
            </Paper>
          </Grid>
        )}
      </Grid>

      <Dialog open={addFolderOpen} onClose={() => setAddFolderOpen(false)} maxWidth="sm" fullWidth>
        <DialogTitle>Add Import Path</DialogTitle>
        <DialogContent>
          <TextField
            autoFocus
            margin="dense"
            label="Folder Path"
            type="text"
            fullWidth
            value={newFolderPath}
            onChange={(e) => setNewFolderPath(e.target.value)}
            placeholder="/path/to/audiobooks"
            helperText="Enter the full path to your audiobook folder"
          />
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setAddFolderOpen(false)}>Cancel</Button>
          <Button onClick={handleAddFolder} variant="contained" disabled={!newFolderPath.trim()}>
            Add Folder
          </Button>
        </DialogActions>
      </Dialog>
    </Box>
  );
}
