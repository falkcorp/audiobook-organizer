// file: web/src/pages/CollectionDetail.tsx
// version: 1.0.0
// guid: c80c9391-2049-4abe-bedf-926fc3e9d2ed
// last-edited: 2026-10-18

import { useEffect, useRef, useState } from 'react';
import { Link as RouterLink, useNavigate, useParams } from 'react-router-dom';
import {
  Alert,
  Box,
  Button,
  Checkbox,
  Chip,
  Dialog,
  DialogActions,
  DialogContent,
  DialogContentText,
  DialogTitle,
  FormControlLabel,
  IconButton,
  Link,
  Paper,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TablePagination,
  TableRow,
  TextField,
  Tooltip,
  Typography,
} from '@mui/material';
import { Delete as DeleteIcon } from '@mui/icons-material';
import { useCollection } from '../hooks/useCollections';
import { type Book, getBooksByIds } from '../services/api';
import type { Collection, CollectionUpdateInput } from '../services/collectionApi';
import { BookPicker } from '../components/collections/BookPicker';
import {
  QueryHelp,
  SortPicker,
  decodeSort,
  encodeSort,
  queryError,
  type SortChoice,
} from '../components/collections/QueryHelp';
import { MANAGE_TOOLTIP, memberCount } from './Collections';

const PAGE_SIZE = 50;

function storedMembers(col: Collection): string[] {
  return (col.type === 'dynamic' ? col.materialized_book_ids : col.book_ids) ?? [];
}

function messageOf(err: unknown, fallback: string): string {
  return err instanceof Error && err.message ? err.message : fallback;
}

export default function CollectionDetail() {
  const { id } = useParams<{ id: string }>();
  const navigate = useNavigate();
  const { collection, loading, error, canManage, refresh, update, materialize, addBooks, removeBook, remove } =
    useCollection(id);

  const [name, setName] = useState('');
  const [description, setDescription] = useState('');
  const [query, setQuery] = useState('');
  const [sort, setSort] = useState<SortChoice>({ field: '', direction: 'asc' });
  const [limit, setLimit] = useState('');
  const [adopt, setAdopt] = useState(false);
  const [actionError, setActionError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [addOpen, setAddOpen] = useState(false);
  const [picked, setPicked] = useState<string[]>([]);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [page, setPage] = useState(0);

  // Reset the edit form whenever the server's copy changes (load, save, refresh).
  useEffect(() => {
    if (!collection) return;
    setName(collection.name);
    setDescription(collection.description ?? '');
    setQuery(collection.query ?? '');
    setSort(decodeSort(collection.sort_json));
    setLimit(collection.limit ? String(collection.limit) : '');
    setAdopt(false);
  }, [collection]);

  const members = collection ? storedMembers(collection) : [];
  const pageIds = members.slice(page * PAGE_SIZE, (page + 1) * PAGE_SIZE);
  const pageKey = pageIds.join(',');
  const [books, setBooks] = useState<Map<string, Book>>(new Map());
  const [booksLoading, setBooksLoading] = useState(false);
  const [booksError, setBooksError] = useState<string | null>(null);
  const booksRequest = useRef(0);

  useEffect(() => {
    if (pageKey === '') {
      setBooks(new Map());
      return;
    }
    const mine = ++booksRequest.current;
    const ctl = new AbortController();
    setBooksLoading(true);
    setBooksError(null);
    getBooksByIds(pageKey.split(','), ctl.signal)
      .then((found) => {
        if (mine !== booksRequest.current) return;
        setBooks(new Map(found.map((b) => [b.id, b])));
      })
      .catch((err: unknown) => {
        if (mine !== booksRequest.current || (err as { name?: string })?.name === 'AbortError') return;
        setBooksError(messageOf(err, 'Failed to load the books'));
      })
      .finally(() => {
        if (mine === booksRequest.current) setBooksLoading(false);
      });
    return () => {
      booksRequest.current++;
      ctl.abort();
    };
  }, [pageKey]);

  // A shrinking list (remove, re-evaluate) must not leave the table on a page that no longer exists.
  const lastPage = Math.max(0, Math.ceil(members.length / PAGE_SIZE) - 1);
  useEffect(() => {
    if (page > lastPage) setPage(lastPage);
  }, [page, lastPage]);

  if (loading && !collection) return <Typography sx={{ p: 3 }}>Loading...</Typography>;
  if (!collection) {
    return (
      <Box sx={{ p: 3 }}>
        <Alert
          severity="error"
          action={
            <Button color="inherit" size="small" onClick={() => void refresh()}>
              Retry
            </Button>
          }
        >
          {error ?? 'Collection not found'}
        </Alert>
        <Button component={RouterLink} to="/collections" sx={{ mt: 2 }}>
          Back to collections
        </Button>
      </Box>
    );
  }

  const isDynamic = collection.type === 'dynamic';
  const limitNum = limit.trim() === '' ? 0 : Number(limit);
  const limitInvalid = !Number.isInteger(limitNum) || limitNum < 0;
  const queryInvalid = isDynamic && (query.trim() === '' || queryError(query) !== null);

  // Run a write; surface its message where the user is looking; refetch after a 409.
  const run = async (fn: () => Promise<unknown>, success?: string) => {
    setActionError(null);
    setNotice(null);
    setSaving(true);
    try {
      await fn();
      if (success) setNotice(success);
    } catch (err) {
      setActionError(messageOf(err, 'The request failed'));
      if ((err as { status?: number })?.status === 409) await refresh();
    } finally {
      setSaving(false);
    }
  };

  const changed = (): CollectionUpdateInput => {
    const patch: CollectionUpdateInput = {};
    if (name.trim() !== collection.name) patch.name = name.trim();
    if (description.trim() !== (collection.description ?? '')) patch.description = description.trim();
    if (isDynamic) {
      if (query.trim() !== (collection.query ?? '')) patch.query = query.trim();
      if (encodeSort(sort) !== (collection.sort_json ?? '')) patch.sort_json = encodeSort(sort);
      if (limitNum !== (collection.limit ?? 0)) patch.limit = limitNum;
      // adopt_owner only ever rides with a query that is being saved.
      if (adopt && patch.query === undefined) patch.query = query.trim();
      if (adopt) patch.adopt_owner = true;
    }
    return patch;
  };
  const patch = changed();
  const dirty = Object.keys(patch).length > 0;
  const canSave = canManage && dirty && name.trim() !== '' && !queryInvalid && !limitInvalid && !saving;

  const blocked = collection.evaluation_blocked;

  return (
    <Box sx={{ p: 3 }}>
      <Box sx={{ display: 'flex', alignItems: 'center', gap: 1, mb: 2, flexWrap: 'wrap' }}>
        <Typography variant="h4">{collection.name}</Typography>
        <Chip label={collection.type} size="small" color={isDynamic ? 'primary' : 'default'} />
        {collection.created_by_user_id && (
          <Typography variant="body2" sx={{ color: 'text.secondary' }}>
            Created by {collection.created_by_user_id}
          </Typography>
        )}
        <Box sx={{ flexGrow: 1 }} />
        <Tooltip title={canManage ? '' : MANAGE_TOOLTIP}>
          <span>
            <Button color="error" disabled={!canManage} onClick={() => setConfirmDelete(true)}>
              Delete
            </Button>
          </span>
        </Tooltip>
      </Box>

      {!canManage && (
        <Alert severity="info" sx={{ mb: 2 }}>
          You can view this collection but not change it ({MANAGE_TOOLTIP}).
        </Alert>
      )}
      {blocked && (
        <Alert severity="warning" sx={{ mb: 2 }}>
          {collection.evaluation_blocked_message ??
            'This collection was not re-evaluated; the stored members are shown.'}
        </Alert>
      )}
      {error && (
        <Alert severity="error" sx={{ mb: 2 }}>
          {error}
        </Alert>
      )}
      {actionError && (
        <Alert severity="error" sx={{ mb: 2 }} onClose={() => setActionError(null)}>
          {actionError}
        </Alert>
      )}
      {notice && (
        <Alert severity="success" sx={{ mb: 2 }} onClose={() => setNotice(null)}>
          {notice}
        </Alert>
      )}

      <Paper variant="outlined" sx={{ p: 2, mb: 3 }}>
        <TextField
          fullWidth
          label="Name"
          value={name}
          onChange={(e) => setName(e.target.value)}
          margin="normal"
          disabled={!canManage}
        />
        <TextField
          fullWidth
          label="Description"
          value={description}
          onChange={(e) => setDescription(e.target.value)}
          margin="normal"
          multiline
          rows={2}
          disabled={!canManage}
        />
        {isDynamic && (
          <>
            <QueryHelp
              value={query}
              onChange={setQuery}
              disabled={!canManage}
              creatorId={collection.created_by_user_id}
            />
            <SortPicker value={sort} onChange={setSort} disabled={!canManage} />
            <TextField
              label="Limit (optional)"
              value={limit}
              onChange={(e) => setLimit(e.target.value)}
              margin="normal"
              type="number"
              disabled={!canManage}
              error={limitInvalid}
              helperText={limitInvalid ? 'A whole number, 0 or more' : 'Blank means no limit'}
              slotProps={{ htmlInput: { min: 0 } }}
            />
            {blocked && canManage && (
              <FormControlLabel
                control={<Checkbox checked={adopt} onChange={(e) => setAdopt(e.target.checked)} />}
                label="Take ownership when saving (first active admin only)"
              />
            )}
          </>
        )}
        <Box sx={{ mt: 1, display: 'flex', gap: 1 }}>
          <Tooltip title={canManage ? '' : MANAGE_TOOLTIP}>
            <span>
              <Button
                variant="contained"
                disabled={!canSave}
                onClick={() => void run(() => update(patch), 'Saved.')}
              >
                Save
              </Button>
            </span>
          </Tooltip>
        </Box>
      </Paper>

      <Box sx={{ display: 'flex', alignItems: 'center', gap: 1, mb: 1, flexWrap: 'wrap' }}>
        <Typography variant="h6" component="h2">
          Members ({memberCount(collection)})
        </Typography>
        <Box sx={{ flexGrow: 1 }} />
        {isDynamic ? (
          <>
            <Button disabled={saving || loading} onClick={() => void run(refresh)}>
              Preview
            </Button>
            <Tooltip title={canManage ? '' : MANAGE_TOOLTIP}>
              <span>
                <Button
                  variant="outlined"
                  disabled={!canManage || saving}
                  onClick={() => void run(materialize, 'Members refreshed and stored.')}
                >
                  Materialize
                </Button>
              </span>
            </Tooltip>
          </>
        ) : (
          <Tooltip title={canManage ? '' : MANAGE_TOOLTIP}>
            <span>
              <Button
                variant="outlined"
                disabled={!canManage || saving}
                onClick={() => {
                  setPicked([]);
                  setAddOpen(true);
                }}
              >
                Add books
              </Button>
            </span>
          </Tooltip>
        )}
      </Box>
      {isDynamic && (
        <Typography variant="caption" sx={{ color: 'text.secondary', display: 'block', mb: 1 }}>
          Preview re-runs the query and shows the live result. Materialize also stores it, which is
          what the Audiobookshelf app serves.
        </Typography>
      )}

      {booksError ? (
        <Alert severity="error">{booksError}</Alert>
      ) : members.length === 0 ? (
        <Typography sx={{ color: 'text.secondary' }}>
          {isDynamic ? 'No books match this query.' : 'No books in this collection yet.'}
        </Typography>
      ) : booksLoading && books.size === 0 ? (
        <Typography sx={{ color: 'text.secondary' }}>Loading books...</Typography>
      ) : (
        <>
          <Table size="small" aria-label="Members">
            <TableHead>
              <TableRow>
                <TableCell>Title</TableCell>
                <TableCell>Author</TableCell>
                {!isDynamic && <TableCell align="right" />}
              </TableRow>
            </TableHead>
            <TableBody>
              {pageIds.map((bid) => {
                const book = books.get(bid);
                return (
                  <TableRow key={bid}>
                    <TableCell>
                      <Link component={RouterLink} to={`/library/${bid}`}>
                        {book?.title ?? bid}
                      </Link>
                    </TableCell>
                    <TableCell>{book?.author_name ?? ''}</TableCell>
                    {!isDynamic && (
                      <TableCell align="right">
                        {canManage && (
                          <IconButton
                            size="small"
                            aria-label={`Remove ${book?.title ?? bid}`}
                            disabled={saving}
                            onClick={() => void run(() => removeBook(bid))}
                          >
                            <DeleteIcon fontSize="small" />
                          </IconButton>
                        )}
                      </TableCell>
                    )}
                  </TableRow>
                );
              })}
            </TableBody>
          </Table>
          <TablePagination
            component="div"
            count={members.length}
            page={page}
            onPageChange={(_, p) => setPage(p)}
            rowsPerPage={PAGE_SIZE}
            rowsPerPageOptions={[PAGE_SIZE]}
          />
        </>
      )}

      <Dialog open={addOpen} onClose={() => setAddOpen(false)} maxWidth="sm" fullWidth>
        <DialogTitle>Add books</DialogTitle>
        <DialogContent>
          <BookPicker selectedIds={picked} onChange={setPicked} excludeIds={members} />
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setAddOpen(false)}>Cancel</Button>
          <Button
            variant="contained"
            disabled={picked.length === 0 || picked.length > 500 || saving}
            onClick={() => {
              setAddOpen(false);
              void run(() => addBooks(picked));
            }}
          >
            Add {picked.length > 0 ? picked.length : ''} {picked.length === 1 ? 'book' : 'books'}
          </Button>
        </DialogActions>
      </Dialog>

      <Dialog open={confirmDelete} onClose={() => setConfirmDelete(false)}>
        <DialogTitle>Delete collection?</DialogTitle>
        <DialogContent>
          <DialogContentText>
            Delete {collection.name}? Its books are not deleted. This also removes it from the
            Audiobookshelf app.
          </DialogContentText>
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setConfirmDelete(false)}>Cancel</Button>
          <Button
            color="error"
            variant="contained"
            onClick={() => {
              setConfirmDelete(false);
              void run(async () => {
                await remove();
                navigate('/collections');
              });
            }}
          >
            Delete
          </Button>
        </DialogActions>
      </Dialog>
    </Box>
  );
}
