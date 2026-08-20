// file: web/src/components/audiobooks/AddToPlaylistDialog.tsx
// version: 1.0.2
// guid: 3c1d2e0f-4a5b-4a70-b8c5-3d7e0f1b9a99

import { useCallback, useEffect, useState } from 'react';
import {
  Button,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  List,
  ListItem,
  ListItemButton,
  ListItemIcon,
  ListItemText,
  Checkbox,
  Typography,
  TextField,
  Box,
} from '@mui/material';
import {
  type UserPlaylist,
  listPlaylists,
  addBooksToPlaylist,
  createPlaylist,
} from '../../services/playlistApi';

interface AddToPlaylistDialogProps {
  open: boolean;
  onClose: () => void;
  bookIds: string[];
}

export default function AddToPlaylistDialog({ open, onClose, bookIds }: AddToPlaylistDialogProps) {
  const [playlists, setPlaylists] = useState<UserPlaylist[]>([]);
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [newName, setNewName] = useState('');
  const [loading, setLoading] = useState(false);

  useEffect(() => {
    if (!open) return;
    listPlaylists('static', 100, 0)
      .then((resp) => setPlaylists(resp.playlists || []))
      .catch(() => {});
    setSelected(new Set());
    setNewName('');
  }, [open]);

  const handleToggle = useCallback((id: string) => {
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  }, []);

  const handleAdd = useCallback(async () => {
    setLoading(true);
    try {
      // Create new playlist if name provided.
      if (newName.trim()) {
        await createPlaylist({
          name: newName.trim(),
          type: 'static',
          book_ids: bookIds,
        });
        // Also add to any selected existing playlists.
        for (const id of selected) {
          await addBooksToPlaylist(id, bookIds);
        }
        onClose();
        return;
      }
      // Add to selected playlists.
      for (const id of selected) {
        await addBooksToPlaylist(id, bookIds);
      }
      onClose();
    } catch {
      // TODO: surface error via toast
    } finally {
      setLoading(false);
    }
  }, [bookIds, newName, onClose, selected]);

  const canSubmit = selected.size > 0 || newName.trim().length > 0;

  return (
    <Dialog open={open} onClose={onClose} maxWidth="sm" fullWidth>
      <DialogTitle>
        Add {bookIds.length === 1 ? 'Book' : `${bookIds.length} Books`} to Playlist
      </DialogTitle>
      <DialogContent>
        {playlists.length > 0 && (
          <List dense sx={{ maxHeight: 300, overflow: 'auto' }}>
            {playlists.map((pl) => (
              <ListItem key={pl.id} disablePadding>
                <ListItemButton onClick={() => handleToggle(pl.id)} dense>
                  <ListItemIcon>
                    <Checkbox edge="start" checked={selected.has(pl.id)} disableRipple />
                  </ListItemIcon>
                  <ListItemText
                    primary={pl.name}
                    secondary={pl.book_ids ? `${pl.book_ids.length} books` : undefined}
                  />
                </ListItemButton>
              </ListItem>
            ))}
          </List>
        )}
        {playlists.length === 0 && (
          <Typography
            sx={{
              color: 'text.secondary',
              mb: 2,
            }}
          >
            No static playlists yet.
          </Typography>
        )}
        <Box sx={{ mt: 2 }}>
          <TextField
            fullWidth
            label="Or create new playlist"
            value={newName}
            onChange={(e) => setNewName(e.target.value)}
            size="small"
            placeholder="New playlist name..."
          />
        </Box>
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose}>Cancel</Button>
        <Button onClick={handleAdd} variant="contained" disabled={!canSubmit || loading}>
          {loading ? 'Adding...' : 'Add'}
        </Button>
      </DialogActions>
    </Dialog>
  );
}
