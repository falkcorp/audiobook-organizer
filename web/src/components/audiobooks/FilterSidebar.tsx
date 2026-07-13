// file: web/src/components/audiobooks/FilterSidebar.tsx
// version: 1.7.0
// guid: 2e3f4a5b-6c7d-8e9f-0a1b-2c3d4e5f6a7b

import React from 'react';
import {
  Drawer,
  Box,
  Typography,
  Button,
  Divider,
  FormControl,
  FormControlLabel,
  InputLabel,
  Select,
  MenuItem,
  Chip,
  Stack,
  Switch,
  Autocomplete,
  TextField,
  Slider,
} from '@mui/material';
import { FilterList as FilterListIcon } from '@mui/icons-material';
import type { FilterOptions } from '../../types';
import { formatTagLabel } from '../../utils/tagDisplay';

interface FilterSidebarProps {
  open: boolean;
  onClose: () => void;
  filters: FilterOptions;
  onFiltersChange: (filters: FilterOptions) => void;
  authors?: string[];
  series?: string[];
  genres?: string[];
  languages?: string[];
  availableTags?: Array<{ tag: string; count: number }>;
  selectedTags?: string[];
  onTagsChange?: (tags: string[]) => void;
}

export const FilterSidebar: React.FC<FilterSidebarProps> = ({
  open,
  onClose,
  filters,
  onFiltersChange,
  authors = [],
  series = [],
  genres = [],
  languages = [],
  availableTags = [],
  selectedTags = [],
  onTagsChange,
}) => {
  const handleFilterChange = (key: keyof FilterOptions, value: string | number | boolean) => {
    onFiltersChange({ ...filters, [key]: value || undefined });
  };

  const handleClearFilters = () => {
    onFiltersChange({});
    onTagsChange?.([]);
  };

  const getActiveFilterCount = () => {
    let count = Object.values(filters).filter(
      (v) => v !== undefined && v !== '' && !(Array.isArray(v) && v.length === 0)
    ).length;
    if (selectedTags.length > 0) count++;
    return count;
  };

  const tagOptions = availableTags.map((t) => t.tag);

  return (
    <Drawer anchor="right" open={open} onClose={onClose}>
      <Box sx={{ width: 320, p: 3 }}>
        <Box
          display="flex"
          alignItems="center"
          justifyContent="space-between"
          mb={2}
        >
          <Box display="flex" alignItems="center" gap={1}>
            <FilterListIcon />
            <Typography variant="h6">Filters</Typography>
            {getActiveFilterCount() > 0 && (
              <Chip
                label={getActiveFilterCount()}
                size="small"
                color="primary"
              />
            )}
          </Box>
          <Button size="small" onClick={handleClearFilters}>
            Clear All
          </Button>
        </Box>

        <Divider sx={{ mb: 3 }} />

        <Stack spacing={3}>
          <FormControl fullWidth>
            <InputLabel id="filter-library-state-label">Library State</InputLabel>
            <Select
              labelId="filter-library-state-label"
              value={filters.libraryState || ''}
              onChange={(e) =>
                handleFilterChange('libraryState', e.target.value)
              }
              label="Library State"
            >
              <MenuItem value="">
                <em>All States</em>
              </MenuItem>
              <MenuItem value="organized">Organized</MenuItem>
              <MenuItem value="imported">Imported</MenuItem>
              <MenuItem value="deleted">Deleted</MenuItem>
              <MenuItem value="suspicious">Suspicious</MenuItem>
            </Select>
          </FormControl>

          <FormControl fullWidth>
            <InputLabel id="filter-author-label">Author</InputLabel>
            <Select
              labelId="filter-author-label"
              value={filters.author || ''}
              onChange={(e) => handleFilterChange('author', e.target.value)}
              label="Author"
            >
              <MenuItem value="">
                <em>All Authors</em>
              </MenuItem>
              {authors.map((author) => (
                <MenuItem key={author} value={author}>
                  {author}
                </MenuItem>
              ))}
            </Select>
          </FormControl>

          <FormControl fullWidth>
            <InputLabel id="filter-series-label">Series</InputLabel>
            <Select
              labelId="filter-series-label"
              value={filters.series || ''}
              onChange={(e) => handleFilterChange('series', e.target.value)}
              label="Series"
            >
              <MenuItem value="">
                <em>All Series</em>
              </MenuItem>
              {series.map((s) => (
                <MenuItem key={s} value={s}>
                  {s}
                </MenuItem>
              ))}
            </Select>
          </FormControl>

          <FormControl fullWidth>
            <InputLabel id="filter-genre-label">Genre</InputLabel>
            <Select
              labelId="filter-genre-label"
              value={filters.genre || ''}
              onChange={(e) => handleFilterChange('genre', e.target.value)}
              label="Genre"
            >
              <MenuItem value="">
                <em>All Genres</em>
              </MenuItem>
              {genres.map((genre) => (
                <MenuItem key={genre} value={genre}>
                  {genre}
                </MenuItem>
              ))}
            </Select>
          </FormControl>

          <FormControl fullWidth>
            <InputLabel id="filter-language-label">Language</InputLabel>
            <Select
              labelId="filter-language-label"
              value={filters.language || ''}
              onChange={(e) => handleFilterChange('language', e.target.value)}
              label="Language"
            >
              <MenuItem value="">
                <em>All Languages</em>
              </MenuItem>
              {languages.map((lang) => (
                <MenuItem key={lang} value={lang}>
                  {lang}
                </MenuItem>
              ))}
            </Select>
          </FormControl>

          {onTagsChange && (
            <div>
              <Typography variant="subtitle2" gutterBottom>
                Tags
              </Typography>

              <Box sx={{ display: 'flex', gap: 0.5, flexWrap: 'wrap', maxHeight: 140, overflowY: 'auto', mb: 1 }}>
                {availableTags.map((t) => (
                  <Chip
                    key={t.tag}
                    label={`${formatTagLabel(t.tag)} (${t.count})`}
                    size="small"
                    onClick={() => {
                      const exists = selectedTags.includes(t.tag);
                      const newTags = exists ? selectedTags.filter((x) => x !== t.tag) : [...selectedTags, t.tag];
                      onTagsChange(newTags);
                    }}
                    color={selectedTags.includes(t.tag) ? 'primary' : undefined}
                    variant={selectedTags.includes(t.tag) ? 'filled' : 'outlined'}
                  />
                ))}
              </Box>

              <Autocomplete
                multiple
                size="small"
                options={tagOptions}
                value={selectedTags}
                onChange={(_e, value) => onTagsChange(value)}
                renderTags={(value, getTagProps) =>
                  value.map((tag, index) => {
                    const { key, ...rest } = getTagProps({ index });
                    return (
                      <Chip
                        key={key}
                        label={formatTagLabel(tag)}
                        size="small"
                        variant="outlined"
                        {...rest}
                      />
                    );
                  })
                }
                renderOption={(props, option) => {
                  const tagInfo = availableTags.find((t) => t.tag === option);
                  const { key, ...rest } = props as React.HTMLAttributes<HTMLLIElement> & { key: string };
                  return (
                    <li key={key} {...rest}>
                      <Box
                        sx={{
                          display: 'flex',
                          justifyContent: 'space-between',
                          width: '100%',
                        }}
                      >
                        <span>{formatTagLabel(option)}</span>
                        {tagInfo && (
                          <Typography
                            variant="caption"
                            color="text.secondary"
                          >
                            ({tagInfo.count})
                          </Typography>
                        )}
                      </Box>
                    </li>
                  );
                }}
                renderInput={(params) => (
                  <TextField
                    {...params}
                    placeholder={
                      selectedTags.length === 0 ? 'Filter by tags...' : ''
                    }
                  />
                )}
              />
              {selectedTags.length > 0 && (
                <Typography
                  variant="caption"
                  color="text.secondary"
                  sx={{ mt: 0.5, display: 'block' }}
                >
                  Books must have ALL selected tags
                </Typography>
              )}
            </div>
          )}

          <Divider />
          <FormControlLabel
            control={
              <Switch
                checked={!!filters.showFailed}
                onChange={(e) =>
                  onFiltersChange({ ...filters, showFailed: e.target.checked })
                }
                color="error"
              />
            }
            label={
              <Box display="flex" alignItems="center" gap={0.5}>
                <Typography variant="body2">Show Failed</Typography>
                <Chip label=".failed/" size="small" color="error" variant="outlined" />
              </Box>
            }
          />

          <Divider />
          <Typography variant="subtitle2" gutterBottom>
            Fingerprinting
          </Typography>

          <FormControl fullWidth>
            <InputLabel id="filter-fingerprint-status-label">Fingerprint Status</InputLabel>
            <Select
              labelId="filter-fingerprint-status-label"
              value={filters.fingerprintStatus || ''}
              onChange={(e) =>
                onFiltersChange({
                  ...filters,
                  fingerprintStatus: (e.target.value as "complete" | "partial" | "none") || undefined,
                })
              }
              label="Fingerprint Status"
            >
              <MenuItem value="">
                <em>Any Status</em>
              </MenuItem>
              <MenuItem value="complete">Complete</MenuItem>
              <MenuItem value="partial">Partial</MenuItem>
              <MenuItem value="none">None</MenuItem>
            </Select>
          </FormControl>

          <Box sx={{ mt: 2 }}>
            <Box display="flex" justifyContent="space-between" mb={1}>
              <Typography variant="body2">Coverage %</Typography>
              <Typography variant="caption" color="text.secondary">
                {filters.coveragePercentMin ?? 0}% - {filters.coveragePercentMax ?? 100}%
              </Typography>
            </Box>
            <Slider
              value={[filters.coveragePercentMin ?? 0, filters.coveragePercentMax ?? 100]}
              onChange={(_e, value) => {
                if (Array.isArray(value)) {
                  const [min, max] = value;
                  onFiltersChange({
                    ...filters,
                    coveragePercentMin: min > 0 ? min : undefined,
                    coveragePercentMax: max < 100 ? max : undefined,
                  });
                }
              }}
              min={0}
              max={100}
              step={1}
            />
          </Box>

        </Stack>
      </Box>
    </Drawer>
  );
};
