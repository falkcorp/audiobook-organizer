// file: web/src/components/dedup/DedupAuthorTab.tsx
// version: 1.1.0
// guid: b2c3d4e5-f6a7-8901-bcde-f12345678901
// last-edited: 2026-08-10
import { useState, useEffect, useCallback } from 'react';
import { useNavigate } from 'react-router-dom';
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
  Popover,
} from '@mui/material';
import MergeIcon from '@mui/icons-material/MergeType';
import RefreshIcon from '@mui/icons-material/Refresh';
import CheckCircleIcon from '@mui/icons-material/CheckCircle';
import MenuBookIcon from '@mui/icons-material/MenuBook';
import PersonIcon from '@mui/icons-material/Person';
import EditIcon from '@mui/icons-material/Edit';
import SaveIcon from '@mui/icons-material/Save';
import CloseIcon from '@mui/icons-material/Close';
import MicIcon from '@mui/icons-material/Mic';
import BusinessIcon from '@mui/icons-material/Business';
import SearchIcon from '@mui/icons-material/Search';
import * as api from '../../services/api';
import type { Book, AuthorDedupGroup, Operation, SuggestionRoles } from '../../services/api';
import {
  cleanDisplayTitle,
  OperationProgress,
  runOperationWithPolling,
  usePagination,
  PaginationControls,
} from './dedupHelpers';

/** Structured role display for AI suggestions with role decomposition */
export function RoleDetails({ roles }: { roles: SuggestionRoles }) {
  return (
    <Box sx={{ ml: 5, mt: 0.5 }}>
      {roles.author && (
        <Box sx={{ display: 'flex', alignItems: 'flex-start', gap: 0.5, mb: 0.5 }}>
          <PersonIcon sx={{ fontSize: 16, mt: 0.3, color: 'primary.main' }} />
          <Box>
            <Typography variant="body2" component="span" sx={{ fontWeight: 500 }}>
              Author: {roles.author.name}
            </Typography>
            {roles.author.variants && roles.author.variants.length > 0 && (
              <Typography variant="caption" display="block" color="text.secondary">
                Variants: {roles.author.variants.join(', ')}
              </Typography>
            )}
            {roles.author.reason && (
              <Typography variant="caption" display="block" sx={{ fontStyle: 'italic', color: 'text.secondary' }}>
                &ldquo;{roles.author.reason}&rdquo;
              </Typography>
            )}
          </Box>
        </Box>
      )}
      {roles.narrator && (
        <Box sx={{ display: 'flex', alignItems: 'flex-start', gap: 0.5, mb: 0.5 }}>
          <MicIcon sx={{ fontSize: 16, mt: 0.3, color: 'secondary.main' }} />
          <Box>
            <Typography variant="body2" component="span" sx={{ fontWeight: 500 }}>
              Narrator: {roles.narrator.name}
            </Typography>
            {roles.narrator.reason && (
              <Typography variant="caption" display="block" sx={{ fontStyle: 'italic', color: 'text.secondary' }}>
                &ldquo;{roles.narrator.reason}&rdquo;
              </Typography>
            )}
          </Box>
        </Box>
      )}
      {roles.publisher && (
        <Box sx={{ display: 'flex', alignItems: 'flex-start', gap: 0.5, mb: 0.5 }}>
          <BusinessIcon sx={{ fontSize: 16, mt: 0.3, color: 'warning.main' }} />
          <Box>
            <Typography variant="body2" component="span" sx={{ fontWeight: 500 }}>
              Publisher: {roles.publisher.name}
            </Typography>
            {roles.publisher.reason && (
              <Typography variant="caption" display="block" sx={{ fontStyle: 'italic', color: 'text.secondary' }}>
                &ldquo;{roles.publisher.reason}&rdquo;
              </Typography>
            )}
          </Box>
        </Box>
      )}
    </Box>
  );
}

/** Popover showing books for a set of author IDs */
function AuthorBooksPopover({
  anchorEl,
  onClose,
  authorIds,
}: {
  anchorEl: HTMLElement | null;
  onClose: () => void;
  authorIds: number[];
}) {
  const [books, setBooks] = useState<Book[]>([]);
  const [loading, setLoading] = useState(false);
  const navigate = useNavigate();

  useEffect(() => {
    if (!anchorEl || authorIds.length === 0) return;
    let cancelled = false;
    setLoading(true);
    Promise.all(authorIds.map((id) => api.getBooksByAuthor(id)))
      .then((results) => {
        if (cancelled) return;
        // Deduplicate by book id
        const seen = new Set<string>();
        const all: Book[] = [];
        for (const list of results) {
          for (const b of list) {
            if (!seen.has(b.id)) {
              seen.add(b.id);
              all.push(b);
            }
          }
        }
        setBooks(all);
      })
      .finally(() => { if (!cancelled) setLoading(false); });
    return () => { cancelled = true; };
  }, [anchorEl, authorIds]);

  return (
    <Popover
      open={Boolean(anchorEl)}
      anchorEl={anchorEl}
      onClose={onClose}
      anchorOrigin={{ vertical: 'bottom', horizontal: 'left' }}
      transformOrigin={{ vertical: 'top', horizontal: 'left' }}
      slotProps={{ paper: { sx: { maxWidth: 480, maxHeight: 400, overflow: 'auto', p: 1 } } }}
    >
      {loading ? (
        <Box sx={{ p: 2, textAlign: 'center' }}><CircularProgress size={24} /></Box>
      ) : books.length === 0 ? (
        <Typography sx={{ p: 2 }} variant="body2" color="text.secondary">No books found</Typography>
      ) : (
        <Stack spacing={0.5}>
          {books.map((book) => (
            <Box
              key={book.id}
              sx={{ display: 'flex', alignItems: 'center', gap: 1, p: 0.5, cursor: 'pointer',
                borderRadius: 1, '&:hover': { bgcolor: 'action.hover' } }}
              onClick={() => { onClose(); navigate(`/library/${book.id}`); }}
            >
              {book.cover_url ? (
                <Box component="img" src={book.cover_url} alt="" sx={{ width: 40, height: 56, objectFit: 'cover', borderRadius: 0.5, flexShrink: 0 }} />
              ) : (
                <Box sx={{ width: 40, height: 56, display: 'flex', alignItems: 'center', justifyContent: 'center',
                  bgcolor: 'action.selected', borderRadius: 0.5, flexShrink: 0 }}>
                  <MenuBookIcon fontSize="small" color="disabled" />
                </Box>
              )}
              <Box sx={{ overflow: 'hidden' }}>
                <Typography variant="body2" noWrap fontWeight="medium">{cleanDisplayTitle(book.title)}</Typography>
                {book.author_name && <Typography variant="caption" color="text.secondary" noWrap>{book.author_name}</Typography>}
              </Box>
            </Box>
          ))}
        </Stack>
      )}
    </Popover>
  );
}

function normalizeGroups(groups: AuthorDedupGroup[]): AuthorDedupGroup[] {
  return (groups || []).map((g) => ({ ...g, variants: g.variants || [] }));
}

export function AuthorDedupTab() {
  const [groups, setGroups] = useState<AuthorDedupGroup[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [activeOp, setActiveOp] = useState<Operation | null>(null);
  const [mergeSuccess, setMergeSuccess] = useState<string | null>(null);
  const [selectedGroups, setSelectedGroups] = useState<Set<string>>(new Set());
  const [confirmOpen, setConfirmOpen] = useState(false);
  const [editingCanonicalId, setEditingCanonicalId] = useState<number | null>(null);
  const [editingCanonicalName, setEditingCanonicalName] = useState('');
  const [narratorFlags, setNarratorFlags] = useState<Set<string>>(new Set()); // "authorId" or "authorId:splitName" keys
  const [removedVariants, setRemovedVariants] = useState<Set<string>>(new Set()); // "canonicalId:variantId" keys
  const [validatingAuthor, setValidatingAuthor] = useState<string | null>(null); // authorId being validated
  const [authorValidation, setAuthorValidation] = useState<Record<string, { results: { source: string; title: string; author: string }[]; query: string }>>({});
  const [popoverAnchor, setPopoverAnchor] = useState<HTMLElement | null>(null);
  const [popoverAuthorIds, setPopoverAuthorIds] = useState<number[]>([]);
  const [resolvingAuthor, setResolvingAuthor] = useState<number | null>(null);
  const pagination = usePagination(groups.length);

  const fetchDuplicates = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const result = await api.getAuthorDuplicates();
      if (result.needs_refresh) {
        // Cache is cold — trigger async scan with progress
        await runOperationWithPolling(
          () => api.refreshAuthorDuplicates(),
          setActiveOp,
          async () => {
            const fresh = await api.getAuthorDuplicates();
            setGroups(normalizeGroups(fresh.groups));
            setSelectedGroups(new Set());
            setLoading(false);
          },
          (msg) => { setError(msg); setLoading(false); },
        );
        return;
      }
      setGroups(normalizeGroups(result.groups));
      setSelectedGroups(new Set());
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to fetch duplicates');
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => { fetchDuplicates(); }, [fetchDuplicates]);

  const handleSaveCanonicalName = async (group: AuthorDedupGroup) => {
    if (!editingCanonicalName.trim()) return;
    try {
      await api.renameAuthor(group.canonical.id, editingCanonicalName.trim());
      setGroups((prev) => prev.map((g) =>
        g.canonical.id === group.canonical.id
          ? { ...g, canonical: { ...g.canonical, name: editingCanonicalName.trim() } }
          : g
      ));
      setEditingCanonicalId(null);
      setEditingCanonicalName('');
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to rename author');
    }
  };

  const handleSplitAuthor = async (group: AuthorDedupGroup) => {
    try {
      // Collect which split names are narrators
      const narratorNames = (group.split_names || []).filter((n) => narratorFlags.has(`${group.canonical.id}:${n}`));
      const result = await api.splitCompositeAuthor(group.canonical.id);
      // After split, reclassify any flagged narrators
      for (const na of narratorNames) {
        const match = result.authors.find((a) => a.name === na);
        if (match) {
          try { await api.reclassifyAuthorAsNarrator(match.id); } catch { /* best effort */ }
        }
      }
      setMergeSuccess(`Split "${group.canonical.name}" into ${result.authors.length} authors`);
      fetchDuplicates();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to split author');
    }
  };

  const handleValidateAuthor = async (authorName: string, authorId: string) => {
    setValidatingAuthor(authorId);
    try {
      const resp = await api.validateDedupEntry(authorName, 'author');
      setAuthorValidation((prev) => ({ ...prev, [authorId]: { results: resp.results, query: resp.query } }));
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Validation failed');
    } finally {
      setValidatingAuthor(null);
    }
  };

  const handleMerge = async (group: AuthorDedupGroup) => {
    setMergeSuccess(null);
    // Filter out removed variants and reclassify narrator-flagged ones first
    const activeVariants = group.variants.filter((v) => !removedVariants.has(`${group.canonical.id}:${v.id}`));
    const narratorVariantIds = activeVariants.filter((v) => narratorFlags.has(String(v.id))).map((v) => v.id);
    const mergeVariantIds = activeVariants.filter((v) => !narratorFlags.has(String(v.id))).map((v) => v.id);

    // Reclassify narrator-flagged variants first
    for (const nId of narratorVariantIds) {
      try { await api.reclassifyAuthorAsNarrator(nId); } catch { /* best effort */ }
    }
    if (mergeVariantIds.length === 0) {
      setMergeSuccess(`Reclassified ${narratorVariantIds.length} variant(s) as narrator`);
      fetchDuplicates();
      return;
    }

    await runOperationWithPolling(
      () => api.mergeAuthors(group.canonical.id, mergeVariantIds),
      setActiveOp,
      (final) => {
        if (final.status === 'failed') {
          setError(final.error_message || 'Merge failed');
        } else {
          setMergeSuccess(`Merged author(s) into "${group.canonical.name}"`);
          setGroups((prev) => prev.filter((g) => g.canonical.id !== group.canonical.id));
          setSelectedGroups((prev) => {
            const next = new Set(prev);
            next.delete(String(group.canonical.id));
            return next;
          });
        }
      },
      setError,
    );
  };

  const handleMergeSelected = async () => {
    setMergeSuccess(null);
    for (const group of groups) {
      const key = String(group.canonical.id);
      if (!selectedGroups.has(key)) continue;
      try {
        const initial = await api.mergeAuthors(group.canonical.id, group.variants.map((v) => v.id));
        setActiveOp(initial);
        await api.pollOperation(initial.id, (update) => setActiveOp(update));
      } catch (err) {
        setError(err instanceof Error ? err.message : `Failed to merge "${group.canonical.name}"`);
      }
    }
    setActiveOp(null);
    setMergeSuccess(`Merged ${selectedGroups.size} selected group(s)`);
    fetchDuplicates();
  };

  const handleMergeAll = async () => {
    setConfirmOpen(false);
    setMergeSuccess(null);
    for (const group of groups) {
      try {
        const initial = await api.mergeAuthors(group.canonical.id, group.variants.map((v) => v.id));
        setActiveOp(initial);
        await api.pollOperation(initial.id, (update) => setActiveOp(update));
      } catch (err) {
        setError(err instanceof Error ? err.message : `Failed to merge "${group.canonical.name}"`);
      }
    }
    setActiveOp(null);
    setMergeSuccess('Merged all duplicate authors');
    fetchDuplicates();
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
      setSelectedGroups(new Set(groups.map((g) => String(g.canonical.id))));
    }
  };

  const busy = activeOp !== null;

  return (
    <Box>
      <OperationProgress operation={activeOp} />
      {error && <Alert severity="error" sx={{ mb: 2 }} onClose={() => setError(null)}>{error}</Alert>}
      {mergeSuccess && <Alert severity="success" sx={{ mb: 2 }} icon={<CheckCircleIcon />} onClose={() => setMergeSuccess(null)}>{mergeSuccess}</Alert>}

      <Box sx={{ display: 'flex', alignItems: 'center', mb: 2 }}>
        <Typography variant="body2" color="text.secondary" sx={{ flexGrow: 1 }}>
          Uses structured name comparison to detect author name variants like &quot;James S. A. Corey&quot; vs &quot;James S.A. Corey&quot;.
        </Typography>
        <Stack direction="row" spacing={1}>
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
                Merge All ({groups.length})
              </Button>
            </>
          )}
          <Tooltip title="Refresh"><IconButton onClick={fetchDuplicates} disabled={loading || busy}><RefreshIcon /></IconButton></Tooltip>
        </Stack>
      </Box>

      {loading ? (
        <Box sx={{ display: 'flex', justifyContent: 'center', py: 4 }}><CircularProgress /></Box>
      ) : groups.length === 0 ? (
        <Paper sx={{ p: 4, textAlign: 'center' }}>
          <CheckCircleIcon sx={{ fontSize: 48, color: 'success.main', mb: 1 }} />
          <Typography variant="h6">No duplicate authors found</Typography>
        </Paper>
      ) : (
        <>
        <PaginationControls total={groups.length} page={pagination.page} rowsPerPage={pagination.rowsPerPage}
          onPageChange={pagination.setPage} onRowsPerPageChange={pagination.setRowsPerPage} />
        <Stack spacing={2}>
          {groups.slice(pagination.startIdx, pagination.endIdx).map((group) => {
            const key = String(group.canonical.id);
            return (
              <Card key={key} variant="outlined">
                <CardContent>
                  <Box sx={{ display: 'flex', alignItems: 'center', gap: 1, mb: 1 }}>
                    <Checkbox
                      checked={selectedGroups.has(key)}
                      onChange={() => toggleGroup(key)}
                      disabled={busy}
                      size="small"
                    />
                    {editingCanonicalId === group.canonical.id ? (
                      <>
                        <TextField size="small" value={editingCanonicalName}
                          onChange={(e) => setEditingCanonicalName(e.target.value)}
                          onKeyDown={(e) => { if (e.key === 'Enter') handleSaveCanonicalName(group); if (e.key === 'Escape') { setEditingCanonicalId(null); setEditingCanonicalName(''); } }}
                          autoFocus sx={{ minWidth: 300 }} />
                        <IconButton size="small" color="primary" onClick={() => handleSaveCanonicalName(group)}><SaveIcon fontSize="small" /></IconButton>
                        <IconButton size="small" onClick={() => { setEditingCanonicalId(null); setEditingCanonicalName(''); }}><CloseIcon fontSize="small" /></IconButton>
                      </>
                    ) : (
                      <>
                        <Typography variant="subtitle1" fontWeight="bold">{cleanDisplayTitle(group.canonical.name)}</Typography>
                        <Tooltip title="Edit canonical name">
                          <IconButton size="small" onClick={() => { setEditingCanonicalId(group.canonical.id); setEditingCanonicalName(group.canonical.name); }}>
                            <EditIcon fontSize="small" />
                          </IconButton>
                        </Tooltip>
                        {group.suggested_name && group.suggested_name !== group.canonical.name && (
                          <Tooltip title={`Use suggested: "${group.suggested_name}"`}>
                            <Chip label={group.suggested_name} size="small" color="info" variant="outlined"
                              onClick={() => { setEditingCanonicalId(group.canonical.id); setEditingCanonicalName(group.suggested_name!); }}
                              sx={{ cursor: 'pointer' }} />
                          </Tooltip>
                        )}
                      </>
                    )}
                    <Chip icon={<MenuBookIcon />} label={`${group.book_count} book(s)`} size="small" variant="outlined"
                      onClick={(e) => {
                        const ids = [group.canonical.id, ...group.variants.map((v) => v.id)];
                        setPopoverAuthorIds(ids);
                        setPopoverAnchor(e.currentTarget);
                      }}
                      sx={{ cursor: 'pointer' }} />
                    {group.is_production_company && (
                      <Chip label="Production Company" size="small" color="warning" />
                    )}
                  </Box>
                  {group.split_names && group.split_names.length > 1 ? (
                    <>
                      <Divider sx={{ my: 1 }} />
                      <Typography variant="body2" color="text.secondary" sx={{ mb: 1 }}>
                        Composite author — will split into:
                      </Typography>
                      <Box sx={{ display: 'flex', flexWrap: 'wrap', gap: 1 }}>
                        {group.split_names.map((name) => {
                          const flagKey = `${group.canonical.id}:${name}`;
                          const isNarrator = narratorFlags.has(flagKey);
                          return (
                            <Box key={name} sx={{ display: 'flex', gap: 0.5, alignItems: 'center' }}>
                              <Chip label={name} color="warning" variant="outlined" size="small" />
                              <Chip
                                label={isNarrator ? 'Narrator' : 'Author'}
                                size="small"
                                color={isNarrator ? 'secondary' : 'default'}
                                variant={isNarrator ? 'filled' : 'outlined'}
                                onClick={() => setNarratorFlags((prev) => {
                                  const next = new Set(prev);
                                  if (next.has(flagKey)) next.delete(flagKey); else next.add(flagKey);
                                  return next;
                                })}
                                sx={{ cursor: 'pointer' }}
                              />
                            </Box>
                          );
                        })}
                      </Box>
                    </>
                  ) : group.variants.length > 0 ? (
                    <>
                      <Divider sx={{ my: 1 }} />
                      <Box sx={{ display: 'flex', flexWrap: 'wrap', gap: 1, alignItems: 'center', mb: 1 }}>
                        <Typography variant="body2" color="text.secondary">Merge target:</Typography>
                        <Chip label={group.canonical.name} color="primary" size="small" variant="outlined" />
                        <Typography variant="body2" color="text.secondary" sx={{ mx: 0.5 }}>←</Typography>
                        <Typography variant="body2" color="text.secondary">
                          {group.variants.filter((v) => !removedVariants.has(`${group.canonical.id}:${v.id}`)).length} variant(s) will be merged into it:
                        </Typography>
                      </Box>
                      <Box sx={{ display: 'flex', flexWrap: 'wrap', gap: 1 }}>
                        {group.variants.map((v) => {
                          const removeKey = `${group.canonical.id}:${v.id}`;
                          if (removedVariants.has(removeKey)) return null;
                          const isNarrator = narratorFlags.has(String(v.id));
                          const isSameAsCanonical = v.name === group.canonical.name;
                          return (
                            <Box key={v.id} sx={{ display: 'flex', gap: 0.5, alignItems: 'center' }}>
                              <Tooltip title={isSameAsCanonical ? `"${v.name}" is the current canonical name (ID ${v.id} will be merged)` : `Click to use "${v.name}" as the merge target (canonical spelling)`}>
                                <Chip label={v.name} color={isSameAsCanonical ? 'default' : 'warning'} variant="outlined" size="small"
                                  onClick={isSameAsCanonical ? undefined : async () => {
                                    try {
                                      await api.renameAuthor(group.canonical.id, v.name);
                                      setGroups((prev) => prev.map((g) =>
                                        g.canonical.id === group.canonical.id
                                          ? { ...g, canonical: { ...g.canonical, name: v.name } }
                                          : g
                                      ));
                                    } catch (err) {
                                      setError(err instanceof Error ? err.message : 'Failed to rename author');
                                    }
                                  }}
                                  sx={[isSameAsCanonical ? {
                                    cursor: 'default'
                                  } : {
                                    cursor: 'pointer'
                                  }]} />
                              </Tooltip>
                              <Chip
                                label={isNarrator ? 'Narrator' : 'Author'}
                                size="small"
                                color={isNarrator ? 'secondary' : 'default'}
                                variant={isNarrator ? 'filled' : 'outlined'}
                                onClick={() => setNarratorFlags((prev) => {
                                  const next = new Set(prev);
                                  const k = String(v.id);
                                  if (next.has(k)) next.delete(k); else next.add(k);
                                  return next;
                                })}
                                sx={{ cursor: 'pointer', minWidth: 60 }}
                              />
                              <Tooltip title={`Remove "${v.name}" from this merge`}>
                                <IconButton size="small" onClick={() => setRemovedVariants((prev) => new Set(prev).add(removeKey))}
                                  sx={{ p: 0.25 }}>
                                  <CloseIcon fontSize="small" />
                                </IconButton>
                              </Tooltip>
                            </Box>
                          );
                        })}
                      </Box>
                      {/* Validate button */}
                      <Box sx={{ mt: 1 }}>
                        <Button size="small" variant="text"
                          disabled={validatingAuthor === key}
                          onClick={() => handleValidateAuthor(group.canonical.name, key)}>
                          {validatingAuthor === key ? 'Searching...' : 'Search external sources'}
                        </Button>
                        {authorValidation[key] && (
                          <Box sx={{ mt: 1 }}>
                            {authorValidation[key].results.length === 0 ? (
                              <Typography variant="caption" color="text.secondary">No external matches found</Typography>
                            ) : authorValidation[key].results.map((r, i) => (
                              <Chip key={i} label={`${r.source}: ${r.author || r.title}`} size="small" variant="outlined" sx={{ mr: 0.5, mb: 0.5 }} />
                            ))}
                          </Box>
                        )}
                      </Box>
                    </>
                  ) : null}
                </CardContent>
                <CardActions>
                  {group.is_production_company ? (
                    <Button startIcon={<SearchIcon />} variant="contained" size="small" color="warning"
                      disabled={busy || resolvingAuthor === group.canonical.id}
                      onClick={async () => {
                        try {
                          setResolvingAuthor(group.canonical.id);
                          const op = await api.resolveProductionAuthor(group.canonical.id);
                          await runOperationWithPolling(
                            () => Promise.resolve(op),
                            setActiveOp,
                            () => { fetchDuplicates(); setResolvingAuthor(null); },
                            (msg) => { setError(msg); setResolvingAuthor(null); },
                          );
                        } catch (err) {
                          setError(err instanceof Error ? err.message : 'Failed to resolve');
                          setResolvingAuthor(null);
                        }
                      }}>
                      {resolvingAuthor === group.canonical.id ? 'Resolving...' : 'Find Real Author'}
                    </Button>
                  ) : group.split_names && group.split_names.length > 1 ? (
                    <Button startIcon={<MergeIcon />} variant="contained" size="small" color="warning"
                      onClick={() => handleSplitAuthor(group)} disabled={busy}>
                      Split into {group.split_names.length} authors
                    </Button>
                  ) : (
                    <Button startIcon={<MergeIcon />} variant="contained" size="small"
                      onClick={() => handleMerge(group)} disabled={busy}>
                      {`Merge into "${group.canonical.name}"`}
                    </Button>
                  )}
                </CardActions>
              </Card>
            );
          })}
        </Stack>
        <PaginationControls total={groups.length} page={pagination.page} rowsPerPage={pagination.rowsPerPage}
          onPageChange={pagination.setPage} onRowsPerPageChange={pagination.setRowsPerPage} />
        </>
      )}
      <AuthorBooksPopover
        anchorEl={popoverAnchor}
        onClose={() => { setPopoverAnchor(null); setPopoverAuthorIds([]); }}
        authorIds={popoverAuthorIds}
      />
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
    </Box>
  );
}