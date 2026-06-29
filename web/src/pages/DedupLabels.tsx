// file: web/src/pages/DedupLabels.tsx
// version: 1.3.0
// guid: 7e3a1c92-4b60-4d85-9f21-6a5e0c9d3f58
// last-edited: 2026-06-28

// DedupLabels — the C6 gold-dataset review page for the dedup feedback loop.
// Lists labeled dedup examples (the dedup:label: keyspace), filterable by label
// / label_source / band, with one-click human override. This is where the user
// reviews the gold dataset that the classifier will train and validate on.

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  Box, Typography, Paper, Table, TableBody, TableCell, TableContainer, TableHead,
  TableRow, Chip, Select, MenuItem, FormControl, InputLabel, Button, Stack,
  CircularProgress, Alert, Tooltip, Link as MuiLink, TextField,
} from '@mui/material';
import { useNavigate } from 'react-router-dom';
import { LabelToggle } from '../components/dedup/LabelToggle';
import { PathVarsLegend } from '../components/common/PathVarsLegend';
import {
  ColumnPicker,
  ResizableHeaderCell,
  type ColumnDef,
  useConfigurableTable,
} from '../components/common/ConfigurableTable';
import { formatPath, usePathVars, type PathVar } from '../utils/formatPath';
import { apiFetch } from '../utils/apiFetch';

const API_BASE = '/api/v1';

interface BookFeatures {
  title?: string;
  primary_path?: string;
  total_duration_sec?: number;
}

interface LabeledExample {
  candidate_id: number;
  entity_a_id: string;
  entity_b_id: string;
  layer: string;
  band?: string;
  label: string;
  label_source: string;
  label_reason?: string;
  decided_at?: string;
  a?: BookFeatures;
  b?: BookFeatures;
}

interface LabelStats {
  total: number;
  by_label: Record<string, number>;
  by_source: Record<string, number>;
}

const labelColor = (l: string): 'success' | 'error' | 'warning' | 'default' =>
  l === 'true_dup' ? 'success' : l === 'not_dup' ? 'error' : l === 'unsure' ? 'warning' : 'default';

const sourceColor = (s: string): 'primary' | 'secondary' | 'info' | 'default' =>
  s === 'human' ? 'primary' : s === 'auto_high_conf' ? 'info' : s === 'rule' ? 'secondary' : 'default';

const PAGE = 50;

// BookCell renders one side of a labeled pair: a clickable title that opens the
// book, plus its abbreviated path (full path on hover).
function BookCell({
  bookId,
  title,
  path,
  pathVars,
  onOpen,
}: {
  bookId: string;
  title?: string;
  path?: string;
  pathVars: PathVar[];
  onOpen: (id: string) => void;
}) {
  return (
    <Box sx={{ minWidth: 0, maxWidth: 360 }}>
      <MuiLink
        component="button"
        type="button"
        underline="hover"
        onClick={() => onOpen(bookId)}
        sx={{ fontWeight: 600, textAlign: 'left', display: 'block', cursor: 'pointer' }}
        title={`Open "${title || bookId}"`}
      >
        {title || bookId}
      </MuiLink>
      {path && (
        <Tooltip title={path} placement="bottom-start" componentsProps={{ tooltip: { sx: { maxWidth: 600 } } }}>
          <Typography
            variant="caption"
            color="text.secondary"
            sx={{ fontFamily: 'monospace', fontSize: '0.65rem', display: 'block' }}
            noWrap
          >
            {formatPath(path, pathVars)}
          </Typography>
        </Tooltip>
      )}
    </Box>
  );
}

export default function DedupLabels() {
  const navigate = useNavigate();
  const pathVars = usePathVars();
  const [rows, setRows] = useState<LabeledExample[]>([]);
  const [total, setTotal] = useState(0);
  const [stats, setStats] = useState<LabelStats | null>(null);
  const [labelFilter, setLabelFilter] = useState('');
  const [sourceFilter, setSourceFilter] = useState('');
  const [bandFilter, setBandFilter] = useState('');
  const [localBandFilter, setLocalBandFilter] = useState('');
  const bandDebounceRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const [offset, setOffset] = useState(0);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const openBook = useCallback((bookId: string) => navigate(`/library/${bookId}`), [navigate]);

  const loadStats = useCallback(async () => {
    try {
      const r = await apiFetch(`${API_BASE}/dedup/labels/stats`);
      if (r.ok) setStats((await r.json()).data);
    } catch {
      /* stats are best-effort */
    }
  }, []);

  const override = useCallback(async (candidateId: number, label: string) => {
    try {
      const r = await apiFetch(`${API_BASE}/dedup/labels/${candidateId}/override`, {
        method: 'POST',
        body: JSON.stringify({ label, reason: 'ui_override' }),
      });
      if (!r.ok) throw new Error(`HTTP ${r.status}`);
      const d = (await r.json()).data;
      setRows((current) => current.map((row) => (
        row.candidate_id === candidateId
          ? { ...row, label: d.label || label, label_source: d.label_source || 'human', label_reason: 'ui_override' }
          : row
      )));
      await loadStats();
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Override failed');
    }
  }, [loadStats]);

  const columns = useMemo<ColumnDef<LabeledExample>[]>(() => [
    {
      key: 'book_a',
      label: 'Book A',
      defaultWidth: 360,
      sortable: false,
      render: (r) => (
        <BookCell
          bookId={r.entity_a_id}
          title={r.a?.title}
          path={r.a?.primary_path}
          pathVars={pathVars}
          onOpen={openBook}
        />
      ),
    },
    {
      key: 'book_b',
      label: 'Book B',
      defaultWidth: 360,
      sortable: false,
      render: (r) => (
        <BookCell
          bookId={r.entity_b_id}
          title={r.b?.title}
          path={r.b?.primary_path}
          pathVars={pathVars}
          onOpen={openBook}
        />
      ),
    },
    {
      key: 'layer',
      label: 'Layer',
      defaultWidth: 120,
      sortValue: (r) => r.layer,
      render: (r) => r.layer,
    },
    {
      key: 'band',
      label: 'Band',
      defaultWidth: 120,
      sortValue: (r) => r.band || '',
      render: (r) => r.band || '—',
    },
    {
      key: 'source',
      label: 'Source',
      defaultWidth: 150,
      sortValue: (r) => r.label_source,
      render: (r) => (
        <Chip size="small" variant="outlined" color={sourceColor(r.label_source)} label={r.label_source} />
      ),
    },
    {
      key: 'reason',
      label: 'Reason',
      defaultWidth: 220,
      sortValue: (r) => r.label_reason || '',
      render: (r) => (
        <Typography variant="caption" color="text.secondary">{r.label_reason}</Typography>
      ),
    },
    {
      key: 'label',
      label: 'Label',
      align: 'center',
      defaultWidth: 220,
      sortValue: (r) => r.label,
      render: (r) => (
        <LabelToggle value={r.label} onChange={(label) => void override(r.candidate_id, label)} />
      ),
    },
  ], [openBook, pathVars, override]);

  const {
    visibleColumns,
    allColumns,
    sortField,
    sortDir,
    columnWidths,
    handleSort,
    toggleColumn,
    isColumnVisible,
    startResize,
    sortRows,
    resetColumns,
  } = useConfigurableTable<LabeledExample>({
    storageKey: 'dedup-labels',
    columns,
    defaultSortField: 'label',
  });

  const load = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const params = new URLSearchParams({ limit: String(PAGE), offset: String(offset) });
      if (labelFilter) params.set('label', labelFilter);
      if (sourceFilter) params.set('label_source', sourceFilter);
      if (bandFilter) params.set('band', bandFilter);
      const r = await apiFetch(`${API_BASE}/dedup/labels?${params}`);
      if (!r.ok) throw new Error(`HTTP ${r.status}`);
      const d = (await r.json()).data;
      setRows(d.labels || []);
      setTotal(d.total || 0);
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Failed to load labels');
    } finally {
      setLoading(false);
    }
  }, [labelFilter, sourceFilter, bandFilter, offset]);

  useEffect(() => { void loadStats(); }, [loadStats]);
  useEffect(() => { void load(); }, [load]);
  useEffect(() => () => { if (bandDebounceRef.current) clearTimeout(bandDebounceRef.current); }, []);

  return (
    <Box sx={{ p: 3 }}>
      <Typography variant="h4" gutterBottom>Dedup Gold Dataset</Typography>
      <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
        Labeled duplicate-candidate examples the classifier trains and validates on.
        Human overrides become gold (<code>label_source=human</code>) and take precedence.
      </Typography>

      {stats && (
        <Stack direction="row" spacing={1} sx={{ mb: 2, flexWrap: 'wrap', gap: 1 }}>
          <Chip label={`total ${stats.total}`} />
          {Object.entries(stats.by_label).map(([k, v]) => (
            <Chip key={k} size="small" color={labelColor(k)} label={`${k}: ${v}`} variant="outlined" />
          ))}
          {Object.entries(stats.by_source).filter(([, v]) => v > 0).map(([k, v]) => (
            <Chip key={k} size="small" color={sourceColor(k)} label={`${k}: ${v}`} />
          ))}
        </Stack>
      )}

      <Stack direction="row" spacing={2} sx={{ mb: 2 }}>
        <FormControl size="small" sx={{ minWidth: 160 }}>
          <InputLabel id="dedup-label-filter-label">Label</InputLabel>
          <Select
            id="dedup-label-filter"
            labelId="dedup-label-filter-label"
            label="Label"
            value={labelFilter}
            onChange={(e) => { setOffset(0); setLabelFilter(e.target.value); }}
          >
            <MenuItem value="">All</MenuItem>
            <MenuItem value="true_dup">true_dup</MenuItem>
            <MenuItem value="not_dup">not_dup</MenuItem>
            <MenuItem value="unsure">unsure</MenuItem>
          </Select>
        </FormControl>
        <FormControl size="small" sx={{ minWidth: 180 }}>
          <InputLabel id="dedup-source-filter-label">Source</InputLabel>
          <Select
            id="dedup-source-filter"
            labelId="dedup-source-filter-label"
            label="Source"
            value={sourceFilter}
            onChange={(e) => { setOffset(0); setSourceFilter(e.target.value); }}
          >
            <MenuItem value="">All</MenuItem>
            <MenuItem value="human">human (gold)</MenuItem>
            <MenuItem value="auto_high_conf">auto_high_conf</MenuItem>
            <MenuItem value="rule">rule</MenuItem>
            <MenuItem value="llm_judge">llm_judge</MenuItem>
          </Select>
        </FormControl>
        <TextField
          size="small"
          label="Band"
          value={localBandFilter}
          onChange={(e) => {
            setLocalBandFilter(e.target.value);
            if (bandDebounceRef.current) clearTimeout(bandDebounceRef.current);
            bandDebounceRef.current = setTimeout(() => {
              setOffset(0);
              setBandFilter(e.target.value);
            }, 300);
          }}
          sx={{ minWidth: 180 }}
        />
        <Box sx={{ display: 'flex', alignItems: 'center', ml: 'auto' }}>
          <ColumnPicker
            columns={allColumns.map(({ key, label }) => ({ key, label }))}
            isVisible={isColumnVisible}
            onToggle={toggleColumn}
            onReset={resetColumns}
          />
        </Box>
      </Stack>

      {error && <Alert severity="error" sx={{ mb: 2 }}>{error}</Alert>}

      <TableContainer component={Paper}>
        <Table size="small" stickyHeader>
          <TableHead>
            <TableRow>
              {visibleColumns.map((column) => (
                <ResizableHeaderCell
                  key={column.key}
                  columnKey={column.key}
                  label={column.label}
                  width={columnWidths[column.key] ?? column.defaultWidth ?? 150}
                  align={column.align}
                  sortable={column.sortable !== false}
                  sortActive={sortField === column.key}
                  sortDirection={sortDir}
                  onSort={() => handleSort(column.key)}
                  onStartResize={startResize}
                />
              ))}
            </TableRow>
          </TableHead>
          <TableBody>
            {loading ? (
              <TableRow><TableCell colSpan={visibleColumns.length} align="center"><CircularProgress size={24} sx={{ my: 2 }} /></TableCell></TableRow>
            ) : rows.length === 0 ? (
              <TableRow><TableCell colSpan={visibleColumns.length} align="center"><Typography variant="body2" color="text.secondary" sx={{ py: 2 }}>No labeled examples for this filter.</Typography></TableCell></TableRow>
            ) : sortRows(rows).map((r) => (
              <TableRow key={r.candidate_id} hover>
                {visibleColumns.map((column) => (
                  <TableCell
                    key={column.key}
                    align={column.align}
                    sx={{ width: columnWidths[column.key] ?? column.defaultWidth ?? 150 }}
                  >
                    {column.render(r)}
                  </TableCell>
                ))}
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </TableContainer>

      <Stack direction="row" spacing={2} alignItems="center" sx={{ mt: 2 }}>
        <Button disabled={offset === 0} onClick={() => setOffset(Math.max(0, offset - PAGE))}>Prev</Button>
        <Typography variant="body2">
          {total === 0 ? '0' : `${offset + 1}–${Math.min(offset + PAGE, total)}`} of {total}
        </Typography>
        <Button disabled={offset + PAGE >= total} onClick={() => setOffset(offset + PAGE)}>Next</Button>
      </Stack>

      <PathVarsLegend />
    </Box>
  );
}
