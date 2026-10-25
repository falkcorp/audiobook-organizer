// file: web/src/pages/CollectionDetail.tsx
// version: 1.3.0
// guid: c80c9391-2049-4abe-bedf-926fc3e9d2ed
// last-edited: 2026-10-25

import { useEffect, useState } from 'react';
import { Link as RouterLink, useNavigate, useParams } from 'react-router-dom';
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
import { useCollection, type UseCollectionResult } from '../hooks/useCollections';
import { type Book, getBooksByIds } from '../services/api';
import { isAbortError } from '../utils/apiFetch';
import {
  CollectionApiError,
  type Collection,
  type CollectionUpdateInput,
} from '../services/collectionApi';
import { BookPicker } from '../components/collections/BookPicker';
import { QueryHelp, SortPicker } from '../components/collections/QueryHelp';
import {
  MANAGE_TOOLTIP,
  memberCount,
  storedMembers,
} from '../components/collections/collectionUtils';
import {
  applySortChoice,
  decodeSort,
  queryError,
  sameSortChoice,
  type SortChoice,
  usesPerUserField,
} from '../components/collections/queryHelpData';

const PAGE_SIZE = 50;

function messageOf(err: unknown, fallback: string): string {
  return err instanceof Error && err.message ? err.message : fallback;
}

export default function CollectionDetail() {
  const { id } = useParams<{ id: string }>();
  const ctl = useCollection(id);
  const { collection, loading, error, refresh } = ctl;
  // Bumped after a successful Save only: it re-seeds the edit form from the
  // saved collection. Preview, membership writes, Materialize and a 409 refetch
  // all replace `collection` but must NOT touch what the user is typing.
  const [savedVersion, setSavedVersion] = useState(0);
  const [notice, setNotice] = useState<string | null>(null);

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

  return (
    <CollectionEditor
      // A new collection id, or a successful save, starts a fresh form.
      key={`${collection.id}:${savedVersion}`}
      collection={collection}
      ctl={ctl}
      notice={notice}
      onNotice={setNotice}
      onSaved={() => setSavedVersion((v) => v + 1)}
    />
  );
}

interface EditorProps {
  collection: Collection;
  ctl: UseCollectionResult;
  notice: string | null;
  onNotice: (n: string | null) => void;
  onSaved: () => void;
}

function CollectionEditor({ collection, ctl, notice, onNotice, onSaved }: EditorProps) {
  const navigate = useNavigate();
  const { loading, error, canManage, refresh, update, materialize, addBooks, removeBook, remove } =
    ctl;
  // What the form was seeded from. Edits are compared with this, not with the
  // live `collection`, which refreshes underneath the form.
  const [seed] = useState(collection);

  const [name, setName] = useState(seed.name);
  const [description, setDescription] = useState(seed.description ?? '');
  const [query, setQuery] = useState(seed.query ?? '');
  const [sort, setSort] = useState<SortChoice>(decodeSort(seed.sort_json));
  const [limit, setLimit] = useState(seed.limit ? String(seed.limit) : '');
  const [actionError, setActionError] = useState<string | null>(null);
  const [queryServerError, setQueryServerError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [addOpen, setAddOpen] = useState(false);
  const [picked, setPicked] = useState<string[]>([]);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [page, setPage] = useState(0);

  const members = storedMembers(collection);
  // A shrinking list (remove, re-evaluate) must not leave the table on a page that no longer exists.
  const lastPage = Math.max(0, Math.ceil(members.length / PAGE_SIZE) - 1);
  const safePage = Math.min(page, lastPage);
  const pageIds = members.slice(safePage * PAGE_SIZE, (safePage + 1) * PAGE_SIZE);
  const pageKey = pageIds.join(',');

  // Book lookup for the visible page. Results are keyed by the page they answer,
  // so "is this page loaded" and "did it fail" are derived, never reset in the
  // effect body. `known` keeps every book seen so far: a row whose book is not
  // known yet reads "Loading..." instead of showing a raw id.
  const [known, setKnown] = useState<Map<string, Book>>(new Map());
  const [lookup, setLookup] = useState<{ key: string; error: string | null } | null>(null);

  useEffect(() => {
    if (pageKey === '') return;
    const abort = new AbortController();
    Promise.resolve()
      .then(() => getBooksByIds(pageKey.split(','), abort.signal))
      .then(
        (found) => {
          if (abort.signal.aborted) return;
          setKnown((prev) => new Map([...prev, ...found.map((b): [string, Book] => [b.id, b])]));
          setLookup({ key: pageKey, error: null });
        },
        (err: unknown) => {
          if (abort.signal.aborted || isAbortError(err)) return;
          setLookup({ key: pageKey, error: messageOf(err, 'Failed to load the books') });
        }
      );
    return () => abort.abort();
  }, [pageKey]);

  const lookupDone = lookup?.key === pageKey;
  const booksError = lookupDone ? lookup.error : null;

  const isDynamic = collection.type === 'dynamic';
  // A query on a per-user field is evaluated for whoever is looking and stores no
  // member list, so there is nothing to materialize (the server clears any list).
  // The SAVED query decides, like Preview, not the text in the box.
  const perViewer = isDynamic && usesPerUserField(collection.query ?? '');
  const membersUnavailable = isDynamic && collection.members_unavailable === true;
  const limitNum = limit.trim() === '' ? 0 : Number(limit);
  const limitInvalid = !Number.isInteger(limitNum) || limitNum < 0;
  const queryInvalid = isDynamic && (query.trim() === '' || queryError(query) !== null);

  // Run a write; surface its message where the user is looking; refetch after a 409.
  const run = async (fn: () => Promise<unknown>, success?: string) => {
    setActionError(null);
    onNotice(null);
    setSaving(true);
    try {
      await fn();
      if (success) onNotice(success);
      return true;
    } catch (err) {
      setActionError(messageOf(err, 'The request failed'));
      if ((err as { status?: number })?.status === 409) await refresh();
      return false;
    } finally {
      setSaving(false);
    }
  };

  const patch: CollectionUpdateInput = {};
  if (name.trim() !== seed.name) patch.name = name.trim();
  if (description.trim() !== (seed.description ?? '')) patch.description = description.trim();
  if (isDynamic) {
    if (query.trim() !== (seed.query ?? '')) patch.query = query.trim();
    // Compared decoded, and written back with any directives past the first kept.
    if (!sameSortChoice(sort, decodeSort(seed.sort_json))) {
      patch.sort_json = applySortChoice(seed.sort_json, sort);
    }
    if (limitNum !== (seed.limit ?? 0)) patch.limit = limitNum;
  }
  const dirty = Object.keys(patch).length > 0;
  const queryDirty = isDynamic && query.trim() !== (seed.query ?? '');
  const canSave =
    canManage && dirty && name.trim() !== '' && !queryInvalid && !limitInvalid && !saving;

  const save = async () => {
    setQueryServerError(null);
    setActionError(null);
    onNotice(null);
    setSaving(true);
    try {
      await update(patch);
      onNotice('Saved.');
      onSaved(); // re-seeds the form from what the server stored
    } catch (err) {
      const message = messageOf(err, 'The request failed');
      if (patch.query !== undefined && err instanceof CollectionApiError && err.status === 400) {
        setQueryServerError(message);
      } else {
        setActionError(message);
      }
      // The edit stays in the form either way; only a conflict needs a re-read.
      if ((err as { status?: number })?.status === 409) await refresh();
    } finally {
      setSaving(false);
    }
  };

  const titleOf = (bid: string, book: Book | undefined) =>
    book?.title ?? (lookupDone ? bid : 'Loading...');

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
        <Alert severity="success" sx={{ mb: 2 }} onClose={() => onNotice(null)}>
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
              onChange={(next) => {
                setQuery(next);
                setQueryServerError(null);
              }}
              disabled={!canManage}
              serverError={queryServerError}
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
          </>
        )}
        <Box sx={{ mt: 1, display: 'flex', gap: 1 }}>
          <Tooltip title={canManage ? '' : MANAGE_TOOLTIP}>
            <span>
              <Button variant="contained" disabled={!canSave} onClick={() => void save()}>
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
            {/* Preview runs the SAVED query, not the text in the box. */}
            <Button disabled={saving || loading} onClick={() => void run(refresh)}>
              {queryDirty ? 'Preview saved query' : 'Preview'}
            </Button>
            {!perViewer && (
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
            )}
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
      {isDynamic && perViewer && (
        <Typography variant="caption" sx={{ color: 'text.secondary', display: 'block', mb: 1 }}>
          These members are worked out for each person who views the collection, from their own
          listening state, on every view. They are never stored, so there is nothing to materialize;
          Preview re-runs the saved query for you.
        </Typography>
      )}
      {isDynamic && !perViewer && (
        <Typography variant="caption" sx={{ color: 'text.secondary', display: 'block', mb: 1 }}>
          Preview re-runs the saved query for anyone who can view the collection. It also refreshes
          the stored members that the Audiobookshelf app serves, but only as a best effort: if the
          search index is not ready it quietly shows the previous members. Materialize needs the
          collections.manage permission, tells you when the index is not ready (503), and reports a
          failed save or a conflict.
        </Typography>
      )}
      {membersUnavailable && (
        <Alert severity="warning" sx={{ mb: 1 }}>
          {collection.members_unavailable_message ??
            "This collection's members could not be worked out right now."}
        </Alert>
      )}

      {booksError ? (
        <Alert severity="error">{booksError}</Alert>
      ) : members.length === 0 && membersUnavailable ? null : members.length === 0 ? (
        <Typography sx={{ color: 'text.secondary' }}>
          {isDynamic ? 'No books match this query.' : 'No books in this collection yet.'}
        </Typography>
      ) : (
        <>
          <Table size="small" aria-label="Members" aria-busy={!lookupDone}>
            <TableHead>
              <TableRow>
                <TableCell>Title</TableCell>
                <TableCell>Author</TableCell>
                {!isDynamic && <TableCell align="right" />}
              </TableRow>
            </TableHead>
            <TableBody>
              {pageIds.map((bid) => {
                const book = known.get(bid);
                return (
                  <TableRow key={bid}>
                    <TableCell>
                      <Link component={RouterLink} to={`/library/${bid}`}>
                        {titleOf(bid, book)}
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
            page={safePage}
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
            disabled={saving}
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
