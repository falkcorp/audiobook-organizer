// file: web/src/components/library/TagCloud.tsx
// version: 1.0.0
// guid: 7e6c9a1d-3f2b-4c8e-9a5d-1b6f8e2c4d9a
// last-edited: 2026-07-01

import { useMemo, useState } from 'react';
import { Box, Chip, Collapse, IconButton, Stack, Typography } from '@mui/material';
import { ExpandMore as ExpandMoreIcon } from '@mui/icons-material';

export interface TagCloudProps {
  availableTags: Array<{ tag: string; count: number }>;
  selectedTags: string[];
  onTagsChange: (tags: string[]) => void;
}

const MIN_FONT_SIZE = 0.75; // rem
const MAX_FONT_SIZE = 1.5; // rem

/**
 * Compute a font size (in rem) for a tag proportional to its count relative
 * to the maximum count in the set. Uses a log scale so a single outlier tag
 * doesn't dwarf the rest of the cloud, and clamps to [MIN_FONT_SIZE,
 * MAX_FONT_SIZE] so the layout stays stable.
 */
function fontSizeForCount(count: number, maxCount: number): number {
  if (maxCount <= 1) return MIN_FONT_SIZE;
  const logCount = Math.log(Math.max(count, 1) + 1);
  const logMax = Math.log(maxCount + 1);
  const ratio = logMax === 0 ? 0 : logCount / logMax;
  const size = MIN_FONT_SIZE + ratio * (MAX_FONT_SIZE - MIN_FONT_SIZE);
  return Math.min(MAX_FONT_SIZE, Math.max(MIN_FONT_SIZE, size));
}

/**
 * Browsable tag cloud for the main Library page. Reuses the existing
 * selectedTags/onTagsChange (handleTagFilterChange) state shared with
 * FilterSidebar so both UIs stay in sync. Tag chip font-size scales with
 * frequency (count) so heavily-used tags are visually larger.
 */
export function TagCloud({ availableTags, selectedTags, onTagsChange }: TagCloudProps) {
  const [expanded, setExpanded] = useState(true);

  const maxCount = useMemo(
    () => availableTags.reduce((max, t) => Math.max(max, t.count), 0),
    [availableTags]
  );

  if (availableTags.length === 0) {
    return null;
  }

  const handleToggleTag = (tag: string) => {
    const exists = selectedTags.includes(tag);
    const newTags = exists ? selectedTags.filter((x) => x !== tag) : [...selectedTags, tag];
    onTagsChange(newTags);
  };

  return (
    <Box sx={{ p: 1.5, border: 1, borderColor: 'divider', borderRadius: 1 }}>
      <Stack
        direction="row"
        alignItems="center"
        justifyContent="space-between"
        sx={{ cursor: 'pointer' }}
        onClick={() => setExpanded((prev) => !prev)}
      >
        <Typography variant="subtitle2">Browse by Tag</Typography>
        <IconButton
          size="small"
          aria-label={expanded ? 'Collapse tag cloud' : 'Expand tag cloud'}
          onClick={(e) => {
            e.stopPropagation();
            setExpanded((prev) => !prev);
          }}
        >
          <ExpandMoreIcon
            sx={{
              transform: expanded ? 'rotate(180deg)' : 'rotate(0deg)',
              transition: 'transform 0.2s',
            }}
          />
        </IconButton>
      </Stack>
      <Collapse in={expanded}>
        <Box sx={{ display: 'flex', gap: 0.75, flexWrap: 'wrap', mt: 1 }}>
          {availableTags.map((t) => {
            const isSelected = selectedTags.includes(t.tag);
            const fontSize = fontSizeForCount(t.count, maxCount);
            return (
              <Chip
                key={t.tag}
                label={`${t.tag} (${t.count})`}
                onClick={() => handleToggleTag(t.tag)}
                color={isSelected ? 'primary' : undefined}
                variant={isSelected ? 'filled' : 'outlined'}
                sx={{ fontSize: `${fontSize}rem`, height: 'auto', py: 0.5 }}
              />
            );
          })}
        </Box>
      </Collapse>
    </Box>
  );
}
