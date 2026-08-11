// file: web/src/components/dedup/DedupSeriesTab.tsx
// version: 1.1.0
// guid: c3d4e5f6-a7b8-9012-cdef-012345678902
// last-edited: 2026-08-10
import { useState, useEffect, useCallback } from 'react';
import {
  Box,
  Typography,
  Paper,
  Button,
  Alert,
  Chip,
  CircularProgress,
  Divider,
  IconButton,
  Tooltip,
  Card,
  CardContent,
  CardActions,
  Stack,
  TextField,
  Dialog,
  DialogTitle,
  DialogContent,
  DialogContentText,
  DialogActions,
  Checkbox,
} from '@mui/material';
import Collapse from '@mui/material/Collapse';
import MergeIcon from '@mui/icons-material/MergeType';
import RefreshIcon from '@mui/icons-material/Refresh';
import CheckCircleIcon from '@mui/icons-material/CheckCircle';
import MenuBookIcon from '@mui/icons-material/MenuBook';
import EditIcon from '@mui/icons-material/Edit';
import SaveIcon from '@mui/icons-material/Save';
import CloseIcon from '@mui/icons-material/Close';
import CleaningServicesIcon from '@mui/icons-material/CleaningServices';
import SearchIcon from '@mui/icons-material/Search';
import * as api from '../../services/api';
import type { SeriesDupGroup, Operation, ValidationResult } from '../../services/api';
import {
  cleanDisplayTitle,
  OperationProgress,
  runOperationWithPolling,
  usePagination,
  PaginationControls,
} from './dedupHelpers';

export function SeriesDedupTab() {
  const [groups, setGroups] = useState<SeriesDupGroup[]>([]);
  const [totalSeries, setTotalSeries] = useState(0);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [activeOp, setActiveOp] = useState<Operation | null>(null);
  const [mergeSuccess, setMergeSuccess] = useState<string | null>(null);
  const [keepSelections, setKeepSelections] = useState<Record<string, number[]>>({});
  const [selectedGroups, setSelectedGroups] = useState<Set<string>>(new Set());
  const [confirmOpen, setConfirmOpen] = useState(false);
  const [editingSeriesId, setEditingSeriesId] = useState<number | null>(null);
  const [editingName, setEditingName] = useState('');
  const [validationResults, setValidationResults] = useState<Record<string, ValidationResult[]>>({});
  const [validatingKey, setValidatingKey] = useState<string | null>(null);
  const [expandedValidation, setExpandedValidation] = useState<Set<string>>(new Set());
  const [floatingCovers, setFloatingCovers] = useState<{ src: string; title: string; author: string }[]>([]);
  const [bubbleSize, setBubbleSize] = useState(500);
  const [narratorFlags, setNarratorFlags] = useState<Record<string, Set<number>>>({});
  const [prunePreview, setPrunePreview] = useState<api.SeriesPrunePreview | null>(null);
  const [pruneLoading, setPruneLoading] = useState(false);
  const [pruneConfirmOpen, setPruneConfirmOpen] = useState(false);
  const pagination = usePagination(groups.length);

  const handleValidate = async (groupKey: string, query: string) => {
    setValidatingKey(groupKey);
    try {
      const resp = await api.validateDedupEntry(query, 'series');
      setValidationResults((prev) => ({ ...prev, [groupKey]: resp.results }));
      setExpandedValidation((prev) => new Set(prev).add(groupKey));
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Validation failed');
    } finally {
      setValidatingKey(null);
    }
  };

  const handleSaveEdit = async () => {
    if (editingSeriesId == null || !editingName.trim()) return;
    try {
      await api.updateSeriesName(editingSeriesId, editingName.trim());
      setEditingSeriesId(null);
      setEditingName('');
      fetchDuplicates();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to rename series');
    }
  };

  const populateFromData = useCallback((data: { groups: SeriesDupGroup[]; total_series: number }) => {
    setGroups(data.groups || []);
    setTotalSeries(data.total_series || 0);
    const defaults: Record<string, number[]> = {};
    (data.groups || []).forEach((g, i) => {
      const sorted = [...g.series].sort((a, b) => (a.author_id != null ? -1 : 0) - (b.author_id != null ? -1 : 0));
      defaults[`group-${i}`] = sorted.map((s) => s.id);
    });
    setKeepSelections(defaults);
    setSelectedGroups(new Set());
  }, []);

  const fetchDuplicates = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const data = await api.getSeriesDuplicates();
      if (data.needs_refresh) {
        await runOperationWithPolling(
          () => api.refreshSeriesDuplicates(),
          setActiveOp,
          async () => {
            const fresh = await api.getSeriesDuplicates();
            populateFromData(fresh);
            setLoading(false);
          },
          (msg) => { setError(msg); setLoading(false); },
        );
        return;
      }
      populateFromData(data);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to fetch series duplicates');
    } finally {
      setLoading(false);
    }
  }, [populateFromData]);

  useEffect(() => { fetchDuplicates(); }, [fetchDuplicates]);

  const handleMerge = async (group: SeriesDupGroup, groupKey: string) => {
    const selected = keepSelections[groupKey] || [];
    if (selected.length === 0) return;
    const keepId = selected[0]; // first selected is the one to keep
    const mergeIds = group.series.filter((s) => s.id !== keepId && selected.includes(s.id)).map((s) => s.id);
    if (mergeIds.length === 0) return;
    setMergeSuccess(null);

    // Reclassify any authors flagged as narrators before merging
    const flagged = narratorFlags[groupKey];
    if (flagged && flagged.size > 0) {
      for (const authorId of flagged) {
        try {
          await api.reclassifyAuthorAsNarrator(authorId);
        } catch (err) {
          setError(err instanceof Error ? err.message : `Failed to reclassify author ${authorId} as narrator`);
          return;
        }
      }
      // Clear flags for this group
      setNarratorFlags((prev) => { const next = { ...prev }; delete next[groupKey]; return next; });
    }

    await runOperationWithPolling(
      () => api.mergeSeriesGroup(keepId, mergeIds),
      setActiveOp,
      (final) => {
        if (final.status === 'failed') {
          setError(final.error_message || 'Series merge failed');
        } else {
          setMergeSuccess(`Merged series "${group.name}"`);
          setGroups((prev) => prev.filter((_, i) => `group-${i}` !== groupKey));
          setSelectedGroups((prev) => { const next = new Set(prev); next.delete(groupKey); return next; });
        }
      },
      setError,
    );
  };

  const handleMergeSelected = async () => {
    setMergeSuccess(null);
    for (let i = 0; i < groups.length; i++) {
      const groupKey = `group-${i}`;
      if (!selectedGroups.has(groupKey)) continue;
      const group = groups[i];
      const selected = keepSelections[groupKey] || [];
      if (selected.length < 2) continue;
      const keepId = selected[0];
      const mergeIds = selected.slice(1);
      try {
        const initial = await api.mergeSeriesGroup(keepId, mergeIds);
        setActiveOp(initial);
        await api.pollOperation(initial.id, (update) => setActiveOp(update));
      } catch (err) {
        setError(err instanceof Error ? err.message : `Failed to merge series "${group.name}"`);
      }
    }
    setActiveOp(null);
    setMergeSuccess(`Merged ${selectedGroups.size} selected group(s)`);
    fetchDuplicates();
  };

  const handleMergeAll = async () => {
    setConfirmOpen(false);
    setMergeSuccess(null);
    setError(null);
    await runOperationWithPolling(
      () => api.deduplicateSeries(),
      setActiveOp,
      (final) => {
        if (final.status === 'failed') {
          setError(final.error_message || 'Series dedup failed');
        } else {
          setMergeSuccess(final.message || 'Series deduplication complete');
          fetchDuplicates();
        }
      },
      setError,
    );
  };

  const toggleGroup = (key: string) => {
    setSelectedGroups((prev) => {
      const next = new Set(prev);
      if (next.has(key)) next.delete(key); else next.add(key);
      return next;
    });
  };

  const toggleAll = () => {
    if (selectedGroups.size === groups.length) {
      setSelectedGroups(new Set());
    } else {
      setSelectedGroups(new Set(groups.map((_, i) => `group-${i}`)));
    }
  };

  const handlePrunePreview = async () => {
    setPruneLoading(true);
    setError(null);
    try {
      const preview = await api.seriesPrunePreview();
      setPrunePreview(preview);
      if (preview.total_count > 0) {
        setPruneConfirmOpen(true);
      } else {
        setMergeSuccess('No series to prune - library is clean!');
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to get prune preview');
    } finally {
      setPruneLoading(false);
    }
  };

  const handlePruneExecute = async () => {
    setPruneConfirmOpen(false);
    setPrunePreview(null);
    setMergeSuccess(null);
    setError(null);
    await runOperationWithPolling(
      () => api.seriesPrune(),
      setActiveOp,
      (final) => {
        if (final.status === 'failed') {
          setError(final.error_message || 'Series prune failed');
        } else {
          setMergeSuccess(final.message || 'Series prune complete');
          fetchDuplicates();
        }
      },
      setError,
    );
  };

  const busy = activeOp !== null;

  return (
    <Box>
      <Box sx={{ display: 'flex', alignItems: 'center', mb: 2 }}>
        <Typography variant="body2" color="text.secondary" sx={{ flexGrow: 1 }}>
          Detects series with identical names (ignoring case). Often caused by reimports creating series with/without author links.
          Total series: {totalSeries}.
        </Typography>
        <Stack direction="row" spacing={1}>
          <Button variant="outlined" color="secondary" startIcon={<CleaningServicesIcon />}
            onClick={handlePrunePreview} disabled={busy || pruneLoading}>
            {pruneLoading ? 'Checking...' : 'Prune Series'}
          </Button>
          {groups.length > 0 && (
            <>
              <Button size="small" onClick={toggleAll} disabled={busy}>
                {selectedGroups.size === groups.length ? 'Deselect All' : 'Select All'}
              </Button>
              {selectedGroups.size > 0 && (
                <Button variant="contained" color="primary" startIcon={<MergeIcon />}
                  onClick={handleMergeSelected} disabled={busy}>
                  Merge Selected ({selectedGroups.size})
                </Button>
              )}
              <Button variant="contained" color="warning" startIcon={<MergeIcon />}
                onClick={() => setConfirmOpen(true)} disabled={busy}>
                {busy ? 'Merging...' : `Merge All (${groups.length} groups)`}
              </Button>
            </>
          )}
          <Tooltip title="Rescan"><IconButton onClick={fetchDuplicates} disabled={loading || busy}><RefreshIcon /></IconButton></Tooltip>
        </Stack>
      </Box>

      <OperationProgress operation={activeOp} />
      {error && <Alert severity="error" sx={{ mb: 2 }} onClose={() => setError(null)}>{error}</Alert>}
      {mergeSuccess && <Alert severity="success" sx={{ mb: 2 }} icon={<CheckCircleIcon />} onClose={() => setMergeSuccess(null)}>{mergeSuccess}</Alert>}

      {loading ? (
        <Box sx={{ display: 'flex', justifyContent: 'center', py: 4 }}><CircularProgress /></Box>
      ) : groups.length === 0 ? (
        <Paper sx={{ p: 4, textAlign: 'center' }}>
          <CheckCircleIcon sx={{ fontSize: 48, color: 'success.main', mb: 1 }} />
          <Typography variant="h6">No duplicate series found</Typography>
          <Typography variant="body2" color="text.secondary">{totalSeries} unique series in library.</Typography>
        </Paper>
      ) : (
        <>
        <PaginationControls total={groups.length} page={pagination.page} rowsPerPage={pagination.rowsPerPage}
          onPageChange={pagination.setPage} onRowsPerPageChange={pagination.setRowsPerPage} />
        <Stack spacing={2}>
          {groups.slice(pagination.startIdx, pagination.endIdx).map((group, sliceIdx) => {
            const idx = pagination.startIdx + sliceIdx;
            const groupKey = `group-${idx}`;
            return (
              <Card key={groupKey} variant="outlined">
                <Box sx={{ display: 'flex' }}>
                <CardContent sx={{ flex: '0 1 auto', minWidth: 280, maxWidth: '50%' }}>
                      <Box sx={{ display: 'flex', alignItems: 'center', gap: 1, mb: 1, flexWrap: 'wrap' }}>
                        <Checkbox
                          checked={selectedGroups.has(groupKey)}
                          onChange={() => toggleGroup(groupKey)}
                          disabled={busy}
                          size="small"
                        />
                        <Typography variant="subtitle1" fontWeight="bold">{cleanDisplayTitle(group.name)}</Typography>
                        <Chip label={`${group.count} entries`} size="small" color="warning" variant="outlined" />
                        {group.match_type === 'subseries' && (
                          <Chip label="sub-series" size="small" color="info" variant="outlined" />
                        )}
                        {group.suggested_name && (
                          <Chip
                            label={`Suggested: "${group.suggested_name}"`}
                            size="small"
                            color="primary"
                            variant="outlined"
                            onClick={() => {
                              const selected = keepSelections[groupKey] || [];
                              if (selected.length > 0) {
                                setEditingSeriesId(selected[0]);
                                setEditingName(group.suggested_name!);
                              }
                            }}
                            sx={{ cursor: 'pointer' }}
                          />
                        )}
                      </Box>
                      <Divider sx={{ my: 1 }} />
                      {group.series.map((s) => {
                        const selected = keepSelections[groupKey] || [];
                        const isChecked = selected.includes(s.id);
                        const authorLabel = s.author_name
                          ? `${s.author_name} (id: ${s.author_id})`
                          : s.author_id != null ? `author ${s.author_id}` : 'no author';
                        return (
                          <Box key={s.id} sx={{ display: 'flex', alignItems: 'center', gap: 1, py: 0.25 }}>
                            <Checkbox size="small" checked={isChecked}
                              onChange={() => setKeepSelections((prev) => {
                                const cur = prev[groupKey] || [];
                                return { ...prev, [groupKey]: isChecked ? cur.filter((id) => id !== s.id) : [...cur, s.id] };
                              })} />
                            {editingSeriesId === s.id ? (
                              <>
                                <TextField size="small" value={editingName}
                                  onChange={(e) => setEditingName(e.target.value)}
                                  onKeyDown={(e) => { if (e.key === 'Enter') handleSaveEdit(); if (e.key === 'Escape') { setEditingSeriesId(null); setEditingName(''); } }}
                                  autoFocus sx={{ minWidth: 300 }} />
                                <IconButton size="small" color="primary" onClick={handleSaveEdit}><SaveIcon fontSize="small" /></IconButton>
                                <IconButton size="small" onClick={() => { setEditingSeriesId(null); setEditingName(''); }}><CloseIcon fontSize="small" /></IconButton>
                              </>
                            ) : (
                              <>
                                <Typography variant="body2">
                                  ID {s.id}: &quot;{s.name}&quot;
                                </Typography>
                                <Tooltip title="Edit series name">
                                  <IconButton size="small" onClick={(e) => { e.stopPropagation(); setEditingSeriesId(s.id); setEditingName(s.name); }}>
                                    <EditIcon fontSize="small" />
                                  </IconButton>
                                </Tooltip>
                              </>
                            )}
                            <Chip label={authorLabel} size="small"
                              color={(narratorFlags[groupKey]?.has(s.author_id!) ? 'secondary' : 'success')}
                              variant="outlined" />
                            {s.author_id != null && (
                              <Chip
                                label={narratorFlags[groupKey]?.has(s.author_id) ? 'Narrator' : 'Author'}
                                size="small"
                                color={narratorFlags[groupKey]?.has(s.author_id) ? 'secondary' : 'default'}
                                variant={narratorFlags[groupKey]?.has(s.author_id) ? 'filled' : 'outlined'}
                                onClick={() => setNarratorFlags((prev) => {
                                  const cur = new Set(prev[groupKey] || []);
                                  if (cur.has(s.author_id!)) cur.delete(s.author_id!); else cur.add(s.author_id!);
                                  return { ...prev, [groupKey]: cur };
                                })}
                                sx={{ cursor: 'pointer' }}
                              />
                            )}
                          </Box>
                        );
                      })}
                    </CardContent>
                {/* Book covers: per series/author, books in a row, vertical divider between, dup badge if shared */}
                {(() => {
                  // Collect all book IDs across all series to detect duplicates
                  const bookIdCounts = new Map<string, number>();
                  group.series.forEach((s) => (s.books || []).forEach((b) => bookIdCounts.set(b.id, (bookIdCounts.get(b.id) || 0) + 1)));
                  return (
                    <Box sx={{ flex: 1, display: 'flex', borderLeft: '1px solid', borderColor: 'divider', overflowX: 'auto', alignItems: 'flex-start', py: 1 }}>
                      {group.series.map((s, sIdx) => {
                        const books = s.books || [];
                        if (books.length === 0) return null;
                        const authorLabel = s.author_name || (s.author_id != null ? `Author ${s.author_id}` : '');
                        return (
                          <Box key={`covers-${s.id}`} sx={{ display: 'flex' }}>
                            {sIdx > 0 && (
                              <Divider orientation="vertical" flexItem sx={{ mx: 1 }} />
                            )}
                            <Box sx={{ px: 1 }}>
                              <Typography variant="caption" color="text.secondary" sx={{ display: 'block', mb: 0.5, fontWeight: 'bold' }}>
                                {authorLabel}
                              </Typography>
                              <Box sx={{ display: 'flex', gap: 1.5, flexWrap: 'nowrap' }}>
                                {books.map((book) => {
                                  const src = book.cover_url
                                    ? (book.cover_url.startsWith('/') || book.cover_url.startsWith('http') ? book.cover_url : `/api/v1/covers/local/${book.cover_url}`)
                                    : '';
                                  const isDup = (bookIdCounts.get(book.id) || 0) > 1;
                                  return (
                                    <Box key={book.id} sx={{ flexShrink: 0, width: 130, textAlign: 'center' }}>
                                      <Box sx={[{
                                        width: 130,
                                        height: 195,
                                        borderRadius: 1,
                                        overflow: 'hidden',
                                        border: '1px solid',
                                        bgcolor: 'action.hover',
                                        position: 'relative'
                                      }, isDup ? {
                                        borderColor: 'warning.main'
                                      } : {
                                        borderColor: 'divider'
                                      }, src ? {
                                        cursor: 'pointer'
                                      } : {
                                        cursor: 'default'
                                      }]}
                                        onClick={() => { if (src) setFloatingCovers((prev) => prev.some((c) => c.src === src) ? prev.filter((c) => c.src !== src) : [...prev, { src, title: cleanDisplayTitle(book.title), author: authorLabel }]); }}>
                                        {isDup && (
                                          <Chip label="dup" size="small" color="warning" sx={{ position: 'absolute', top: 4, right: 4, zIndex: 1, height: 18, fontSize: '0.6rem' }} />
                                        )}
                                        {src ? (
                                          <img src={src} alt={book.title} style={{ width: '100%', height: '100%', objectFit: 'cover' }}
                                            onError={(e) => { (e.target as HTMLImageElement).style.display = 'none'; }} />
                                        ) : (
                                          <Box sx={{ width: '100%', height: '100%', display: 'flex', alignItems: 'center', justifyContent: 'center' }}>
                                            <MenuBookIcon color="disabled" />
                                          </Box>
                                        )}
                                      </Box>
                                      <Typography variant="caption" sx={{ display: 'block', mt: 0.5, fontSize: '0.65rem', lineHeight: 1.2, whiteSpace: 'normal', wordBreak: 'break-word' }}>
                                        {cleanDisplayTitle(book.title)}
                                      </Typography>
                                      <Typography variant="caption" color="text.secondary" sx={{ display: 'block', fontSize: '0.6rem', lineHeight: 1.1 }} noWrap>
                                        {authorLabel}
                                      </Typography>
                                    </Box>
                                  );
                                })}
                              </Box>
                            </Box>
                          </Box>
                        );
                      })}
                    </Box>
                  );
                })()}
                </Box>
                <CardActions>
                  <Button startIcon={<MergeIcon />} variant="contained" size="small"
                    onClick={() => handleMerge(group, groupKey)} disabled={busy}>
                    Merge
                  </Button>
                  <Button startIcon={validatingKey === groupKey ? <CircularProgress size={16} /> : <SearchIcon />}
                    variant="outlined" size="small"
                    onClick={() => handleValidate(groupKey, group.name)}
                    disabled={validatingKey === groupKey}>
                    Validate
                  </Button>
                </CardActions>
                <Collapse in={expandedValidation.has(groupKey)}>
                  <Box sx={{ px: 2, pb: 2 }}>
                    {validationResults[groupKey]?.length ? (
                      <>
                        <Typography variant="caption" color="text.secondary" gutterBottom>
                          Found {validationResults[groupKey].length} result(s) from metadata sources:
                        </Typography>
                        <Stack spacing={0.5} sx={{ mt: 0.5 }}>
                          {validationResults[groupKey].map((r, i) => (
                            <Box key={i} sx={{ display: 'flex', alignItems: 'center', gap: 1, p: 0.5, borderRadius: 1, bgcolor: 'action.hover' }}>
                              {r.cover_url && (
                                <img src={r.cover_url.startsWith('http') ? `/api/v1/covers/proxy?url=${encodeURIComponent(r.cover_url)}` : r.cover_url}
                                  alt="" style={{ width: 32, height: 44, objectFit: 'cover', borderRadius: 2 }}
                                  onError={(e) => { (e.target as HTMLImageElement).style.display = 'none'; }} />
                              )}
                              <Box sx={{ flex: 1, minWidth: 0 }}>
                                <Typography variant="body2" noWrap>{r.title}</Typography>
                                <Typography variant="caption" color="text.secondary" noWrap>
                                  {r.author}{r.series ? ` — Series: ${r.series}${r.series_position ? ` #${r.series_position}` : ''}` : ''}
                                </Typography>
                              </Box>
                              <Chip label={r.source} size="small" variant="outlined" />
                            </Box>
                          ))}
                        </Stack>
                      </>
                    ) : validationResults[groupKey] ? (
                      <Typography variant="caption" color="text.secondary">No results found from metadata sources.</Typography>
                    ) : null}
                  </Box>
                </Collapse>
              </Card>
            );
          })}
        </Stack>
        <PaginationControls total={groups.length} page={pagination.page} rowsPerPage={pagination.rowsPerPage}
          onPageChange={pagination.setPage} onRowsPerPageChange={pagination.setRowsPerPage} />
        </>
      )}

      <Dialog open={confirmOpen} onClose={() => setConfirmOpen(false)}>
        <DialogTitle>Confirm Merge All</DialogTitle>
        <DialogContent>
          <DialogContentText>
            This will merge {groups.length} groups. This action cannot be undone. Are you sure?
          </DialogContentText>
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setConfirmOpen(false)}>Cancel</Button>
          <Button onClick={handleMergeAll} color="warning" variant="contained">Confirm</Button>
        </DialogActions>
      </Dialog>

      <Dialog open={pruneConfirmOpen} onClose={() => setPruneConfirmOpen(false)} maxWidth="sm" fullWidth>
        <DialogTitle>Prune Series</DialogTitle>
        <DialogContent>
          <DialogContentText>
            {prunePreview && (
              <>
                This will clean up {prunePreview.total_count} series entries:
                <br />
                - {prunePreview.duplicate_count} duplicate series will be merged (books reassigned to canonical entry)
                <br />
                - {prunePreview.orphan_count} orphan series (0 books) will be deleted
                <br /><br />
                This action cannot be undone.
              </>
            )}
          </DialogContentText>
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setPruneConfirmOpen(false)}>Cancel</Button>
          <Button onClick={handlePruneExecute} color="secondary" variant="contained" startIcon={<CleaningServicesIcon />}>
            Prune {prunePreview?.total_count} Series
          </Button>
        </DialogActions>
      </Dialog>

      {/* Floating cover bubble — fixed to right side, resizable */}
      {floatingCovers.length > 0 && (
        <Paper elevation={8} sx={{ position: 'fixed', top: 80, right: 16, zIndex: 1300, p: 1.5, maxWidth: '60vw', maxHeight: '85vh', overflowY: 'auto', borderRadius: 2 }}>
          <Box sx={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', mb: 1, gap: 2 }}>
            <Typography variant="caption" color="text.secondary">{floatingCovers.length} cover(s) — click to dismiss</Typography>
            <Box sx={{ display: 'flex', alignItems: 'center', gap: 1 }}>
              <Typography variant="caption" color="text.secondary">Size:</Typography>
              <input type="range" min={150} max={800} step={25} value={bubbleSize}
                onChange={(e) => setBubbleSize(Number(e.target.value))}
                style={{ width: 100, accentColor: '#90caf9' }} />
              <Typography variant="caption" color="text.secondary">{bubbleSize}px</Typography>
              <IconButton size="small" onClick={() => setFloatingCovers([])}><CloseIcon fontSize="small" /></IconButton>
            </Box>
          </Box>
          <Box sx={{ display: 'flex', gap: 2, flexWrap: 'wrap' }}>
            {floatingCovers.map((cover, ci) => (
              <Box key={ci} sx={{ textAlign: 'center', cursor: 'pointer', width: bubbleSize }}
                onClick={() => setFloatingCovers((prev) => prev.filter((_, j) => j !== ci))}>
                <img src={cover.src} alt={cover.title} style={{ width: bubbleSize, borderRadius: 4, display: 'block' }}
                  onError={(e) => { (e.target as HTMLImageElement).style.display = 'none'; }} />
                <Typography variant="caption" sx={{ display: 'block', mt: 0.5, fontSize: '0.75rem' }}>{cover.title}</Typography>
                <Typography variant="caption" color="text.secondary" sx={{ display: 'block', fontSize: '0.65rem' }}>{cover.author}</Typography>
              </Box>
            ))}
          </Box>
        </Paper>
      )}

    </Box>
  );
}
