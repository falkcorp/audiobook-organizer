// file: web/src/components/BatchToolbar.tsx
// version: 1.3.0
// guid: 5e6f7a8b-9c0d-1e2f-3a4b5c6d7e8f9a0b
// last-edited: 2026-10-25

import { Button, Chip, Box, Stack, Tooltip } from '@mui/material';
import {
  LIBRARY_DELETE_PERMISSION,
  LIBRARY_ORGANIZE_PERMISSION,
  useHasPermission,
} from '../hooks/useLibraryPermission';

export interface BatchToolbarProps {
  selectedCount: number;
  onBatchEditClick: () => void;
  onFetchReviewClick: () => void;
  onResumeReviewClick: () => void;
  onSearchMetadataClick: () => void;
  onSaveToFilesClick: () => void;
  onOrganizeSelectedClick: () => void;
  onMergeAsVersionsClick: () => void;
  onCombineIntoOneBookClick: () => void;
  onTagClick: () => void;
  onRateClick: () => void;
  onDeleteSelectedClick: () => void;
  onRestoreSelectedClick: () => void;
  batchRestoreInProgress: boolean;
  selectedHasActive: boolean;
  selectedHasDeleted: boolean;
  selectedHasImport: boolean;
  selectedAudiobooksLength: number;
}

export const BatchToolbar = ({
  selectedCount,
  onBatchEditClick,
  onFetchReviewClick,
  onResumeReviewClick,
  onSearchMetadataClick,
  onSaveToFilesClick,
  onOrganizeSelectedClick,
  onMergeAsVersionsClick,
  onCombineIntoOneBookClick,
  onTagClick,
  onRateClick,
  onDeleteSelectedClick,
  onRestoreSelectedClick,
  batchRestoreInProgress,
  selectedHasActive,
  selectedHasDeleted,
  selectedHasImport,
  selectedAudiobooksLength,
}: BatchToolbarProps) => {
  // Trashing needs library.delete and restoring library.organize on the server.
  // Advisory: the buttons are disabled with the reason instead of failing after
  // the click; the server still refuses a caller who lacks them.
  const mayDelete = useHasPermission(LIBRARY_DELETE_PERMISSION);
  const mayRestore = useHasPermission(LIBRARY_ORGANIZE_PERMISSION);
  if (selectedCount === 0) {
    return null;
  }

  return (
    <Stack
      direction="row"
      spacing={1}
      sx={{
        alignItems: 'center',
        flexWrap: 'wrap',
        width: '100%',
      }}
    >
      <Chip label={`${selectedCount} selected`} size="small" color="primary" />
      <Button size="small" variant="outlined" onClick={onBatchEditClick} disabled={!selectedCount}>
        Batch Edit
      </Button>
      <Button
        size="small"
        variant="outlined"
        color="primary"
        onClick={onFetchReviewClick}
        disabled={selectedAudiobooksLength < 2}
        title="Fetch metadata candidates for selected books into the persistent cache. The Review button opens the dialog when results are ready."
      >
        Fetch Selected
      </Button>
      <Button
        size="small"
        variant="outlined"
        onClick={onResumeReviewClick}
        title="Open the review dialog for books with cached candidates pending review"
      >
        Review
      </Button>
      <Button
        size="small"
        variant="outlined"
        onClick={onSearchMetadataClick}
        disabled={!selectedCount}
      >
        Search Metadata
      </Button>
      <Box sx={{ borderLeft: 1, borderColor: 'divider', height: 24 }} />
      <Button
        size="small"
        variant="outlined"
        onClick={onSaveToFilesClick}
        disabled={!selectedHasActive}
      >
        Save to Files
      </Button>
      <Button
        size="small"
        variant="outlined"
        onClick={onOrganizeSelectedClick}
        disabled={!selectedHasImport}
      >
        Organize Selected
      </Button>
      <Box sx={{ borderLeft: 1, borderColor: 'divider', height: 24 }} />
      <Button
        size="small"
        variant="outlined"
        color="primary"
        onClick={onMergeAsVersionsClick}
        disabled={selectedAudiobooksLength < 2}
      >
        Merge as Versions
      </Button>
      <Button
        size="small"
        variant="outlined"
        color="primary"
        onClick={onCombineIntoOneBookClick}
        disabled={selectedAudiobooksLength < 2}
        title="Combine the selected single-file books into ONE multi-file book (moves all files onto one survivor, deletes the rest). Distinct from Merge as Versions."
      >
        Combine into One Book
      </Button>
      <Button size="small" variant="outlined" onClick={onTagClick} disabled={!selectedCount}>
        Tag
      </Button>
      <Button size="small" variant="outlined" onClick={onRateClick} disabled={!selectedCount}>
        Rate
      </Button>
      <Box sx={{ flex: 1 }} />
      {/* A disabled MUI Button has pointer-events: none, so neither a title
          attribute on it nor a Tooltip around it ever sees the pointer; the
          Tooltip wraps a span that does. */}
      <Tooltip
        title={
          mayDelete ? '' : "You don't have permission to move books to the trash (library.delete)."
        }
      >
        <span>
          <Button
            size="small"
            variant="outlined"
            color="secondary"
            onClick={onDeleteSelectedClick}
            disabled={!selectedHasActive || !mayDelete}
          >
            Delete Selected
          </Button>
        </span>
      </Tooltip>
      <Tooltip
        title={
          mayRestore
            ? ''
            : "You don't have permission to restore books from the trash (library.organize)."
        }
      >
        <span>
          <Button
            size="small"
            variant="outlined"
            color="success"
            onClick={onRestoreSelectedClick}
            disabled={!selectedHasDeleted || batchRestoreInProgress || !mayRestore}
          >
            {batchRestoreInProgress ? 'Restoring...' : 'Restore Selected'}
          </Button>
        </span>
      </Tooltip>
    </Stack>
  );
};
