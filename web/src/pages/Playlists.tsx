// file: web/src/pages/Playlists.tsx
// version: 1.0.3
// guid: 2b0c1d6e-3f4a-4a70-b8c5-3d7e0f1b9a99

import { useCallback, useEffect, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import {
  Box,
  Button,
  Chip,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  IconButton,
  List,
  ListItem,
  ListItemButton,
  ListItemText,
  Tab,
  Tabs,
  TextField,
  ToggleButton,
  ToggleButtonGroup,
  Typography,
} from '@mui/material';
import {
  Add as AddIcon,
  Delete as DeleteIcon,
  PlaylistPlay as SmartIcon,
  QueueMusic as StaticIcon,
} from '@mui/icons-material';
import {
  type UserPlaylist,
  listPlaylists,
  createPlaylist,
  deletePlaylist,
} from '../services/playlistApi';

export default function Playlists() {
  const navigate = useNavigate();
  const [playlists, setPlaylists] = useState<UserPlaylist[]>([]);
  const [tab, setTab] = useState<'all' | 'static' | 'smart'>('all');
  const [createOpen, setCreateOpen] = useState(false);
  const [loading, setLoading] = useState(true);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const typeFilter = tab === 'all' ? undefined : tab;
      const resp = await listPlaylists(typeFilter as 'static' | 'smart' | undefined);
      setPlaylists(resp.playlists || []);
    } catch {
      setPlaylists([]);
    } finally {
      setLoading(false);
    }
  }, [tab]);

  useEffect(() => {
    load();
  }, [load]);

  const handleDelete = useCallback(
    async (id: string) => {
      await deletePlaylist(id);
      load();
    },
    [load]
  );

  return (
    <Box sx={{ p: 3 }}>
      <Box sx={{ display: 'flex', justifyContent: 'space-between', mb: 2 }}>
        <Typography variant="h4">Playlists</Typography>
        <Button variant="contained" startIcon={<AddIcon />} onClick={() => setCreateOpen(true)}>
          New Playlist
        </Button>
      </Box>

      <Tabs
        value={tab}
        onChange={(_, v) => setTab(v)}
        variant="scrollable"
        scrollButtons="auto"
        allowScrollButtonsMobile
        sx={{ mb: 2 }}
      >
        <Tab label="All" value="all" />
        <Tab label="Static" value="static" />
        <Tab label="Smart" value="smart" />
      </Tabs>

      {loading ? (
        <Typography
          sx={{
            color: 'text.secondary',
          }}
        >
          Loading...
        </Typography>
      ) : playlists.length === 0 ? (
        <Typography
          sx={{
            color: 'text.secondary',
          }}
        >
          No playlists yet. Create one to get started.
        </Typography>
      ) : (
        <List>
          {playlists.map((pl) => (
            <ListItem
              key={pl.id}
              disablePadding
              secondaryAction={
                <IconButton edge="end" onClick={() => handleDelete(pl.id)}>
                  <DeleteIcon />
                </IconButton>
              }
            >
              <ListItemButton onClick={() => navigate(`/playlists/${pl.id}`)}>
                <ListItemText
                  primary={
                    <Box sx={{ display: 'flex', alignItems: 'center', gap: 1 }}>
                      {pl.type === 'smart' ? (
                        <SmartIcon fontSize="small" />
                      ) : (
                        <StaticIcon fontSize="small" />
                      )}
                      {pl.name}
                      <Chip
                        label={pl.type}
                        size="small"
                        color={pl.type === 'smart' ? 'primary' : 'default'}
                      />
                      {pl.book_ids && (
                        <Typography
                          variant="caption"
                          sx={{
                            color: 'text.secondary',
                          }}
                        >
                          {pl.book_ids.length} books
                        </Typography>
                      )}
                    </Box>
                  }
                  secondary={pl.description || (pl.query ? `Query: ${pl.query}` : undefined)}
                />
              </ListItemButton>
            </ListItem>
          ))}
        </List>
      )}

      <CreatePlaylistDialog
        open={createOpen}
        onClose={() => setCreateOpen(false)}
        onCreated={load}
      />
    </Box>
  );
}

function CreatePlaylistDialog({
  open,
  onClose,
  onCreated,
}: {
  open: boolean;
  onClose: () => void;
  onCreated: () => void;
}) {
  const [name, setName] = useState('');
  const [type, setType] = useState<'static' | 'smart'>('static');
  const [query, setQuery] = useState('');
  const [description, setDescription] = useState('');
  const [error, setError] = useState('');

  const handleCreate = async () => {
    setError('');
    try {
      await createPlaylist({
        name,
        type,
        description: description || undefined,
        query: type === 'smart' ? query : undefined,
      });
      setName('');
      setQuery('');
      setDescription('');
      onClose();
      onCreated();
    } catch (err: unknown) {
      const msg = (err as { response?: { data?: { error?: string } } })?.response?.data?.error;
      setError(msg || 'Failed to create playlist');
    }
  };

  return (
    <Dialog open={open} onClose={onClose} maxWidth="sm" fullWidth>
      <DialogTitle>Create Playlist</DialogTitle>
      <DialogContent>
        <ToggleButtonGroup
          value={type}
          exclusive
          onChange={(_, v) => v && setType(v)}
          sx={{ mb: 2, mt: 1 }}
          size="small"
        >
          <ToggleButton value="static">
            <StaticIcon sx={{ mr: 0.5 }} /> Static
          </ToggleButton>
          <ToggleButton value="smart">
            <SmartIcon sx={{ mr: 0.5 }} /> Smart
          </ToggleButton>
        </ToggleButtonGroup>

        <TextField
          fullWidth
          label="Name"
          value={name}
          onChange={(e) => setName(e.target.value)}
          margin="normal"
          required
        />
        <TextField
          fullWidth
          label="Description"
          value={description}
          onChange={(e) => setDescription(e.target.value)}
          margin="normal"
          multiline
          rows={2}
        />
        {type === 'smart' && (
          <TextField
            fullWidth
            label="Query (DSL)"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            margin="normal"
            required
            placeholder="e.g. author:sanderson year:>2015"
            helperText="Uses the search DSL: field:value, &&, ||, NOT, ranges, wildcards"
          />
        )}
        {error && (
          <Typography color="error" variant="body2" sx={{ mt: 1 }}>
            {error}
          </Typography>
        )}
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose}>Cancel</Button>
        <Button
          onClick={handleCreate}
          variant="contained"
          disabled={!name || (type === 'smart' && !query)}
        >
          Create
        </Button>
      </DialogActions>
    </Dialog>
  );
}
