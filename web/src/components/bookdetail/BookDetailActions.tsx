// file: web/src/components/bookdetail/BookDetailActions.tsx
// version: 1.0.1
// guid: d4e5f6a7-b8c9-0123-defa-234567890123
// last-edited: 2026-08-07

import {
  Button,
  CircularProgress,
  IconButton,
  Paper,
  Stack,
  Tooltip,
} from '@mui/material';
import DeleteIcon from '@mui/icons-material/Delete';
import RestoreIcon from '@mui/icons-material/Restore';
import EditIcon from '@mui/icons-material/Edit';
import PsychologyIcon from '@mui/icons-material/Psychology';
import CloudDownloadIcon from '@mui/icons-material/CloudDownload';
import HistoryIcon from '@mui/icons-material/History';
import SaveIcon from '@mui/icons-material/Save';
import SearchIcon from '@mui/icons-material/Search';
import TransformIcon from '@mui/icons-material/Transform';
import FolderOpenIcon from '@mui/icons-material/FolderOpen';
import BuildIcon from '@mui/icons-material/Build';
import RefreshIcon from '@mui/icons-material/Refresh';
import type { Book } from '../../services/api';

export interface BookDetailActionsProps {
  book: Book;
  isSoftDeleted: boolean;
  loading: boolean;
  actionLoading: boolean;
  parsingWithAI: boolean;
  rescanningFolder: boolean;
  fetchingMetadata: boolean;
  transcoding: boolean;
  organizePreviewLoading: boolean;
  applyingOrganize: boolean;
  writingToFiles: boolean;
  onOpenHistory: () => void;
  onParseWithAI: () => void;
  onRescanFolder: () => void;
  onRescanFiles: () => void;
  onRefresh: () => void;
  onOpenEdit: () => void;
  onFetchMetadata: () => void;
  onOpenMetadataSearch: () => void;
  onOpenPlaylist: () => void;
  onTranscode: () => void;
  onPreviewOrganize: () => void;
  onOpenWriteBack: () => void;
  onOpenDelete: () => void;
  onRestore: () => void;
}

export const BookDetailActions = ({
  book,
  isSoftDeleted,
  loading,
  actionLoading,
  parsingWithAI,
  rescanningFolder,
  fetchingMetadata,
  transcoding,
  organizePreviewLoading,
  applyingOrganize,
  writingToFiles,
  onOpenHistory,
  onParseWithAI,
  onRescanFolder,
  onRescanFiles,
  onRefresh,
  onOpenEdit,
  onFetchMetadata,
  onOpenMetadataSearch,
  onOpenPlaylist,
  onTranscode,
  onPreviewOrganize,
  onOpenWriteBack,
  onOpenDelete,
  onRestore,
}: BookDetailActionsProps) => {
  return (
    <Paper sx={{ p: 2, mb: 3 }}>
      <Stack
        direction={{ xs: 'column', md: 'row' }}
        spacing={2}
        justifyContent="space-between"
      >
        <Stack direction="row" spacing={1} flexWrap="wrap">
          <Button
            variant="outlined"
            startIcon={<HistoryIcon />}
            onClick={onOpenHistory}
            disabled={actionLoading}
          >
            History
          </Button>
          <Button
            variant="outlined"
            startIcon={
              parsingWithAI ? (
                <CircularProgress size={20} />
              ) : (
                <PsychologyIcon />
              )
            }
            onClick={onParseWithAI}
            disabled={parsingWithAI || actionLoading}
          >
            {parsingWithAI ? 'Parsing...' : 'Parse with AI'}
          </Button>
          <Button
            variant="outlined"
            startIcon={
              rescanningFolder ? (
                <CircularProgress size={20} />
              ) : (
                <FolderOpenIcon />
              )
            }
            onClick={onRescanFolder}
            disabled={rescanningFolder || actionLoading}
          >
            {rescanningFolder ? 'Scanning...' : 'Rescan Folder'}
          </Button>
          <Tooltip title="Re-stat this book's files on disk and update size">
            <span>
              <Button
                variant="outlined"
                size="small"
                onClick={onRescanFiles}
                disabled={actionLoading || isSoftDeleted}
              >
                Rescan Files
              </Button>
            </span>
          </Tooltip>
        </Stack>
        <Stack
          direction="row"
          spacing={1}
          flexWrap="wrap"
          justifyContent="flex-end"
          alignItems="center"
        >
          <Tooltip title="Refresh book data">
            <IconButton
              onClick={onRefresh}
              disabled={loading || actionLoading}
              size="small"
            >
              <RefreshIcon />
            </IconButton>
          </Tooltip>
          <Button
            variant="outlined"
            startIcon={<EditIcon />}
            onClick={onOpenEdit}
            disabled={actionLoading}
          >
            Edit Metadata
          </Button>
          <Button
            variant="outlined"
            startIcon={
              fetchingMetadata ? (
                <CircularProgress size={20} />
              ) : (
                <CloudDownloadIcon />
              )
            }
            onClick={onFetchMetadata}
            disabled={fetchingMetadata || actionLoading}
          >
            {fetchingMetadata ? 'Fetching...' : 'Fetch Metadata'}
          </Button>
          <Button
            variant="outlined"
            startIcon={<SearchIcon />}
            onClick={onOpenMetadataSearch}
            disabled={actionLoading}
          >
            Search Metadata
          </Button>
          <Button
            variant="outlined"
            onClick={onOpenPlaylist}
            disabled={actionLoading}
          >
            Add to Playlist
          </Button>
          {book && book.format?.toLowerCase() !== 'm4b' && (
            <Button
              variant="outlined"
              startIcon={
                transcoding ? (
                  <CircularProgress size={20} />
                ) : (
                  <TransformIcon />
                )
              }
              onClick={onTranscode}
              disabled={transcoding || actionLoading}
            >
              {transcoding ? 'Converting...' : 'Convert to M4B'}
            </Button>
          )}
          <Button
            variant="outlined"
            startIcon={
              organizePreviewLoading || applyingOrganize ? (
                <CircularProgress size={20} />
              ) : (
                <BuildIcon />
              )
            }
            onClick={onPreviewOrganize}
            disabled={organizePreviewLoading || applyingOrganize || actionLoading}
          >
            {applyingOrganize ? 'Organizing...' : organizePreviewLoading ? 'Loading...' : 'Preview Organize'}
          </Button>
          <Button
            variant="outlined"
            startIcon={
              writingToFiles ? (
                <CircularProgress size={20} />
              ) : (
                <SaveIcon />
              )
            }
            onClick={onOpenWriteBack}
            disabled={writingToFiles || actionLoading}
          >
            {writingToFiles ? 'Writing...' : 'Save to Files'}
          </Button>
          {!isSoftDeleted ? (
            <Button
              variant="contained"
              color="error"
              startIcon={<DeleteIcon />}
              onClick={onOpenDelete}
              disabled={actionLoading}
            >
              Delete
            </Button>
          ) : (
            <Button
              variant="outlined"
              color="success"
              startIcon={<RestoreIcon />}
              onClick={onRestore}
              disabled={actionLoading}
            >
              Restore
            </Button>
          )}
        </Stack>
      </Stack>
    </Paper>
  );
};

