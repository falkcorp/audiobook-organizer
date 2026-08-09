// file: web/src/components/library/TagCloud.tsx
// version: 1.1.0
// guid: 7e6c9a1d-3f2b-4c8e-9a5d-1b6f8e2c4d9a
// last-edited: 2026-08-08

import { useMemo, useState } from 'react';
import { Box, Button, Chip, Collapse, IconButton, Stack, Typography } from '@mui/material';
import { ExpandMore as ExpandMoreIcon } from '@mui/icons-material';

import { STORAGE_KEYS } from '../../lib/storageKeys';

export interface TagCloudProps {
  availableTags: Array<{ tag: string; count: number }>;
  selectedTags: string[];
  onTagsChange: (tags: string[]) => void;
}

const MIN_FONT_SIZE = 0.75; // rem
const MAX_FONT_SIZE = 1.5; // rem

/**
 * How many tags to show while the cloud is collapsed.
 *
 * A fully collapsed panel gives no hint that tags are worth browsing, so the
 * collapsed state still shows the busiest few rather than nothing at all.
 */
const PREVIEW_COUNT = 5;

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

function readStoredExpanded(): boolean {
  try {
    return localStorage.getItem(STORAGE_KEYS.LIBRARY_TAG_CLOUD_EXPANDED) === 'true';
  } catch {
    // Private browsing / storage disabled — fall back to the collapsed default.
    return false;
  }
}

/**
 * Browsable tag cloud for the main Library page. Reuses the existing
 * selectedTags/onTagsChange (handleTagFilterChange) state shared with
 * FilterSidebar so both UIs stay in sync. Tag chip font-size scales with
 * frequency (count) so heavily-used tags are visually larger.
 *
 * Starts COLLAPSED. On a library with hundreds of tags the expanded cloud is a
 * wall of chips that pushes the book grid below the fold, which is what the
 * page is actually for. Collapsed still shows the top few, so the feature stays
 * discoverable — a disclosure control that reveals nothing tells the user
 * nothing. The open/closed choice persists, so anyone who does want the full
 * cloud only has to say so once.
 */
export function TagCloud({ availableTags, selectedTags, onTagsChange }: TagCloudProps) {
  const [expanded, setExpanded] = useState(readStoredExpanded);

  // Sort here rather than trusting the caller. `availableTags` is passed
  // straight through from Library.tsx and is not guaranteed to arrive ordered
  // by count; until now only font size depended on `count`, where order is
  // irrelevant, so an unsorted list would have been invisible. Slicing a
  // preview off an unsorted list would silently show "the first five" instead
  // of "the busiest five".
  const sortedTags = useMemo(
    () => [...availableTags].sort((a, b) => b.count - a.count || a.tag.localeCompare(b.tag)),
    [availableTags]
  );

  const maxCount = useMemo(
    () => availableTags.reduce((max, t) => Math.max(max, t.count), 0),
    [availableTags]
  );

  // While collapsed, always include any SELECTED tag that falls outside the
  // preview. Hiding an active filter would leave the user looking at a filtered
  // list with no visible control to clear it.
  const previewTags = useMemo(() => {
    const top = sortedTags.slice(0, PREVIEW_COUNT);
    const shown = new Set(top.map((t) => t.tag));
    const selectedOutside = sortedTags.filter((t) => selectedTags.includes(t.tag) && !shown.has(t.tag));
    return [...top, ...selectedOutside];
  }, [sortedTags, selectedTags]);

  if (availableTags.length === 0) {
    return null;
  }

  const setExpandedPersisted = (next: boolean) => {
    setExpanded(next);
    try {
      localStorage.setItem(STORAGE_KEYS.LIBRARY_TAG_CLOUD_EXPANDED, String(next));
    } catch {
      // Non-fatal: the panel still works for this session.
    }
  };

  const handleToggleTag = (tag: string) => {
    const exists = selectedTags.includes(tag);
    const newTags = exists ? selectedTags.filter((x) => x !== tag) : [...selectedTags, tag];
    onTagsChange(newTags);
  };

  const renderChip = (t: { tag: string; count: number }) => {
    const isSelected = selectedTags.includes(t.tag);
    return (
      <Chip
        key={t.tag}
        label={`${t.tag} (${t.count})`}
        onClick={() => handleToggleTag(t.tag)}
        color={isSelected ? 'primary' : undefined}
        variant={isSelected ? 'filled' : 'outlined'}
        sx={{ fontSize: `${fontSizeForCount(t.count, maxCount)}rem`, height: 'auto', py: 0.5 }}
      />
    );
  };

  const hiddenCount = sortedTags.length - previewTags.length;

  return (
    <Box sx={{ p: 1.5, border: 1, borderColor: 'divider', borderRadius: 1 }}>
      <Stack
        direction="row"
        alignItems="center"
        justifyContent="space-between"
        sx={{ cursor: 'pointer' }}
        onClick={() => setExpandedPersisted(!expanded)}
      >
        <Typography variant="subtitle2">
          Browse by Tag{!expanded && sortedTags.length > 0 ? ` (${sortedTags.length})` : ''}
        </Typography>
        <IconButton
          size="small"
          aria-label={expanded ? 'Collapse tag cloud' : 'Expand tag cloud'}
          onClick={(e) => {
            e.stopPropagation();
            setExpandedPersisted(!expanded);
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

      {!expanded && (
        <Box sx={{ display: 'flex', gap: 0.75, flexWrap: 'wrap', mt: 1, alignItems: 'center' }}>
          {previewTags.map(renderChip)}
          {hiddenCount > 0 && (
            <Button size="small" onClick={() => setExpandedPersisted(true)}>
              Show all {sortedTags.length}
            </Button>
          )}
        </Box>
      )}

      {/*
        unmountOnExit is required, not cosmetic. Collapse keeps its children
        mounted by default, so without it the collapsed state renders every
        chip twice — once in the preview above, once hidden in here. That is a
        duplicated accessible name for every tag, and it breaks any query that
        expects a tag to appear once.
      */}
      <Collapse in={expanded} unmountOnExit>
        <Box sx={{ display: 'flex', gap: 0.75, flexWrap: 'wrap', mt: 1 }}>
          {sortedTags.map(renderChip)}
        </Box>
      </Collapse>
    </Box>
  );
}
