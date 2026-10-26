// file: web/src/components/collections/QueryHelp.tsx
// version: 1.2.0
// guid: ba818f08-1260-44b3-99c2-11db1dd24fab
// last-edited: 2026-10-25

import { Alert, Box, Button, Chip, Paper, Stack, TextField, Typography } from '@mui/material';
import {
  PER_USER_NOTE,
  QUERY_FIELD_HELP,
  SORT_FIELDS,
  WORKED_EXAMPLES,
  applySuggestion,
  queryError,
  suggestFields,
  usesPerUserField,
  type SortChoice,
  type SortDirection,
} from './queryHelpData';

interface QueryHelpProps {
  value: string;
  onChange: (next: string) => void;
  disabled?: boolean;
  /** A server-side message for the query (a 400 body), shown verbatim. */
  serverError?: string | null;
}

/** The dynamic-collection query box: field autocomplete, validation, help, examples. */
export function QueryHelp({ value, onChange, disabled, serverError }: QueryHelpProps) {
  const parseError = queryError(value);
  const suggestions = suggestFields(value);
  const perUser = usesPerUserField(value);
  const shownError = parseError ?? serverError ?? null;

  return (
    <Box>
      <TextField
        fullWidth
        label="Query"
        value={value}
        onChange={(e) => onChange(e.target.value)}
        margin="normal"
        required
        disabled={disabled}
        error={Boolean(shownError)}
        helperText={shownError ?? 'The same search syntax as the Library search bar.'}
        placeholder="e.g. author:smith year:>=2015"
        slotProps={{ htmlInput: { 'aria-label': 'Query' } }}
      />
      {suggestions.length > 0 && !disabled && (
        <Stack
          direction="row"
          spacing={1}
          sx={{ mb: 1, flexWrap: 'wrap', gap: 0.5 }}
          aria-label="Field suggestions"
        >
          {suggestions.map((name) => (
            <Chip
              key={name}
              size="small"
              label={name}
              onClick={() => onChange(applySuggestion(value, name))}
            />
          ))}
        </Stack>
      )}
      {perUser && (
        <Alert severity="info" role="status" sx={{ mb: 1 }}>
          {PER_USER_NOTE}
        </Alert>
      )}
      <Paper variant="outlined" sx={{ p: 1.5, mb: 1 }} aria-label="Query help">
        <Typography variant="subtitle2" gutterBottom>
          Fields
        </Typography>
        <Box component="ul" sx={{ m: 0, pl: 2 }}>
          {QUERY_FIELD_HELP.map((f) => (
            <li key={f.name}>
              <Typography variant="body2" component="span">
                <code>{f.example}</code> &mdash; {f.description}
                {f.group === 'per_user' ? ' (per-user)' : ''}
              </Typography>
            </li>
          ))}
        </Box>
        <Typography variant="subtitle2" sx={{ mt: 1.5 }} gutterBottom>
          Examples
        </Typography>
        {WORKED_EXAMPLES.map((ex) => (
          <Box
            key={ex.query}
            sx={{ display: 'flex', alignItems: 'center', gap: 1, flexWrap: 'wrap', mb: 0.5 }}
          >
            <Typography variant="body2">
              {ex.title}: <code>{ex.query}</code>
            </Typography>
            <Button size="small" disabled={disabled} onClick={() => onChange(ex.query)}>
              Use this query
            </Button>
            {ex.note && (
              <Typography variant="caption" sx={{ color: 'text.secondary' }}>
                {ex.note}
              </Typography>
            )}
          </Box>
        ))}
      </Paper>
    </Box>
  );
}

interface SortPickerProps {
  value: SortChoice;
  onChange: (next: SortChoice) => void;
  disabled?: boolean;
}

/** Sort field + direction; writes the `sort_json` shape through encodeSort. */
export function SortPicker({ value, onChange, disabled }: SortPickerProps) {
  return (
    <Stack direction="row" spacing={2} sx={{ mt: 1 }}>
      <TextField
        select
        label="Sort by"
        value={value.field}
        disabled={disabled}
        onChange={(e) => onChange({ ...value, field: e.target.value })}
        sx={{ minWidth: 240 }}
        slotProps={{ select: { native: true }, inputLabel: { shrink: true } }}
      >
        <option value="">No sort</option>
        {SORT_FIELDS.map((f) => (
          <option key={f.value} value={f.value}>
            {f.label}
          </option>
        ))}
      </TextField>
      <TextField
        select
        label="Direction"
        value={value.direction}
        disabled={disabled || !value.field}
        onChange={(e) => onChange({ ...value, direction: e.target.value as SortDirection })}
        slotProps={{ select: { native: true } }}
      >
        <option value="asc">Ascending</option>
        <option value="desc">Descending</option>
      </TextField>
    </Stack>
  );
}
