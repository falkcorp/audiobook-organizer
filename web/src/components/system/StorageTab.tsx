// file: web/src/components/system/StorageTab.tsx
// version: 1.4.0
// guid: 9e0f1a2b-3c4d-5e6f-7a8b-9c0d1e2f3a4b
// last-edited: 2026-08-10
import { useState, useEffect } from 'react';
import {
  Box,
  Typography,
  Stack,
  Grid,
  Card,
  CardContent,
  Divider,
  Button,
  CircularProgress,
  Chip,
  Tooltip,
} from '@mui/material';
import {
  Folder as FolderIcon,
  LibraryBooks as LibraryIcon,
  Refresh as RefreshIcon,
  Storage as StorageIcon,
  InfoOutlined as InfoIcon,
} from '@mui/icons-material';
import * as api from '../../services/api';

interface ImportPath {
  id: number;
  path: string;
  name: string;
  enabled: boolean;
  book_count: number;
}

interface StorageInfo {
  totalLibrarySize: number;
  bookCount: number;
  folderCount: number;
  folders: ImportPath[];
  pathPrefixes: Array<{ prefix: string; book_count: number }>;
}

export function StorageTab() {
  const [storage, setStorage] = useState<StorageInfo | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    fetchStorageInfo();
  }, []);

  const fetchStorageInfo = async () => {
    setLoading(true);
    setError(null);
    try {
      const [statusData, foldersData, dbHealth] = await Promise.all([
        api.getSystemStatus(),
        api.getImportPaths(),
        api.getDBHealthStats().catch(() => null),
      ]);
      const librarySize =
        statusData.library_size_bytes ?? statusData.library.total_size;
      const libraryBookCount =
        statusData.library_book_count ?? statusData.library.book_count;

      setStorage({
        totalLibrarySize: librarySize,
        bookCount: libraryBookCount,
        folderCount: statusData.import_paths?.folder_count || 0,
        folders: foldersData,
        pathPrefixes: dbHealth?.book_path_prefixes ?? [],
      });
    } catch (err) {
      console.error('Failed to fetch storage info:', err);
      setError(
        err instanceof Error ? err.message : 'Failed to fetch storage info'
      );
    } finally {
      setLoading(false);
    }
  };

  const formatBytes = (bytes: number): string => {
    const sizes = ['Bytes', 'KB', 'MB', 'GB', 'TB'];
    if (bytes === 0) return '0 Bytes';
    const i = Math.floor(Math.log(bytes) / Math.log(1024));
    return `${(bytes / Math.pow(1024, i)).toFixed(2)} ${sizes[i]}`;
  };

  if (loading) {
    return (
      <Box
        display="flex"
        justifyContent="center"
        alignItems="center"
        minHeight="400px"
      >
        <CircularProgress />
      </Box>
    );
  }

  if (error) {
    return (
      <Box
        display="flex"
        justifyContent="center"
        alignItems="center"
        minHeight="400px"
      >
        <Typography color="error">{error}</Typography>
      </Box>
    );
  }

  if (!storage) {
    return null;
  }

  return (
    <Box>
      <Stack
        direction="row"
        justifyContent="space-between"
        alignItems="center"
        mb={2}
      >
        <Typography variant="h6">Library Storage</Typography>
        <Button
          variant="outlined"
          startIcon={<RefreshIcon />}
          onClick={fetchStorageInfo}
          disabled={loading}
        >
          Refresh
        </Button>
      </Stack>

      <Grid container spacing={3}>
        {/* Library Summary */}
        <Grid item xs={12}>
          <Card>
            <CardContent>
              <Stack direction="row" spacing={2} alignItems="center" mb={2}>
                <LibraryIcon color="primary" />
                <Typography variant="h6">Library Summary</Typography>
              </Stack>
              <Grid container spacing={3}>
                <Grid item xs={12} sm={4}>
                  <Box>
                    <Typography variant="body2" color="text.secondary">
                      Total Library Size
                    </Typography>
                    <Typography variant="h5">
                      {formatBytes(storage.totalLibrarySize)}
                    </Typography>
                  </Box>
                </Grid>
                <Grid item xs={12} sm={4}>
                  <Box>
                    <Typography variant="body2" color="text.secondary">
                      Total Books
                    </Typography>
                    <Typography variant="h5">
                      {storage.bookCount.toLocaleString()}
                    </Typography>
                  </Box>
                </Grid>
                <Grid item xs={12} sm={4}>
                  <Box>
                    <Typography variant="body2" color="text.secondary">
                      Import Folders
                    </Typography>
                    <Typography variant="h5">{storage.folderCount}</Typography>
                  </Box>
                </Grid>
              </Grid>
            </CardContent>
          </Card>
        </Grid>

        {/* Library Folders */}
        <Grid item xs={12}>
          <Card>
            <CardContent>
              <Stack direction="row" spacing={2} alignItems="center" mb={2}>
                <FolderIcon color="primary" />
                <Typography variant="h6">Import Folders</Typography>
              </Stack>
              {storage.folders.length === 0 ? (
                <Typography
                  variant="body2"
                  color="text.secondary"
                  sx={{ py: 2, textAlign: 'center' }}
                >
                  No import folders configured. Add folders in Settings or
                  Library page.
                </Typography>
              ) : (
                storage.folders.map((folder, index) => (
                  <Box
                    key={folder.id}
                    sx={[index < storage.folders.length - 1 ? {
                      mb: 2
                    } : {
                      mb: 0
                    }]}
                  >
                    <Stack
                      direction="row"
                      justifyContent="space-between"
                      alignItems="center"
                      mb={0.5}
                    >
                      <Box sx={{ flex: 1, mr: 2 }}>
                        <Typography variant="body2" fontWeight="medium" noWrap>
                          {folder.name || folder.path}
                        </Typography>
                        <Typography
                          variant="caption"
                          color="text.secondary"
                          noWrap
                        >
                          {folder.path}
                        </Typography>
                      </Box>
                      <Stack direction="row" spacing={2} alignItems="center">
                        <Typography variant="body2" color="text.secondary">
                          {folder.book_count}{' '}
                          {folder.book_count === 1 ? 'book' : 'books'}
                        </Typography>
                        <Typography
                          variant="caption"
                          sx={{
                            px: 1,
                            py: 0.5,
                            borderRadius: 1,
                            bgcolor: folder.enabled
                              ? 'success.light'
                              : 'grey.300',
                            color: folder.enabled
                              ? 'success.dark'
                              : 'text.secondary',
                          }}
                        >
                          {folder.enabled ? 'Enabled' : 'Disabled'}
                        </Typography>
                      </Stack>
                    </Stack>
                    {index < storage.folders.length - 1 && (
                      <Divider sx={{ mt: 1 }} />
                    )}
                  </Box>
                ))
              )}
            </CardContent>
          </Card>
        </Grid>

        {/* DB Path Distribution */}
        {storage.pathPrefixes.length > 0 && (
          <Grid item xs={12}>
            <Card>
              <CardContent>
                <Stack direction="row" spacing={2} alignItems="center" mb={2}>
                  <StorageIcon color="primary" />
                  <Typography variant="h6">DB Path Distribution</Typography>
                  <Tooltip title="Shows where books are actually stored in the database. If a path here doesn't match your import folders, those books may have been auto-organized or moved.">
                    <InfoIcon fontSize="small" color="action" />
                  </Tooltip>
                </Stack>
                <Typography variant="body2" color="text.secondary" mb={2}>
                  Top path prefixes in the database vs your configured import folders.
                  Mismatches indicate books that were organized away or are stored under a different root.
                </Typography>
                <Stack spacing={1}>
                  {storage.pathPrefixes.map((p, i) => {
                    const isConfigured = storage.folders.some(f =>
                      f.path.startsWith(p.prefix) || p.prefix.startsWith(f.path)
                    );
                    return (
                      <Stack key={i} direction="row" justifyContent="space-between" alignItems="center">
                        <Typography variant="body2" sx={{ fontFamily: 'monospace', flex: 1, mr: 1 }} noWrap>
                          {p.prefix}
                        </Typography>
                        <Stack direction="row" spacing={1} alignItems="center">
                          <Chip
                            label={`${p.book_count.toLocaleString()} books`}
                            size="small"
                            variant="outlined"
                          />
                          {isConfigured ? (
                            <Chip label="configured" size="small" color="success" variant="outlined" />
                          ) : (
                            <Chip label="not in import paths" size="small" color="warning" variant="outlined" />
                          )}
                        </Stack>
                      </Stack>
                    );
                  })}
                </Stack>
              </CardContent>
            </Card>
          </Grid>
        )}
      </Grid>
    </Box>
  );
}
