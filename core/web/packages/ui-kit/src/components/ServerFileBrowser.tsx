// file: core/web/packages/ui-kit/src/components/ServerFileBrowser.tsx
// version: 0.1.0
// guid: f821d18a-3195-4a94-8e05-ffe97869703d
// last-edited: 2026-11-21

import { useState, useEffect, useCallback, useRef, type MouseEvent } from 'react';
import {
  Box,
  Typography,
  List,
  ListItem,
  ListItemButton,
  ListItemIcon,
  ListItemText,
  Breadcrumbs,
  Link,
  Paper,
  CircularProgress,
  Alert,
  type AlertColor,
  Chip,
  Stack,
  TextField,
  IconButton,
  Menu,
  MenuItem,
  Snackbar,
} from '@mui/material';
import {
  Folder as FolderIcon,
  InsertDriveFile as FileIcon,
  Home as HomeIcon,
  NavigateNext as NavigateNextIcon,
  Block as BlockIcon,
  Edit as EditIcon,
  Check as CheckIcon,
} from '@mui/icons-material';

/** One entry of a directory listing. Structurally assignable from the generated core type. */
export interface BrowseEntry {
  name: string;
  path: string;
  is_dir: boolean;
  size?: number;
  /** Unix seconds. */
  mod_time?: number;
  /** An excluded folder is shown dimmed and cannot be entered. */
  excluded?: boolean;
}

export interface BrowseDiskInfo {
  exists: boolean;
  readable: boolean;
  writable: boolean;
  total_bytes?: number;
  free_bytes?: number;
  library_bytes?: number;
}

export interface BrowseResult {
  /** The directory actually listed (the server may canonicalise the one asked for). */
  path: string;
  items: readonly BrowseEntry[];
  count?: number;
  disk_info?: BrowseDiskInfo;
}

export interface ServerFileBrowserProps {
  /** Lists a directory. The component owns no transport. */
  browse: (path: string) => Promise<BrowseResult>;

  /**
   * Takes a folder back into scans. With `exclude`, enables the folder context
   * menu; a folder that is currently excluded offers this action.
   */
  include?: (path: string) => Promise<unknown>;

  /** Takes a folder out of scans. A folder that is not excluded offers this action in the context menu. */
  exclude?: (path: string) => Promise<unknown>;

  /**
   * Initial path to start browsing from
   */
  initialPath?: string;

  /**
   * Callback when a file or folder is selected
   */
  onSelect?: (path: string, isDir: boolean) => void;

  /**
   * Whether to show files or only directories
   */
  showFiles?: boolean;

  /**
   * Whether to allow selecting directories
   */
  allowDirSelect?: boolean;

  /**
   * Whether to allow selecting files
   */
  allowFileSelect?: boolean;
}

export function ServerFileBrowser({
  browse,
  include,
  exclude,
  initialPath = '/',
  onSelect,
  showFiles = true,
  allowDirSelect = true,
  allowFileSelect = false,
}: ServerFileBrowserProps) {
  const [currentPath, setCurrentPath] = useState(initialPath);
  const [items, setItems] = useState<readonly BrowseEntry[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [diskInfo, setDiskInfo] = useState<BrowseDiskInfo | undefined>();
  const [editingPath, setEditingPath] = useState(false);
  const [editPath, setEditPath] = useState(currentPath);
  const [searchFilter, setSearchFilter] = useState('');
  const [regexError, setRegexError] = useState<string | null>(null);
  const [notice, setNotice] = useState<{
    message: string;
    severity: AlertColor;
  } | null>(null);
  const [contextMenu, setContextMenu] = useState<{
    mouseX: number;
    mouseY: number;
  } | null>(null);
  const [contextItem, setContextItem] = useState<BrowseEntry | null>(null);

  // Read through refs: a parent that passes onSelect or browse inline would
  // otherwise give fetchDirectory a new identity every render, and the effect
  // below would reload the directory on every render of the parent.
  const onSelectRef = useRef(onSelect);
  const browseRef = useRef(browse);
  useEffect(() => {
    onSelectRef.current = onSelect;
    browseRef.current = browse;
  }, [onSelect, browse]);

  const fetchDirectory = useCallback(
    async (path: string) => {
      setLoading(true);
      setError(null);
      try {
        const result = await browseRef.current(path);
        setItems(result.items);
        setCurrentPath(result.path);
        setDiskInfo(result.disk_info);

        // Automatically notify parent of current path after successful fetch (if directory selection is allowed)
        if (allowDirSelect) {
          onSelectRef.current?.(result.path, true);
        }
      } catch (err) {
        console.error('Failed to browse filesystem:', err);
        setError(err instanceof Error ? err.message : 'Failed to browse filesystem');
      } finally {
        setLoading(false);
      }
    },
    [allowDirSelect]
  );

  useEffect(() => {
    fetchDirectory(currentPath);
    setEditPath(currentPath);
  }, [currentPath, fetchDirectory]);

  const handleItemClick = (item: BrowseEntry) => {
    if (item.is_dir) {
      // Navigate into directory — clear search so all contents are visible
      setSearchFilter('');
      setRegexError(null);
      setCurrentPath(item.path);
    } else {
      // Select file if allowed
      if (allowFileSelect && onSelect) {
        onSelect(item.path, false);
      }
    }
  };

  const handleItemSelect = (item: BrowseEntry) => {
    if (item.is_dir && allowDirSelect && onSelect) {
      onSelect(item.path, true);
    } else if (!item.is_dir && allowFileSelect && onSelect) {
      onSelect(item.path, false);
    }
  };

  const handleEditPath = () => {
    setEditingPath(true);
    setEditPath(currentPath);
  };

  const handleSavePath = () => {
    setEditingPath(false);
    if (editPath !== currentPath) {
      setCurrentPath(editPath);
    }
  };

  const handleCancelEdit = () => {
    setEditingPath(false);
    setEditPath(currentPath);
  };

  // A folder's menu offers one action; it exists only when the host supplied it.
  const canToggleExclusion = (item: BrowseEntry) => (item.excluded ? !!include : !!exclude);

  const handleContextMenu = (event: MouseEvent, item: BrowseEntry) => {
    if (!item.is_dir || !canToggleExclusion(item)) return;
    event.preventDefault();
    setContextItem(item);
    setContextMenu({ mouseX: event.clientX + 2, mouseY: event.clientY - 2 });
  };

  const handleCloseContextMenu = () => {
    setContextMenu(null);
    setContextItem(null);
  };

  const handleToggleExclude = async () => {
    if (!contextItem || !canToggleExclusion(contextItem)) return;
    try {
      if (contextItem.excluded) {
        await include?.(contextItem.path);
        setNotice({
          message: 'Folder included in scan.',
          severity: 'success',
        });
      } else {
        await exclude?.(contextItem.path);
        setNotice({
          message: 'Folder excluded from scan.',
          severity: 'success',
        });
      }
      await fetchDirectory(currentPath);
    } catch (err) {
      const message = err instanceof Error ? err.message : 'Failed to update exclusion';
      setError(message);
      setNotice({ message, severity: 'error' });
    } finally {
      handleCloseContextMenu();
    }
  };

  const getPathParts = (path: string): string[] => {
    const parts = path.split('/').filter(Boolean);
    return ['/', ...parts];
  };

  const navigateToPath = (index: number) => {
    setSearchFilter('');
    setRegexError(null);
    const parts = getPathParts(currentPath);
    if (index === 0) {
      setCurrentPath('/');
    } else {
      const newPath = '/' + parts.slice(1, index + 1).join('/');
      setCurrentPath(newPath);
    }
  };

  const formatBytes = (bytes: number): string => {
    const sizes = ['B', 'KB', 'MB', 'GB', 'TB'];
    if (bytes === 0) return '0 B';
    const i = Math.floor(Math.log(bytes) / Math.log(1024));
    return `${(bytes / Math.pow(1024, i)).toFixed(2)} ${sizes[i]}`;
  };

  const formatDate = (timestamp: number): string => {
    return new Date(timestamp * 1000).toLocaleDateString();
  };

  // Sort items: directories first, then files, both alphabetically
  const sortedItems = [...items].sort((a, b) => {
    if (a.is_dir && !b.is_dir) return -1;
    if (!a.is_dir && b.is_dir) return 1;
    return a.name.localeCompare(b.name);
  });

  const filteredItems = sortedItems.filter((item) => {
    if (!item.is_dir && !showFiles) {
      return false;
    }
    const trimmed = searchFilter.trim();
    if (!trimmed) {
      return true;
    }
    try {
      const re = new RegExp(trimmed, 'i');
      if (regexError) setRegexError(null);
      return re.test(item.name);
    } catch {
      // Fall back to plain substring match on invalid regex
      return item.name.toLowerCase().includes(trimmed.toLowerCase());
    }
  });

  const pathParts = getPathParts(currentPath);
  const availableLabel =
    diskInfo?.total_bytes !== undefined && diskInfo?.free_bytes !== undefined
      ? [
          'Available',
          formatBytes(diskInfo.free_bytes),
          '/',
          formatBytes(diskInfo.total_bytes),
        ].join(' ')
      : null;
  const libraryLabel =
    diskInfo?.library_bytes !== undefined ? `Library ${formatBytes(diskInfo.library_bytes)}` : null;

  return (
    <Box>
      {notice && (
        <Snackbar
          open={true}
          autoHideDuration={4000}
          onClose={() => setNotice(null)}
          anchorOrigin={{ vertical: 'top', horizontal: 'center' }}
        >
          <Alert severity={notice.severity} onClose={() => setNotice(null)} sx={{ width: '100%' }}>
            {notice.message}
          </Alert>
        </Snackbar>
      )}
      {/* Sticky Path Editor */}
      <Paper
        sx={{
          p: 2,
          mb: 2,
          position: 'sticky',
          top: 0,
          zIndex: 10,
          bgcolor: 'background.paper',
        }}
      >
        <Stack
          direction="row"
          spacing={2}
          sx={{
            alignItems: 'center',
            mb: 1,
          }}
        >
          <Typography
            variant="subtitle2"
            sx={{
              color: 'text.secondary',
            }}
          >
            Current Path:
          </Typography>
          {diskInfo && (
            <Stack direction="row" spacing={1}>
              {diskInfo.readable && <Chip label="Readable" size="small" color="success" />}
              {diskInfo.writable && <Chip label="Writable" size="small" color="success" />}
            </Stack>
          )}
        </Stack>

        {editingPath ? (
          <Stack
            direction="row"
            spacing={1}
            sx={{
              alignItems: 'center',
            }}
          >
            <TextField
              fullWidth
              size="small"
              value={editPath}
              onChange={(e) => setEditPath(e.target.value)}
              onKeyPress={(e) => {
                if (e.key === 'Enter') {
                  handleSavePath();
                }
              }}
              autoFocus
            />
            <IconButton
              size="small"
              color="primary"
              onClick={handleSavePath}
              aria-label="Save path"
            >
              <CheckIcon />
            </IconButton>
            <IconButton size="small" onClick={handleCancelEdit} aria-label="Cancel editing path">
              <EditIcon />
            </IconButton>
          </Stack>
        ) : (
          <Stack
            direction="row"
            spacing={1}
            sx={{
              alignItems: 'center',
            }}
          >
            <Breadcrumbs separator={<NavigateNextIcon fontSize="small" />} sx={{ flex: 1 }}>
              {pathParts.map((part, index) => (
                <Link
                  key={index}
                  component="button"
                  variant="body1"
                  onClick={() => navigateToPath(index)}
                  sx={{ cursor: 'pointer' }}
                  // The root crumb renders as an icon with no text, so without
                  // this the button has no accessible name at all. The label
                  // goes on the button rather than the svg: an aria-label on a
                  // decorative child does not reliably name its parent.
                  aria-label={index === 0 ? 'Filesystem root' : undefined}
                >
                  {index === 0 ? <HomeIcon fontSize="small" /> : part}
                </Link>
              ))}
            </Breadcrumbs>
            <IconButton size="small" onClick={handleEditPath} aria-label="Edit path">
              <EditIcon />
            </IconButton>
          </Stack>
        )}
        <Stack
          direction={{ xs: 'column', md: 'row' }}
          spacing={2}
          sx={{
            alignItems: { xs: 'stretch', md: 'center' },
            mt: 2,
          }}
        >
          <TextField
            size="small"
            label="Search (regex)"
            placeholder="\.m4b$|\.mp3$"
            value={searchFilter}
            onChange={(e) => {
              setSearchFilter(e.target.value);
              try {
                if (e.target.value.trim()) new RegExp(e.target.value.trim());
                setRegexError(null);
              } catch (err) {
                setRegexError(err instanceof Error ? err.message : 'Invalid regex');
              }
            }}
            error={!!regexError}
            helperText={regexError}
            sx={{ minWidth: 220, flexGrow: 1, maxWidth: 400 }}
          />
          {availableLabel && <Chip label={availableLabel} size="small" color="info" />}
          {libraryLabel && (
            <Chip label={libraryLabel} size="small" color="primary" variant="outlined" />
          )}
        </Stack>
      </Paper>

      {/* Loading State */}
      {loading && (
        <Box
          sx={{
            display: 'flex',
            justifyContent: 'center',
            py: 4,
          }}
        >
          <CircularProgress />
        </Box>
      )}

      {/* Error State */}
      {error && (
        <Alert severity="error" sx={{ mb: 2 }}>
          {error}
        </Alert>
      )}

      {/* File/Directory List */}
      {!loading && !error && (
        <Paper>
          <List>
            {filteredItems.length === 0 && (
              <ListItem>
                <ListItemText
                  primary="No items found"
                  secondary={showFiles ? 'This directory is empty' : 'No subdirectories found'}
                />
              </ListItem>
            )}
            {filteredItems.map((item) => (
              <ListItem
                key={item.path}
                disablePadding
                onContextMenu={(event) => handleContextMenu(event, item)}
                secondaryAction={
                  item.is_dir && item.excluded ? (
                    <Chip icon={<BlockIcon />} label="Excluded" size="small" color="warning" />
                  ) : null
                }
              >
                <ListItemButton
                  onClick={() => handleItemClick(item)}
                  onDoubleClick={() => handleItemSelect(item)}
                  disabled={item.excluded}
                >
                  <ListItemIcon>
                    {item.is_dir ? (
                      <FolderIcon color={item.excluded ? 'disabled' : 'primary'} />
                    ) : (
                      <FileIcon color="action" />
                    )}
                  </ListItemIcon>
                  <ListItemText
                    primary={item.name}
                    secondary={
                      !item.is_dir && item.size !== undefined
                        ? `${formatBytes(item.size)}${
                            item.mod_time ? ` • ${formatDate(item.mod_time)}` : ''
                          }`
                        : null
                    }
                    sx={{
                      opacity: item.excluded ? 0.5 : 1,
                    }}
                  />
                </ListItemButton>
              </ListItem>
            ))}
          </List>
        </Paper>
      )}

      <Menu
        open={contextMenu !== null}
        onClose={handleCloseContextMenu}
        anchorReference="anchorPosition"
        anchorPosition={
          contextMenu !== null ? { top: contextMenu.mouseY, left: contextMenu.mouseX } : undefined
        }
      >
        <MenuItem onClick={handleToggleExclude} disabled={!contextItem}>
          {contextItem?.excluded ? 'Include in scan' : 'Exclude from scan'}
        </MenuItem>
      </Menu>

      {/* Help Text */}
      <Box
        sx={{
          mt: 2,
        }}
      >
        <Typography
          variant="caption"
          sx={{
            color: 'text.secondary',
          }}
        >
          {allowDirSelect && allowFileSelect
            ? 'Click folders to navigate. Current folder is automatically selected. Double-click items to explicitly select.'
            : allowDirSelect
              ? 'Click folders to navigate. Current folder is automatically selected.'
              : allowFileSelect
                ? 'Click to navigate, double-click files to select'
                : 'Click to navigate through directories'}
        </Typography>
      </Box>
    </Box>
  );
}
