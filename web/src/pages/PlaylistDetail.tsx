// file: web/src/pages/PlaylistDetail.tsx
// version: 1.0.1
// guid: 7a5b6c4d-8e9f-4a70-b8c5-3d7e0f1b9a99

import { useCallback, useEffect, useState } from 'react';
import { useParams, useNavigate } from 'react-router-dom';
import {
  Box,
  Button,
  Chip,
  IconButton,
  List,
  ListItem,
  ListItemText,
  TextField,
  Tooltip,
  Typography,
  Paper,
} from '@mui/material';
import {
  Delete as DeleteIcon,
  DragIndicator as DragIcon,
  Save as SaveIcon,
  ContentCopy as MaterializeIcon,
} from '@mui/icons-material';
import {
  type UserPlaylist,
  getPlaylist,
  updatePlaylist,
  removeBookFromPlaylist,
  materializePlaylist,
  deletePlaylist,
} from '../services/playlistApi';
import { type Book, getBook } from '../services/api';

export default function PlaylistDetail() {
  const { id } = useParams<{ id: string }>();
  const navigate = useNavigate();
  const [playlist, setPlaylist] = useState<UserPlaylist | null>(null);
  const [bookIds, setBookIds] = useState<string[]>([]);
  const [books, setBooks] = useState<Map<string, Book>>(new Map());
  const [editName, setEditName] = useState('');
  const [editQuery, setEditQuery] = useState('');
  const [editDescription, setEditDescription] = useState('');
  const [dirty, setDirty] = useState(false);
  const [saving, setSaving] = useState(false);

  const load = useCallback(async () => {
    if (!id) return;
    try {
      const data = await getPlaylist(id);
      setPlaylist(data.playlist);
      setBookIds(data.book_ids || []);
      setEditName(data.playlist.name);
      setEditQuery(data.playlist.query || '');
      setEditDescription(data.playlist.description || '');
      setDirty(false);

      const bookMap = new Map<string, Book>();
      for (const bid of (data.book_ids || []).slice(0, 100)) {
        try {
          const b = await getBook(bid);
          bookMap.set(bid, b);
        } catch {
          // skip missing books
        }
      }
      setBooks(bookMap);
    } catch {
      navigate('/playlists');
    }
  }, [id, navigate]);

  useEffect(() => {
    load();
  }, [load]);

  const handleSave = useCallback(async () => {
    if (!id || !playlist) return;
    setSaving(true);
    try {
      const update: Record<string, unknown> = {};
      if (editName !== playlist.name) update.name = editName;
      if (editDescription !== (playlist.description || '')) update.description = editDescription;
      if (playlist.type === 'smart' && editQuery !== (playlist.query || ''))
        update.query = editQuery;
      await updatePlaylist(id, update);
      setDirty(false);
      load();
    } catch {
      // TODO: toast
    } finally {
      setSaving(false);
    }
  }, [id, playlist, editName, editDescription, editQuery, load]);

  const handleRemoveBook = useCallback(
    async (bookId: string) => {
      if (!id) return;
      await removeBookFromPlaylist(id, bookId);
      load();
    },
    [id, load]
  );

  const handleMaterialize = useCallback(async () => {
    if (!id) return;
    const snapshot = await materializePlaylist(id);
    navigate(`/playlists/${snapshot.id}`);
  }, [id, navigate]);

  const handleDelete = useCallback(async () => {
    if (!id) return;
    if (!confirm('Delete this playlist?')) return;
    await deletePlaylist(id);
    navigate('/playlists');
  }, [id, navigate]);

  if (!playlist) return <Typography sx={{ p: 3 }}>Loading...</Typography>;

  return (
    <Box sx={{ p: 3 }}>
      <Box sx={{ display: 'flex', justifyContent: 'space-between', mb: 2 }}>
        <Box>
          <TextField
            value={editName}
            onChange={(e) => {
              setEditName(e.target.value);
              setDirty(true);
            }}
            variant="standard"
            inputProps={{ style: { fontSize: '1.5rem', fontWeight: 'bold' } }}
          />
          <Chip
            label={playlist.type}
            size="small"
            color={playlist.type === 'smart' ? 'primary' : 'default'}
            sx={{ ml: 1 }}
          />
        </Box>
        <Box sx={{ display: 'flex', gap: 1 }}>
          {dirty && (
            <Button
              variant="contained"
              startIcon={<SaveIcon />}
              onClick={handleSave}
              disabled={saving}
            >
              Save
            </Button>
          )}
          {playlist.type === 'smart' && (
            <Button variant="outlined" startIcon={<MaterializeIcon />} onClick={handleMaterialize}>
              Snapshot
            </Button>
          )}
          <Button variant="outlined" color="error" onClick={handleDelete}>
            Delete
          </Button>
        </Box>
      </Box>

      <TextField
        fullWidth
        label="Description"
        value={editDescription}
        onChange={(e) => {
          setEditDescription(e.target.value);
          setDirty(true);
        }}
        margin="normal"
        multiline
        rows={2}
        size="small"
      />

      {playlist.type === 'smart' && (
        <TextField
          fullWidth
          label="Query (DSL)"
          value={editQuery}
          onChange={(e) => {
            setEditQuery(e.target.value);
            setDirty(true);
          }}
          margin="normal"
          size="small"
          helperText="e.g. author:sanderson year:>2015 format:m4b"
        />
      )}

      <Typography variant="h6" sx={{ mt: 3, mb: 1 }}>
        Books ({bookIds.length})
      </Typography>

      <Paper variant="outlined">
        <List dense>
          {bookIds.map((bid) => {
            const book = books.get(bid);
            return (
              <ListItem
                key={bid}
                secondaryAction={
                  playlist.type === 'static' ? (
                    <Tooltip title="Remove">
                      <IconButton size="small" onClick={() => handleRemoveBook(bid)}>
                        <DeleteIcon fontSize="small" />
                      </IconButton>
                    </Tooltip>
                  ) : undefined
                }
              >
                {playlist.type === 'static' && (
                  <DragIcon fontSize="small" sx={{ mr: 1, color: 'text.disabled' }} />
                )}
                <ListItemText
                  primary={book?.title || bid}
                  secondary={
                    book
                      ? `${book.format?.toUpperCase() || ''} — ${book.authors?.map((a: { name: string }) => a.name).join(', ') || 'Unknown'}`
                      : 'Loading...'
                  }
                />
              </ListItem>
            );
          })}
          {bookIds.length === 0 && (
            <ListItem>
              <ListItemText
                primary="No books"
                secondary={
                  playlist.type === 'smart'
                    ? 'Query returned no results'
                    : 'Add books from the library'
                }
              />
            </ListItem>
          )}
        </List>
      </Paper>
    </Box>
  );
}
