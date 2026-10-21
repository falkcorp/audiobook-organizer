// file: web/src/components/library/TagCloud.tsx
// version: 1.6.1
// guid: 7e6c9a1d-3f2b-4c8e-9a5d-1b6f8e2c4d9a
// last-edited: 2026-10-21
import { useLayoutEffect, useMemo, useRef, useState } from 'react';
import type { RefObject } from 'react';
import {
  Box,
  Button,
  Chip,
  CircularProgress,
  IconButton,
  Skeleton,
  Stack,
  Typography,
  useTheme,
} from '@mui/material';
import type { Theme } from '@mui/material';
import { ExpandMore as ExpandMoreIcon } from '@mui/icons-material';

import { STORAGE_KEYS } from '../../lib/storageKeys';

export interface TagCloudProps {
  availableTags: Array<{ tag: string; count: number }>;
  selectedTags: string[];
  onTagsChange: (tags: string[]) => void;
  /**
   * Shown when the answer is in and no tag is displayable (an empty answer, or
   * one made only of hidden namespaces). The panel is never removed: a scoped
   * cloud whose results carry no tags says so instead of vanishing.
   */
  emptyMessage: string;
  /**
   * The answer for the current query is outstanding. With no tags yet this
   * renders skeleton chips; with tags already shown (a reload) it keeps them,
   * dimmed, with a small spinner — the panel never collapses.
   */
  loading?: boolean;
  /**
   * The current query's request failed. Rendered in the panel, in place of
   * chips, in the same fixed-height body as every other state.
   */
  errorMessage?: string;
  /**
   * Another error on the page already says the query failed (and is announced
   * as an alert). Announce this one politely instead of as a second alert.
   */
  errorIsSecondary?: boolean;
}

const MIN_FONT_SIZE = 0.75; // rem
const MAX_FONT_SIZE = 1.5; // rem

// Chip geometry the fixed heights below are derived from. The chip's line
// height is set explicitly (rather than inherited) so that the arithmetic in
// chipRowHeight is true by construction, not by the current theme's slack.
const CHIP_LINE_HEIGHT = 1.5;
const CHIP_BORDER_PX = 1; // MUI outlined chip border; filled chips have none
const EXPANDED_ROWS = 4;
const SKELETON_WIDTHS = [96, 72, 120, 84, 104];

/**
 * Tags shown while collapsed when the container cannot be measured (first
 * render, and environments with no layout such as jsdom). Real layouts show as
 * many as fit on one row; see useFitCount.
 */
const FALLBACK_PREVIEW_COUNT = 5;

/**
 * Upper bound on a chip's height: the tallest chip is MAX_FONT_SIZE text on
 * CHIP_LINE_HEIGHT, plus the chip's vertical padding (py: 0.5 on each side, so
 * spacing(1) in total) and its border on both edges. Every chip is at most
 * this tall, so a row of this height never clips one.
 */
function chipRowHeight(theme: Theme): string {
  return `calc(${MAX_FONT_SIZE * CHIP_LINE_HEIGHT}rem + ${theme.spacing(1)} + ${2 * CHIP_BORDER_PX}px)`;
}

/** The fixed height of the panel body: one chip row collapsed, EXPANDED_ROWS expanded. */
function bodyHeight(theme: Theme, expanded: boolean): string {
  const row = chipRowHeight(theme);
  if (!expanded) return row;
  return `calc(${EXPANDED_ROWS} * ${row} + ${EXPANDED_ROWS - 1} * ${theme.spacing(0.75)})`;
}

/**
 * Returns true for tags that are internal bookkeeping and should never
 * appear as chips in the Browse by Tag cloud: `dedup:*` (pure dedup-engine
 * bookkeeping, e.g. `dedup:duration-match`) and `metadata:source:*` (last
 * metadata-apply provenance). This is deliberately display-only — the tag
 * itself is untouched, so it stays selectable in FilterSidebar and any other
 * consumer of the same `availableTags` array; only this widget's own chip
 * list hides it.
 */
function isHiddenTagNamespace(tag: string): boolean {
  return tag.startsWith('dedup:') || tag.startsWith('metadata:source:');
}

/**
 * Formats a `metadata:<key>:<value>` tag (e.g. `metadata:language:en`) as
 * `key: value` (`language: en`) for display. Any other tag, including a
 * malformed `metadata:*` tag with fewer than two colons (`metadata:` or
 * `metadata:foo`), passes through unchanged rather than throwing or
 * rendering `undefined`. Display-only: the raw tag string is still what
 * `onTagsChange` receives when the chip is clicked.
 */
export function formatTagLabel(tag: string): string {
  if (!tag.startsWith('metadata:')) return tag;
  const parts = tag.split(':');
  if (parts.length < 3) return tag;
  const key = parts[1];
  const value = parts.slice(2).join(':');
  return `${key}: ${value}`;
}

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
 * page is actually for. Collapsed still shows the busiest tags that fit on one
 * row, so the feature stays discoverable. The open/closed choice persists.
 *
 * LAYOUT CONTRACT: the body is a fixed height — one chip row collapsed,
 * EXPANDED_ROWS rows (scrolling inside) expanded — in EVERY state (loading,
 * reload, empty, error, populated) and at every width. The Select All bar and
 * card checkboxes below the panel therefore never move when a facets answer
 * arrives, which is what a click aimed at them depends on.
 */
export function TagCloud({
  availableTags,
  selectedTags,
  onTagsChange,
  emptyMessage,
  loading = false,
  errorMessage,
  errorIsSecondary = false,
}: TagCloudProps) {
  const theme = useTheme();
  const [expanded, setExpanded] = useState(readStoredExpanded);

  // Hide internal-bookkeeping namespaces (dedup:*, metadata:source:*) from
  // this widget's own chip list — display-only, so `availableTags` itself is
  // never mutated and other consumers (FilterSidebar, etc.) are unaffected.
  // A tag that is hidden by namespace but currently SELECTED is kept in, so
  // an active filter always has a visible control to clear it. "Empty" is
  // decided on THIS list, not on the raw answer.
  const visibleTags = useMemo(
    () => availableTags.filter((t) => !isHiddenTagNamespace(t.tag) || selectedTags.includes(t.tag)),
    [availableTags, selectedTags]
  );

  // Sort here rather than trusting the caller: slicing a preview off an
  // unsorted list would silently show "the first few" instead of "the busiest".
  const sortedTags = useMemo(
    () => [...visibleTags].sort((a, b) => b.count - a.count || a.tag.localeCompare(b.tag)),
    [visibleTags]
  );

  const maxCount = useMemo(
    () => visibleTags.reduce((max, t) => Math.max(max, t.count), 0),
    [visibleTags]
  );

  // Collapsed priority order: SELECTED tags first (an active filter must stay
  // visible and clearable), then the rest by count. The row shows the first
  // `fitCount` of these; the on-screen order is still by count.
  const priorityTags = useMemo(
    () => [
      ...sortedTags.filter((t) => selectedTags.includes(t.tag)),
      ...sortedTags.filter((t) => !selectedTags.includes(t.tag)),
    ],
    [sortedTags, selectedTags]
  );

  const rowRef = useRef<HTMLDivElement>(null);
  const { fit, measuring } = useFitCount(rowRef, priorityTags, !expanded);

  // While measuring, every candidate renders in PRIORITY order (unwrapped,
  // clipped) so real widths can be read; afterwards only the ones that fit,
  // in display (count) order. The fit count is a count of PRIORITY slots, so
  // it must be applied to the priority list and measured against it.
  const previewTags = useMemo(() => {
    if (measuring) return priorityTags;
    const count = fit ?? FALLBACK_PREVIEW_COUNT;
    const shown = new Set(priorityTags.slice(0, count).map((t) => t.tag));
    return sortedTags.filter((t) => shown.has(t.tag));
  }, [priorityTags, sortedTags, fit, measuring]);

  // Reloading with a previous answer on screen: keep it, dimmed.
  const reloading = loading && visibleTags.length > 0;

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
        label={`${formatTagLabel(t.tag)} (${t.count})`}
        data-tag={t.tag}
        onClick={() => handleToggleTag(t.tag)}
        color={isSelected ? 'primary' : undefined}
        variant={isSelected ? 'filled' : 'outlined'}
        sx={{
          fontSize: `${fontSizeForCount(t.count, maxCount)}rem`,
          lineHeight: CHIP_LINE_HEIGHT,
          height: 'auto',
          py: 0.5,
          flexShrink: 0,
          maxWidth: '100%',
        }}
      />
    );
  };

  const hiddenCount = sortedTags.length - previewTags.length;
  const state: 'error' | 'loading' | 'empty' | 'populated' = errorMessage
    ? 'error'
    : visibleTags.length > 0
      ? 'populated'
      : loading
        ? 'loading'
        : 'empty';

  return (
    <Box
      data-testid="tag-cloud-panel"
      data-state={state}
      aria-busy={loading}
      sx={{ p: 1.5, border: 1, borderColor: 'divider', borderRadius: 1 }}
    >
      <Stack
        direction="row"
        onClick={() => setExpandedPersisted(!expanded)}
        sx={{
          alignItems: 'center',
          justifyContent: 'space-between',
          cursor: 'pointer',
        }}
      >
        <Typography variant="subtitle2">
          Browse by Tag{!expanded && sortedTags.length > 0 ? ` (${sortedTags.length})` : ''}
        </Typography>
        {loading && (
          <CircularProgress
            size={14}
            aria-label="Loading tags"
            sx={{ ml: 'auto', mr: 1 }}
            data-testid="tag-cloud-spinner"
          />
        )}
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
              transition: 'transform 0.2s',
              transform: expanded ? 'rotate(180deg)' : 'rotate(0deg)',
            }}
          />
        </IconButton>
      </Stack>

      {/*
        One body, one fixed height, for every state. Collapsed it is a single
        non-wrapping row that clips; expanded it wraps and scrolls inside. A
        body whose height followed its content is what made the Select All bar
        jump by a different amount at every width.
      */}
      <Box
        ref={rowRef}
        data-testid="tag-cloud-body"
        sx={{
          mt: 1,
          height: bodyHeight(theme, expanded),
          display: 'flex',
          gap: 0.75,
          flexWrap: expanded ? 'wrap' : 'nowrap',
          alignContent: 'flex-start',
          alignItems: 'center',
          overflowX: 'hidden',
          overflowY: expanded ? 'auto' : 'hidden',
          opacity: reloading ? 0.5 : 1,
          transition: 'opacity 0.15s',
        }}
      >
        {state === 'error' && (
          <Typography
            variant="body2"
            color="error"
            role={errorIsSecondary ? 'status' : 'alert'}
            aria-live={errorIsSecondary ? 'polite' : undefined}
          >
            {errorMessage}
          </Typography>
        )}
        {state === 'loading' &&
          SKELETON_WIDTHS.map((w) => (
            <Skeleton
              key={w}
              variant="rounded"
              width={w}
              height={32}
              sx={{ borderRadius: 4, flexShrink: 0 }}
              data-testid="tag-cloud-skeleton"
            />
          ))}
        {state === 'empty' && (
          <Typography variant="body2" color="text.secondary">
            {emptyMessage}
          </Typography>
        )}
        {state === 'populated' && (
          <>
            {(expanded ? sortedTags : previewTags).map((t) => renderChip(t))}
            {!expanded && (measuring || hiddenCount > 0) && (
              <Button
                size="small"
                sx={{ flexShrink: 0 }}
                onClick={() => setExpandedPersisted(true)}
              >
                Show all {sortedTags.length}
              </Button>
            )}
          </>
        )}
      </Box>
    </Box>
  );
}

/**
 * How many of `tags` (already in priority order) fit on ONE row of the
 * collapsed body, leaving room for the "Show all N" button when some do not.
 *
 * Two passes, both inside one layout effect so the browser never paints the
 * first: while `measuring`, the caller renders EVERY candidate plus the button
 * in the (non-wrapping, clipped) row; this reads their real widths — chip width
 * depends on its label and its count-scaled font, so it cannot be computed —
 * and stores how many fit, tagged with the inputs it was measured for. When
 * the tags or the row's width change, the stored answer is stale and the
 * caller measures again. A ResizeObserver feeds the row's width in, so a window
 * resize or sidebar toggle reflows the preview.
 *
 * `fit` stays null where there is no layout (jsdom, first render); callers fall
 * back to FALLBACK_PREVIEW_COUNT.
 */
function useFitCount(
  rowRef: RefObject<HTMLDivElement | null>,
  tags: Array<{ tag: string; count: number }>,
  enabled: boolean
): { fit: number | null; measuring: boolean } {
  const [width, setWidth] = useState(0);
  const [measured, setMeasured] = useState<{ key: string; count: number | null } | null>(null);
  const key = `${width}|${tags.map((t) => `${t.tag}:${t.count}`).join('\u0000')}`;
  const measuring = enabled && measured?.key !== key;

  // The row's width, kept current by a ResizeObserver.
  useLayoutEffect(() => {
    const row = rowRef.current;
    if (!row || typeof ResizeObserver === 'undefined') return;
    const observer = new ResizeObserver(() => setWidth(row.clientWidth));
    observer.observe(row);
    return () => observer.disconnect();
  }, [rowRef]);

  useLayoutEffect(() => {
    if (!measuring) return;
    const row = rowRef.current;
    const avail = row?.clientWidth ?? 0;
    let count: number | null = null;
    if (row && avail > 0) {
      // Width by tag, not by DOM position: the row's order is the caller's
      // business, the sum below must follow priority order regardless.
      const byTag = new Map<string, number>();
      let buttonWidth = 0;
      for (const kid of Array.from(row.children) as HTMLElement[]) {
        const w = kid.getBoundingClientRect().width;
        const tag = kid.dataset.tag;
        if (tag === undefined) buttonWidth = w;
        else byTag.set(tag, w);
      }
      const widths = tags.map((t) => byTag.get(t.tag) ?? 0);
      const gap = parseFloat(getComputedStyle(row).columnGap) || 0;
      const total = widths.length;
      const chipsWidth = (k: number) =>
        widths.slice(0, k).reduce((sum, w) => sum + w, 0) + Math.max(0, k - 1) * gap;
      count = 0;
      for (let k = total; k >= 0; k--) {
        const needed =
          k === total ? chipsWidth(k) : chipsWidth(k) + (k > 0 ? gap : 0) + buttonWidth;
        if (needed <= avail) {
          count = k;
          break;
        }
      }
    }
    // Layout measurement is the legitimate reason to set state from an effect:
    // the value does not exist until the browser has laid the row out.
    setMeasured({ key, count });
  }, [measuring, key, rowRef, tags]);

  return { fit: measured?.count ?? null, measuring };
}
