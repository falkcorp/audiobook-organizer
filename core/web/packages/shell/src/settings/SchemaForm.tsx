// file: core/web/packages/shell/src/settings/SchemaForm.tsx
// version: 0.1.0
// guid: 7176ae0c-99d0-4176-bfc1-2ecc9bacf623
// last-edited: 2026-11-20

import { DataState } from '@mediacore/ui-kit';
import {
  Alert,
  Autocomplete,
  Box,
  Button,
  Chip,
  FormControlLabel,
  MenuItem,
  Stack,
  Switch,
  TextField,
  Typography,
} from '@mui/material';
import { useEffect, useRef, useState, type ReactNode } from 'react';
import type { JsonSchema, KeyStateView, SettingsPatchBody, SettingsPatchResult, ShellClient, ShellProblem } from '../api/interim';
import { ApiCallError, errorMessage, isAbortError, problemMessage, unwrap } from '../api/problems';
import { formatError, jsonEqual, keyLabel, parseList, parseNumber, sectionKeys, widgetFor } from './keyState';

export interface SchemaFormProps {
  client: ShellClient;
  /** The config section path, e.g. "modules.batcave"; only this section's keys render. */
  section: string;
  /** The section's own schema from GET /settings/schema: its title and description head the form. */
  schema?: JsonSchema;
  /** GET /settings, as a list of the key states. */
  keys: readonly KeyStateView[];
  /** Called after a PATCH the server accepted. */
  onSaved?: (result: SettingsPatchResult) => void;
}

type Raw = string | string[];

/**
 * The schema-driven settings form of 07 §7.4.5. Widgets follow the key's JSON
 * Schema; a key an operator layer supplies is read-only with its `locked_by`; a
 * secret is write-only (it shows "set" or "not set", clearing is an explicit
 * action that goes in `unset`); a restart-required change shows a notice. Save
 * sends only what changed: `set` for edited keys, `unset` for keys reset to their
 * default. A refusal (4xx) is shown, per key where it names one.
 */
export function SchemaForm({ client, section, schema, keys, onSaved }: SchemaFormProps) {
  const [edits, setEdits] = useState<Record<string, unknown>>({});
  const [raw, setRaw] = useState<Record<string, Raw>>({});
  const [unset, setUnset] = useState<string[]>([]);
  const [saving, setSaving] = useState(false);
  const [problem, setProblem] = useState<ShellProblem | null>(null);
  const [failure, setFailure] = useState<string | null>(null);
  const [result, setResult] = useState<SettingsPatchResult | null>(null);
  const abortRef = useRef<AbortController | null>(null);

  useEffect(() => () => abortRef.current?.abort(), []);

  const visible = sectionKeys(keys, section);
  if (visible.length === 0) return <DataState status="empty" emptyText="This section has no settings." />;

  // Per-key client-side errors of what is being typed; a key with one is not staged.
  const invalid: Record<string, string> = {};
  for (const k of visible) {
    const kind = widgetFor(k);
    const r = raw[k.path];
    if (kind === 'number' && typeof r === 'string') {
      const p = parseNumber(k.schema, r);
      if ('error' in p) invalid[k.path] = p.error;
    } else if (kind === 'list' && Array.isArray(r)) {
      const p = parseList(k.schema, r);
      if ('error' in p) invalid[k.path] = p.error;
    } else if (kind === 'text' && typeof edits[k.path] === 'string') {
      const msg = formatError(k.schema.format, edits[k.path] as string);
      if (msg) invalid[k.path] = msg;
    }
  }

  const serverErrors = fieldErrors(problem, visible);
  const dirty = Object.keys(edits).length > 0 || unset.length > 0;
  const blocked = Object.keys(invalid).length > 0;

  const stage = (k: KeyStateView, value: unknown) => {
    setEdits((prev) => {
      const next = { ...prev };
      if (jsonEqual(value, k.effective)) delete next[k.path];
      else next[k.path] = value;
      return next;
    });
    setUnset((prev) => prev.filter((p) => p !== k.path));
  };

  const dropEdit = (path: string) => {
    setEdits((prev) => {
      const next = { ...prev };
      delete next[path];
      return next;
    });
    setRaw((prev) => {
      const next = { ...prev };
      delete next[path];
      return next;
    });
  };

  const markUnset = (k: KeyStateView) => {
    dropEdit(k.path);
    setUnset((prev) => [...prev, k.path]);
  };

  const discard = () => {
    setEdits({});
    setRaw({});
    setUnset([]);
    setProblem(null);
    setFailure(null);
  };

  const save = async () => {
    abortRef.current?.abort();
    const controller = new AbortController();
    abortRef.current = controller;
    const body: SettingsPatchBody = {};
    if (Object.keys(edits).length > 0) body.set = edits;
    if (unset.length > 0) body.unset = unset;
    setSaving(true);
    setProblem(null);
    setFailure(null);
    setResult(null);
    try {
      const done = await unwrap(client.PATCH('/api/core/v1/settings', { body, signal: controller.signal }));
      setEdits({});
      setRaw({});
      setUnset([]);
      setResult(done);
      onSaved?.(done);
    } catch (err) {
      if (isAbortError(err)) return;
      if (err instanceof ApiCallError) setProblem(err.problem);
      else setFailure(errorMessage(err));
    } finally {
      setSaving(false);
    }
  };

  const groups = new Map<string, KeyStateView[]>();
  for (const k of visible) {
    const g = k.schema['x-mediacore-ui']?.group ?? '';
    groups.set(g, [...(groups.get(g) ?? []), k]);
  }

  return (
    <Box component="form" onSubmit={(e) => { e.preventDefault(); void save(); }}>
      <Stack spacing={3}>
        <Box>
          <Typography variant="h6" component="h2">
            {schema?.title ?? section}
          </Typography>
          {schema?.description && <Typography color="text.secondary">{schema.description}</Typography>}
        </Box>

        {problem && <ProblemAlert problem={problem} />}
        {failure && <Alert severity="error">{failure}</Alert>}
        {result && <SavedNotice result={result} />}

        {[...groups].map(([group, groupKeys]) => (
          <Stack key={group} spacing={2} component="section" aria-label={group || undefined}>
            {group && <Typography variant="subtitle1" component="h3">{group}</Typography>}
            {groupKeys.map((k) => (
              <KeyRow key={k.path} k={k} staged={k.path in edits} unsetStaged={unset.includes(k.path)} onReset={() => markUnset(k)} onUndo={() => { dropEdit(k.path); setUnset((prev) => prev.filter((p) => p !== k.path)); }}>
                <Widget
                  k={k}
                  value={k.path in edits ? edits[k.path] : k.effective}
                  raw={raw[k.path]}
                  disabled={Boolean(k.locked_by) || saving || unset.includes(k.path)}
                  error={invalid[k.path] ?? serverErrors[k.path]}
                  setRaw={(r) => setRaw((prev) => ({ ...prev, [k.path]: r }))}
                  stage={(v) => stage(k, v)}
                  clearRaw={() => setRaw((prev) => { const next = { ...prev }; delete next[k.path]; return next; })}
                />
              </KeyRow>
            ))}
          </Stack>
        ))}

        <Stack direction="row" spacing={1}>
          <Button type="submit" variant="contained" disabled={!dirty || blocked || saving}>
            {saving ? 'Saving…' : 'Save'}
          </Button>
          <Button onClick={discard} disabled={(!dirty && !problem && !failure) || saving}>
            Discard changes
          </Button>
        </Stack>
      </Stack>
    </Box>
  );
}

/** What the server named per key in a refusal, as the text to show under that key. */
function fieldErrors(problem: ShellProblem | null, visible: readonly KeyStateView[]): Record<string, string> {
  const out: Record<string, string> = {};
  if (!problem) return out;
  for (const k of visible) {
    const hit = problem.errors?.find((e) => e.field === k.path || e.field.endsWith(`/${k.path}`) || e.field.endsWith(`.${k.path}`));
    if (hit) out[k.path] = hit.message;
    else if (problem.locked_by?.[k.path]) out[k.path] = `Locked by ${problem.locked_by[k.path]}.`;
    else if (problem.need?.[k.path]) out[k.path] = `Needs ${problem.need[k.path]}.`;
  }
  return out;
}

function ProblemAlert({ problem }: { problem: ShellProblem }) {
  const lines = [
    ...(problem.errors ?? []).map((e) => `${e.field}: ${e.message}`),
    ...Object.entries(problem.locked_by ?? {}).map(([p, by]) => `${p} is locked by ${by}`),
    ...Object.entries(problem.need ?? {}).map(([p, need]) => `${p} needs ${need}`),
  ];
  return (
    <Alert severity="error">
      {problemMessage(problem)}
      {lines.length > 0 && (
        <ul style={{ margin: 0, paddingLeft: '1.25rem' }}>
          {lines.map((l) => (
            <li key={l}>{l}</li>
          ))}
        </ul>
      )}
    </Alert>
  );
}

function SavedNotice({ result }: { result: SettingsPatchResult }) {
  const pending = result.restart_pending ?? [];
  const failed = result.post_commit_error ?? [];
  return (
    <Stack spacing={1}>
      <Alert severity="success">{`Saved ${result.applied.length} setting${result.applied.length === 1 ? '' : 's'}.`}</Alert>
      {pending.length > 0 && <Alert severity="info">{`A restart is needed before these take effect: ${pending.join(', ')}.`}</Alert>}
      {failed.map((f) => (
        <Alert key={f.section} severity="warning">{`${f.section}: saved, but applying it failed: ${f.message}`}</Alert>
      ))}
    </Stack>
  );
}

interface KeyRowProps {
  k: KeyStateView;
  staged: boolean;
  unsetStaged: boolean;
  onReset: () => void;
  onUndo: () => void;
  children: ReactNode;
}

function KeyRow({ k, staged, unsetStaged, onReset, onUndo, children }: KeyRowProps) {
  const locked = Boolean(k.locked_by);
  const canClearSecret = k.secret?.set === true && k.secret.source === 'stored';
  const canReset = !locked && !unsetStaged && (k.secret ? canClearSecret : k.source === 'override');
  return (
    <Box>
      {children}
      <Stack direction="row" spacing={1} sx={{ mt: 0.5, alignItems: 'center', flexWrap: 'wrap' }}>
        {locked && <Typography variant="caption" color="text.secondary">{`Locked by ${k.locked_by}. Change it there.`}</Typography>}
        {k.pending && <Chip size="small" color="warning" label="Restart required" />}
        {!k.pending && k.reload !== 'live' && <Typography variant="caption" color="text.secondary">Applies after a restart.</Typography>}
        {k.quarantined && <Chip size="small" color="warning" label="Stored value ignored at boot" />}
        {staged && <Chip size="small" color="primary" label="Changed" />}
        {unsetStaged && <Chip size="small" color="primary" label={k.secret ? 'Will be cleared' : 'Will reset to default'} onDelete={onUndo} />}
        {canReset && (
          <Button size="small" onClick={onReset}>
            {k.secret ? `Clear ${keyLabel(k)}` : `Reset ${keyLabel(k)} to default`}
          </Button>
        )}
      </Stack>
    </Box>
  );
}

interface WidgetProps {
  k: KeyStateView;
  value: unknown;
  raw: Raw | undefined;
  disabled: boolean;
  error?: string;
  setRaw: (r: Raw) => void;
  clearRaw: () => void;
  stage: (v: unknown) => void;
}

function Widget({ k, value, raw, disabled, error, setRaw, clearRaw, stage }: WidgetProps) {
  const label = keyLabel(k);
  const help = error ?? k.schema.description;
  switch (widgetFor(k)) {
    case 'switch':
      return (
        <FormControlLabel
          label={label}
          control={<Switch checked={value === true} disabled={disabled} onChange={(e) => stage(e.target.checked)} />}
        />
      );
    case 'select':
      return (
        <TextField select label={label} value={value === undefined || value === null ? '' : String(value)} disabled={disabled} error={Boolean(error)} helperText={help} onChange={(e) => {
          const picked = (k.schema.enum ?? []).find((o) => String(o) === e.target.value);
          if (picked !== undefined) stage(picked);
        }}>
          {(k.schema.enum ?? []).map((o) => (
            <MenuItem key={String(o)} value={String(o)}>{String(o)}</MenuItem>
          ))}
        </TextField>
      );
    case 'number':
      return (
        <TextField
          type="number"
          label={label}
          value={raw ?? (value === undefined || value === null ? '' : String(value))}
          disabled={disabled}
          error={Boolean(error)}
          helperText={help}
          slotProps={{ htmlInput: { min: k.schema.minimum, max: k.schema.maximum } }}
          onChange={(e) => {
            const text = e.target.value;
            setRaw(text);
            const p = parseNumber(k.schema, text);
            if ('value' in p) stage(p.value);
          }}
        />
      );
    case 'secret':
      return (
        <TextField
          type="password"
          label={label}
          value={typeof value === 'string' ? value : ''}
          disabled={disabled}
          error={Boolean(error)}
          placeholder={k.secret?.set ? '(unchanged)' : undefined}
          helperText={error ?? `${k.secret?.set ? `Set${k.secret.source ? ` (${k.secret.source})` : ''}` : 'Not set'}. Write-only: the value is never shown.${k.schema.description ? ` ${k.schema.description}` : ''}`}
          autoComplete="new-password"
          // Typing back to empty means "no change": a secret has no effective value, so staging undefined drops the edit.
          onChange={(e) => stage(e.target.value === '' ? undefined : e.target.value)}
        />
      );
    case 'list': {
      const chips = Array.isArray(raw) ? raw : Array.isArray(value) ? value.map(String) : [];
      return (
        <Autocomplete
          multiple
          freeSolo
          options={[] as string[]}
          value={chips}
          disabled={disabled}
          onChange={(_e, next) => {
            const list = next.map(String);
            setRaw(list);
            const p = parseList(k.schema, list);
            if ('value' in p) stage(p.value);
          }}
          renderValue={(vals, getItemProps) => vals.map((v, i) => <Chip {...getItemProps({ index: i })} key={`${v}-${i}`} label={v} size="small" />)}
          renderInput={(params) => <TextField {...params} label={label} error={Boolean(error)} helperText={help} />}
        />
      );
    }
    case 'text':
      return (
        <TextField
          label={label}
          value={value === undefined || value === null ? '' : String(value)}
          disabled={disabled}
          error={Boolean(error)}
          helperText={help}
          onChange={(e) => {
            clearRaw();
            stage(e.target.value);
          }}
        />
      );
    default:
      return (
        <TextField
          label={label}
          value={JSON.stringify(value ?? null)}
          disabled
          helperText="This value has no form widget; edit it in the module's own panel."
        />
      );
  }
}
