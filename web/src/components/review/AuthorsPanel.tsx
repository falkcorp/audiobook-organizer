// file: web/src/components/review/AuthorsPanel.tsx
// version: 1.1.0
// guid: 6d03b9f2-a4e1-4875-b3c6-0f92d8e15a47
// last-edited: 2026-10-13

/**
 * The Authors & series lane's surface: a rail, the group spine, and the lane's
 * action bar.
 *
 * The segmented control at the top picks the lane's sub-view. Only Authors exists
 * today; Series and AI scans join it in later changes, and the control is here
 * now so the lane does not change shape when they do.
 *
 * THE FOUR STATES (plus one). Loading, error, empty and populated each render
 * differently, and so does a cold cache (`needs-scan`): the server answered, but
 * nobody has scanned, which is neither "no duplicates" nor a failure. The error
 * state names the failure and offers Retry; none of them is only a console line.
 *
 * COUNTS CLICK THROUGH. The kind chips (All / Name variants / Composite /
 * Production companies) partition the list and each filters the spine to exactly
 * the rows its number counts. "Author candidates (N)" opens the N pairs it counts.
 * "Merge Selected (N)" counts the ticked groups that still exist, and Merge All
 * counts the groups Merge actually applies to -- a composite or production-company
 * group has no variants to fold in, so counting it would promise a merge that
 * never happens.
 */

import { useEffect, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import {
  Alert,
  Box,
  Button,
  Checkbox,
  Chip,
  CircularProgress,
  Collapse,
  Dialog,
  DialogActions,
  DialogContent,
  DialogContentText,
  DialogTitle,
  IconButton,
  LinearProgress,
  Paper,
  Popover,
  Stack,
  TablePagination,
  ToggleButton,
  ToggleButtonGroup,
  Tooltip,
  Typography,
} from '@mui/material';
import CheckCircleIcon from '@mui/icons-material/CheckCircle';
import MenuBookIcon from '@mui/icons-material/MenuBook';
import MergeIcon from '@mui/icons-material/MergeType';
import RefreshIcon from '@mui/icons-material/Refresh';
import type { Book } from '../../services/api';
import { isAbortError } from '../../utils/apiFetch';
import { BulkConfirmDialog } from '../common/BulkConfirmDialog';
import { SelectAllMatchingBanner } from '../common/SelectAllMatchingBanner';
import type { AuthorId } from './reviewActions';
import { authorsLane } from './lanes/authors';
import {
  AUTHOR_CANDIDATES_PAGE,
  AUTHOR_PAGE_SIZES,
  AUTHOR_SHORTCUTS,
  type AuthorKindFilter,
  type AuthorsLane,
} from './lanes/useAuthorsLane';
import { fetchBooksByAuthor } from './lanes/authorsReads';
import { AuthorGroupSpine } from './spine/AuthorGroupSpine';

/** The lane's sub-views. Series and AI scans are added by later changes. */
const SUB_VIEWS = [{ value: 'authors', label: 'Authors' }] as const;

const KIND_CHIPS: ReadonlyArray<{ value: AuthorKindFilter; label: string }> = [
  { value: 'all', label: 'All groups' },
  { value: 'variants', label: 'Name variants' },
  { value: 'composite', label: 'Composite' },
  { value: 'production', label: 'Production companies' },
];

/** Strips "(Unabridged)" and a leading "[tag]" from a title, for display only. */
function cleanDisplayTitle(title: string): string {
  return title
    .replace(/\s*\((un)?abridged\)/gi, '')
    .replace(/^\[.*?\]\s*/g, '')
    .trim();
}

// ---------------------------------------------------------------------------
// Books popover
// ---------------------------------------------------------------------------

interface BooksResult {
  books: Book[];
  /** Authors whose fetch failed: kept apart from `books` so a failure is never "0 books". */
  failed: number;
}

export function AuthorBooksPopover({
  anchorEl,
  onClose,
  authorIds,
}: {
  anchorEl: HTMLElement | null;
  onClose: () => void;
  authorIds: number[];
}) {
  const navigate = useNavigate();
  const [retry, setRetry] = useState(0);
  const key = `${authorIds.join(',')}:${retry}`;
  const [result, setResult] = useState<{ key: string; value: BooksResult } | null>(null);
  const idsKey = authorIds.join(',');

  useEffect(() => {
    if (!anchorEl || idsKey === '') return;
    const ctl = new AbortController();
    const ids = idsKey.split(',').map(Number);
    Promise.allSettled(ids.map((id) => fetchBooksByAuthor(id, ctl.signal))).then((settled) => {
      if (ctl.signal.aborted) return;
      const seen = new Set<string>();
      const books: Book[] = [];
      let failed = 0;
      for (const r of settled) {
        if (r.status === 'fulfilled') {
          for (const b of r.value) {
            if (!seen.has(b.id)) {
              seen.add(b.id);
              books.push(b);
            }
          }
        } else if (!isAbortError(r.reason)) failed += 1;
      }
      setResult({ key, value: { books, failed } });
    });
    return () => ctl.abort();
  }, [anchorEl, idsKey, key]);

  // Anything that is not the answer to THIS request (another author's, an earlier
  // retry's) reads as loading.
  const answer = result && result.key === key ? result.value : null;

  return (
    <Popover
      open={Boolean(anchorEl)}
      anchorEl={anchorEl}
      onClose={onClose}
      anchorOrigin={{ vertical: 'bottom', horizontal: 'left' }}
      transformOrigin={{ vertical: 'top', horizontal: 'left' }}
      slotProps={{ paper: { sx: { maxWidth: 480, maxHeight: 400, overflow: 'auto', p: 1 } } }}
    >
      {!answer ? (
        <Box sx={{ p: 2, textAlign: 'center' }} data-testid="author-books-loading">
          <CircularProgress size={24} />
        </Box>
      ) : (
        <>
          {answer.failed > 0 && (
            <Box sx={{ p: 1, display: 'flex', alignItems: 'center', gap: 1, flexWrap: 'wrap' }}>
              <Typography variant="body2" color="error">
                {`Could not load ${answer.failed} of ${authorIds.length} author${
                  authorIds.length === 1 ? "'s" : "s'"
                } books — count may be incomplete.`}
              </Typography>
              <Button size="small" variant="outlined" color="error" onClick={() => setRetry((t) => t + 1)}>
                Retry
              </Button>
            </Box>
          )}
          {(answer.books.length > 0 || answer.failed === 0) && (
            // The chip on the card sums each author record's credits, so a book
            // credited to two of them is counted twice there; this is the
            // distinct list below.
            <Typography variant="caption" sx={{ color: 'text.secondary', px: 1 }} data-testid="author-books-header">
              {answer.failed > 0 ? 'At least ' : ''}
              {answer.books.length} book{answer.books.length === 1 ? '' : 's'} across {authorIds.length} author
              record{authorIds.length === 1 ? '' : 's'}
            </Typography>
          )}
          {answer.books.length === 0 ? (
            // Empty copy only when every fetch succeeded: a failure must never
            // read as "this author has no books" inside a merge decision.
            answer.failed === 0 && (
              <Typography variant="body2" sx={{ color: 'text.secondary', p: 2 }}>
                No books found
              </Typography>
            )
          ) : (
            <Stack spacing={0.5}>
              {answer.books.map((book) => (
                <Box
                  key={book.id}
                  role="button"
                  tabIndex={0}
                  onClick={() => {
                    onClose();
                    navigate(`/library/${book.id}`);
                  }}
                  onKeyDown={(e) => {
                    if (e.key === 'Enter') {
                      onClose();
                      navigate(`/library/${book.id}`);
                    }
                  }}
                  sx={{
                    display: 'flex',
                    alignItems: 'center',
                    gap: 1,
                    p: 0.5,
                    cursor: 'pointer',
                    borderRadius: 1,
                    '&:hover': { bgcolor: 'action.hover' },
                  }}
                >
                  {book.cover_url ? (
                    <Box
                      component="img"
                      src={book.cover_url}
                      alt=""
                      sx={{ width: 40, height: 56, objectFit: 'cover', borderRadius: 0.5, flexShrink: 0 }}
                    />
                  ) : (
                    <Box
                      sx={{
                        width: 40,
                        height: 56,
                        display: 'flex',
                        alignItems: 'center',
                        justifyContent: 'center',
                        bgcolor: 'action.selected',
                        borderRadius: 0.5,
                        flexShrink: 0,
                      }}
                    >
                      <MenuBookIcon fontSize="small" color="disabled" />
                    </Box>
                  )}
                  <Box sx={{ overflow: 'hidden' }}>
                    <Typography variant="body2" noWrap sx={{ fontWeight: 'medium' }}>
                      {cleanDisplayTitle(book.title)}
                    </Typography>
                    {book.author_name && (
                      <Typography variant="caption" noWrap sx={{ color: 'text.secondary' }}>
                        {book.author_name}
                      </Typography>
                    )}
                  </Box>
                </Box>
              ))}
            </Stack>
          )}
        </>
      )}
    </Popover>
  );
}

// ---------------------------------------------------------------------------
// Author candidates (the embedding pairs the Duplicates lane hides)
// ---------------------------------------------------------------------------

function CandidatesSection({ authors }: { authors: AuthorsLane }) {
  const [open, setOpen] = useState(false);
  const c = authors.candidates;
  const groupOf = (id: string) =>
    authors.groups.find((g) => g.key === id || g.variants.some((v) => String(v.id) === id));

  return (
    <Box data-testid="author-candidates" sx={{ px: 2, pb: 1 }}>
      <Stack direction="row" spacing={1} sx={{ alignItems: 'center' }}>
        <Button
          size="small"
          aria-expanded={open}
          onClick={() => setOpen((o) => !o)}
          data-testid="author-candidates-toggle"
        >
          {c.status === 'loading'
            ? 'Author candidates (…)'
            : c.status === 'error'
              ? 'Author candidates (unavailable)'
              : `Author candidates (${c.total})`}
        </Button>
        <Typography variant="caption" sx={{ color: 'text.secondary' }}>
          Pairs of authors the similarity scan flagged. The Duplicates lane lists books only.
        </Typography>
      </Stack>
      <Collapse in={open} unmountOnExit>
        {c.status === 'loading' && <LinearProgress data-testid="author-candidates-loading" />}
        {c.status === 'error' && (
          <Alert
            severity="error"
            data-testid="author-candidates-error"
            action={
              <Button color="inherit" size="small" onClick={c.reload}>
                Retry
              </Button>
            }
          >
            {c.error}
          </Alert>
        )}
        {c.status === 'empty' && (
          <Typography variant="body2" sx={{ color: 'text.secondary', py: 1 }} data-testid="author-candidates-empty">
            No pending author candidates.
          </Typography>
        )}
        {c.status === 'populated' && (
          <>
            <Stack spacing={0.5} sx={{ mt: 1 }}>
              {c.rows.map((row) => {
                const target = groupOf(row.entity_a_id) ?? groupOf(row.entity_b_id);
                return (
                  <Stack
                    key={row.id}
                    direction="row"
                    spacing={1}
                    data-testid={`author-candidate-${row.id}`}
                    sx={{ alignItems: 'center' }}
                  >
                    <Typography variant="body2">
                      {c.nameOf(row.entity_a_id)} ↔ {c.nameOf(row.entity_b_id)}
                    </Typography>
                    {row.similarity != null && (
                      <Chip size="small" variant="outlined" label={`${Math.round(row.similarity * 100)}% similar`} />
                    )}
                    {target && (
                      <Button size="small" onClick={() => authors.focusGroup(target.key)}>
                        Show group
                      </Button>
                    )}
                  </Stack>
                );
              })}
            </Stack>
            <TablePagination
              component="div"
              count={c.total}
              page={Math.floor(c.offset / AUTHOR_CANDIDATES_PAGE)}
              rowsPerPage={AUTHOR_CANDIDATES_PAGE}
              rowsPerPageOptions={[AUTHOR_CANDIDATES_PAGE]}
              onPageChange={(_, p) => c.setOffset(p * AUTHOR_CANDIDATES_PAGE)}
            />
          </>
        )}
      </Collapse>
    </Box>
  );
}

// ---------------------------------------------------------------------------
// The panel
// ---------------------------------------------------------------------------

export interface AuthorsPanelProps {
  authors: AuthorsLane;
}

export function AuthorsPanel({ authors }: AuthorsPanelProps) {
  // The groups the reviewer is confirming, captured when the dialog opened.
  const [confirmAll, setConfirmAll] = useState<AuthorId[] | null>(null);
  const [confirmSelected, setConfirmSelected] = useState(false);
  const { selection } = authors;
  const mergeableCount = authors.mergeable.length;
  const booksGroup = authors.booksFor ? authors.groups.find((g) => g.key === authors.booksFor?.key) : undefined;

  const mergeSelected = () => {
    setConfirmSelected(false);
    authors.dispatch({
      lane: 'authors',
      entity: 'author',
      type: 'mergeSelected',
      ids: authors.selectedGroups.map((g) => g.canonical.id),
    });
  };
  const requestMergeSelected = () => {
    // A selection wider than the page asks first, with its count.
    if (authors.selectedCount > authors.pageGroups.length) setConfirmSelected(true);
    else mergeSelected();
  };

  return (
    <Box sx={{ flex: 1, minHeight: 0, display: 'flex', flexDirection: 'column' }} data-testid="authors-panel">
      <Box
        data-testid="authors-rail"
        sx={{ borderBottom: 1, borderColor: 'divider', px: 2, py: 1, display: 'flex', flexDirection: 'column', gap: 1 }}
      >
        <Stack direction="row" spacing={1} useFlexGap sx={{ flexWrap: 'wrap', alignItems: 'center' }}>
          <ToggleButtonGroup size="small" exclusive value="authors" aria-label={`${authorsLane.label} sub-view`}>
            {SUB_VIEWS.map(({ value, label }) => (
              <ToggleButton key={value} value={value} data-testid={`authors-subview-${value}`}>
                {label}
              </ToggleButton>
            ))}
          </ToggleButtonGroup>
          <Typography variant="body2" sx={{ color: 'text.secondary', flexGrow: 1 }}>
            Author names that look like one person. Uses structured name comparison to detect variants like
            &quot;Author A. Example&quot; vs &quot;Author A Example&quot;.
          </Typography>
          {authors.groups.length > 0 && (
            <>
              {authors.selectedCount > 0 && (
                <Button
                  variant="contained"
                  startIcon={<MergeIcon />}
                  onClick={requestMergeSelected}
                  disabled={authors.busy}
                >
                  Merge Selected ({authors.selectedCount})
                </Button>
              )}
              <Button
                variant="contained"
                color="warning"
                startIcon={<MergeIcon />}
                onClick={() => setConfirmAll(authors.mergeable.map((g) => g.canonical.id))}
                disabled={authors.busy || mergeableCount === 0}
              >
                Merge all {mergeableCount} name-variant group{mergeableCount === 1 ? '' : 's'}
              </Button>
            </>
          )}
          <Button size="small" onClick={() => authors.setHelpOpen(true)}>
            Keyboard shortcuts (?)
          </Button>
          <Tooltip title="Refresh">
            <span>
              <IconButton onClick={authors.refresh} disabled={authors.busy} aria-label="Refresh">
                <RefreshIcon />
              </IconButton>
            </span>
          </Tooltip>
        </Stack>

        {authors.groups.length > 0 && (
          <Stack direction="row" spacing={1} useFlexGap sx={{ flexWrap: 'wrap', alignItems: 'center' }}>
            {KIND_CHIPS.map(({ value, label }) => (
              <Chip
                key={value}
                size="small"
                color={authors.filter === value ? 'primary' : 'default'}
                variant={authors.filter === value ? 'filled' : 'outlined'}
                label={`${label} (${authors.kindCounts[value]})`}
                aria-pressed={authors.filter === value}
                data-testid={`authors-kind-${value}`}
                onClick={() => authors.setFilter(value)}
              />
            ))}
          </Stack>
        )}
      </Box>

      {authors.stale && (
        <Alert
          severity="warning"
          data-testid="authors-stale"
          sx={{ mx: 2, mb: 1 }}
          action={
            <Button color="inherit" size="small" onClick={authors.refresh} disabled={authors.busy}>
              Rescan
            </Button>
          }
        >
          These results are stale since your last change: the server cleared its list of duplicate authors, so
          what is shown is the list as this screen edited it. Rescan to rebuild it from the library.
        </Alert>
      )}

      <CandidatesSection authors={authors} />

      {authors.bulk && (
        <Box sx={{ px: 2 }} data-testid="authors-bulk-progress">
          <LinearProgress variant="determinate" value={(authors.bulk.done / authors.bulk.total) * 100} />
          <Typography variant="caption" sx={{ color: 'text.secondary' }}>
            Processed {authors.bulk.done} of {authors.bulk.total} groups
          </Typography>
        </Box>
      )}
      {authors.scanning && <LinearProgress data-testid="authors-scanning" />}

      <Box sx={{ flex: 1, minHeight: 0, overflowY: 'auto', px: 2, py: 1 }}>
        {authors.status === 'loading' && (
          <Box sx={{ display: 'flex', justifyContent: 'center', py: 4 }} data-testid="authors-loading">
            <CircularProgress />
          </Box>
        )}

        {authors.status === 'error' && (
          <Alert
            severity="error"
            data-testid="authors-error"
            action={
              <Button color="inherit" size="small" onClick={authors.reload}>
                Retry
              </Button>
            }
          >
            Could not load duplicate authors: {authors.error}
          </Alert>
        )}

        {authors.status === 'needs-scan' && (
          <Paper sx={{ p: 4, textAlign: 'center' }} data-testid="authors-needs-scan">
            <Typography variant="h6">Authors have not been scanned yet</Typography>
            <Typography variant="body2" sx={{ color: 'text.secondary', mb: 2 }}>
              The server holds no duplicate-author results. This is not &quot;no duplicates&quot; — run the scan to find out.
            </Typography>
            <Button variant="contained" onClick={authors.refresh} disabled={authors.scanning}>
              Scan for duplicate authors
            </Button>
          </Paper>
        )}

        {authors.status === 'empty' && (
          <Paper sx={{ p: 4, textAlign: 'center' }} data-testid="authors-empty">
            <CheckCircleIcon sx={{ fontSize: 48, color: 'success.main', mb: 1 }} />
            <Typography variant="h6">No duplicate authors found</Typography>
            <Typography variant="body2" sx={{ color: 'text.secondary' }}>
              {authorsLane.emptyMessage}
            </Typography>
          </Paper>
        )}

        {authors.status === 'populated' && authors.filtered.length === 0 && (
          <Paper sx={{ p: 3, textAlign: 'center' }} data-testid="authors-filter-empty">
            <Typography variant="body1">No groups of this kind.</Typography>
            <Button size="small" onClick={() => authors.setFilter('all')}>
              Show all groups
            </Button>
          </Paper>
        )}

        {authors.status === 'populated' && authors.filtered.length > 0 && (
          <>
            <Stack direction="row" spacing={1} sx={{ alignItems: 'center', mb: 1 }}>
              <Checkbox
                size="small"
                checked={selection.header.checked}
                indeterminate={selection.header.indeterminate}
                disabled={authors.busy || selection.header.disabled}
                onChange={selection.togglePage}
                slotProps={{
                  input: { 'aria-label': `Select all ${authors.pageGroups.length} groups on this page` },
                }}
              />
              <Typography variant="caption" color="text.secondary">
                Select page · Shift-click a checkbox to select a range
              </Typography>
            </Stack>
            <SelectAllMatchingBanner
              selection={selection}
              pageCount={authors.pageGroups.length}
              totalMatching={authors.selectableTotal}
              noun="groups"
              testIdPrefix="author-groups-select-all"
            />
            <AuthorGroupSpine lane={authors} />
            <TablePagination
              component="div"
              count={authors.filtered.length}
              page={authors.page}
              onPageChange={(_, p) => authors.setPage(p)}
              rowsPerPage={authors.pageSize}
              onRowsPerPageChange={(e) => authors.setPageSize(parseInt(e.target.value, 10))}
              rowsPerPageOptions={[...AUTHOR_PAGE_SIZES]}
              labelRowsPerPage="Groups per page:"
              sx={{ mt: 2 }}
            />
          </>
        )}
      </Box>

      <AuthorBooksPopover
        anchorEl={authors.booksFor?.anchor ?? null}
        onClose={authors.closeBooks}
        authorIds={booksGroup ? [booksGroup.canonical.id, ...booksGroup.variants.map((v) => v.id)] : []}
      />

      <Dialog open={confirmAll !== null} onClose={() => setConfirmAll(null)}>
        <DialogTitle>Confirm Merge All</DialogTitle>
        <DialogContent>
          <DialogContentText>
            This will merge {confirmAll?.length} name-variant groups (composite and production-company groups
            are not included). This action cannot be undone. Are you sure?
          </DialogContentText>
          <DialogContentText sx={{ mt: 1 }} data-testid="authors-merge-all-warning">
            Keep this lane open: leaving it stops the remaining merges.
          </DialogContentText>
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setConfirmAll(null)}>Cancel</Button>
          <Button
            color="warning"
            variant="contained"
            onClick={() => {
              const expectedIds = confirmAll ?? [];
              setConfirmAll(null);
              authors.dispatch({ lane: 'authors', entity: 'author', type: 'mergeAllGroups', expectedIds });
            }}
          >
            Confirm
          </Button>
        </DialogActions>
      </Dialog>

      <BulkConfirmDialog
        open={confirmSelected}
        title={`Merge ${authors.selectedCount} selected author groups?`}
        confirmLabel={`Merge ${authors.selectedCount}`}
        onCancel={() => setConfirmSelected(false)}
        onConfirm={mergeSelected}
        testId="author-merge-selected-confirm"
      >
        The selection covers {authors.selectedCount} groups, more than the {authors.pageGroups.length} on this page.
        Each group&apos;s variants are merged into its canonical author.
      </BulkConfirmDialog>

      <Dialog open={authors.helpOpen} onClose={() => authors.setHelpOpen(false)} data-testid="authors-shortcut-help">
        <DialogTitle>Keyboard shortcuts</DialogTitle>
        <DialogContent>
          <Stack spacing={1} sx={{ minWidth: 280 }}>
            {AUTHOR_SHORTCUTS.map((s) => (
              <Stack key={s.keys} direction="row" spacing={2} sx={{ justifyContent: 'space-between' }}>
                <Chip label={s.keys} size="small" variant="outlined" />
                <Typography variant="body2">{s.action}</Typography>
              </Stack>
            ))}
          </Stack>
        </DialogContent>
      </Dialog>
    </Box>
  );
}
