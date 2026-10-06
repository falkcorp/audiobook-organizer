// file: web/src/components/library/LibrarySoftDeletedSection.tsx
// version: 1.3.0
// guid: 26804E8D-51BA-462C-9BBE-45ED69E17B9F
// last-edited: 2026-10-05

import { useState } from 'react';
import {
  Paper,
  Stack,
  Typography,
  Button,
  Chip,
  Collapse,
  Alert,
  List,
  ListItem,
  ListItemText,
  ListItemSecondaryAction,
  Dialog,
  DialogTitle,
  DialogContent,
  DialogContentText,
  DialogActions,
  Tooltip,
} from '@mui/material';
import { ExpandMore as ExpandMoreIcon, Refresh as RefreshIcon } from '@mui/icons-material';
import type { Audiobook } from '../../types';

export interface LibrarySoftDeletedSectionProps {
  softDeletedCount: number;
  softDeletedBooks: Audiobook[];
  softDeletedLoading: boolean;
  softDeletedExpanded: boolean;
  restoringBookId: string | null;
  purgeInProgress: boolean;
  purgingBookId: string | null;
  onToggleExpanded: () => void;
  onRefresh: () => void;
  onRestoreOne: (book: Audiobook) => void;
  onPurgeOne: (book: Audiobook) => void;
  // The book whose "discard progress and purge" is in flight, if any.
  discardingBookId?: string | null;
  // Called once the user confirmed the dialog for a book with progress.
  onDiscardProgressOne?: (book: Audiobook) => void;
  // A book whose "Purge now" was just refused because of its listening
  // progress: the discard confirmation opens for it, so the owner can choose
  // to drop the progress on purpose.
  discardPrompt?: Audiobook | null;
  onDiscardPromptClose?: () => void;
}

// The progress description a row and the confirm dialog show: the viewer's
// own progress, plus how many other users have some (their names are shown
// only to a user who may manage users).
export function progressText(book: Audiobook): string {
  const others = book.progress_other_users ?? 0;
  const othersText = others > 0 ? `${others} other user${others === 1 ? '' : 's'}` : '';
  if (book.progress_summary && othersText) return `${book.progress_summary}; and ${othersText}`;
  return book.progress_summary || othersText;
}

// The caption under a trashed book with (or with unreadable) progress. It
// says what the purge will actually do with this book: move the progress to
// the copy Audiobookshelf lists, or keep the book because there is none.
export function progressCaption(book: Audiobook): string | null {
  if (book.progress_unknown) {
    return 'Listening progress on this book could not be read, so the purge keeps it until it can be checked.';
  }
  if (!book.has_progress) return null;
  const what = progressText(book);
  const lead = `Has listening progress${what ? `: ${what}` : ''}.`;
  if (book.listed_copy_id) {
    return book.purge_eligible
      ? `${lead} A copy is in the Audiobookshelf library: the nightly purge moves the progress there and then purges this book. If it is still here after a nightly purge, moving the progress failed (see the purge log).`
      : `${lead} A copy is in the Audiobookshelf library: when the nightly purge takes this book, it moves the progress there first.`;
  }
  if (book.listed_copy_unknown) {
    return `${lead} Whether another copy is in the Audiobookshelf library could not be checked, so the purge keeps this book for now.`;
  }
  return book.purge_eligible
    ? `Kept in the trash because of listening progress${what ? `: ${what}` : ''}. There is no other copy of this book in the Audiobookshelf library to move it to.`
    : `${lead} There is no other copy of this book in the Audiobookshelf library to move it to, so the nightly purge will keep it.`;
}

export function LibrarySoftDeletedSection({
  softDeletedCount,
  softDeletedBooks,
  softDeletedLoading,
  softDeletedExpanded,
  restoringBookId,
  purgeInProgress,
  purgingBookId,
  onToggleExpanded,
  onRefresh,
  onRestoreOne,
  onPurgeOne,
  discardingBookId = null,
  onDiscardProgressOne,
  discardPrompt = null,
  onDiscardPromptClose,
}: LibrarySoftDeletedSectionProps) {
  // The book the confirm dialog is open for. The discard is irreversible, so
  // the button only opens this; the handler runs on Confirm.
  const [confirmDiscardState, setConfirmDiscard] = useState<Audiobook | null>(null);
  const confirmDiscard = confirmDiscardState ?? (onDiscardProgressOne ? discardPrompt : null);
  const closeDiscard = () => {
    setConfirmDiscard(null);
    onDiscardPromptClose?.();
  };

  return (
    <Paper sx={{ p: 2, mt: 3 }}>
      <Stack
        direction="row"
        spacing={2}
        onClick={onToggleExpanded}
        sx={{
          alignItems: 'center',
          justifyContent: 'space-between',
          cursor: 'pointer',
        }}
      >
        <Stack
          direction="row"
          spacing={1}
          sx={{
            alignItems: 'center',
          }}
        >
          <ExpandMoreIcon
            sx={[
              {
                transition: 'transform 0.2s',
              },
              softDeletedExpanded
                ? {
                    transform: 'rotate(180deg)',
                  }
                : {
                    transform: 'rotate(0deg)',
                  },
            ]}
          />
          <Typography variant="h6">Soft-Deleted Books</Typography>
        </Stack>
        <Stack
          direction="row"
          spacing={1}
          sx={{
            alignItems: 'center',
          }}
        >
          <Chip
            label={`${softDeletedCount} ${softDeletedCount === 1 ? 'item' : 'items'}`}
            color={softDeletedCount > 0 ? 'warning' : 'default'}
          />
          <Button
            size="small"
            variant="outlined"
            startIcon={<RefreshIcon />}
            onClick={(e) => {
              e.stopPropagation();
              onRefresh();
            }}
            disabled={softDeletedLoading}
          >
            {softDeletedLoading ? 'Refreshing...' : 'Refresh'}
          </Button>
        </Stack>
      </Stack>
      {/*
        `unmountOnExit` is load-bearing, not tidiness.

        MUI's Collapse keeps its children MOUNTED when closed — it animates
        height, it does not conditionally render. This panel is collapsed on
        every library load and the list it was handed held up to 10,000 books,
        so every one of those rows was built, styled and inserted into the
        document on a page the user opened to look at their books. Measured in
        library-load-perf.spec.ts (axis C), unthrottled: 10,000 collapsed rows
        add 140,000 DOM nodes and 8-11s of blocked main thread to a load whose
        page size was the default 20. Expanding the section afterwards changed
        the document's node count by exactly ZERO, which is how "collapsed does
        not mean unrendered" was confirmed rather than assumed.

        With unmountOnExit the closed panel costs nothing, and the fetch that
        supplies it is now count-only until it opens (see loadSoftDeleted).
      */}
      <Collapse in={softDeletedExpanded} unmountOnExit>
        {softDeletedLoading ? (
          <Typography variant="body2" sx={{ mt: 2 }}>
            Loading soft-deleted books...
          </Typography>
        ) : softDeletedBooks.length === 0 ? (
          <Alert severity="info" sx={{ mt: 2 }}>
            No soft-deleted books at the moment.
          </Alert>
        ) : (
          <>
            {/*
              The list is capped at useLibraryQuery's SOFT_DELETED_PAGE_SIZE
              rows. Say so, rather
              than showing a short list next to a bigger count and letting the
              two silently disagree — a user with 900 soft-deleted books must
              not be left believing the 400 they cannot see are gone.
            */}
            {softDeletedCount > softDeletedBooks.length && (
              <Alert severity="info" sx={{ mt: 2 }}>
                Showing the first {softDeletedBooks.length.toLocaleString()} of{' '}
                {softDeletedCount.toLocaleString()} soft-deleted books. Rendering all of them
                freezes the page, so the rest are reachable through the bulk purge/restore controls
                rather than row by row.
              </Alert>
            )}
            <List dense sx={{ mt: 1 }} data-testid="soft-deleted-list">
              {softDeletedBooks.map((book) => {
                const deletedAt =
                  book.marked_for_deletion_at && new Date(book.marked_for_deletion_at);
                return (
                  <ListItem key={book.id} alignItems="flex-start" data-testid="soft-deleted-item">
                    <ListItemText
                      primary={
                        <Stack direction="row" spacing={1} sx={{ alignItems: 'center' }}>
                          <span>{book.title || 'Untitled'}</span>
                          {book.has_progress && (
                            <Tooltip
                              title={
                                progressText(book) || 'A user has listening progress on this book'
                              }
                            >
                              <Chip
                                size="small"
                                color="info"
                                label="has progress"
                                data-testid="soft-deleted-has-progress"
                              />
                            </Tooltip>
                          )}
                          {book.progress_unknown && (
                            <Tooltip title="Listening progress on this book could not be read">
                              <Chip
                                size="small"
                                color="warning"
                                label="progress unknown"
                                data-testid="soft-deleted-progress-unknown"
                              />
                            </Tooltip>
                          )}
                        </Stack>
                      }
                      secondary={
                        <Stack spacing={0.5}>
                          <Typography
                            variant="body2"
                            sx={{
                              color: 'text.secondary',
                            }}
                          >
                            {book.author || 'Unknown Author'}
                          </Typography>
                          {deletedAt && (
                            <Typography
                              variant="caption"
                              sx={{
                                color: 'text.secondary',
                              }}
                            >
                              Soft deleted at {deletedAt.toLocaleString()}
                            </Typography>
                          )}
                          {progressCaption(book) && (
                            <Typography
                              variant="caption"
                              sx={{
                                color: 'text.secondary',
                              }}
                              data-testid="soft-deleted-progress-caption"
                            >
                              {progressCaption(book)}
                            </Typography>
                          )}
                          {book.file_path && (
                            <Typography
                              variant="caption"
                              sx={{
                                color: 'text.secondary',
                              }}
                            >
                              {book.file_path}
                            </Typography>
                          )}
                        </Stack>
                      }
                    />
                    <ListItemSecondaryAction>
                      <Button
                        size="small"
                        variant="outlined"
                        sx={{ mr: 1 }}
                        onClick={() => onRestoreOne(book)}
                        disabled={
                          restoringBookId === book.id ||
                          purgeInProgress ||
                          purgingBookId === book.id
                        }
                      >
                        {restoringBookId === book.id ? 'Restoring...' : 'Restore'}
                      </Button>
                      {book.has_progress && !book.listed_copy_id && onDiscardProgressOne && (
                        <Button
                          size="small"
                          color="error"
                          variant="outlined"
                          sx={{ mr: 1 }}
                          onClick={() => setConfirmDiscard(book)}
                          disabled={
                            discardingBookId === book.id ||
                            purgeInProgress ||
                            purgingBookId === book.id ||
                            restoringBookId === book.id
                          }
                        >
                          {discardingBookId === book.id
                            ? 'Discarding...'
                            : 'Discard progress and purge'}
                        </Button>
                      )}
                      <Button
                        size="small"
                        color="error"
                        variant="outlined"
                        onClick={() => onPurgeOne(book)}
                        disabled={purgingBookId === book.id || purgeInProgress}
                      >
                        {purgingBookId === book.id
                          ? 'Purging...'
                          : book.has_progress && book.listed_copy_id
                            ? 'Move progress and purge'
                            : 'Purge now'}
                      </Button>
                    </ListItemSecondaryAction>
                  </ListItem>
                );
              })}
            </List>
          </>
        )}
      </Collapse>
      <Dialog
        open={confirmDiscard !== null}
        onClose={closeDiscard}
        aria-labelledby="discard-progress-title"
      >
        <DialogTitle id="discard-progress-title">Discard progress and purge?</DialogTitle>
        <DialogContent>
          <DialogContentText component="div">
            <Typography variant="body1" sx={{ mb: 1 }}>
              &ldquo;{confirmDiscard?.title || 'Untitled'}&rdquo; will be permanently deleted from
              the library, and every user&apos;s listening progress on it will be lost:
            </Typography>
            <Typography variant="body2" sx={{ mb: 1 }} data-testid="discard-progress-summary">
              {(confirmDiscard && progressText(confirmDiscard)) || 'listening progress'}
            </Typography>
            <Typography variant="body2">
              That includes positions, finished status, percent listened and bookmarks. This cannot
              be undone. To keep the progress, restore the book instead.
            </Typography>
          </DialogContentText>
        </DialogContent>
        <DialogActions>
          <Button onClick={closeDiscard}>Cancel</Button>
          <Button
            color="error"
            variant="contained"
            onClick={() => {
              const book = confirmDiscard;
              closeDiscard();
              if (book && onDiscardProgressOne) onDiscardProgressOne(book);
            }}
          >
            Discard progress and purge
          </Button>
        </DialogActions>
      </Dialog>
    </Paper>
  );
}
