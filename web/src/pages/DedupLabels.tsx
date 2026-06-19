// file: web/src/pages/DedupLabels.tsx
// version: 1.0.0
// guid: 7e3a1c92-4b60-4d85-9f21-6a5e0c9d3f58
// last-edited: 2026-06-19

// DedupLabels — the C6 gold-dataset review page for the dedup feedback loop.
// Lists labeled dedup examples (the dedup:label: keyspace), filterable by label
// and label_source, with one-click human override. This is where the user reviews
// the gold dataset that the classifier will train and validate on.

import { useCallback, useEffect, useState } from 'react';
import {
  Box, Typography, Paper, Table, TableBody, TableCell, TableContainer, TableHead,
  TableRow, Chip, Select, MenuItem, FormControl, InputLabel, Button, Stack,
  CircularProgress, Alert, Tooltip,
} from '@mui/material';

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

export default function DedupLabels() {
  const [rows, setRows] = useState<LabeledExample[]>([]);
  const [total, setTotal] = useState(0);
  const [stats, setStats] = useState<LabelStats | null>(null);
  const [labelFilter, setLabelFilter] = useState('');
  const [sourceFilter, setSourceFilter] = useState('');
  const [offset, setOffset] = useState(0);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const loadStats = useCallback(async () => {
    try {
      const r = await fetch(`${API_BASE}/dedup/labels/stats`);
      if (r.ok) setStats((await r.json()).data);
    } catch {
      /* stats are best-effort */
    }
  }, []);

  const load = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const params = new URLSearchParams({ limit: String(PAGE), offset: String(offset) });
      if (labelFilter) params.set('label', labelFilter);
      if (sourceFilter) params.set('label_source', sourceFilter);
      const r = await fetch(`${API_BASE}/dedup/labels?${params}`);
      if (!r.ok) throw new Error(`HTTP ${r.status}`);
      const d = (await r.json()).data;
      setRows(d.labels || []);
      setTotal(d.total || 0);
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Failed to load labels');
    } finally {
      setLoading(false);
    }
  }, [labelFilter, sourceFilter, offset]);

  useEffect(() => { void loadStats(); }, [loadStats]);
  useEffect(() => { void load(); }, [load]);

  const override = async (candidateId: number, label: string) => {
    try {
      const r = await fetch(`${API_BASE}/dedup/labels/${candidateId}/override`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ label, reason: 'ui_override' }),
      });
      if (!r.ok) throw new Error(`HTTP ${r.status}`);
      await load();
      await loadStats();
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Override failed');
    }
  };

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
          <InputLabel>Label</InputLabel>
          <Select label="Label" value={labelFilter} onChange={(e) => { setOffset(0); setLabelFilter(e.target.value); }}>
            <MenuItem value="">All</MenuItem>
            <MenuItem value="true_dup">true_dup</MenuItem>
            <MenuItem value="not_dup">not_dup</MenuItem>
            <MenuItem value="unsure">unsure</MenuItem>
          </Select>
        </FormControl>
        <FormControl size="small" sx={{ minWidth: 180 }}>
          <InputLabel>Source</InputLabel>
          <Select label="Source" value={sourceFilter} onChange={(e) => { setOffset(0); setSourceFilter(e.target.value); }}>
            <MenuItem value="">All</MenuItem>
            <MenuItem value="human">human (gold)</MenuItem>
            <MenuItem value="auto_high_conf">auto_high_conf</MenuItem>
            <MenuItem value="rule">rule</MenuItem>
            <MenuItem value="llm_judge">llm_judge</MenuItem>
          </Select>
        </FormControl>
      </Stack>

      {error && <Alert severity="error" sx={{ mb: 2 }}>{error}</Alert>}

      <TableContainer component={Paper}>
        <Table size="small" stickyHeader>
          <TableHead>
            <TableRow>
              <TableCell>Book A</TableCell>
              <TableCell>Book B</TableCell>
              <TableCell>Layer</TableCell>
              <TableCell>Label</TableCell>
              <TableCell>Source</TableCell>
              <TableCell>Reason</TableCell>
              <TableCell align="right">Override</TableCell>
            </TableRow>
          </TableHead>
          <TableBody>
            {loading ? (
              <TableRow><TableCell colSpan={7} align="center"><CircularProgress size={24} sx={{ my: 2 }} /></TableCell></TableRow>
            ) : rows.length === 0 ? (
              <TableRow><TableCell colSpan={7} align="center"><Typography variant="body2" color="text.secondary" sx={{ py: 2 }}>No labeled examples for this filter.</Typography></TableCell></TableRow>
            ) : rows.map((r) => (
              <TableRow key={r.candidate_id} hover>
                <TableCell><Tooltip title={r.a?.primary_path || r.entity_a_id}><span>{r.a?.title || r.entity_a_id}</span></Tooltip></TableCell>
                <TableCell><Tooltip title={r.b?.primary_path || r.entity_b_id}><span>{r.b?.title || r.entity_b_id}</span></Tooltip></TableCell>
                <TableCell>{r.layer}</TableCell>
                <TableCell><Chip size="small" color={labelColor(r.label)} label={r.label || 'unlabeled'} /></TableCell>
                <TableCell><Chip size="small" variant="outlined" color={sourceColor(r.label_source)} label={r.label_source} /></TableCell>
                <TableCell><Typography variant="caption">{r.label_reason}</Typography></TableCell>
                <TableCell align="right">
                  <Button size="small" color="success" onClick={() => void override(r.candidate_id, 'true_dup')}>dup</Button>
                  <Button size="small" color="error" onClick={() => void override(r.candidate_id, 'not_dup')}>not</Button>
                </TableCell>
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
    </Box>
  );
}
