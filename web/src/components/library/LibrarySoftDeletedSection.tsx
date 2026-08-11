// file: web/src/components/library/LibrarySoftDeletedSection.tsx
// version: 1.1.0
// guid: 26804E8D-51BA-462C-9BBE-45ED69E17B9F
// last-edited: 2026-08-11

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
} from '@mui/material';
import {
  ExpandMore as ExpandMoreIcon,
  Refresh as RefreshIcon,
} from '@mui/icons-material';
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
}: LibrarySoftDeletedSectionProps) {
  return (
    <Paper sx={{ p: 2, mt: 3 }}>
      <Stack
        direction="row"
        alignItems="center"
        justifyContent="space-between"
        spacing={2}
        sx={{ cursor: 'pointer' }}
        onClick={onToggleExpanded}
      >
        <Stack direction="row" alignItems="center" spacing={1}>
          <ExpandMoreIcon
            sx={{
              transform: softDeletedExpanded ? 'rotate(180deg)' : 'rotate(0deg)',
              transition: 'transform 0.2s',
            }}
          />
          <Typography variant="h6">Soft-Deleted Books</Typography>
        </Stack>
        <Stack direction="row" spacing={1} alignItems="center">
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
                {softDeletedCount.toLocaleString()} soft-deleted books. Rendering
                all of them freezes the page, so the rest are reachable through
                the bulk purge/restore controls rather than row by row.
              </Alert>
            )}
            <List dense sx={{ mt: 1 }} data-testid="soft-deleted-list">
              {softDeletedBooks.map((book) => {
              const deletedAt =
                book.marked_for_deletion_at && new Date(book.marked_for_deletion_at);
              return (
                <ListItem
                  key={book.id}
                  alignItems="flex-start"
                  data-testid="soft-deleted-item"
                >
                  <ListItemText
                    primary={book.title || 'Untitled'}
                    secondary={
                      <Stack spacing={0.5}>
                        <Typography variant="body2" color="text.secondary">
                          {book.author || 'Unknown Author'}
                        </Typography>
                        {deletedAt && (
                          <Typography variant="caption" color="text.secondary">
                            Soft deleted at {deletedAt.toLocaleString()}
                          </Typography>
                        )}
                        {book.file_path && (
                          <Typography variant="caption" color="text.secondary">
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
                    <Button
                      size="small"
                      color="error"
                      variant="outlined"
                      onClick={() => onPurgeOne(book)}
                      disabled={purgingBookId === book.id || purgeInProgress}
                    >
                      {purgingBookId === book.id ? 'Purging...' : 'Purge now'}
                    </Button>
                  </ListItemSecondaryAction>
                </ListItem>
                );
              })}
            </List>
          </>
        )}
      </Collapse>
    </Paper>
  );
}
