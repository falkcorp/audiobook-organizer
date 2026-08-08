// file: web/src/components/bookdetail/BookDetailFilesTab.tsx
// version: 1.0.1
// guid: a7b8c9d0-e1f2-3456-abcd-567890123456
// last-edited: 2026-08-07

import {
  Alert,
  Box,
  Button,
  CircularProgress,
  Paper,
  Stack,
  TextField,
  Typography,
} from '@mui/material';
import LinkIcon from '@mui/icons-material/Link';
import type { Book, BookFile, BookSegment, BookTags } from '../../services/api';
import * as api from '../../services/api';
import { BookDetailVersionGroup } from './BookDetailVersionGroup';
import { ChangeLog } from '../ChangeLog';
import { formatDuration } from './bookDetailUtils';

export interface BookDetailFilesTabProps {
  book: Book;
  versions: Book[];
  bookFiles: BookFile[];
  segments: BookSegment[];
  versionSegments: Record<string, BookSegment[]>;
  versionFileTags: Record<string, BookTags | null>;
  expandedVersionIds: Set<string>;
  expandedSegmentVersionIds: Set<string>;
  selectedSegmentIds: Set<string>;
  filesRefreshKey: number;
  compareSnapshotTs: string | null;
  itunesLinked: boolean;
  itunesPidCount: number;
  itunesExternalIDs: api.ExternalIDMapping[];
  linkSearchOpen: boolean;
  linkSearchQuery: string;
  linkSearchResults: Book[];
  linkSearchLoading: boolean;
  splittingVersion: boolean;
  splittingToBooks: boolean;
  onSetLinkSearchOpen: (open: boolean) => void;
  onSetLinkSearchQuery: (q: string) => void;
  onInlineLinkVersion: (targetId: string) => void;
  onToggleVersionExpanded: (versionId: string) => void;
  onSetExpandedSegmentVersionIds: (fn: (prev: Set<string>) => Set<string>) => void;
  onSetSelectedSegmentIds: (ids: Set<string>) => void;
  onSetActiveTab: (tab: 'info' | 'files') => void;
  onSetRelocateSegment: (seg: BookSegment | null) => void;
  onSetPrimary: (versionId: string) => void;
  onUnlinkVersion: (versionId: string) => void;
  onMoveToVersion: (targetBookId: string) => void;
  onSplitVersion: () => void;
  onSplitToBooks: () => void;
  onClearCompareSnapshot: (ts: string | null) => void;
  onExtractTrackInfo: () => Promise<void>;
  onLoadBook: () => void;
  onRefreshFiles: () => void;
}

export const BookDetailFilesTab = ({
  book,
  versions,
  bookFiles,
  segments,
  versionSegments,
  versionFileTags,
  expandedVersionIds,
  expandedSegmentVersionIds,
  selectedSegmentIds,
  filesRefreshKey,
  compareSnapshotTs,
  itunesLinked,
  itunesPidCount,
  itunesExternalIDs,
  linkSearchOpen,
  linkSearchQuery,
  linkSearchResults,
  linkSearchLoading,
  splittingVersion,
  splittingToBooks,
  onSetLinkSearchOpen,
  onSetLinkSearchQuery,
  onInlineLinkVersion,
  onToggleVersionExpanded,
  onSetExpandedSegmentVersionIds,
  onSetSelectedSegmentIds,
  onSetActiveTab,
  onSetRelocateSegment,
  onSetPrimary,
  onUnlinkVersion,
  onMoveToVersion,
  onSplitVersion,
  onSplitToBooks,
  onClearCompareSnapshot,
  onExtractTrackInfo,
  onLoadBook,
  onRefreshFiles,
}: BookDetailFilesTabProps) => {
  const allVersions = versions.length > 0 ? versions : [book];

  // Group versions by format, but use a unique key per version when
  // multiple versions share the same format (so each gets its own tray).
  const formatCounts = new Map<string, number>();
  for (const v of allVersions) {
    const fmt = v.format?.toUpperCase() || 'UNKNOWN';
    formatCounts.set(fmt, (formatCounts.get(fmt) ?? 0) + 1);
  }
  const formatGroups = new Map<string, Book[]>();
  for (const v of allVersions) {
    const fmt = v.format?.toUpperCase() || 'UNKNOWN';
    const key = (formatCounts.get(fmt) ?? 0) > 1 ? v.id : fmt;
    if (!formatGroups.has(key)) formatGroups.set(key, []);
    formatGroups.get(key)!.push(v);
  }

  return (
    <Stack spacing={0}>
      {/* Link another version — above format trays */}
      <Box sx={{ mb: 1 }}>
        {!linkSearchOpen ? (
          <Button size="small" variant="text" startIcon={<LinkIcon />}
            onClick={() => onSetLinkSearchOpen(true)}>
            Link Another Version
          </Button>
        ) : (
          <Stack spacing={1} sx={{ maxWidth: 400 }}>
            <Stack direction="row" alignItems="center" justifyContent="space-between">
              <Typography variant="caption">Search for a book to link as a version</Typography>
              <Button size="small" onClick={() => onSetLinkSearchOpen(false)}>Cancel</Button>
            </Stack>
            <TextField size="small" autoFocus placeholder="Search by title or author..."
              value={linkSearchQuery} onChange={(e) => onSetLinkSearchQuery(e.target.value)} fullWidth />
            {linkSearchLoading && <CircularProgress size={16} />}
            {linkSearchResults.map((result) => (
              <Button key={result.id} variant="outlined" size="small"
                sx={{ justifyContent: 'flex-start', textTransform: 'none' }}
                onClick={() => onInlineLinkVersion(result.id)}>
                {result.title} — {result.author_name}
              </Button>
            ))}
          </Stack>
        )}
      </Box>

      {/* Format group sections */}
      {Array.from(formatGroups.entries()).map(([, groupVersions]) => (
        <BookDetailVersionGroup
          key={groupVersions.map((v) => v.id).join('-')}
          book={book}
          groupVersions={groupVersions}
          expandedVersionIds={expandedVersionIds}
          expandedSegmentVersionIds={expandedSegmentVersionIds}
          bookFiles={bookFiles}
          segments={segments}
          versionSegments={versionSegments}
          versionFileTags={versionFileTags}
          selectedSegmentIds={selectedSegmentIds}
          versions={versions}
          filesRefreshKey={filesRefreshKey}
          compareSnapshotTs={compareSnapshotTs}
          splittingVersion={splittingVersion}
          splittingToBooks={splittingToBooks}
          onToggleVersionExpanded={onToggleVersionExpanded}
          onSetPrimary={onSetPrimary}
          onUnlinkVersion={onUnlinkVersion}
          onSetSelectedSegmentIds={onSetSelectedSegmentIds}
          onSetExpandedSegmentVersionIds={onSetExpandedSegmentVersionIds}
          onSetActiveTab={onSetActiveTab}
          onSetRelocateSegment={onSetRelocateSegment}
          onMoveToVersion={onMoveToVersion}
          onSplitVersion={onSplitVersion}
          onSplitToBooks={onSplitToBooks}
          onExtractTrackInfo={onExtractTrackInfo}
          onClearCompareSnapshot={() => onClearCompareSnapshot(null)}
        />
      ))}

      {/* iTunes link info panel */}
      {itunesLinked && (
        <Alert
          severity="info"
          variant="outlined"
          icon={false}
          sx={{ mt: 1 }}
        >
          <Stack direction="row" spacing={3} flexWrap="wrap" useFlexGap alignItems="center">
            <Typography variant="subtitle2" sx={{ fontWeight: 'bold' }}>
              iTunes Linked
            </Typography>
            {book.itunes_persistent_id && (
              <Box>
                <Typography variant="caption" color="text.secondary">PID</Typography>
                <Typography variant="body2" sx={{ fontFamily: 'monospace' }}>{book.itunes_persistent_id}</Typography>
              </Box>
            )}
            <Box>
              <Typography variant="caption" color="text.secondary">Tracks Mapped</Typography>
              <Typography variant="body2">{itunesPidCount}</Typography>
            </Box>
            {book.itunes_date_added && (
              <Box>
                <Typography variant="caption" color="text.secondary">Date Added</Typography>
                <Typography variant="body2">{new Date(book.itunes_date_added).toLocaleDateString()}</Typography>
              </Box>
            )}
            {book.itunes_play_count != null && book.itunes_play_count > 0 && (
              <Box>
                <Typography variant="caption" color="text.secondary">Play Count</Typography>
                <Typography variant="body2">{book.itunes_play_count}</Typography>
              </Box>
            )}
            {book.itunes_last_played && (
              <Box>
                <Typography variant="caption" color="text.secondary">Last Played</Typography>
                <Typography variant="body2">{new Date(book.itunes_last_played).toLocaleDateString()}</Typography>
              </Box>
            )}
            {book.itunes_rating != null && book.itunes_rating > 0 && (
              <Box>
                <Typography variant="caption" color="text.secondary">Rating</Typography>
                <Typography variant="body2">{'★'.repeat(Math.round(book.itunes_rating / 20))}{'☆'.repeat(5 - Math.round(book.itunes_rating / 20))}</Typography>
              </Box>
            )}
            {book.itunes_bookmark != null && book.itunes_bookmark > 0 && (
              <Box>
                <Typography variant="caption" color="text.secondary">Bookmark</Typography>
                <Typography variant="body2">{formatDuration(book.itunes_bookmark / 1000)}</Typography>
              </Box>
            )}
            {book.itunes_import_source && (
              <Box>
                <Typography variant="caption" color="text.secondary">Import Source</Typography>
                <Typography variant="body2">{book.itunes_import_source}</Typography>
              </Box>
            )}
            {book.file_path && (
              <Box sx={{ flex: '1 1 100%' }}>
                <Typography variant="caption" color="text.secondary">File Path</Typography>
                <Typography variant="body2" sx={{ fontFamily: 'monospace', fontSize: '0.8rem', wordBreak: 'break-all' }}>{book.file_path}</Typography>
              </Box>
            )}
            {/* Per-track file paths from external_id_map (when no book-level file_path or when multi-track) */}
            {itunesExternalIDs.length > 0 && itunesExternalIDs.some((e) => e.file_path) && (
              <Box sx={{ flex: '1 1 100%' }}>
                <Typography variant="caption" color="text.secondary">iTunes Track Files</Typography>
                {[...itunesExternalIDs]
                  .sort((a, b) => (a.track_number ?? Infinity) - (b.track_number ?? Infinity))
                  .map((e) => (
                    e.file_path ? (
                      <Typography
                        key={e.id}
                        variant="body2"
                        sx={{ fontFamily: 'monospace', fontSize: '0.8rem', wordBreak: 'break-all' }}
                      >
                        {e.track_number != null ? `[${e.track_number}] ` : ''}{e.file_path}
                      </Typography>
                    ) : null
                  ))}
              </Box>
            )}
            {/* Track PIDs when multiple tracks mapped */}
            {itunesExternalIDs.length > 1 && (
              <Box sx={{ flex: '1 1 100%' }}>
                <Typography variant="caption" color="text.secondary">Track PIDs ({itunesExternalIDs.length})</Typography>
                <Stack direction="row" flexWrap="wrap" spacing={0.5} useFlexGap sx={{ mt: 0.25 }}>
                  {[...itunesExternalIDs]
                    .sort((a, b) => (a.track_number ?? Infinity) - (b.track_number ?? Infinity))
                    .map((e) => (
                      <Typography
                        key={e.id}
                        variant="body2"
                        sx={{ fontFamily: 'monospace', fontSize: '0.75rem', bgcolor: 'action.hover', px: 0.5, borderRadius: 0.5 }}
                      >
                        {e.track_number != null ? `${e.track_number}: ` : ''}{e.external_id}
                      </Typography>
                    ))}
                </Stack>
              </Box>
            )}
          </Stack>
        </Alert>
      )}

      {/* Change Log */}
      <Paper sx={{ p: 2, mt: 2 }} data-testid="changelog-section">
        <Typography variant="subtitle1" fontWeight="bold" sx={{ mb: 1 }}>
          Change Log
        </Typography>
        <ChangeLog bookId={book.id} refreshKey={filesRefreshKey} onRevert={() => { onRefreshFiles(); onLoadBook(); }} onCompareSnapshot={(ts) => onClearCompareSnapshot(ts)} />
      </Paper>

    </Stack>
  );
};
