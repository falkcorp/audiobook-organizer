// file: web/src/components/bookdetail/BookDetailVersionGroup.tsx
// version: 1.1.1
// guid: f6a7b8c9-d0e1-2345-fabc-456789012345
// last-edited: 2026-08-07

import {
  Alert,
  Box,
  Button,
  Checkbox,
  Chip,
  Collapse,
  Grid,
  IconButton,
  Paper,
  Stack,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableRow,
  Tooltip,
  Typography,
} from '@mui/material';
import StarIcon from '@mui/icons-material/Star';
import StarBorderIcon from '@mui/icons-material/StarBorder';
import KeyboardArrowDownIcon from '@mui/icons-material/KeyboardArrowDown';
import KeyboardArrowUpIcon from '@mui/icons-material/KeyboardArrowUp';
import LinkOffIcon from '@mui/icons-material/LinkOff';
import TransformIcon from '@mui/icons-material/Transform';
import ErrorOutlineIcon from '@mui/icons-material/ErrorOutline';
import type { Book, BookFile, BookSegment, BookTags } from '../../services/api';
import * as api from '../../services/api';
import { TagComparison } from '../TagComparison';
import { formatDuration, formatBytes, formatTagValue } from './bookDetailUtils';
import { useNavigate } from 'react-router-dom';

const SEGMENT_PREVIEW_COUNT = 5;

// Hash chain (HASH-CHAIN-2, #1270): Download -> Original -> Post-metadata -> Current.
// Sourced PER FILE from the BookFile prop — never from Book (which lacks these fields).
const HASH_CHAIN_LINKS: ReadonlyArray<{
  key: 'download_hash' | 'original_file_hash' | 'post_metadata_hash' | 'file_hash';
  label: string;
}> = [
  { key: 'download_hash', label: 'Download' },
  { key: 'original_file_hash', label: 'Original' },
  { key: 'post_metadata_hash', label: 'Post-metadata' },
  { key: 'file_hash', label: 'Current' },
];

const HashChainLink = ({ label, value }: { label: string; value?: string }) => {
  const hasValue = typeof value === 'string' && value.length > 0;
  // Missing/absent hashes render as an em dash (never hidden, never an error).
  const shown = hasValue ? value.slice(0, 12) : '—';
  const content = (
    <Box component="span" sx={{ display: 'inline-flex', flexDirection: 'column', minWidth: 0 }}>
      <Typography variant="caption" color="text.secondary" sx={{ lineHeight: 1.2 }}>
        {label}
      </Typography>
      <Typography
        variant="caption"
        sx={{ fontFamily: 'monospace', wordBreak: 'break-all', lineHeight: 1.2 }}
      >
        {shown}
      </Typography>
    </Box>
  );
  return hasValue ? <Tooltip title={value}>{content}</Tooltip> : content;
};
const OVERALL_METADATA_FIELDS = [
  { key: 'title', label: 'Title' },
  { key: 'author_name', label: 'Author' },
  { key: 'narrator', label: 'Narrator' },
  { key: 'series_name', label: 'Series' },
  { key: 'publisher', label: 'Publisher' },
  { key: 'language', label: 'Language' },
  { key: 'isbn13', label: 'ISBN' },
] as const;

export interface BookDetailVersionGroupProps {
  book: Book;
  groupVersions: Book[];
  expandedVersionIds: Set<string>;
  expandedSegmentVersionIds: Set<string>;
  bookFiles: BookFile[];
  segments: BookSegment[];
  versionSegments: Record<string, BookSegment[]>;
  versionFileTags: Record<string, BookTags | null>;
  selectedSegmentIds: Set<string>;
  versions: Book[];
  filesRefreshKey: number;
  compareSnapshotTs: string | null;
  splittingVersion: boolean;
  splittingToBooks: boolean;
  onToggleVersionExpanded: (versionId: string) => void;
  onSetPrimary: (versionId: string) => void;
  onUnlinkVersion: (versionId: string) => void;
  onSetSelectedSegmentIds: (ids: Set<string>) => void;
  onSetExpandedSegmentVersionIds: (fn: (prev: Set<string>) => Set<string>) => void;
  onSetActiveTab: (tab: 'info' | 'files') => void;
  onSetRelocateSegment: (seg: BookSegment | null) => void;
  onMoveToVersion: (targetBookId: string) => void;
  onSplitVersion: () => void;
  onSplitToBooks: () => void;
  onExtractTrackInfo: () => Promise<void>;
  onClearCompareSnapshot: () => void;
}

export const BookDetailVersionGroup = ({
  book,
  groupVersions,
  expandedVersionIds,
  expandedSegmentVersionIds,
  bookFiles,
  segments,
  versionSegments,
  versionFileTags,
  selectedSegmentIds,
  versions,
  filesRefreshKey,
  compareSnapshotTs,
  splittingVersion,
  splittingToBooks,
  onToggleVersionExpanded,
  onSetPrimary,
  onUnlinkVersion,
  onSetSelectedSegmentIds,
  onSetExpandedSegmentVersionIds,
  onSetActiveTab,
  onSetRelocateSegment,
  onMoveToVersion,
  onSplitVersion,
  onSplitToBooks,
  onExtractTrackInfo,
  onClearCompareSnapshot,
}: BookDetailVersionGroupProps) => {
  const navigate = useNavigate();

  const representative = groupVersions[0];
  const isExpanded = groupVersions.some((v) => expandedVersionIds.has(v.id));
  const groupId = groupVersions.map((v) => v.id).join('-');
  const hasPrimary = groupVersions.some((v) => v.is_primary_version);
  const hasItunes = groupVersions.some((v) => v.itunes_persistent_id);
  const totalFiles = groupVersions.reduce((sum, v) => {
    const segs =
      v.id === book.id
        ? bookFiles.length > 0
          ? bookFiles
          : segments
        : (versionSegments[v.id] || []);
    return sum + (segs.length || 1);
  }, 0);
  const totalSize = groupVersions.reduce((sum, v) => sum + (v.file_size || 0), 0);
  const totalDuration = groupVersions.reduce((sum, v) => sum + (v.duration || 0), 0);

  const fmt = representative.format?.toUpperCase() || 'UNKNOWN';
  const trayLabel = (() => {
    if (groupVersions.length === 1 && representative.file_path) {
      const parts = representative.file_path.replace(/\\/g, '/').split('/').filter(Boolean);
      const segs = parts.slice(-3).join('/');
      if (hasItunes && representative.itunes_persistent_id) {
        return `iTunes: ${segs}`;
      }
      return segs || fmt;
    }
    return fmt;
  })();

  const allVersions = versions.length > 0 ? versions : [book];

  return (
    <Paper key={groupId} sx={{ mb: 1, overflow: 'hidden' }} data-testid={`format-tray-${fmt.toLowerCase()}`}>
      {/* Format tray header */}
      <Box
        sx={{
          display: 'flex',
          alignItems: 'center',
          px: 2,
          py: 1,
          bgcolor: hasPrimary ? 'primary.dark' : 'background.paper',
          cursor: 'pointer',
          '&:hover': { bgcolor: hasPrimary ? 'primary.dark' : 'action.hover' },
          borderBottom: isExpanded ? '1px solid' : 'none',
          borderColor: 'divider',
        }}
        onClick={() => {
          for (const v of groupVersions) {
            onToggleVersionExpanded(v.id);
          }
        }}
      >
        <IconButton size="small" sx={{ mr: 1, color: 'inherit' }}>
          {isExpanded ? <KeyboardArrowUpIcon /> : <KeyboardArrowDownIcon />}
        </IconButton>
        {hasPrimary ? (
          <StarIcon fontSize="small" sx={{ mr: 1, color: 'warning.main' }} />
        ) : (
          <StarBorderIcon fontSize="small" sx={{ mr: 1, opacity: 0.4 }} />
        )}
        <Typography variant="subtitle1" fontWeight="bold" sx={{ flex: 1, minWidth: 0 }} noWrap>
          {fmt}{representative.codec ? ` (${representative.codec})` : ''}
          {trayLabel !== fmt && (
            <Typography component="span" variant="body2" sx={{ ml: 1, opacity: 0.75, fontWeight: 'normal' }}>
              {trayLabel}
            </Typography>
          )}
        </Typography>
        <Stack direction="row" spacing={1} alignItems="center" sx={{ ml: 2, flexShrink: 0 }}>
          <Chip
            label={`${totalFiles} file${totalFiles !== 1 ? 's' : ''}`}
            size="small"
            variant="outlined"
          />
          {totalSize > 0 && (
            <Typography variant="body2" color="text.secondary">
              {formatBytes(totalSize)}
            </Typography>
          )}
          {totalDuration > 0 && (
            <Typography variant="body2" color="text.secondary" sx={{ minWidth: 50 }}>
              {formatDuration(totalDuration)}
            </Typography>
          )}
          {hasPrimary && (
            <Chip label="Primary" size="small" color="warning" />
          )}
          {hasItunes && (
            <Chip label="iTunes" size="small" color="info" variant="outlined" />
          )}
        </Stack>
      </Box>

      {/* Expanded content for each version in this format group */}
      <Collapse in={isExpanded}>
        {groupVersions.map((version) => {
          const isCurrent = version.id === book.id;
          const isPrimary = version.is_primary_version;
          const vSegs: BookSegment[] = isCurrent && bookFiles.length > 0
            ? bookFiles.map((f) => ({
                id: f.id,
                file_path: f.file_path,
                format: f.format ?? '',
                size_bytes: f.file_size ?? 0,
                duration_seconds: f.duration ?? 0,
                track_number: f.track_number,
                total_tracks: f.track_count,
                active: !f.missing,
                file_exists: f.file_exists,
              }))
            : isCurrent
              ? segments
              : (versionSegments[version.id] || []);
          const showAllSegments = expandedSegmentVersionIds.has(version.id);
          const visibleSegments = showAllSegments
            ? vSegs
            : vSegs.slice(0, SEGMENT_PREVIEW_COUNT);
          const hiddenSegmentCount = Math.max(vSegs.length - visibleSegments.length, 0);
          const metadataEntries = OVERALL_METADATA_FIELDS
            .map(({ key, label }) => {
              const tag = versionFileTags[version.id]?.tags?.[key];
              if (!tag) return null;
              const fileValue = formatTagValue(tag.file_value);
              const storedValue = formatTagValue(tag.stored_value);
              if (fileValue === '—' && storedValue === '—') return null;
              return {
                key,
                label,
                fileValue,
                storedValue,
                differsFromDb:
                  fileValue !== '—' &&
                  storedValue !== '—' &&
                  fileValue !== storedValue,
              };
            })
            .filter((entry): entry is NonNullable<typeof entry> => entry !== null);

          return (
            <Box key={version.id} sx={{ p: 2, borderBottom: groupVersions.length > 1 ? '2px solid' : 'none', borderColor: 'divider', '&:not(:last-child)': { pb: 3 }, '&:not(:first-of-type)': { pt: 3 } }}>
              {/* Version action buttons */}
              <Stack direction="row" spacing={1} sx={{ mb: 2 }} flexWrap="wrap" useFlexGap>
                {!isPrimary && versions.length > 1 && (
                  <Button
                    size="small"
                    variant="outlined"
                    startIcon={<StarIcon />}
                    onClick={(e) => { e.stopPropagation(); onSetPrimary(version.id); }}
                  >
                    Set as Primary
                  </Button>
                )}
                {!isCurrent && (
                  <Button
                    size="small"
                    variant="outlined"
                    onClick={(e) => { e.stopPropagation(); navigate(`/library/${version.id}`); }}
                  >
                    View Details
                  </Button>
                )}
                {!isCurrent && (
                  <Button
                    size="small"
                    variant="outlined"
                    color="error"
                    startIcon={<LinkOffIcon />}
                    onClick={(e) => {
                      e.stopPropagation();
                      onUnlinkVersion(version.id);
                    }}
                  >
                    Unlink
                  </Button>
                )}
              </Stack>

              {/* Path and codec info */}
              <Table size="small" sx={{ mb: 2 }}>
                <TableBody>
                  <TableRow>
                    <TableCell sx={{ fontWeight: 'bold', width: 140, color: 'text.secondary' }}>Path</TableCell>
                    <TableCell sx={{ wordBreak: 'break-all', fontSize: '0.85rem' }}>{version.file_path}</TableCell>
                  </TableRow>
                  {version.bitrate && (
                    <TableRow>
                      <TableCell sx={{ fontWeight: 'bold', color: 'text.secondary' }}>Bitrate</TableCell>
                      <TableCell>{version.bitrate} kbps</TableCell>
                    </TableRow>
                  )}
                  {version.sample_rate && (
                    <TableRow>
                      <TableCell sx={{ fontWeight: 'bold', color: 'text.secondary' }}>Sample Rate</TableCell>
                      <TableCell>{version.sample_rate} Hz</TableCell>
                    </TableRow>
                  )}
                </TableBody>
              </Table>

              {vSegs.length > 1 && metadataEntries.length > 0 && (
                <Box
                  sx={{
                    mb: 2,
                    p: 1.5,
                    border: '1px solid',
                    borderColor: 'divider',
                    borderRadius: 1,
                    bgcolor: 'background.default',
                  }}
                >
                  <Stack
                    direction="row"
                    spacing={1}
                    alignItems="center"
                    flexWrap="wrap"
                    useFlexGap
                    sx={{ mb: 1 }}
                  >
                    <Typography variant="subtitle2">
                      Overall Metadata
                    </Typography>
                    <Chip
                      label={`${metadataEntries.length} field${metadataEntries.length !== 1 ? 's' : ''}`}
                      size="small"
                      variant="outlined"
                    />
                  </Stack>
                  <Grid container spacing={1.5}>
                    {metadataEntries.map((entry) => (
                      <Grid item xs={12} md={6} key={`${version.id}-${entry.key}`}>
                        <Box
                          sx={{
                            p: 1.25,
                            borderRadius: 1,
                            border: '1px solid',
                            borderColor: entry.differsFromDb ? 'warning.main' : 'divider',
                            bgcolor: entry.differsFromDb ? 'warning.50' : 'background.paper',
                            height: '100%',
                          }}
                        >
                          <Stack
                            direction="row"
                            alignItems="center"
                            justifyContent="space-between"
                            spacing={1}
                            sx={{ mb: 0.5 }}
                          >
                            <Typography variant="caption" color="text.secondary">
                              {entry.label}
                            </Typography>
                            {entry.differsFromDb && (
                              <Chip label="≠ DB" size="small" color="warning" />
                            )}
                          </Stack>
                          <Typography variant="body2" sx={{ wordBreak: 'break-word' }}>
                            {entry.fileValue}
                          </Typography>
                          {entry.differsFromDb && (
                            <Typography variant="caption" color="text.secondary">
                              DB: {entry.storedValue}
                            </Typography>
                          )}
                        </Box>
                      </Grid>
                    ))}
                  </Grid>
                </Box>
              )}

              {/* Tag comparison component */}
              <TagComparison bookId={version.id} versions={allVersions} refreshKey={filesRefreshKey} snapshotTimestamp={compareSnapshotTs} onClearSnapshot={onClearCompareSnapshot} />

              {/* Segments/files table for multi-file books */}
              {vSegs.length === 0 && (
                <Typography variant="body2" color="text.secondary" sx={{ mt: 2, fontStyle: 'italic' }}>
                  No files found for this version.
                </Typography>
              )}
              {vSegs.length > 0 && (() => {
                const missingCount = vSegs.filter((s) => s.file_exists === false).length;
                const isCurrentBook = isCurrent;
                const allSelected = isCurrentBook && vSegs.length > 0 && selectedSegmentIds.size === vSegs.length;
                const someSelected = isCurrentBook && selectedSegmentIds.size > 0 && !allSelected;
                return (
                  <Box sx={{ mt: 2 }}>
                    <Stack direction="row" alignItems="center" spacing={2} sx={{ mb: 1 }}>
                      <Typography variant="subtitle2" color="text.secondary">
                        Files ({vSegs.length})
                      </Typography>
                      {isCurrentBook && vSegs.length > 1 && (
                        <Button
                          size="small"
                          variant="outlined"
                          onClick={onExtractTrackInfo}
                        >
                          Auto-fill Track Numbers
                        </Button>
                      )}
                    </Stack>
                    {missingCount > 0 && (
                      <Alert severity="warning" sx={{ mb: 1 }}>
                        {missingCount} of {vSegs.length} file{vSegs.length !== 1 ? 's' : ''} missing on disk.
                      </Alert>
                    )}
                    {/* Segment action bar for current version */}
                    {isCurrentBook && selectedSegmentIds.size > 0 && vSegs.length > 1 && (
                      <Stack direction="row" spacing={1} alignItems="center" sx={{ mb: 1, p: 1, bgcolor: 'action.selected', borderRadius: 1 }}>
                        <Typography variant="body2">{selectedSegmentIds.size} selected</Typography>
                        <Button size="small" variant="contained" startIcon={<TransformIcon />}
                          disabled={splittingVersion} onClick={onSplitVersion}>
                          {splittingVersion ? 'Splitting...' : 'Split to New Version'}
                        </Button>
                        <Button size="small" variant="contained" color="secondary" startIcon={<TransformIcon />}
                          disabled={splittingToBooks} onClick={onSplitToBooks}>
                          {splittingToBooks ? 'Splitting...' : 'Split to New Books'}
                        </Button>
                        {versions.length > 1 && versions
                          .filter((v) => v.id !== book.id)
                          .map((v) => (
                            <Button key={v.id} size="small" variant="outlined"
                              onClick={() => onMoveToVersion(v.id)}>
                              Move to: {v.title}{v.format ? ` (${v.format.toUpperCase()})` : ''}
                            </Button>
                          ))}
                      </Stack>
                    )}
                    <Table size="small" data-testid="segment-table">
                      <TableHead>
                        <TableRow>
                          {isCurrentBook && vSegs.length > 1 && (
                            <TableCell padding="checkbox">
                              <Checkbox size="small" checked={allSelected} indeterminate={someSelected}
                                onChange={(e) => {
                                  if (e.target.checked) {
                                    onSetSelectedSegmentIds(new Set(vSegs.map((s) => s.id)));
                                  } else {
                                    onSetSelectedSegmentIds(new Set());
                                  }
                                }} />
                            </TableCell>
                          )}
                          <TableCell>#</TableCell>
                          <TableCell>File</TableCell>
                          <TableCell>Origin</TableCell>
                          <TableCell>Duration</TableCell>
                          <TableCell align="right">Size</TableCell>
                        </TableRow>
                      </TableHead>
                      <TableBody>
                        {visibleSegments.map((seg) => {
                          const isMissing = seg.file_exists === false;
                          const isSelected = isCurrentBook && selectedSegmentIds.has(seg.id);
                          return (
                            <TableRow key={seg.id} hover selected={isSelected}
                              sx={{ cursor: isCurrentBook ? 'pointer' : 'default',
                                ...(isMissing && { bgcolor: 'error.50', '&:hover': { bgcolor: 'error.100' } }) }}
                              onClick={() => {
                                if (!isCurrentBook) return;
                                if (isMissing) { onSetRelocateSegment(seg); }
                                else { onSetSelectedSegmentIds(new Set([seg.id])); onSetActiveTab('info'); }
                              }}>
                              {isCurrentBook && vSegs.length > 1 && (
                                <TableCell padding="checkbox" onClick={(e) => e.stopPropagation()}>
                                  <Checkbox size="small" checked={isSelected}
                                    onChange={(e) => {
                                      const next = new Set(selectedSegmentIds);
                                      if (e.target.checked) next.add(seg.id); else next.delete(seg.id);
                                      onSetSelectedSegmentIds(next);
                                    }} />
                                </TableCell>
                              )}
                              <TableCell>
                                <Stack direction="row" alignItems="center" spacing={0.5}>
                                  {isMissing && (
                                    <Tooltip title={`Missing: ${seg.file_path}`}>
                                      <ErrorOutlineIcon color="error" fontSize="small" />
                                    </Tooltip>
                                  )}
                                  <span>{seg.track_number ?? '\u2014'}</span>
                                </Stack>
                              </TableCell>
                              <TableCell sx={{ wordBreak: 'break-all', fontSize: '0.8rem', ...(isMissing && { color: 'error.main' }) }}>
                                <Tooltip title={seg.file_path}><span>{seg.file_path}</span></Tooltip>
                              </TableCell>
                              <TableCell>
                                {(seg as unknown as api.BookFile).deluge_original_path
                                  ? (
                                    <Tooltip title={(seg as unknown as api.BookFile).deluge_original_path!}>
                                      <Chip label="Deluge" size="small" variant="outlined" color="secondary" />
                                    </Tooltip>
                                  )
                                  : '\u2014'}
                              </TableCell>
                              <TableCell>{formatDuration(seg.duration_seconds)}</TableCell>
                              <TableCell align="right">
                                {formatBytes(seg.size_bytes)}
                              </TableCell>
                            </TableRow>
                          );
                        })}
                      </TableBody>
                    </Table>
                    {vSegs.length > SEGMENT_PREVIEW_COUNT && (
                      <Box sx={{ mt: 1 }}>
                        <Button
                          size="small"
                          onClick={() => {
                            onSetExpandedSegmentVersionIds((prev) => {
                              const next = new Set(prev);
                              if (next.has(version.id)) {
                                next.delete(version.id);
                              } else {
                                next.add(version.id);
                              }
                              return next;
                            });
                          }}
                        >
                          {showAllSegments
                            ? 'Show fewer files'
                            : `Show all ${vSegs.length} files${hiddenSegmentCount > 0 ? ` (${hiddenSegmentCount} more)` : ''}`}
                        </Button>
                      </Box>
                    )}
                  </Box>
                );
              })()}

              {/* Hash chain per file (HASH-CHAIN-2, #1270). Only the current
                  version carries per-file hash data (bookFiles); non-current
                  versions have no hash data and are out of scope. */}
              {isCurrent && bookFiles.length > 0 && (
                <Box sx={{ mt: 2 }} data-testid="hash-chain-section">
                  <Typography variant="subtitle2" color="text.secondary" sx={{ mb: 1 }}>
                    Hash Chain
                  </Typography>
                  <Stack spacing={1}>
                    {bookFiles.map((f) => (
                      <Box
                        key={f.id}
                        sx={{
                          p: 1,
                          border: '1px solid',
                          borderColor: 'divider',
                          borderRadius: 1,
                          bgcolor: 'background.default',
                        }}
                      >
                        <Typography
                          variant="caption"
                          sx={{ display: 'block', wordBreak: 'break-all', mb: 0.5 }}
                        >
                          {f.file_path}
                        </Typography>
                        <Stack
                          direction="row"
                          spacing={1.5}
                          alignItems="flex-start"
                          flexWrap="wrap"
                          useFlexGap
                          divider={
                            <Box component="span" sx={{ alignSelf: 'center', color: 'text.disabled' }}>
                              →
                            </Box>
                          }
                        >
                          {HASH_CHAIN_LINKS.map((link) => (
                            <HashChainLink key={link.key} label={link.label} value={f[link.key]} />
                          ))}
                        </Stack>
                      </Box>
                    ))}
                  </Stack>
                </Box>
              )}
            </Box>
          );
        })}
      </Collapse>
    </Paper>
  );
};
