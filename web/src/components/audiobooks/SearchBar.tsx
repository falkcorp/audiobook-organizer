// file: web/src/components/audiobooks/SearchBar.tsx
// version: 2.5.0
// guid: 1d2e3f4a-5b6c-7d8e-9f0a-1b2c3d4e5f6a

import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  Autocomplete,
  Box,
  Chip,
  IconButton,
  InputAdornment,
  Paper,
  Popper,
  TextField,
  ToggleButton,
  ToggleButtonGroup,
  Tooltip,
  Typography,
} from '@mui/material';
import {
  Search as SearchIcon,
  Clear as ClearIcon,
  GridView as GridViewIcon,
  ViewList as ViewListIcon,
  HelpOutline as HelpIcon,
  Close as CloseIcon,
} from '@mui/icons-material';
import { parseSearch, SEARCH_FIELDS, type ParsedSearch } from '../../utils/searchParser';
import { STORAGE_KEYS } from '../../lib/storageKeys';

export type ViewMode = 'grid' | 'list';

const MAX_RECENT = 15;

function getRecentSearches(): string[] {
  try {
    return JSON.parse(localStorage.getItem(STORAGE_KEYS.LIBRARY_RECENT_SEARCHES) || '[]');
  } catch {
    return [];
  }
}

function saveRecentSearch(query: string) {
  if (!query.trim()) return;
  const recent = getRecentSearches().filter((s) => s !== query);
  recent.unshift(query);
  localStorage.setItem(STORAGE_KEYS.LIBRARY_RECENT_SEARCHES, JSON.stringify(recent.slice(0, MAX_RECENT)));
}

// Build autocomplete options: field prefixes, prefix wildcard, + recent searches
function buildOptions(input: string, recent: string[]): string[] {
  const opts: string[] = [];
  const lower = input.toLowerCase();

  // If typing a field prefix (e.g. "aut"), suggest field:
  if (lower && !lower.includes(':')) {
    for (const field of SEARCH_FIELDS) {
      if (field.startsWith(lower)) {
        opts.push(`${field}:`);
      }
    }
    // Suggest prefix wildcard for bare words (3+ chars)
    if (lower.length >= 3 && !lower.endsWith('*')) {
      opts.push(`${input}*`);
    }
  }

  // Recent searches matching input
  for (const r of recent) {
    if (!input || r.toLowerCase().includes(lower)) {
      if (!opts.includes(r)) opts.push(r);
    }
  }

  return opts.slice(0, 10);
}

const SEARCH_HELP = [
  { example: 'author:"Brandon Sanderson"', desc: 'Books by a specific author' },
  { example: 'series:Mistborn', desc: 'Books in a series' },
  { example: 'narrator:Kramer', desc: 'Books by narrator' },
  { example: 'tag:favorites', desc: 'Books with a tag' },
  { example: '-tag:read', desc: 'Exclude a tag' },
  { example: 'format:m4b', desc: 'Filter by file format' },
  { example: 'has_cover:yes', desc: 'Books with cover art' },
  { example: 'has_cover:no', desc: 'Books missing cover art' },
  { example: 'has_written:yes', desc: 'Tags written to files' },
  { example: 'has_written:no', desc: 'Tags not yet written' },
  { example: 'needs_writeback:yes', desc: 'DB metadata updated after last file write' },
  { example: 'has_organized:yes', desc: 'Files organized into library' },
  { example: 'has_organized:no', desc: 'Files not yet organized' },
  { example: 'review:matched', desc: 'Manually applied metadata' },
  { example: 'review:no_match', desc: 'Marked as no match' },
  { example: 'library_state:organized', desc: 'Organized books' },
  { example: 'library_state:imported', desc: 'Imported but not organized' },
  { example: 'library_state:suspicious', desc: 'Suspicious / incomplete files' },
  { example: 'language:en', desc: 'Filter by language' },
  { example: 'year:2024', desc: 'Published in a year' },
  { example: 'NOT author:Unknown', desc: 'Exclude a field value' },
  { example: 'quality:320kbps', desc: 'Filter by audio quality' },
  { example: 'publisher:Audible', desc: 'Filter by publisher' },
  // Common combinations
  { example: 'review:matched has_written:yes has_organized:yes', desc: 'Fully processed books' },
  { example: '-review:matched library_state:organized -has_written:yes', desc: 'Organized but needs metadata + file write' },
  { example: '-review:matched', desc: 'Books still needing metadata' },
  { example: 'review:matched -has_written:yes', desc: 'Metadata applied but not written to files' },
  { example: 'review:matched has_written:yes -has_organized:yes', desc: 'Written but not organized' },
  { example: 'has_cover:no review:matched', desc: 'Matched but missing cover art' },
  { example: 'library_state:imported -review:matched', desc: 'Imported books needing metadata' },
  // Read/unread tracking (per-user)
  { example: 'read_status:finished', desc: 'Books you\'ve finished' },
  { example: 'read_status:in_progress', desc: 'Books you\'re reading' },
  { example: '-read_status:finished', desc: 'Unfinished books' },
  { example: 'progress_pct:>75', desc: 'Nearly finished books' },
  // Advanced DSL operators
  { example: 'author:sanderson || author:jemisin', desc: 'OR — match either author' },
  { example: 'year:>2020', desc: 'Published after 2020' },
  { example: 'year:[2015 TO 2020]', desc: 'Year range' },
  { example: 'title:vamp*', desc: 'Prefix wildcard' },
  { example: 'author:smith~', desc: 'Fuzzy match (~2 edit distance)' },
  { example: 'format:(m4b|mp3)', desc: 'Match either value' },
];

interface SearchBarProps {
  value: string;
  onChange: (value: string) => void;
  onParsedSearchChange?: (parsed: ParsedSearch) => void;
  viewMode: ViewMode;
  onViewModeChange: (mode: ViewMode) => void;
  placeholder?: string;
}

export const SearchBar: React.FC<SearchBarProps> = ({
  value,
  onChange,
  onParsedSearchChange,
  viewMode,
  onViewModeChange,
  placeholder = 'Search audiobooks... (try author:"Name" tag:scifi)',
}) => {
  const parsed = useMemo(() => parseSearch(value), [value]);
  const [helpOpen, setHelpOpen] = useState(false);
  const [recentSearches, setRecentSearches] = useState<string[]>(getRecentSearches);
  const helpAnchorRef = useRef<HTMLButtonElement>(null);

  useEffect(() => {
    onParsedSearchChange?.(parsed);
  }, [parsed, onParsedSearchChange]);

  const handleChange = (_event: React.SyntheticEvent, newValue: string | null) => {
    onChange(newValue || '');
  };

  const handleInputChange = (_event: React.SyntheticEvent, newValue: string) => {
    onChange(newValue);
  };

  const handleClear = () => {
    onChange('');
  };

  // Save to recent on Enter
  const handleKeyDown = useCallback(
    (event: React.KeyboardEvent) => {
      if (event.key === 'Enter' && value.trim()) {
        saveRecentSearch(value.trim());
        setRecentSearches(getRecentSearches());
      }
    },
    [value]
  );

  const handleRemoveFilter = (index: number) => {
    const filter = parsed.fieldFilters[index];
    const prefix = filter.negated ? (value.includes('NOT ') ? 'NOT ' : '-') : '';
    const valStr = filter.quoted ? `"${filter.value}"` : filter.value;
    const token = `${prefix}${filter.field}:${valStr}`;
    const newValue = value.replace(token, '').replace(/\s{2,}/g, ' ').trim();
    onChange(newValue);
  };

  const handleViewModeChange = (
    _event: React.MouseEvent<HTMLElement>,
    newMode: ViewMode | null
  ) => {
    if (newMode !== null) {
      onViewModeChange(newMode);
    }
  };

  const handleHelpExampleClick = (example: string) => {
    onChange(example);
  };

  const options = useMemo(() => buildOptions(value, recentSearches), [value, recentSearches]);

  return (
    <Box display="flex" flexDirection="column" gap={1}>
      <Box display="flex" gap={2} alignItems="center">
        <Autocomplete
          freeSolo
          fullWidth
          value={value}
          onChange={handleChange}
          onInputChange={handleInputChange}
          options={options}
          filterOptions={(x) => x} // we do our own filtering
          renderInput={(params) => (
            <TextField
              {...params}
              placeholder={placeholder}
              onKeyDown={handleKeyDown}
              InputProps={{
                ...params.InputProps,
                startAdornment: (
                  <InputAdornment position="start">
                    <SearchIcon />
                  </InputAdornment>
                ),
                endAdornment: (
                  <>
                    {value && (
                      <InputAdornment position="end">
                        <IconButton size="small" onClick={handleClear}>
                          <ClearIcon />
                        </IconButton>
                      </InputAdornment>
                    )}
                    <InputAdornment position="end">
                      <Tooltip title="Search help">
                        <IconButton size="small" ref={helpAnchorRef} onClick={() => setHelpOpen(!helpOpen)}>
                          <HelpIcon />
                        </IconButton>
                      </Tooltip>
                    </InputAdornment>
                  </>
                ),
              }}
            />
          )}
        />

        <ToggleButtonGroup
          value={viewMode}
          exclusive
          onChange={handleViewModeChange}
          aria-label="view mode"
        >
          <ToggleButton value="grid" aria-label="grid view">
            <GridViewIcon />
          </ToggleButton>
          <ToggleButton value="list" aria-label="list view">
            <ViewListIcon />
          </ToggleButton>
        </ToggleButtonGroup>
      </Box>

      {parsed.fieldFilters.length > 0 && (
        <Box display="flex" gap={0.5} flexWrap="wrap">
          {parsed.fieldFilters.map((filter, index) => {
            const label = `${filter.negated ? 'NOT ' : ''}${filter.field}:${filter.quoted ? `"${filter.value}"` : filter.value}`;
            return (
              <Chip
                key={`${filter.field}-${filter.value}-${index}`}
                label={label}
                size="small"
                color={filter.negated ? 'error' : 'primary'}
                variant="outlined"
                onDelete={() => handleRemoveFilter(index)}
              />
            );
          })}
        </Box>
      )}

      {/* Search help panel — stays open until explicitly closed */}
      <Popper
        open={helpOpen}
        anchorEl={helpAnchorRef.current}
        placement="bottom-end"
        style={{ zIndex: 1300 }}
      >
        <Paper
          elevation={8}
          sx={{
            p: 2,
            maxWidth: 480,
            maxHeight: '70vh',
            overflowY: 'auto',
          }}
        >
          <Box display="flex" justifyContent="space-between" alignItems="center" mb={1}>
            <Typography variant="subtitle1" fontWeight="bold">
              Search Syntax
            </Typography>
            <IconButton size="small" onClick={() => setHelpOpen(false)}>
              <CloseIcon fontSize="small" />
            </IconButton>
          </Box>

          <Typography variant="body2" color="text.secondary" sx={{ mb: 1.5 }}>
            Type free text to search titles, or use field filters:
          </Typography>

          <Box sx={{ display: 'flex', flexDirection: 'column', gap: 0.5 }}>
            {SEARCH_HELP.map((h) => (
              <Box
                key={h.example}
                onClick={() => handleHelpExampleClick(h.example)}
                sx={{ cursor: 'pointer', py: 0.5, px: 1, borderRadius: 1, '&:hover': { bgcolor: 'action.hover' } }}
              >
                <Typography variant="body2" sx={{ fontFamily: 'monospace', fontSize: '0.8rem', color: 'primary.main', wordBreak: 'break-word' }}>
                  {h.example}
                </Typography>
                <Typography variant="caption" color="text.secondary">
                  {h.desc}
                </Typography>
              </Box>
            ))}
          </Box>
          <Typography variant="body2" color="text.secondary" sx={{ mt: 1.5, mb: 0.5 }}>
            Available fields:
          </Typography>
          <Box display="flex" gap={0.5} flexWrap="wrap">
            {SEARCH_FIELDS.map((f) => (
              <Chip
                key={f}
                label={f}
                size="small"
                variant="outlined"
                onClick={() => handleHelpExampleClick(`${f}:`)}
                sx={{ cursor: 'pointer' }}
              />
            ))}
          </Box>

          {recentSearches.length > 0 && (
            <>
              <Typography variant="body2" color="text.secondary" sx={{ mt: 1.5, mb: 0.5 }}>
                Recent searches:
              </Typography>
              <Box display="flex" gap={0.5} flexWrap="wrap">
                {recentSearches.slice(0, 8).map((s) => (
                  <Chip
                    key={s}
                    label={s}
                    size="small"
                    onClick={() => { onChange(s); setHelpOpen(false); }}
                    sx={{ cursor: 'pointer' }}
                  />
                ))}
              </Box>
            </>
          )}
        </Paper>
      </Popper>
    </Box>
  );
};
