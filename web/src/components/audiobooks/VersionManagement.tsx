// file: web/src/components/audiobooks/VersionManagement.tsx
// version: 1.1.5
// guid: 8b9c0d1e-2f3a-4b5c-6d7e-8f9a0b1c2d3e

import { useState, useEffect } from 'react';
import {
  Dialog,
  DialogTitle,
  DialogContent,
  DialogActions,
  Button,
  Box,
  Typography,
  Chip,
  Stack,
  IconButton,
  Alert,
  AlertTitle,
  List,
  ListItem,
  ListItemText,
  ListItemButton,
  Divider,
  TextField,
  CircularProgress,
} from '@mui/material';
import {
  Star as StarIcon,
  StarBorder as StarBorderIcon,
  Link as LinkIcon,
  Info as InfoIcon,
  Compare as CompareIcon,
} from '@mui/icons-material';
import * as api from '../../services/api';

type Version = api.Book;

interface VersionManagementProps {
  audiobookId: string;
  open: boolean;
  onClose: () => void;
  onUpdate?: () => void;
}

export function VersionManagement({
  audiobookId,
  open,
  onClose,
  onUpdate,
}: VersionManagementProps) {
  const [versions, setVersions] = useState<Version[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [linkDialogOpen, setLinkDialogOpen] = useState(false);
  const [linkAudiobookId, setLinkAudiobookId] = useState('');
  const [versionNotes, setVersionNotes] = useState('');
  const [searchQuery, setSearchQuery] = useState('');
  const [searchResults, setSearchResults] = useState<Version[]>([]);
  const [searchLoading, setSearchLoading] = useState(false);
  const [searchError, setSearchError] = useState<string | null>(null);
  const [unlinkTarget, setUnlinkTarget] = useState<Version | null>(null);

  const loadVersions = async () => {
    setLoading(true);
    setError(null);
    try {
      const data = await api.getBookVersions(audiobookId);
      setVersions(data);
    } catch (err) {
      setError('Failed to load versions');
      console.error('Failed to load versions:', err);
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    if (open && audiobookId) {
      loadVersions();
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, audiobookId]);

  useEffect(() => {
    if (linkDialogOpen) return;
    setSearchQuery('');
    setSearchResults([]);
    setSearchError(null);
    setLinkAudiobookId('');
    setVersionNotes('');
  }, [linkDialogOpen]);

  useEffect(() => {
    if (!linkDialogOpen) return;
    const query = searchQuery.trim();
    if (!query) {
      setSearchResults([]);
      setSearchError(null);
      return;
    }

    let cancelled = false;
    setSearchLoading(true);
    setSearchError(null);
    const timer = window.setTimeout(async () => {
      if (cancelled) return;
      try {
        const results = await api.searchBooks(query, 10);
        if (!cancelled) {
          setSearchResults(results);
        }
      } catch (_err) {
        if (!cancelled) {
          setSearchError('Failed to search audiobooks');
        }
      } finally {
        if (!cancelled) {
          setSearchLoading(false);
        }
      }
    }, 300);

    return () => {
      cancelled = true;
      clearTimeout(timer);
    };
  }, [linkDialogOpen, searchQuery]);

  const handleSetPrimary = async (versionId: string) => {
    try {
      await api.setPrimaryVersion(versionId);
      await loadVersions();
      onUpdate?.();
    } catch (err) {
      setError('Failed to set primary version');
      console.error('Failed to set primary version:', err);
    }
  };

  const handleLinkVersion = async () => {
    const targetId = linkAudiobookId.trim();
    if (!targetId) {
      setError('Please select an audiobook to link');
      return;
    }
    if (targetId === audiobookId) {
      setError('Cannot link a book to itself');
      return;
    }
    if (versions.some((version) => version.id === targetId)) {
      setError('Cannot create circular version links');
      return;
    }
    const currentGroup =
      versions.find((version) => version.id === audiobookId)?.version_group_id ??
      versions[0]?.version_group_id;
    const selected = searchResults.find((version) => version.id === targetId);
    if (currentGroup && selected?.version_group_id === currentGroup) {
      setError('Cannot create circular version links');
      return;
    }

    try {
      // TODO: Add version_notes support to backend API
      await api.linkBookVersion(audiobookId, targetId);
      setLinkDialogOpen(false);
      setLinkAudiobookId('');
      setSearchQuery('');
      setSearchResults([]);
      setVersionNotes('');
      await loadVersions();
      onUpdate?.();
    } catch (err) {
      setError('Failed to link version');
      console.error('Failed to link version:', err);
    }
  };

  const handleRequestUnlink = (version: Version) => {
    setUnlinkTarget(version);
  };

  const handleConfirmUnlink = async () => {
    if (!unlinkTarget) return;
    try {
      await api.unlinkBookVersion(audiobookId, unlinkTarget.id);
      setUnlinkTarget(null);
      await loadVersions();
      onUpdate?.();
    } catch (err) {
      setError('Failed to unlink version');
      console.error('Failed to unlink version:', err);
    }
  };

  const getQualityTier = (version: Version): number => {
    if (version.codec === 'FLAC') return 100;
    if (version.bitrate && version.bitrate >= 320) return 90;
    if (version.bitrate && version.bitrate >= 256) return 80;
    if (version.bitrate && version.bitrate >= 192) return 70;
    if (version.bitrate && version.bitrate >= 128) return 60;
    return 50;
  };

  const getQualityColor = (tier: number): 'success' | 'info' | 'warning' | 'default' => {
    if (tier >= 90) return 'success';
    if (tier >= 70) return 'info';
    if (tier >= 50) return 'warning';
    return 'default';
  };

  return (
    <>
      <Dialog open={open} onClose={onClose} maxWidth="md" fullWidth>
        <DialogTitle>
          <Stack
            direction="row"
            spacing={1}
            sx={{
              alignItems: 'center',
            }}
          >
            <CompareIcon />
            <Typography variant="h6">Manage Versions</Typography>
          </Stack>
        </DialogTitle>
        <DialogContent>
          {error && (
            <Alert severity="error" sx={{ mb: 2 }} onClose={() => setError(null)}>
              {error}
            </Alert>
          )}

          {loading ? (
            <Typography>Loading versions...</Typography>
          ) : versions.length === 0 ? (
            <Alert severity="info">
              <AlertTitle>No Additional Versions</AlertTitle>
              This audiobook has no linked versions. Link another version to compare quality,
              format, or editions.
            </Alert>
          ) : (
            <List>
              {versions.map((version, index) => {
                const qualityTier = getQualityTier(version);
                const qualityColor = getQualityColor(qualityTier);
                const labelTitle = version.title || 'audiobook';

                return (
                  <Box key={version.id}>
                    {index > 0 && <Divider />}
                    <ListItem>
                      <Box sx={{ width: '100%' }}>
                        <Stack
                          direction="row"
                          spacing={1}
                          sx={{
                            alignItems: 'center',
                            mb: 1,
                          }}
                        >
                          <IconButton
                            size="small"
                            onClick={() => handleSetPrimary(version.id)}
                            color={version.is_primary_version ? 'primary' : 'default'}
                            aria-label={
                              version.is_primary_version
                                ? `Primary version for ${labelTitle}`
                                : `Set primary for ${labelTitle}`
                            }
                          >
                            {version.is_primary_version ? <StarIcon /> : <StarBorderIcon />}
                          </IconButton>
                          <Typography variant="subtitle1" sx={{ flex: 1 }}>
                            {version.title}
                          </Typography>
                          {version.is_primary_version && (
                            <Chip label="Primary" color="primary" size="small" />
                          )}
                          {version.id !== audiobookId && (
                            <Button
                              size="small"
                              color="secondary"
                              onClick={() => handleRequestUnlink(version)}
                            >
                              Unlink
                            </Button>
                          )}
                        </Stack>

                        <Stack direction="row" spacing={1} sx={{ ml: 5, mb: 1 }}>
                          {version.quality && (
                            <Chip label={version.quality} color={qualityColor} size="small" />
                          )}
                          {version.codec && (
                            <Chip label={version.codec} size="small" variant="outlined" />
                          )}
                          {version.bitrate && (
                            <Chip
                              label={`${version.bitrate} kbps`}
                              size="small"
                              variant="outlined"
                            />
                          )}
                          {version.sample_rate && (
                            <Chip
                              label={`${version.sample_rate} Hz`}
                              size="small"
                              variant="outlined"
                            />
                          )}
                        </Stack>

                        {version.version_notes && (
                          <Typography
                            variant="body2"
                            sx={{
                              color: 'text.secondary',
                              ml: 5,
                            }}
                          >
                            <InfoIcon
                              sx={{
                                fontSize: 14,
                                verticalAlign: 'middle',
                                mr: 0.5,
                              }}
                            />
                            {version.version_notes}
                          </Typography>
                        )}

                        <Typography
                          variant="caption"
                          sx={{
                            color: 'text.secondary',
                            ml: 5,
                            display: 'block',
                          }}
                        >
                          {version.file_path}
                        </Typography>
                      </Box>
                    </ListItem>
                  </Box>
                );
              })}
            </List>
          )}

          <Button
            startIcon={<LinkIcon />}
            variant="outlined"
            onClick={() => setLinkDialogOpen(true)}
            sx={{ mt: 2 }}
          >
            Link Another Version
          </Button>
        </DialogContent>
        <DialogActions>
          <Button onClick={onClose}>Close</Button>
        </DialogActions>
      </Dialog>

      {/* Link Version Dialog */}
      <Dialog open={linkDialogOpen} onClose={() => setLinkDialogOpen(false)}>
        <DialogTitle>Link Version</DialogTitle>
        <DialogContent>
          <Typography
            variant="body2"
            sx={{
              color: 'text.secondary',
              mb: 2,
            }}
          >
            Search for an audiobook to link as another version. This is useful for different
            qualities, formats, or editions of the same book.
          </Typography>
          <TextField
            autoFocus
            margin="dense"
            label="Search by title or author"
            fullWidth
            value={searchQuery}
            onChange={(e) => setSearchQuery(e.target.value)}
            placeholder="Search audiobooks"
          />
          {searchLoading && (
            <Stack
              direction="row"
              spacing={1}
              sx={{
                alignItems: 'center',
                mt: 2,
              }}
            >
              <CircularProgress size={16} />
              <Typography variant="body2">Searching...</Typography>
            </Stack>
          )}
          {searchError && (
            <Alert severity="error" sx={{ mt: 2 }}>
              {searchError}
            </Alert>
          )}
          {searchResults.length > 0 && (
            <List dense sx={{ mt: 2, maxHeight: 200, overflow: 'auto' }}>
              {searchResults.map((result) => (
                <ListItem key={result.id} disablePadding>
                  <ListItemButton
                    selected={linkAudiobookId === result.id}
                    onClick={() => setLinkAudiobookId(result.id)}
                  >
                    <ListItemText
                      primary={result.title}
                      secondary={result.author_name || result.author_id || ''}
                    />
                  </ListItemButton>
                </ListItem>
              ))}
            </List>
          )}
          <TextField
            margin="dense"
            label="Selected Audiobook ID"
            fullWidth
            value={linkAudiobookId}
            onChange={(e) => setLinkAudiobookId(e.target.value)}
            placeholder="Select from search results"
            sx={{ mt: 2 }}
          />
          <TextField
            margin="dense"
            label="Version Notes (Optional)"
            fullWidth
            multiline
            rows={2}
            value={versionNotes}
            onChange={(e) => setVersionNotes(e.target.value)}
            placeholder="e.g., Remastered 2020, Unabridged, Higher quality"
            sx={{ mt: 2 }}
          />
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setLinkDialogOpen(false)}>Cancel</Button>
          <Button onClick={handleLinkVersion} variant="contained">
            Link Version
          </Button>
        </DialogActions>
      </Dialog>

      <Dialog open={Boolean(unlinkTarget)} onClose={() => setUnlinkTarget(null)}>
        <DialogTitle>Unlink Version</DialogTitle>
        <DialogContent>
          <Typography
            variant="body2"
            sx={{
              color: 'text.secondary',
            }}
          >
            Unlink <strong>{unlinkTarget?.title || 'this audiobook'}</strong> from this version
            group?
          </Typography>
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setUnlinkTarget(null)}>Cancel</Button>
          <Button variant="contained" color="secondary" onClick={handleConfirmUnlink}>
            Unlink
          </Button>
        </DialogActions>
      </Dialog>
    </>
  );
}
