// file: web/src/components/bookdetail/BookDetailDialogs.tsx
// version: 1.1.0
// guid: b8c9d0e1-f2a3-4567-bcde-678901234567
// last-edited: 2026-08-10
import {
  Alert,
  Box,
  Button,
  Checkbox,

  Collapse,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  FormControlLabel,
  IconButton,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableRow,
  Typography,
} from '@mui/material';
import SaveIcon from '@mui/icons-material/Save';
import BuildIcon from '@mui/icons-material/Build';
import KeyboardArrowDownIcon from '@mui/icons-material/KeyboardArrowDown';
import KeyboardArrowUpIcon from '@mui/icons-material/KeyboardArrowUp';
import FileCopyIcon from '@mui/icons-material/FileCopy';
import LabelIcon from '@mui/icons-material/Label';
import ImageIcon from '@mui/icons-material/Image';
import WarningAmberIcon from '@mui/icons-material/WarningAmber';
import DriveFileRenameOutlineIcon from '@mui/icons-material/DriveFileRenameOutline';
import type { Book, BookSegment, OrganizePreviewResponse } from '../../services/api';
import { MetadataEditDialog } from '../audiobooks/MetadataEditDialog';
import { MetadataHistory } from '../MetadataHistory';
import { MetadataSearchDialog } from '../audiobooks/MetadataSearchDialog';
import { RelocateFileDialog } from '../audiobooks/RelocateFileDialog';
import VersionsPanel from '../audiobooks/VersionsPanel';
import AddToPlaylistDialog from '../audiobooks/AddToPlaylistDialog';
import type { Audiobook } from '../../types';
import { formatDateTime } from './bookDetailUtils';

export type MetadataRejection = {
  id: string;
  book_id: string;
  source: string;
  candidate_asin?: string;
  candidate_isbn?: string;
  candidate_title?: string;
  candidate_author?: string;
  rejection_reason: string;
  score?: number;
  rejected_at: string;
};

export interface BookDetailDialogsProps {
  book: Book;
  segments: BookSegment[];
  // delete
  deleteDialogOpen: boolean;
  deleteOptions: { softDelete: boolean; blockHash: boolean };
  onCloseDelete: () => void;
  onSetDeleteOptions: (opts: { softDelete: boolean; blockHash: boolean }) => void;
  onDelete: () => void;
  // organize preview
  organizePreviewDialogOpen: boolean;
  organizePreview: OrganizePreviewResponse | null;
  expandedTagStep: boolean;
  applyingOrganize: boolean;
  onSetExpandedTagStep: (v: boolean) => void;
  onCloseOrganizePreview: () => void;
  onApplyOrganize: () => void;
  // write back
  writeBackDialogOpen: boolean;
  writingToFiles: boolean;
  onCloseWriteBack: () => void;
  onWriteBackMetadata: () => void;
  // purge
  purgeDialogOpen: boolean;
  purgeConfirmed: boolean;
  actionLoading: boolean;
  onSetPurgeConfirmed: (v: boolean) => void;
  onClosePurge: () => void;
  onPurge: () => void;
  // edit metadata
  editDialogOpen: boolean;
  editAudiobook: Audiobook | null;
  onCloseEdit: () => void;
  onEditSave: (audiobook: Audiobook, dirtyFields: Set<string>) => Promise<void>;
  // conflict
  conflictDialogOpen: boolean;
  onCloseConflict: () => void;
  onConflictReload: () => void;
  onConflictOverwrite: () => void;
  // history
  historyDialogOpen: boolean;
  onCloseHistory: () => void;
  onUndoComplete: () => void;
  // metadata search
  metadataSearchOpen: boolean;
  onCloseMetadataSearch: () => void;
  onMetadataApplied: (updatedBook: Book) => void;
  toast: (message: string, severity?: 'success' | 'error' | 'warning' | 'info', action?: { label: string; onClick: () => void }) => void;
  // relocate
  relocateSegment: BookSegment | null;
  onCloseRelocate: () => void;
  onRelocated: () => Promise<void>;
  // rejection history
  rejections: MetadataRejection[];
  rejHistoryOpen: boolean;
  onSetRejHistoryOpen: (v: boolean) => void;
  // playlist
  playlistDialogOpen: boolean;
  onClosePlaylist: () => void;
}

export const BookDetailDialogs = ({
  book,
  segments,
  deleteDialogOpen,
  deleteOptions,
  onCloseDelete,
  onSetDeleteOptions,
  onDelete,
  organizePreviewDialogOpen,
  organizePreview,
  expandedTagStep,
  applyingOrganize,
  onSetExpandedTagStep,
  onCloseOrganizePreview,
  onApplyOrganize,
  writeBackDialogOpen,
  writingToFiles,
  onCloseWriteBack,
  onWriteBackMetadata,
  purgeDialogOpen,
  purgeConfirmed,
  actionLoading,
  onSetPurgeConfirmed,
  onClosePurge,
  onPurge,
  editDialogOpen,
  editAudiobook,
  onCloseEdit,
  onEditSave,
  conflictDialogOpen,
  onCloseConflict,
  onConflictReload,
  onConflictOverwrite,
  historyDialogOpen,
  onCloseHistory,
  onUndoComplete,
  metadataSearchOpen,
  onCloseMetadataSearch,
  onMetadataApplied,
  toast,
  relocateSegment,
  onCloseRelocate,
  onRelocated,
  rejections,
  rejHistoryOpen,
  onSetRejHistoryOpen,
  playlistDialogOpen,
  onClosePlaylist,
}: BookDetailDialogsProps) => {
  return (
    <>
      <Dialog open={deleteDialogOpen} onClose={onCloseDelete}>
        <DialogTitle>Delete Audiobook</DialogTitle>
        <DialogContent dividers>
          <Typography variant="body1" gutterBottom>
            {deleteOptions.softDelete
              ? 'Soft delete hides the book from the library but keeps it available for purge or restore.'
              : 'Hard delete will remove this book immediately.'}
          </Typography>
          <FormControlLabel
            control={
              <Checkbox
                checked={deleteOptions.softDelete}
                onChange={(e) =>
                  onSetDeleteOptions({
                    ...deleteOptions,
                    softDelete: e.target.checked,
                  })
                }
              />
            }
            label="Soft delete (recommended)"
          />
          <FormControlLabel
            control={
              <Checkbox
                checked={deleteOptions.blockHash}
                onChange={(e) =>
                  onSetDeleteOptions({
                    ...deleteOptions,
                    blockHash: e.target.checked,
                  })
                }
              />
            }
            label="Prevent reimporting this file (block hash)"
          />
          <Typography variant="caption" color="text.secondary" sx={{ ml: 4, display: 'block', mt: -0.5, mb: 1 }}>
            Block hash prevents this exact file from being re-imported by remembering its unique fingerprint.
          </Typography>
          <Alert severity="warning" sx={{ mt: 2 }}>
            Soft deleted books can be restored or purged later. Blocking the
            hash prevents reimports of the same file.
          </Alert>
        </DialogContent>
        <DialogActions>
          <Button onClick={onCloseDelete}>Cancel</Button>
          <Button
            onClick={onDelete}
            color={deleteOptions.softDelete ? 'warning' : 'error'}
            variant="contained"
            disabled={actionLoading}
          >
            {deleteOptions.softDelete ? 'Soft Delete' : 'Delete Now'}
          </Button>
        </DialogActions>
      </Dialog>

      {/* Organize preview dialog */}
      <Dialog
        open={organizePreviewDialogOpen}
        onClose={onCloseOrganizePreview}
        maxWidth="md"
        fullWidth
      >
        <DialogTitle>Preview Organize</DialogTitle>
        <DialogContent>
          {organizePreview && (
            <>
              {organizePreview.steps.length === 0 && (
                <Alert severity="info" sx={{ mb: 2 }}>
                  This book is already organized. No changes needed.
                </Alert>
              )}

              {organizePreview.steps.map((step, index) => (
                <Box
                  key={index}
                  sx={[{
                    display: 'flex',
                    alignItems: 'flex-start',
                    gap: 1.5,
                    mb: 2,
                    p: 1.5,
                    borderRadius: 1
                  }, step.action === 'warning' ? {
                    bgcolor: 'warning.main'
                  } : {
                    bgcolor: 'action.hover'
                  }, step.action === 'warning' ? {
                    opacity: 0.9
                  } : {
                    opacity: 1
                  }]}
                >
                  <Box sx={[{
                    mt: 0.25
                  }, step.action === 'warning' ? {
                    color: 'warning.contrastText'
                  } : {
                    color: 'primary.main'
                  }]}>
                    {step.action === 'copy' && <FileCopyIcon />}
                    {step.action === 'rename' && <DriveFileRenameOutlineIcon />}
                    {step.action === 'write_tags' && <LabelIcon />}
                    {step.action === 'embed_cover' && <ImageIcon />}
                    {step.action === 'warning' && <WarningAmberIcon />}
                  </Box>
                  <Box sx={{ flex: 1, minWidth: 0 }}>
                    <Typography
                      variant="subtitle2"
                      sx={[step.action === 'warning' ? {
                        color: 'warning.contrastText'
                      } : {
                        color: 'text.primary'
                      }]}
                    >
                      {step.description}
                    </Typography>

                    {(step.from || step.to) && (
                      <Table size="small" sx={{ mt: 0.5 }}>
                        <TableBody>
                          {step.from && (
                            <TableRow>
                              <TableCell sx={{ fontWeight: 'bold', width: 60, py: 0.5, border: 0 }}>From</TableCell>
                              <TableCell sx={{ fontFamily: 'monospace', fontSize: '0.8rem', wordBreak: 'break-all', py: 0.5, border: 0 }}>
                                {step.from}
                              </TableCell>
                            </TableRow>
                          )}
                          {step.to && (
                            <TableRow>
                              <TableCell sx={{ fontWeight: 'bold', width: 60, py: 0.5, border: 0 }}>To</TableCell>
                              <TableCell sx={{ fontFamily: 'monospace', fontSize: '0.8rem', wordBreak: 'break-all', py: 0.5, border: 0 }}>
                                {step.to}
                              </TableCell>
                            </TableRow>
                          )}
                        </TableBody>
                      </Table>
                    )}

                    {step.files && step.files.length > 0 && (
                      <Box sx={{ mt: 0.5 }}>
                        <Typography variant="caption" color="text.secondary">
                          Files to copy: {step.files.join(', ')}
                        </Typography>
                      </Box>
                    )}

                    {step.tags && (
                      <>
                        <Button
                          size="small"
                          onClick={() => onSetExpandedTagStep(!expandedTagStep)}
                          endIcon={expandedTagStep ? <KeyboardArrowUpIcon /> : <KeyboardArrowDownIcon />}
                          sx={{ mt: 0.5, textTransform: 'none' }}
                        >
                          {Object.keys(step.tags).length} tags will be written
                        </Button>
                        <Collapse in={expandedTagStep}>
                          <Table size="small" sx={{ mt: 0.5 }}>
                            <TableHead>
                              <TableRow>
                                <TableCell>Tag</TableCell>
                                <TableCell>Value</TableCell>
                              </TableRow>
                            </TableHead>
                            <TableBody>
                              {Object.entries(step.tags).map(([key, val]) => (
                                <TableRow key={key}>
                                  <TableCell sx={{ fontWeight: 'bold' }}>{key}</TableCell>
                                  <TableCell>{String(val)}</TableCell>
                                </TableRow>
                              ))}
                            </TableBody>
                          </Table>
                        </Collapse>
                      </>
                    )}

                    {step.cover_url && (
                      <Typography variant="body2" sx={{ mt: 0.5, fontFamily: 'monospace', fontSize: '0.8rem', wordBreak: 'break-all' }}>
                        {step.cover_url}
                      </Typography>
                    )}
                  </Box>
                </Box>
              ))}
            </>
          )}
        </DialogContent>
        <DialogActions>
          <Button onClick={onCloseOrganizePreview}>Cancel</Button>
          <Button
            variant="contained"
            startIcon={<BuildIcon />}
            onClick={onApplyOrganize}
            disabled={applyingOrganize}
          >
            Apply
          </Button>
        </DialogActions>
      </Dialog>

      {/* Write-back confirmation dialog */}
      <Dialog
        open={writeBackDialogOpen}
        onClose={onCloseWriteBack}
        maxWidth="sm"
        fullWidth
      >
        <DialogTitle>Save Metadata to Files</DialogTitle>
        <DialogContent>
          <Typography variant="body1" gutterBottom>
            This will write the following metadata from the database directly
            into the audio file tags on disk:
          </Typography>
          <Box component="ul" sx={{ mt: 1, '& li': { mb: 0.5 } }}>
            {[
              { label: 'Title', value: book?.title },
              { label: 'Album', value: book?.title ? `${book.title} (groups tracks in players)` : undefined },
              { label: 'Artist', value: book?.authors?.map((a) => a.name).join(' & ') || book?.author_name },
              { label: 'Narrator', value: book?.narrators?.map((n) => n.name).join(' & ') || book?.narrator },
              { label: 'Year', value: book?.audiobook_release_year || book?.print_year },
              { label: 'Genre', value: 'Audiobook' },
              { label: 'Language', value: book?.language },
              { label: 'Publisher', value: book?.publisher },
              { label: 'Series', value: book?.series_name },
              { label: 'Series Index', value: book?.series_position },
              { label: 'Description', value: book?.description ? `${book.description.slice(0, 60)}…` : undefined },
              { label: 'ISBN-13', value: book?.isbn13 },
              { label: 'ISBN-10', value: book?.isbn10 },
              { label: 'ASIN', value: book?.asin },
              { label: 'Edition', value: book?.edition },
              { label: 'Print Year', value: book?.print_year },
              { label: 'Book ID', value: book?.id },
              { label: 'Open Library', value: book?.open_library_id },
              { label: 'Google Books', value: book?.google_books_id },
              { label: 'Hardcover', value: book?.hardcover_id },
              { label: 'Cover art', value: book?.cover_url ? 'embedded (old cover archived to history)' : undefined },
              { label: 'Track numbers', value: segments.length > 1 ? 'written for multi-file books' : undefined },
            ]
              .filter((item) => item.value != null && item.value !== '' && item.value !== 0)
              .map((item) => (
                <li key={item.label}>
                  <strong>{item.label}</strong> — {String(item.value)}
                </li>
              ))}
          </Box>
          <Typography variant="body2" color="text.secondary" sx={{ mt: 2 }}>
            A backup of each file is created before writing and removed on
            success. The original file is restored automatically if writing
            fails.
          </Typography>
        </DialogContent>
        <DialogActions>
          <Button onClick={onCloseWriteBack}>Cancel</Button>
          <Button
            variant="contained"
            startIcon={<SaveIcon />}
            onClick={onWriteBackMetadata}
            disabled={writingToFiles}
          >
            Write to Files
          </Button>
        </DialogActions>
      </Dialog>
      <Dialog open={purgeDialogOpen} onClose={onClosePurge}>
        <DialogTitle>Purge Audiobook</DialogTitle>
        <DialogContent dividers>
          <Alert severity="error" sx={{ mb: 2 }}>
            This will permanently delete this audiobook. This cannot be undone.
          </Alert>
          <Typography gutterBottom>
            Are you sure you want to purge{' '}
            <strong>{book.title || 'this audiobook'}</strong> from the library?
            All associated files and metadata will be removed.
          </Typography>
          {book.marked_for_deletion_at && (
            <Typography variant="body2" color="text.secondary" sx={{ mt: 1 }}>
              Soft deleted on {formatDateTime(book.marked_for_deletion_at)}.
            </Typography>
          )}
          <FormControlLabel
            control={
              <Checkbox
                checked={purgeConfirmed}
                onChange={(e) => onSetPurgeConfirmed(e.target.checked)}
              />
            }
            label="I understand this action is permanent and cannot be undone"
            sx={{ mt: 2 }}
          />
        </DialogContent>
        <DialogActions>
          <Button onClick={onClosePurge}>Cancel</Button>
          <Button
            onClick={onPurge}
            color="error"
            variant="contained"
            disabled={actionLoading || !purgeConfirmed}
          >
            Purge Permanently
          </Button>
        </DialogActions>
      </Dialog>

      <MetadataEditDialog
        open={editDialogOpen}
        audiobook={editAudiobook}
        onClose={onCloseEdit}
        onSave={onEditSave}
      />

      <Dialog open={conflictDialogOpen} onClose={onCloseConflict}>
        <DialogTitle>Update Conflict</DialogTitle>
        <DialogContent>
          <Typography variant="body1" gutterBottom>
            This audiobook was updated by another user while you were editing.
          </Typography>
          <Typography variant="body2" color="text.secondary">
            Reload to fetch the latest data, or overwrite to save your changes
            anyway.
          </Typography>
        </DialogContent>
        <DialogActions>
          <Button onClick={onConflictReload}>Reload</Button>
          <Button variant="contained" onClick={onConflictOverwrite}>
            Overwrite
          </Button>
        </DialogActions>
      </Dialog>

      <MetadataHistory
        bookId={book.id}
        open={historyDialogOpen}
        onClose={onCloseHistory}
        onUndoComplete={onUndoComplete}
      />
      <MetadataSearchDialog
        open={metadataSearchOpen}
        book={book}
        onClose={onCloseMetadataSearch}
        onApplied={onMetadataApplied}
        toast={toast}
      />

      {relocateSegment && (
        <RelocateFileDialog
          open={!!relocateSegment}
          onClose={onCloseRelocate}
          segment={relocateSegment}
          bookId={book.id}
          onRelocated={onRelocated}
        />
      )}
      {book && (
        <Box sx={{ mt: 3 }}>
          <VersionsPanel bookId={book.id} />
        </Box>
      )}
      {/* Rejection History (META-REJ-1) */}
      {rejections.length > 0 && (
        <Box sx={{ mt: 2 }}>
          <Box
            sx={{ display: 'flex', alignItems: 'center', cursor: 'pointer', userSelect: 'none' }}
            onClick={() => onSetRejHistoryOpen(!rejHistoryOpen)}
          >
            <IconButton size="small">
              {rejHistoryOpen ? <KeyboardArrowUpIcon /> : <KeyboardArrowDownIcon />}
            </IconButton>
            <Typography variant="subtitle2" sx={{ ml: 1 }}>
              Rejection History ({rejections.length})
            </Typography>
          </Box>
          <Collapse in={rejHistoryOpen}>
            <Table size="small" sx={{ mt: 1 }}>
              <TableHead>
                <TableRow>
                  <TableCell>Date</TableCell>
                  <TableCell>Source</TableCell>
                  <TableCell>Reason</TableCell>
                  <TableCell>Candidate</TableCell>
                  <TableCell>Score</TableCell>
                </TableRow>
              </TableHead>
              <TableBody>
                {rejections.map((r) => (
                  <TableRow key={r.id}>
                    <TableCell>{new Date(r.rejected_at).toLocaleString()}</TableCell>
                    <TableCell>{r.source}</TableCell>
                    <TableCell>{r.rejection_reason}</TableCell>
                    <TableCell>
                      {r.candidate_title
                        ? `${r.candidate_title}${r.candidate_author ? ` — ${r.candidate_author}` : ''}`
                        : r.candidate_asin || r.candidate_isbn || '—'}
                    </TableCell>
                    <TableCell>{r.score != null ? r.score.toFixed(3) : '—'}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </Collapse>
        </Box>
      )}
      {book && (
        <AddToPlaylistDialog
          open={playlistDialogOpen}
          onClose={onClosePlaylist}
          bookIds={[book.id]}
        />
      )}

    </>
  );
};
