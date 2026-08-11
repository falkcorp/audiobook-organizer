// file: web/src/components/audiobooks/AudiobookList.tsx
// version: 2.9.0
// guid: 0c1d2e3f-4a5b-6c7d-8e9f-0a1b2c3d4e5f
// last-edited: 2026-08-10
import React, { useCallback, useEffect, useRef, useState } from 'react';
import {
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  Paper,
  IconButton,
  Menu,
  MenuItem,
  Typography,
  Box,
  CircularProgress,
  Checkbox,
  Chip,
  Collapse,
  Switch,
  FormControlLabel,
  Button,
  Divider,
} from '@mui/material';
import {
  MoreVert as MoreVertIcon,
  Edit as EditIcon,
  Delete as DeleteIcon,
  ArrowUpward as ArrowUpwardIcon,
  ArrowDownward as ArrowDownwardIcon,
  ExpandMore as ExpandMoreIcon,
  ExpandLess as ExpandLessIcon,
  CheckCircle as CheckCircleIcon,
  Cancel as CancelIcon,
} from '@mui/icons-material';
import type { Audiobook, BookFile, FilterOptions } from '../../types';
import { type ColumnDefinition, getDefaultVisibleColumns } from '../../config/columnDefinitions';
import * as api from '../../services/api';
import type { QuickQueryItem } from '../../services/api';
import { LoadingWithCancel } from './LoadingWithCancel';

interface AudiobookListProps {
  audiobooks: Audiobook[];
  loading?: boolean;
  onCancelLoad?: () => void;
  onEdit?: (audiobook: Audiobook) => void;
  onDelete?: (audiobook: Audiobook) => void;
  onClick?: (audiobook: Audiobook) => void;
  selectedIds?: Set<string>;
  onToggleSelect?: (audiobook: Audiobook, event?: React.MouseEvent) => void;
  onSelectAll?: () => void;
  columns?: ColumnDefinition[];
  columnWidths?: Record<string, number>;
  sortBy?: string;
  sortOrder?: 'asc' | 'desc';
  onSortChange?: (sortKey: string, order: 'asc' | 'desc') => void;
  onColumnResize?: (columnId: string, width: number) => void;
  visibleColumnIds?: string[];
  onToggleColumn?: (columnId: string) => void;
  /** Called when a quick filter preset is clicked — applies the preset's filter. */
  onFiltersChange?: (filters: FilterOptions) => void;
}

const fallbackColumns = getDefaultVisibleColumns();

export const AudiobookList: React.FC<AudiobookListProps> = ({
  audiobooks,
  loading = false,
  onCancelLoad,
  onEdit,
  onDelete,
  onClick,
  selectedIds,
  onToggleSelect,
  onSelectAll,
  columns,
  columnWidths,
  sortBy,
  sortOrder = 'asc',
  onSortChange,
  onColumnResize,
  visibleColumnIds,
  onToggleColumn,
  onFiltersChange,
}) => {
  const activeColumns = columns ?? fallbackColumns;

  const [anchorEls, setAnchorEls] = React.useState<Record<string, HTMLElement | null>>({});
  const [headerMenuAnchor, setHeaderMenuAnchor] = React.useState<HTMLElement | null>(null);
  // Quick-query preset counts, loaded lazily when the header menu opens.
  const [quickQueries, setQuickQueries] = useState<QuickQueryItem[]>([]);
  const [quickQueriesLoaded, setQuickQueriesLoaded] = useState(false);
  // Fix #1: use Record instead of Set for proper React equality comparisons
  const [expandedRows, setExpandedRows] = useState<Record<string, boolean>>({});
  const [filesCache, setFilesCache] = useState<Record<string, BookFile[]>>({});
  const [loadingFiles, setLoadingFiles] = useState<Record<string, boolean>>({});
  // Fix #6: track per-book fetch errors to show retry UI
  const [fetchErrors, setFetchErrors] = useState<Record<string, string | null>>({});
  // Fix #2: per-book AbortControllers to cancel in-flight fetches on collapse
  const abortControllersRef = useRef<Record<string, AbortController>>({});

  // --- Resize state (non-React for perf during drag) ---
  const resizingRef = useRef<{
    columnId: string;
    startX: number;
    startWidth: number;
    minWidth: number;
  } | null>(null);
  const cleanupRef = useRef<(() => void) | null>(null);

  // Cleanup resize listeners on unmount
  useEffect(() => {
    return () => {
      cleanupRef.current?.();
    };
  }, []);

  const handleMenuClick = (event: React.MouseEvent<HTMLElement>, id: string) => {
    event.stopPropagation();
    setAnchorEls((prev) => ({ ...prev, [id]: event.currentTarget }));
  };

  const handleClose = (id: string) => {
    setAnchorEls((prev) => ({ ...prev, [id]: null }));
  };

  const handleHeaderMenuClick = (event: React.MouseEvent<HTMLElement>) => {
    event.stopPropagation();
    setHeaderMenuAnchor(event.currentTarget);
    // Lazy-load quick-query counts on first open (or re-open after close).
    // Session-level cache: once loaded, counts stay until the component unmounts.
    if (!quickQueriesLoaded) {
      api.getQuickQueries()
        .then((items) => {
          setQuickQueries(items);
          setQuickQueriesLoaded(true);
        })
        .catch(() => {
          // Non-fatal: quick filters just won't show counts.
        });
    }
  };

  const handleHeaderMenuClose = () => {
    setHeaderMenuAnchor(null);
  };

  const handleEdit = (audiobook: Audiobook) => {
    handleClose(audiobook.id);
    onEdit?.(audiobook);
  };

  const handleDelete = (audiobook: Audiobook) => {
    handleClose(audiobook.id);
    onDelete?.(audiobook);
  };

  const handleRowClick = (audiobook: Audiobook) => {
    onClick?.(audiobook);
  };

  const handleFileUpdate = (bookId: string, updatedFile: BookFile) => {
    setFilesCache((prev) => ({
      ...prev,
      [bookId]: (prev[bookId] || []).map((f) =>
        f.id === updatedFile.id ? updatedFile : f
      ),
    }));
  };

  const handleSortClick = useCallback(
    (col: ColumnDefinition) => {
      if (!col.sortable || !onSortChange) return;
      const newOrder = sortBy === col.sortKey && sortOrder === 'asc' ? 'desc' : 'asc';
      onSortChange(col.sortKey, newOrder);
    },
    [sortBy, sortOrder, onSortChange]
  );

  const handleResizeStart = useCallback(
    (e: React.MouseEvent, col: ColumnDefinition) => {
      e.preventDefault();
      e.stopPropagation();
      const currentWidth = columnWidths?.[col.id] ?? col.defaultWidth;
      resizingRef.current = {
        columnId: col.id,
        startX: e.clientX,
        startWidth: currentWidth,
        minWidth: col.minWidth,
      };

      const handleMouseMove = (moveEvt: MouseEvent) => {
        if (!resizingRef.current) return;
        const delta = moveEvt.clientX - resizingRef.current.startX;
        const newWidth = Math.max(
          resizingRef.current.minWidth,
          resizingRef.current.startWidth + delta
        );
        // Apply width visually via the header cell's parent
        const th = (e.target as HTMLElement).closest('th');
        if (th) {
          th.style.width = `${newWidth}px`;
          th.style.minWidth = `${newWidth}px`;
        }
      };

      const handleMouseUp = (upEvt: MouseEvent) => {
        document.removeEventListener('mousemove', handleMouseMove);
        document.removeEventListener('mouseup', handleMouseUp);
        cleanupRef.current = null;
        if (!resizingRef.current) return;
        const delta = upEvt.clientX - resizingRef.current.startX;
        const newWidth = Math.max(
          resizingRef.current.minWidth,
          resizingRef.current.startWidth + delta
        );
        onColumnResize?.(resizingRef.current.columnId, newWidth);
        resizingRef.current = null;
      };

      document.addEventListener('mousemove', handleMouseMove);
      document.addEventListener('mouseup', handleMouseUp);
      cleanupRef.current = () => {
        document.removeEventListener('mousemove', handleMouseMove);
        document.removeEventListener('mouseup', handleMouseUp);
        resizingRef.current = null;
      };
    },
    [columnWidths, onColumnResize]
  );

  const allSelected =
    audiobooks.length > 0 && audiobooks.every((book) => selectedIds?.has(book.id));
  const someSelected = audiobooks.some((book) => selectedIds?.has(book.id));

  const hasSelection = Boolean(onToggleSelect);
  const hasActions = Boolean(onEdit || onDelete);

  const formatCellValue = (col: ColumnDefinition, book: Audiobook): string => {
    const raw = col.accessor(book);
    if (col.formatter) return col.formatter(raw);
    return raw != null ? String(raw) : '--';
  };

  const toggleExpandRow = (bookId: string) => {
    setExpandedRows((prev) => {
      const isCurrentlyExpanded = !!prev[bookId];
      if (isCurrentlyExpanded) {
        // Fix #2: abort any in-flight fetch for this book
        abortControllersRef.current[bookId]?.abort();
        delete abortControllersRef.current[bookId];
        // Fix #3: clean up cache, errors, and loading state on collapse
        setFilesCache((fc) => { const n = { ...fc }; delete n[bookId]; return n; });
        setFetchErrors((fe) => { const n = { ...fe }; delete n[bookId]; return n; });
        setLoadingFiles((lf) => { const n = { ...lf }; delete n[bookId]; return n; });
        return { ...prev, [bookId]: false };
      } else {
        // Fetch files if not already loading
        if (!loadingFiles[bookId]) {
          fetchFiles(bookId);
        }
        return { ...prev, [bookId]: true };
      }
    });
  };

  const fetchFiles = async (bookId: string) => {
    // Fix #2: create a fresh AbortController for this fetch
    const controller = new AbortController();
    abortControllersRef.current[bookId] = controller;

    setLoadingFiles((prev) => ({ ...prev, [bookId]: true }));
    setFetchErrors((prev) => ({ ...prev, [bookId]: null }));
    try {
      const result = await api.getBookFiles(bookId, { signal: controller.signal });
      setFilesCache((prev) => ({ ...prev, [bookId]: result.files }));
    } catch (error) {
      if (error instanceof Error && error.name === 'AbortError') {
        // Fetch was cancelled by collapse — do not update state
        return;
      }
      const msg = error instanceof Error ? error.message : 'Unknown error';
      console.error(`Failed to fetch files for book ${bookId}:`, error);
      // Fix #6: surface the error so the UI can show a Retry button
      setFetchErrors((prev) => ({ ...prev, [bookId]: msg }));
    } finally {
      setLoadingFiles((prev) => ({ ...prev, [bookId]: false }));
      delete abortControllersRef.current[bookId];
    }
  };

  // Fix #4: check all 7 acoustid segments, not just seg0
  const hasFingerprint = (file: BookFile): boolean =>
    !!(file.acoustid_seg0 || file.acoustid_seg1 || file.acoustid_seg2 ||
       file.acoustid_seg3 || file.acoustid_seg4 || file.acoustid_seg5 ||
       file.acoustid_seg6);

  const getFingerprinterStatusColor = (file: BookFile): 'success' | 'default' => {
    return hasFingerprint(file) ? 'success' : 'default';
  };

  const getFingerprinterStatusIcon = (file: BookFile): React.ReactElement => {
    return hasFingerprint(file) ? (
      <CheckCircleIcon sx={{ fontSize: 16 }} />
    ) : (
      <CancelIcon sx={{ fontSize: 16 }} />
    );
  };

  const formatFileSize = (bytes?: number): string => {
    if (!bytes) return '--';
    return `${(bytes / 1024 / 1024).toFixed(2)} MB`;
  };

  if (loading) {
    return <LoadingWithCancel onCancel={onCancelLoad} />;
  }

  if (audiobooks.length === 0) {
    return (
      <Box
        display="flex"
        justifyContent="center"
        alignItems="center"
        minHeight="400px"
        flexDirection="column"
        gap={2}
      >
        <Typography variant="h6" color="text.secondary">
          No audiobooks found
        </Typography>
        <Typography variant="body2" color="text.secondary">
          Try adjusting your filters or add audiobooks to your library
        </Typography>
      </Box>
    );
  }

  return (
    <TableContainer component={Paper} sx={{ overflowX: 'auto' }}>
      <Table sx={{ tableLayout: 'fixed' }}>
        <TableHead>
          <TableRow>
            {/* Expand column */}
            <TableCell width={50} padding="checkbox" />

            {/* Checkbox column */}
            {hasSelection && (
              <TableCell width={50} padding="checkbox">
                {onSelectAll && (
                  <Checkbox
                    checked={allSelected}
                    indeterminate={someSelected && !allSelected}
                    onChange={onSelectAll}
                    inputProps={{
                      'aria-label': 'Select all books on page',
                    }}
                  />
                )}
              </TableCell>
            )}

            {/* Dynamic columns */}
            {activeColumns.map((col) => {
              const width = columnWidths?.[col.id] ?? col.defaultWidth;
              const isActiveSortCol = sortBy === col.sortKey;
              return (
                <TableCell
                  key={col.id}
                  sx={{
                    width,
                    minWidth: col.minWidth,
                    position: 'relative',
                    userSelect: 'none',
                    cursor: col.sortable ? 'pointer' : 'default',
                    whiteSpace: 'nowrap',
                    overflow: 'hidden',
                    textOverflow: 'ellipsis',
                    pr: 2,
                  }}
                  onClick={() => handleSortClick(col)}
                >
                  <Box
                    sx={{
                      display: 'flex',
                      alignItems: 'center',
                      gap: 0.5,
                    }}
                  >
                    <span>{col.label}</span>
                    {isActiveSortCol &&
                      (sortOrder === 'asc' ? (
                        <ArrowUpwardIcon sx={{ fontSize: 16 }} />
                      ) : (
                        <ArrowDownwardIcon sx={{ fontSize: 16 }} />
                      ))}
                  </Box>

                  {/* Resize handle */}
                  {onColumnResize && (
                    <Box
                      onMouseDown={(e) => handleResizeStart(e, col)}
                      sx={{
                        position: 'absolute',
                        right: 0,
                        top: 0,
                        bottom: 0,
                        width: 5,
                        cursor: 'col-resize',
                        '&:hover': {
                          backgroundColor: 'action.hover',
                        },
                      }}
                    />
                  )}
                </TableCell>
              );
            })}

            {/* Header actions column (kebab menu) */}
            <TableCell width={50} sx={{ textAlign: 'center' }}>
              <IconButton
                size="small"
                onClick={handleHeaderMenuClick}
                aria-label="Header actions"
              >
                <MoreVertIcon />
              </IconButton>
              <Menu
                anchorEl={headerMenuAnchor}
                open={Boolean(headerMenuAnchor)}
                onClose={handleHeaderMenuClose}
              >
                {/* Quick Filters section */}
                {onFiltersChange && (
                  <>
                    <MenuItem disabled sx={{ py: 0.5 }}>
                      <Typography variant="overline" color="text.secondary" sx={{ lineHeight: 1.5 }}>
                        Quick Filters
                      </Typography>
                    </MenuItem>
                    {quickQueries.length === 0 && !quickQueriesLoaded && (
                      <MenuItem disabled>
                        <Typography variant="body2" color="text.disabled">Loading...</Typography>
                      </MenuItem>
                    )}
                    {quickQueries.map((q) => (
                      q.count > 0 ? (
                        <MenuItem
                          key={q.id}
                          onClick={() => {
                            onFiltersChange(q.filter as FilterOptions);
                            handleHeaderMenuClose();
                          }}
                          sx={{ pl: 3 }}
                        >
                          <Typography variant="body2">
                            {q.label} ({q.count.toLocaleString()})
                          </Typography>
                        </MenuItem>
                      ) : (
                        <MenuItem key={q.id} disabled sx={{ pl: 3 }}>
                          <Typography variant="body2" color="text.disabled">
                            {q.label} (0)
                          </Typography>
                        </MenuItem>
                      )
                    ))}
                    <Divider />
                  </>
                )}

                {/* Column visibility toggle — grouped by category */}
                {onToggleColumn && visibleColumnIds && (
                  <>
                    <MenuItem disabled sx={{ py: 1 }}>
                      <Typography variant="caption" fontWeight="bold" color="text.secondary">
                        COLUMNS
                      </Typography>
                    </MenuItem>
                    {Array.from(
                      new Map(activeColumns.map((col) => [col.category, col])).keys()
                    ).map((category) => {
                      const colsInCategory = activeColumns.filter((col) => col.category === category);
                      return (
                        <Box key={category}>
                          <MenuItem disabled sx={{ py: 0.75, pl: 2 }}>
                            <Typography variant="body2" fontWeight="600" color="text.primary">
                              {category}
                            </Typography>
                          </MenuItem>
                          {colsInCategory.map((col) => (
                            <MenuItem
                              key={col.id}
                              onClick={() => onToggleColumn(col.id)}
                              sx={{ pl: 3 }}
                            >
                              <FormControlLabel
                                control={
                                  <Checkbox
                                    size="small"
                                    checked={visibleColumnIds.includes(col.id)}
                                    onClick={(e) => e.stopPropagation()}
                                  />
                                }
                                label={<Typography variant="body2">{col.label}</Typography>}
                                sx={{ m: 0, flex: 1 }}
                              />
                            </MenuItem>
                          ))}
                        </Box>
                      );
                    })}
                  </>
                )}
              </Menu>
            </TableCell>
          </TableRow>
        </TableHead>
        <TableBody>
          {audiobooks.map((audiobook) => {
            // Fix #1: Record-based lookups instead of Set.has()
            const isExpanded = !!expandedRows[audiobook.id];
            const files = filesCache[audiobook.id] ?? [];
            const isLoadingFiles = !!loadingFiles[audiobook.id];
            const fetchError = fetchErrors[audiobook.id] ?? null;

            return (
              <React.Fragment key={audiobook.id}>
                <TableRow
                  hover
                  onClick={() => handleRowClick(audiobook)}
                  sx={[{
                    contentVisibility: 'auto',
                    containIntrinsicSize: '1px 52px'
                  }, onClick ? {
                    cursor: 'pointer'
                  } : {
                    cursor: 'default'
                  }]}
                >
                  {/* Expand cell */}
                  <TableCell padding="checkbox" sx={{ width: 50 }}>
                    {/* Fix #5: accessibility attributes for expand button */}
                    <IconButton
                      size="small"
                      aria-label={`${isExpanded ? 'Collapse' : 'Expand'} files for ${audiobook.title}`}
                      aria-expanded={isExpanded}
                      onClick={(e) => {
                        e.stopPropagation();
                        toggleExpandRow(audiobook.id);
                      }}
                    >
                      {isExpanded ? <ExpandLessIcon /> : <ExpandMoreIcon />}
                    </IconButton>
                  </TableCell>

                  {/* Checkbox cell */}
                  {hasSelection && (
                    <TableCell padding="checkbox">
                      {onToggleSelect && (
                        <Checkbox
                          checked={selectedIds?.has(audiobook.id) || false}
                          onClick={(event) => {
                            event.stopPropagation();
                            onToggleSelect(audiobook, event as unknown as React.MouseEvent);
                          }}
                          onChange={() => {}}
                          inputProps={{
                            'aria-label': `Select ${audiobook.title || 'audiobook'}`,
                          }}
                        />
                      )}
                    </TableCell>
                  )}

              {/* Dynamic data cells */}
              {activeColumns.map((col) => (
                <TableCell
                  key={col.id}
                  sx={{
                    maxWidth: columnWidths?.[col.id] ?? col.defaultWidth,
                    overflow: 'hidden',
                    textOverflow: 'ellipsis',
                    whiteSpace: 'nowrap',
                  }}
                >
                  <Typography variant="body2" color="text.secondary" noWrap>
                    {formatCellValue(col, audiobook)}
                  </Typography>
                </TableCell>
              ))}

              {/* Actions cell */}
              <TableCell width={50} sx={{ textAlign: 'center' }}>
                {hasActions && (
                  <>
                    <IconButton size="small" onClick={(e) => handleMenuClick(e, audiobook.id)}>
                      <MoreVertIcon />
                    </IconButton>
                    <Menu
                      anchorEl={anchorEls[audiobook.id] || null}
                      open={Boolean(anchorEls[audiobook.id])}
                      onClose={() => handleClose(audiobook.id)}
                    >
                      {onEdit && (
                        <MenuItem onClick={() => handleEdit(audiobook)}>
                          <EditIcon sx={{ mr: 1 }} fontSize="small" />
                          Edit
                        </MenuItem>
                      )}
                      {onDelete && (
                        <MenuItem onClick={() => handleDelete(audiobook)}>
                          <DeleteIcon sx={{ mr: 1 }} fontSize="small" />
                          Delete
                        </MenuItem>
                      )}
                    </Menu>
                  </>
                )}
              </TableCell>
            </TableRow>

                {/* Expanded files rows */}
                {isExpanded && (
                  <TableRow sx={{ bgcolor: 'background.default' }}>
                    <TableCell colSpan={hasSelection ? activeColumns.length + 3 : activeColumns.length + 2} sx={{ p: 0 }}>
                      <Collapse in={isExpanded} timeout="auto" unmountOnExit>
                        <Box sx={{ p: 2 }}>
                          <Typography variant="subtitle2" sx={{ mb: 1.5, fontWeight: 600 }}>
                            Files ({audiobook.total_file_count || 0})
                          </Typography>

                          {isLoadingFiles ? (
                            <Box display="flex" justifyContent="center" p={2}>
                              <CircularProgress size={24} />
                            </Box>
                          ) : fetchError ? (
                            // Fix #6: show error UI with Retry button instead of silent spinner/empty list
                            <Box display="flex" alignItems="center" gap={1} p={1}>
                              <Typography variant="body2" color="error">
                                Failed to load files: {fetchError}
                              </Typography>
                              <Button size="small" variant="outlined" color="error" onClick={() => fetchFiles(audiobook.id)}>
                                Retry
                              </Button>
                            </Box>
                          ) : files.length === 0 ? (
                            <Typography variant="body2" color="text.secondary">
                              No files found
                            </Typography>
                          ) : (
                            <Box sx={{ display: 'flex', flexDirection: 'column', gap: 1 }}>
                              {files.map((file) => (
                                <Box
                                  key={file.id}
                                  sx={{
                                    display: 'grid',
                                    gridTemplateColumns: '1fr auto',
                                    gap: 1,
                                    alignItems: 'center',
                                    p: 1.5,
                                    bgcolor: 'background.paper',
                                    borderRadius: 1,
                                    border: '1px solid',
                                    borderColor: 'divider',
                                  }}
                                >
                                  <Box sx={{ minWidth: 0 }}>
                                    <Typography variant="body2" sx={{ fontWeight: 500, wordBreak: 'break-word' }}>
                                      {file.original_filename || file.file_path.split('/').pop() || 'Unknown'}
                                    </Typography>
                                    <Typography variant="caption" color="text.secondary" sx={{ display: 'block', wordBreak: 'break-word' }}>
                                      {file.file_path}
                                    </Typography>
                                  </Box>

                                  <Box sx={{ display: 'flex', gap: 1, alignItems: 'center', flexShrink: 0 }}>
                                    <Chip
                                      icon={getFingerprinterStatusIcon(file)}
                                      label={hasFingerprint(file) ? '✓ Fingerprinted' : '✗ Not Fingerprinted'}
                                      color={getFingerprinterStatusColor(file)}
                                      variant="outlined"
                                      size="small"
                                    />
                                    <FormControlLabel
                                      control={
                                        <Switch
                                          size="small"
                                          checked={!!file.skip_scan}
                                          onChange={async (e) => {
                                            const newValue = e.target.checked;
                                            try {
                                              const updated = await api.patchBookFile(audiobook.id, file.id, {
                                                skip_scan: newValue,
                                              });
                                              handleFileUpdate(audiobook.id, updated);
                                            } catch (err) {
                                              console.error('Failed to update skip_scan:', err);
                                            }
                                          }}
                                        />
                                      }
                                      label="Skip"
                                      sx={{ ml: 0.5 }}
                                    />
                                    <Typography variant="caption" color="text.secondary" sx={{ minWidth: 80, textAlign: 'right' }}>
                                      {formatFileSize(file.file_size)}
                                    </Typography>
                                  </Box>
                                </Box>
                              ))}
                            </Box>
                          )}
                        </Box>
                      </Collapse>
                    </TableCell>
                  </TableRow>
                )}
              </React.Fragment>
            );
          })}
        </TableBody>
      </Table>
    </TableContainer>
  );
};
