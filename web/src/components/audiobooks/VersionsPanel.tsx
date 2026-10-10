// file: web/src/components/audiobooks/VersionsPanel.tsx
// version: 1.1.0
// guid: 5e3f4a2b-6c7d-4a70-b8c5-3d7e0f1b9a99
// last-edited: 2026-10-10

import { useCallback, useEffect, useState } from 'react';
import {
  Alert,
  Box,
  Chip,
  IconButton,
  List,
  ListItem,
  ListItemText,
  Tooltip,
  Typography,
} from '@mui/material';
import {
  Delete as TrashIcon,
  Restore as RestoreIcon,
  Star as ActiveIcon,
} from '@mui/icons-material';
import { apiFetch } from '../../utils/apiFetch';
import { type BookVersion, trashVersion, restoreVersion } from '../../services/versionApi';

const API_BASE = '/api/v1';

interface VersionsPanelProps {
  bookId: string;
}

const STATUS_COLORS: Record<string, 'success' | 'primary' | 'warning' | 'error' | 'default'> = {
  active: 'success',
  alt: 'primary',
  trash: 'warning',
  inactive_purged: 'error',
  blocked_for_redownload: 'error',
  pending: 'default',
  swapping_in: 'warning',
  swapping_out: 'warning',
};

export default function VersionsPanel({ bookId }: VersionsPanelProps) {
  const [versions, setVersions] = useState<BookVersion[]>([]);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState('');

  const load = useCallback(async () => {
    setLoading(true);
    setLoadError('');
    try {
      const resp = await apiFetch(`${API_BASE}/audiobooks/${bookId}/versions`);
      if (resp.ok) {
        const data = await resp.json();
        setVersions(data.versions || []);
      } else {
        setLoadError(`Failed to load versions (HTTP ${resp.status})`);
      }
    } catch (err) {
      // An expired session (ApiAuthRedirectError) lands here too; show it
      // instead of rendering an empty list that looks like "no versions".
      setLoadError(err instanceof Error ? err.message : 'Failed to load versions');
    } finally {
      setLoading(false);
    }
  }, [bookId]);

  useEffect(() => {
    load();
  }, [load]);

  const handleTrash = useCallback(
    async (vid: string) => {
      if (!confirm('Move this version to trash?')) return;
      await trashVersion(bookId, vid);
      load();
    },
    [bookId, load]
  );

  const handleRestore = useCallback(
    async (vid: string) => {
      await restoreVersion(bookId, vid);
      load();
    },
    [bookId, load]
  );

  if (loading)
    return (
      <Typography
        sx={{
          color: 'text.secondary',
        }}
      >
        Loading versions...
      </Typography>
    );
  if (loadError) return <Alert severity="error">{loadError}</Alert>;
  if (versions.length === 0) return null;

  return (
    <Box>
      <Typography variant="h6" sx={{ mb: 1 }}>
        Versions
      </Typography>
      <List dense>
        {versions.map((v) => (
          <ListItem
            key={v.id}
            secondaryAction={
              <Box>
                {v.status === 'active' && (
                  <Tooltip title="Primary version">
                    <ActiveIcon color="success" fontSize="small" />
                  </Tooltip>
                )}
                {v.status === 'alt' && (
                  <Tooltip title="Trash">
                    <IconButton size="small" onClick={() => handleTrash(v.id)}>
                      <TrashIcon fontSize="small" />
                    </IconButton>
                  </Tooltip>
                )}
                {v.status === 'trash' && (
                  <Tooltip title="Restore">
                    <IconButton size="small" onClick={() => handleRestore(v.id)}>
                      <RestoreIcon fontSize="small" />
                    </IconButton>
                  </Tooltip>
                )}
              </Box>
            }
          >
            <ListItemText
              primary={
                <Box sx={{ display: 'flex', alignItems: 'center', gap: 1 }}>
                  <Typography
                    variant="body2"
                    sx={{
                      fontWeight: 'bold',
                    }}
                  >
                    {v.format?.toUpperCase()}
                  </Typography>
                  <Chip
                    label={v.status}
                    size="small"
                    color={STATUS_COLORS[v.status] || 'default'}
                  />
                  {v.source && (
                    <Typography
                      variant="caption"
                      sx={{
                        color: 'text.secondary',
                      }}
                    >
                      via {v.source}
                    </Typography>
                  )}
                </Box>
              }
              secondary={`Ingested ${new Date(v.ingest_date || v.created_at).toLocaleDateString()}`}
            />
          </ListItem>
        ))}
      </List>
    </Box>
  );
}
