// file: web/src/components/collections/BookPicker.tsx
// version: 1.1.0
// guid: 5fdbda04-5379-4733-a2fe-2a59e4bb747d
// last-edited: 2026-10-18

import { useEffect, useRef, useState } from 'react';
import {
  Alert,
  Box,
  Button,
  Checkbox,
  Chip,
  List,
  ListItem,
  ListItemButton,
  ListItemIcon,
  ListItemText,
  Stack,
  TextField,
  Typography,
} from '@mui/material';
import { type Book, searchBooksPage } from '../../services/api';
import { isAbortError } from '../../utils/apiFetch';
import { queryError } from './queryHelpData';

/** Most results one search shows; a picker is for choosing, not for browsing the library. */
export const BOOK_PICKER_RESULT_LIMIT = 50;

interface BookPickerProps {
  selectedIds: string[];
  onChange: (ids: string[]) => void;
  /** Ids that cannot be picked again (already members). */
  excludeIds?: string[];
  disabled?: boolean;
}

/**
 * Search the library (the app's one search syntax) and tick books. Loading,
 * error, empty and populated are four different renderings; a failed search is
 * never shown as "no results".
 */
export function BookPicker({ selectedIds, onChange, excludeIds = [], disabled }: BookPickerProps) {
  const [text, setText] = useState('');
  const [results, setResults] = useState<Book[] | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // Titles of every book ever shown, so a selected chip keeps its title after the results change.
  const [titles, setTitles] = useState<Map<string, string>>(new Map());
  const requestId = useRef(0);
  const controller = useRef<AbortController | null>(null);

  // Closing the dialog (unmount) cancels a search still in flight.
  useEffect(() => {
    return () => {
      // A request handle, not a DOM node: reading .current at cleanup time is the point.
      controller.current?.abort();
    };
  }, []);

  const parseError = text.trim() ? queryError(text) : null;

  const run = async () => {
    const q = text.trim();
    if (!q || parseError) return;
    controller.current?.abort();
    const ctl = new AbortController();
    controller.current = ctl;
    const mine = ++requestId.current;
    setLoading(true);
    setError(null);
    try {
      const { items: found } = await searchBooksPage(
        q,
        BOOK_PICKER_RESULT_LIMIT,
        0,
        false,
        ctl.signal
      );
      if (mine !== requestId.current) return;
      setTitles(
        (prev) => new Map([...prev, ...found.map((b): [string, string] => [b.id, b.title])])
      );
      setResults(found);
    } catch (err) {
      if (isAbortError(err) || mine !== requestId.current) return;
      setResults(null);
      setError(err instanceof Error && err.message ? err.message : 'Search failed');
    } finally {
      if (mine === requestId.current) setLoading(false);
    }
  };

  const toggle = (id: string) =>
    onChange(selectedIds.includes(id) ? selectedIds.filter((x) => x !== id) : [...selectedIds, id]);

  const visible = (results ?? []).filter((b) => !excludeIds.includes(b.id));

  return (
    <Box>
      <Stack direction="row" spacing={1} sx={{ alignItems: 'flex-start' }}>
        <TextField
          fullWidth
          size="small"
          label="Search books"
          value={text}
          disabled={disabled}
          onChange={(e) => setText(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter') {
              e.preventDefault();
              void run();
            }
          }}
          error={Boolean(parseError)}
          helperText={parseError ?? 'Same syntax as the Library search bar. Press Enter to search.'}
        />
        <Button
          variant="outlined"
          disabled={disabled || loading || !text.trim() || Boolean(parseError)}
          onClick={() => void run()}
        >
          Search
        </Button>
      </Stack>

      {selectedIds.length > 0 && (
        <Stack
          direction="row"
          sx={{ flexWrap: 'wrap', gap: 0.5, my: 1 }}
          aria-label="Selected books"
        >
          {selectedIds.map((id) => (
            <Chip
              key={id}
              size="small"
              label={titles.get(id) ?? id}
              onDelete={disabled ? undefined : () => toggle(id)}
            />
          ))}
        </Stack>
      )}

      {loading ? (
        <Typography sx={{ color: 'text.secondary', mt: 1 }}>Searching...</Typography>
      ) : error ? (
        <Alert severity="error" sx={{ mt: 1 }}>
          {error}
        </Alert>
      ) : results === null ? null : visible.length === 0 ? (
        <Typography sx={{ color: 'text.secondary', mt: 1 }}>No matching books.</Typography>
      ) : (
        <List dense sx={{ maxHeight: 240, overflow: 'auto' }}>
          {visible.map((b) => (
            <ListItem key={b.id} disablePadding>
              <ListItemButton disabled={disabled} onClick={() => toggle(b.id)}>
                <ListItemIcon>
                  <Checkbox
                    edge="start"
                    checked={selectedIds.includes(b.id)}
                    tabIndex={-1}
                    disableRipple
                    slotProps={{ input: { 'aria-label': `Select ${b.title}` } }}
                  />
                </ListItemIcon>
                <ListItemText primary={b.title} secondary={b.author_name} />
              </ListItemButton>
            </ListItem>
          ))}
        </List>
      )}
    </Box>
  );
}
