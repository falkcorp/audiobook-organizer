// file: web/src/pages/Collections.tsx
// version: 1.1.0
// guid: f3330351-d6f6-4583-a859-a262bcee0c4c
// last-edited: 2026-10-18

import { useState } from 'react';
import { useNavigate } from 'react-router-dom';
import {
  Alert,
  Box,
  Button,
  Chip,
  Dialog,
  DialogActions,
  DialogContent,
  DialogContentText,
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
  Tooltip,
  Typography,
} from '@mui/material';
import { Add as AddIcon, Delete as DeleteIcon } from '@mui/icons-material';
import { useCollections } from '../hooks/useCollections';
import {
  CollectionApiError,
  type Collection,
  type CollectionCreateInput,
  type CollectionType,
} from '../services/collectionApi';
import { BookPicker } from '../components/collections/BookPicker';
import { MANAGE_TOOLTIP, memberCount } from '../components/collections/collectionUtils';
import { QueryHelp, SortPicker } from '../components/collections/QueryHelp';
import { type SortChoice, encodeSort, queryError } from '../components/collections/queryHelpData';

type TabValue = 'all' | CollectionType;

export default function Collections() {
  const navigate = useNavigate();
  const [tab, setTab] = useState<TabValue>('all');
  const { collections, count, loading, refreshing, error, canManage, reload, create, remove } =
    useCollections(tab === 'all' ? undefined : tab);
  const [createOpen, setCreateOpen] = useState(false);
  const [pendingDelete, setPendingDelete] = useState<Collection | null>(null);
  const [deleteError, setDeleteError] = useState<string | null>(null);
  const [deleting, setDeleting] = useState(false);

  const confirmDelete = async () => {
    // One DELETE per confirm: a double-click must not send a second one.
    if (!pendingDelete || deleting) return;
    setDeleting(true);
    try {
      await remove(pendingDelete.id);
      setPendingDelete(null);
      setDeleteError(null);
    } catch (err) {
      setDeleteError(err instanceof Error ? err.message : 'Failed to delete collection');
    } finally {
      setDeleting(false);
    }
  };

  return (
    <Box sx={{ p: 3 }}>
      <Box sx={{ display: 'flex', justifyContent: 'space-between', mb: 2 }}>
        <Typography variant="h4">Collections</Typography>
        {canManage && (
          <Button variant="contained" startIcon={<AddIcon />} onClick={() => setCreateOpen(true)}>
            New Collection
          </Button>
        )}
      </Box>

      <Tabs
        value={tab}
        onChange={(_, v: TabValue) => setTab(v)}
        variant="scrollable"
        scrollButtons="auto"
        allowScrollButtonsMobile
        sx={{ mb: 2 }}
      >
        <Tab label="All" value="all" />
        <Tab label="Static" value="static" />
        <Tab label="Dynamic" value="dynamic" />
      </Tabs>

      {loading ? (
        <Typography sx={{ color: 'text.secondary' }}>Loading...</Typography>
      ) : (
        <>
          {error && (
            <Alert
              severity="error"
              sx={{ mb: 1 }}
              action={
                <Button color="inherit" size="small" onClick={() => void reload()}>
                  Retry
                </Button>
              }
            >
              {error}
            </Alert>
          )}
          {!error && collections.length === 0 && (
            <Typography sx={{ color: 'text.secondary' }}>
              {canManage ? 'No collections yet. Create one to get started.' : 'No collections yet.'}
            </Typography>
          )}
          {collections.length > 0 && (
            <>
              {count > collections.length && (
                <Alert severity="info" sx={{ mb: 1 }}>
                  Showing {collections.length} of {count} collections.
                </Alert>
              )}
              {/* Rows stay mounted while a reload after a write is in flight. */}
              <List aria-busy={refreshing}>
                {collections.map((col) => (
                  <ListItem
                    key={col.id}
                    disablePadding
                    secondaryAction={
                      <Box sx={{ display: 'flex', alignItems: 'center', gap: 1 }}>
                        <Button
                          size="small"
                          aria-label={`Open the ${memberCount(col)} books in ${col.name}`}
                          onClick={() => navigate(`/collections/${col.id}`)}
                        >
                          {memberCount(col)} books
                        </Button>
                        {canManage && (
                          <IconButton
                            edge="end"
                            aria-label={`Delete ${col.name}`}
                            onClick={() => {
                              setDeleteError(null);
                              setPendingDelete(col);
                            }}
                          >
                            <DeleteIcon />
                          </IconButton>
                        )}
                      </Box>
                    }
                  >
                    <ListItemButton
                      onClick={() => navigate(`/collections/${col.id}`)}
                      sx={{ pr: 20 }}
                    >
                      <ListItemText
                        primary={
                          <Box sx={{ display: 'flex', alignItems: 'center', gap: 1 }}>
                            {col.name}
                            <Chip
                              label={col.type}
                              size="small"
                              color={col.type === 'dynamic' ? 'primary' : 'default'}
                            />
                          </Box>
                        }
                        secondary={
                          col.description || (col.query ? `Query: ${col.query}` : undefined)
                        }
                      />
                    </ListItemButton>
                  </ListItem>
                ))}
              </List>
            </>
          )}
        </>
      )}
      {!canManage && !loading && (
        <Tooltip title={MANAGE_TOOLTIP}>
          <Typography variant="caption" sx={{ color: 'text.secondary', display: 'block', mt: 2 }}>
            You can view collections but not change them.
          </Typography>
        </Tooltip>
      )}

      <CreateCollectionDialog
        open={createOpen}
        onClose={() => setCreateOpen(false)}
        onCreate={create}
      />

      <Dialog open={pendingDelete !== null} onClose={() => setPendingDelete(null)}>
        <DialogTitle>Delete collection?</DialogTitle>
        <DialogContent>
          <DialogContentText>
            Delete {pendingDelete?.name}? Its books are not deleted. This also removes it from the
            Audiobookshelf app.
          </DialogContentText>
          {deleteError && (
            <Alert severity="error" sx={{ mt: 1 }}>
              {deleteError}
            </Alert>
          )}
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setPendingDelete(null)}>Cancel</Button>
          <Button
            color="error"
            variant="contained"
            disabled={deleting}
            onClick={() => void confirmDelete()}
          >
            Delete
          </Button>
        </DialogActions>
      </Dialog>
    </Box>
  );
}

interface CreateDialogProps {
  open: boolean;
  onClose: () => void;
  onCreate: (input: CollectionCreateInput) => Promise<Collection>;
}

function CreateCollectionDialog({ open, onClose, onCreate }: CreateDialogProps) {
  const [type, setType] = useState<CollectionType>('static');
  const [name, setName] = useState('');
  const [description, setDescription] = useState('');
  const [bookIds, setBookIds] = useState<string[]>([]);
  const [query, setQuery] = useState('');
  const [sort, setSort] = useState<SortChoice>({ field: '', direction: 'asc' });
  const [limit, setLimit] = useState('');
  const [error, setError] = useState('');
  // A 400 on a dynamic collection is about its query: show it on the query field.
  const [queryServerError, setQueryServerError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);

  const limitNum = limit.trim() === '' ? 0 : Number(limit);
  const limitInvalid = !Number.isInteger(limitNum) || limitNum < 0;
  // Mirrors validateCollectionCreate: a name, and a dynamic collection needs a query.
  const valid =
    name.trim() !== '' &&
    (type === 'static' || (query.trim() !== '' && !queryError(query) && !limitInvalid));

  const reset = () => {
    setType('static');
    setName('');
    setDescription('');
    setBookIds([]);
    setQuery('');
    setSort({ field: '', direction: 'asc' });
    setLimit('');
    setError('');
    setQueryServerError(null);
  };

  const handleCreate = async () => {
    setError('');
    setQueryServerError(null);
    setSaving(true);
    const base = { name: name.trim(), description: description.trim() || undefined };
    const input: CollectionCreateInput =
      type === 'static'
        ? { type: 'static', ...base, book_ids: bookIds.length > 0 ? bookIds : undefined }
        : {
            type: 'dynamic',
            ...base,
            query: query.trim(),
            sort_json: encodeSort(sort) || undefined,
            limit: limitNum > 0 ? limitNum : undefined,
          };
    try {
      await onCreate(input);
      reset();
      onClose();
    } catch (err) {
      const message =
        err instanceof Error && err.message ? err.message : 'Failed to create collection';
      if (type === 'dynamic' && err instanceof CollectionApiError && err.status === 400) {
        setQueryServerError(message);
      } else {
        setError(message);
      }
    } finally {
      setSaving(false);
    }
  };

  return (
    <Dialog open={open} onClose={onClose} maxWidth="md" fullWidth>
      <DialogTitle>Create Collection</DialogTitle>
      <DialogContent>
        <ToggleButtonGroup
          value={type}
          exclusive
          onChange={(_, v: CollectionType | null) => v && setType(v)}
          sx={{ mb: 2, mt: 1 }}
          size="small"
        >
          <ToggleButton value="static">Static</ToggleButton>
          <ToggleButton value="dynamic">Dynamic</ToggleButton>
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

        {type === 'static' ? (
          <Box sx={{ mt: 2 }}>
            <Typography variant="subtitle2" gutterBottom>
              Books (optional)
            </Typography>
            <BookPicker selectedIds={bookIds} onChange={setBookIds} />
          </Box>
        ) : (
          <>
            <QueryHelp
              value={query}
              onChange={(next) => {
                setQuery(next);
                setQueryServerError(null);
              }}
              serverError={queryServerError}
            />
            <SortPicker value={sort} onChange={setSort} />
            <TextField
              label="Limit (optional)"
              value={limit}
              onChange={(e) => setLimit(e.target.value)}
              margin="normal"
              type="number"
              error={limitInvalid}
              helperText={
                limitInvalid
                  ? 'A whole number, 0 or more'
                  : 'At most this many books; blank means no limit'
              }
              slotProps={{ htmlInput: { min: 0 } }}
            />
          </>
        )}

        {error && (
          <Alert severity="error" sx={{ mt: 1 }}>
            {error}
          </Alert>
        )}
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose}>Cancel</Button>
        <Button onClick={() => void handleCreate()} variant="contained" disabled={!valid || saving}>
          Create
        </Button>
      </DialogActions>
    </Dialog>
  );
}
