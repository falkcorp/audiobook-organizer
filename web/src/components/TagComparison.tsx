// file: web/src/components/TagComparison.tsx
// version: 1.4.2
// guid: cfed2692-76f6-47b0-bc84-cc2a4075e554

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  Alert,
  Box,
  Chip,
  Collapse,
  FormControl,
  IconButton,
  InputLabel,
  LinearProgress,
  MenuItem,
  Select,
  Stack,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableRow,
  Tooltip,
  Typography,
} from '@mui/material';
import CloseIcon from '@mui/icons-material/Close';
import VisibilityOffIcon from '@mui/icons-material/VisibilityOff';
import RestoreIcon from '@mui/icons-material/Restore';
import type { Book, BookTags, TagSourceValues } from '../services/api';
import * as api from '../services/api';

interface TagComparisonProps {
  bookId: string;
  versions: Book[];
  refreshKey?: number;
  snapshotTimestamp?: string | null;
  onClearSnapshot?: () => void;
}

// KEY_TAGS removed — badges now dynamically show all tags from the comparison table

const TAG_LABELS: Record<string, string> = {
  title: 'title',
  author_name: 'author',
  narrator: 'narrator',
  series_name: 'series',
  publisher: 'publisher',
  language: 'language',
  isbn13: 'isbn',
  isbn10: 'isbn10',
  audiobook_release_year: 'year',
  description: 'description',
  asin: 'asin',
  edition: 'edition',
  print_year: 'print year',
  series_index: 'series #',
  album: 'album',
  genre: 'genre',
  book_id: 'book ID',
  open_library_id: 'OpenLibrary',
  hardcover_id: 'Hardcover',
  google_books_id: 'Google Books',
};

export const TagComparison = ({ bookId, versions, refreshKey, snapshotTimestamp, onClearSnapshot }: TagComparisonProps) => {
  const [tags, setTags] = useState<BookTags | null>(null);
  const [loading, setLoading] = useState(false);
  const [compareId, setCompareId] = useState<string>('');
  const [expanded, setExpanded] = useState(true); // Always show table when tray is open
  const [hiddenTags, setHiddenTags] = useState<Set<string>>(new Set());
  const [colWidths, setColWidths] = useState<Record<number, number>>({});
  const resizingCol = useRef<number | null>(null);
  const resizeStartX = useRef(0);
  const resizeStartWidth = useRef(0);
  const tableRef = useRef<HTMLTableElement | null>(null);

  const snapshotComparisonActive = Boolean(snapshotTimestamp) && compareId === '';

  const loadTags = useCallback(async () => {
    setLoading(true);
    try {
      const result = await api.getBookTags(
        bookId,
        compareId || undefined,
        snapshotComparisonActive ? snapshotTimestamp ?? undefined : undefined
      );
      setTags(result);
    } catch {
      setTags(null);
    } finally {
      setLoading(false);
    }
  }, [bookId, compareId, snapshotComparisonActive, snapshotTimestamp]);

  useEffect(() => {
    loadTags();
  }, [loadTags, refreshKey]);

  // Auto-expand when snapshot is selected; loadTags fires automatically
  // via the first useEffect since snapshotTimestamp changes loadTags' dependencies
  useEffect(() => {
    if (snapshotTimestamp) {
      setExpanded(true);
      setCompareId('');
    }
  }, [snapshotTimestamp]);

  const tagEntries = useMemo(() => {
    if (!tags?.tags) return [];
    return Object.entries(tags.tags);
  }, [tags]);

  const visibleTagEntries = useMemo(
    () => tagEntries.filter(([name]) => !hiddenTags.has(name)),
    [tagEntries, hiddenTags]
  );

  // keyTagStatus removed — badges are now dynamic from tagEntries

  const otherVersions = useMemo(
    () => versions.filter((v) => v.id !== bookId),
    [versions, bookId]
  );

  const hasComparison = compareId !== '' || snapshotComparisonActive;

  // Column resize handlers
  const handleResizeStart = (colIndex: number, e: React.MouseEvent) => {
    e.preventDefault();
    e.stopPropagation();
    resizingCol.current = colIndex;
    resizeStartX.current = e.clientX;

    if (tableRef.current) {
      const headerCells = tableRef.current.querySelectorAll('thead th');
      if (headerCells[colIndex]) {
        resizeStartWidth.current = headerCells[colIndex].getBoundingClientRect().width;
      }
    }

    const handleMouseMove = (ev: MouseEvent) => {
      if (resizingCol.current === null) return;
      const delta = ev.clientX - resizeStartX.current;
      const newWidth = Math.max(60, resizeStartWidth.current + delta);
      setColWidths((prev) => ({ ...prev, [resizingCol.current!]: newWidth }));
    };

    const handleMouseUp = () => {
      resizingCol.current = null;
      document.removeEventListener('mousemove', handleMouseMove);
      document.removeEventListener('mouseup', handleMouseUp);
      document.body.style.cursor = '';
      document.body.style.userSelect = '';
    };

    document.body.style.cursor = 'col-resize';
    document.body.style.userSelect = 'none';
    document.addEventListener('mousemove', handleMouseMove);
    document.addEventListener('mouseup', handleMouseUp);

    // Cleanup on unmount via useEffect pattern
    return () => {
      document.removeEventListener('mousemove', handleMouseMove);
      document.removeEventListener('mouseup', handleMouseUp);
    };
  };

  const resizeHandle = (colIndex: number) => (
    <Box
      sx={{
        position: 'absolute',
        right: -3,
        top: 0,
        bottom: 0,
        width: 6,
        cursor: 'col-resize',
        zIndex: 1,
        '&:hover': { bgcolor: 'primary.main', opacity: 0.4 },
      }}
      onMouseDown={(e) => handleResizeStart(colIndex, e)}
    />
  );

  // Helper to get cell color for a specific tag+source combination
  const getCellStyle = (tagValues: TagSourceValues, source: 'file' | 'db' | 'comparison') => {
    const fileVal = tagValues.file_value != null ? String(tagValues.file_value) : '';
    const storedVal = tagValues.stored_value != null ? String(tagValues.stored_value) : '';
    const compVal = (tagValues as TagSourceValues & { comparison_value?: string | number | boolean | null }).comparison_value;
    const compStr = compVal != null ? String(compVal) : '';

    if (source === 'db') {
      if (storedVal && !fileVal) return { color: '#4caf50' }; // green: in DB but not file
      if (fileVal && storedVal && fileVal !== storedVal) return { color: '#ff9800' }; // amber: differs
    }
    if (source === 'comparison' && hasComparison) {
      if (compStr && fileVal && compStr !== fileVal) return { color: '#ef5350' }; // red: differs
      if (fileVal && !compStr) return { color: '#4caf50' }; // green: present here, missing there
    }
    return {};
  };

  const clearComparison = () => {
    setCompareId('');
    onClearSnapshot?.();
  };

  return (
    <Box>
      {/* Dynamic tag badges — click to toggle column visibility */}
      {/* Tags visible in the table below are shown as hidden badges; clicking shows them */}
      <Stack direction="row" spacing={0.5} flexWrap="wrap" useFlexGap sx={{ mb: 1 }}>
        {tagEntries.map(([key, tagValues]) => {
          const isVisible = !hiddenTags.has(key);
          const fileVal = tagValues.file_value;
          const hasValue = fileVal != null && fileVal !== '' && fileVal !== false;
          const storedVal = tagValues.stored_value;
          const hasStored = storedVal != null && storedVal !== '' && storedVal !== false;
          const matches = hasValue && hasStored && String(fileVal) === String(storedVal);

          return (
            <Chip
              key={key}
              label={`${matches ? '\u2713' : hasValue || hasStored ? '\u2717' : '\u2013'} ${TAG_LABELS[key] || key}`}
              size="small"
              color={matches ? 'success' : (hasValue || hasStored) ? 'warning' : 'default'}
              variant={isVisible ? 'outlined' : 'filled'}
              clickable
              onClick={() => {
                setHiddenTags((prev) => {
                  const next = new Set(prev);
                  if (next.has(key)) {
                    next.delete(key);
                  } else {
                    next.add(key);
                  }
                  return next;
                });
              }}
              sx={{
                fontSize: '0.7rem',
                opacity: isVisible ? 0.6 : 1,
                textDecoration: isVisible ? 'none' : 'none',
              }}
            />
          );
        })}
      </Stack>

      <Collapse in={expanded}>
        {/* Snapshot/comparison banner with dismiss */}
        {(snapshotComparisonActive || compareId) && (
          <Alert
            severity="info"
            sx={{ mb: 2 }}
            data-testid="snapshot-comparison-banner"
            action={
              <IconButton size="small" onClick={clearComparison}>
                <CloseIcon fontSize="small" />
              </IconButton>
            }
          >
            {snapshotComparisonActive
              ? `Comparing against snapshot from ${new Date(snapshotTimestamp ?? '').toLocaleString()}`
              : `Comparing against version`}
          </Alert>
        )}

        {/* Comparison dropdown */}
        <Stack direction="row" spacing={1} alignItems="center" sx={{ mb: 2 }}>
          {otherVersions.length > 0 && (
            <FormControl size="small" sx={{ minWidth: 280 }}>
              <InputLabel>Compare against</InputLabel>
              <Select
                value={compareId}
                label="Compare against"
                onChange={(e) => { setCompareId(e.target.value); if (e.target.value) onClearSnapshot?.(); }}
                data-testid="tag-comparison-select"
              >
                <MenuItem value="">None</MenuItem>
                {otherVersions.map((v) => (
                  <MenuItem key={v.id} value={v.id}>
                    {v.title || 'Untitled'}{v.format ? ` (${v.format.toUpperCase()})` : ''}
                  </MenuItem>
                ))}
              </Select>
            </FormControl>
          )}
        </Stack>

        {loading && <LinearProgress sx={{ mb: 1 }} />}

        {/* Hidden tags restore bar */}
        {hiddenTags.size > 0 && (
          <Stack direction="row" spacing={0.5} flexWrap="wrap" useFlexGap alignItems="center" sx={{ mb: 1 }}>
            <Typography variant="caption" color="text.secondary">
              {hiddenTags.size} hidden:
            </Typography>
            {Array.from(hiddenTags).map((tag) => (
              <Chip
                key={tag}
                label={TAG_LABELS[tag] || tag}
                size="small"
                variant="outlined"
                onDelete={() => setHiddenTags((prev) => { const next = new Set(prev); next.delete(tag); return next; })}
                sx={{ fontSize: '0.7rem' }}
              />
            ))}
            <Chip
              label="Show all"
              size="small"
              color="primary"
              variant="outlined"
              icon={<RestoreIcon sx={{ fontSize: 14 }} />}
              onClick={() => setHiddenTags(new Set())}
              sx={{ fontSize: '0.7rem' }}
            />
          </Stack>
        )}

        {/* Transposed table: tags as columns, sources as rows */}
        {visibleTagEntries.length > 0 && (
          <Box sx={{ overflowX: 'auto' }}>
            <Table
              size="small"
              ref={tableRef}
              sx={{ '& td, & th': { py: 0.75, px: 1.5, whiteSpace: 'nowrap' } }}
            >
              <TableHead>
                <TableRow>
                  {/* Row label column */}
                  <TableCell sx={{ fontWeight: 'bold', position: 'sticky', left: 0, bgcolor: 'background.paper', zIndex: 2, minWidth: 100 }}>
                    Source
                  </TableCell>
                  {/* One column per tag */}
                  {visibleTagEntries.map(([tagName], colIdx) => (
                    <TableCell
                      key={tagName}
                      sx={{
                        fontWeight: 'bold',
                        position: 'relative',
                        minWidth: 80,
                        ...(colWidths[colIdx] ? { width: colWidths[colIdx] } : {}),
                      }}
                    >
                      <Stack direction="row" alignItems="center" spacing={0.5}>
                        <span>{TAG_LABELS[tagName] || tagName}</span>
                        <Tooltip title={`Hide "${TAG_LABELS[tagName] || tagName}"`}>
                          <IconButton
                            size="small"
                            onClick={() => setHiddenTags((prev) => new Set(prev).add(tagName))}
                            sx={{ opacity: 0.3, '&:hover': { opacity: 1 }, p: 0 }}
                          >
                            <VisibilityOffIcon sx={{ fontSize: 12 }} />
                          </IconButton>
                        </Tooltip>
                      </Stack>
                      {resizeHandle(colIdx)}
                    </TableCell>
                  ))}
                </TableRow>
              </TableHead>
              <TableBody>
                {/* File Value row */}
                <TableRow>
                  <TableCell sx={{ fontWeight: 'bold', color: 'text.secondary', position: 'sticky', left: 0, bgcolor: 'background.paper', zIndex: 1 }}>
                    File
                  </TableCell>
                  {visibleTagEntries.map(([tagName, tagValues]) => {
                    const val = tagValues.file_value != null ? String(tagValues.file_value) : '\u2014';
                    return (
                      <TableCell key={tagName} sx={{ fontSize: '0.85rem' }}>
                        {val}
                      </TableCell>
                    );
                  })}
                </TableRow>

                {/* DB Value row */}
                <TableRow>
                  <TableCell sx={{ fontWeight: 'bold', color: 'text.secondary', position: 'sticky', left: 0, bgcolor: 'background.paper', zIndex: 1 }}>
                    DB
                  </TableCell>
                  {visibleTagEntries.map(([tagName, tagValues]) => {
                    const val = tagValues.stored_value != null ? String(tagValues.stored_value) : '\u2014';
                    const style = getCellStyle(tagValues, 'db');
                    return (
                      <TableCell key={tagName} sx={{ fontSize: '0.85rem', ...style }}>
                        {val}
                      </TableCell>
                    );
                  })}
                </TableRow>

                {/* Comparison row (only when active) */}
                {hasComparison && (
                  <TableRow>
                    <TableCell sx={{ fontWeight: 'bold', color: 'text.secondary', position: 'sticky', left: 0, bgcolor: 'background.paper', zIndex: 1 }}>
                      {snapshotComparisonActive ? 'Snapshot' : 'Compare'}
                    </TableCell>
                    {visibleTagEntries.map(([tagName, tagValues]) => {
                      const compVal = (tagValues as TagSourceValues & { comparison_value?: string | number | boolean | null }).comparison_value;
                      const val = compVal != null ? String(compVal) : '\u2014';
                      const style = getCellStyle(tagValues, 'comparison');
                      return (
                        <TableCell key={tagName} sx={{ fontSize: '0.85rem', ...style }}>
                          {val}
                        </TableCell>
                      );
                    })}
                  </TableRow>
                )}
              </TableBody>
            </Table>
          </Box>
        )}

        {visibleTagEntries.length === 0 && !loading && tagEntries.length === 0 && (
          <Typography variant="body2" color="text.secondary" sx={{ py: 2 }}>
            No tag data available.
          </Typography>
        )}
      </Collapse>
    </Box>
  );
};
