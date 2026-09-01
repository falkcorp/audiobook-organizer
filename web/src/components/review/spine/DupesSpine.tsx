// file: web/src/components/review/spine/DupesSpine.tsx
// version: 1.3.0
// guid: 9c4e7b21-6a58-4d03-8b7f-1e5d2a9c6403
// last-edited: 2026-09-01
//
// The duplicate-candidate renderer: book against book.
//
// A sibling of CompareSpine rather than a generic over it. That file's header
// reserved the decision for the moment a second real case arrived, on the
// grounds that a type parameter written earlier would have abstracted over "one
// real case and two guesses". The case arrived, and the answer is no: a
// CandidateResult is {book, candidate, status} -- one book and a proposal about
// it -- while a DedupCandidate is {id, book_a, book_b, band, score_breakdown},
// two books and a claim about the pair. They share a page layout, not a row
// shape, and the ids are not even the same primitive. A parameter over those
// two would have to be instantiated with a union of everything either side
// touches, which is how a generic ends up harder to read than the two concrete
// renderers it replaced.
//
// MEMOIZATION (2026-09-01). `perf(review): memoize spine rows so one checkbox
// re-renders one row` (d01f15a87) did CompareSpine and RegroupSpine and skipped
// this file, and benchmark-review-lanes.spec.ts then measured the consequence:
// at N=100 a dupes checkbox tick cost 61 ms against a 13 ms N=5 noise floor,
// while the memoized metadata lane's cost 26 ms. This file now follows the same
// shape as CompareSpine -- a stable `handlers` object, per-row VALUES resolved
// by the spine, and memo()-wrapped row renderers.

import { memo, useMemo, type MouseEvent } from 'react';
import { Link as RouterLink } from 'react-router-dom';
import {
  Box,
  Button,
  Checkbox,
  Chip,
  Link,
  Stack,
  Tooltip,
  Typography,
} from '@mui/material';
import StarIcon from '@mui/icons-material/Star';
import type { Book, DedupCandidate, PathAlias } from '../../../services/api';
import type { DupesAction } from '../reviewActions';
import { dedupEvidence } from '../evidence/adapters';
import { primarySignals } from '../evidence/signalLabels';
import { EvidencePanel } from '../evidence/EvidencePanel';
import { FolderFilesChip } from '../../dedup/FolderFilesChip';
import { metadataQuality, qualityBand, recommendedKeepSide } from '../lanes/keepDecision';
import { PathLinks, usePathAliases } from '../../common/PathLinks';
import type { SpineViewMode } from './CompareSpine';

export interface DupesSpineContext {
  isSelected: (id: number) => boolean;
  onToggleSelect: (id: number, index?: number, shiftKey?: boolean) => void;
  onAction: (action: DupesAction) => void;
  /** Row the keyboard is pointing at. Rendered as a ring, not as selection. */
  focusedId: number | null;
  /** Compact mode only. Single-open, matching the metadata spine. */
  expandedId: number | null;
  onToggleExpand: (id: number) => void;
  onOpenCompare: (id: number) => void;
}

/**
 * The CALLBACK half of DupesSpineContext, split out so CandidateRow can be
 * memoized. Mirrors CompareSpine's SpineHandlers, for the same reason.
 *
 * DupesPanel builds `ctx` as an object literal in its JSX, so `ctx` has a new
 * identity on EVERY render of the panel -- and a checkbox tick re-renders the
 * panel, because useDupesLane's `selectedIds` lives in ReviewWorkspace above
 * it. Handing the whole `ctx` to each row therefore gave all N rows a changed
 * prop in order to repaint one, which at the 100-row page cap is 99 wasted row
 * renders per click.
 *
 * The callbacks INSIDE `ctx` do hold still across a selection change:
 * `toggleSelect` is `useCallback(..., [visible])` and `visible` does not move
 * when a checkbox is ticked; `dispatch`'s deps do not include `selectedIds`;
 * `setDrawerCandidateId` is a setState setter; and `onToggleExpand` is a
 * `useCallback` in ReviewWorkspace as of this change -- it was an inline arrow,
 * which alone would have made every memo below inert.
 */
export type DupesSpineHandlers = Pick<
  DupesSpineContext,
  'onToggleSelect' | 'onAction' | 'onToggleExpand' | 'onOpenCompare'
>;

export interface DupesSpineProps {
  candidates: DedupCandidate[];
  viewMode: SpineViewMode;
  ctx: DupesSpineContext;
  emptyMessage: string;
  /**
   * Set when the ?book= deep link is active. The empty state changes meaning
   * with it: before entity_id was a server-side filter this said "none on this
   * page", which described a bug rather than the library.
   */
  deepLinkedBookId?: string | null;
}

const BAND_COLOR: Record<string, 'success' | 'info' | 'warning' | 'default'> = {
  CERTAIN: 'success',
  HIGH: 'info',
  MEDIUM: 'warning',
  REVIEW: 'default',
};

// Hoisted because they are constant. An `sx` object literal written inside the
// component body is a fresh object on every row render, which emotion then has
// to re-serialize before it can hit its cache. These have no dependency on any
// prop, so there is nothing to recompute.
const SX_SIDE_ROOT = { alignItems: 'stretch', minWidth: 0, flex: 1 } as const;
const SX_COVER = {
  width: 56,
  flexShrink: 0,
  borderRadius: 0.5,
  overflow: 'hidden',
  alignSelf: 'stretch',
  minHeight: 68,
  bgcolor: 'action.selected',
} as const;
const SX_COVER_IMG = { width: 56, height: '100%', objectFit: 'cover', display: 'block' } as const;
const SX_SIDE_BODY = { minWidth: 0, flex: 1 } as const;
const SX_SIDE_TITLE_ROW = { alignItems: 'center', flexWrap: 'wrap' } as const;
const SX_MISSING = { color: 'error.main', fontStyle: 'italic' } as const;
const SX_AUTHOR = { color: 'text.secondary' } as const;
// Two variants rather than one object with two ternaries inside it, so the
// common (non-garbage) case reuses one stable object.
const SX_TITLE_LINK = {
  fontWeight: 600,
  fontSize: '1rem',
  textAlign: 'left',
  wordBreak: 'break-word',
  color: 'primary.main',
  fontStyle: 'normal',
} as const;
const SX_TITLE_LINK_GARBAGE = {
  ...SX_TITLE_LINK,
  color: 'warning.main',
  fontStyle: 'italic',
} as const;
const SX_ROW_HEADER = { alignItems: 'center', mb: 1, flexWrap: 'wrap' } as const;
const SX_PUSH_RIGHT = { ml: 'auto' } as const;
const SX_SIDES = { alignItems: 'flex-start', minWidth: 0 } as const;
const SIDES_DIRECTION = { xs: 'column', md: 'row' } as const;
const SX_ACTIONS = { mt: 1.5, flexWrap: 'wrap' } as const;
const SX_EVIDENCE = { mt: 2 } as const;

const stopPropagation = (e: MouseEvent) => e.stopPropagation();

function QualityChip({ book }: { book: Book | null | undefined }) {
  const band = qualityBand(metadataQuality(book));
  if (band === 'rich') {
    return <Chip label="Rich metadata" size="small" color="success" variant="outlined" />;
  }
  if (band === 'partial') {
    return <Chip label="Partial metadata" size="small" color="warning" variant="outlined" />;
  }
  return <Chip label="Poor metadata" size="small" color="error" variant="outlined" />;
}

/**
 * One side of a pair. Ported from UnifiedDedupTab's renderBookCard, which was a
 * function returning JSX rather than a component -- fine there, but it meant the
 * cover image re-mounted on every parent render.
 *
 * memo()-wrapped on top of that. When a row DOES legitimately re-render -- its
 * own checkbox was ticked, or the keyboard focus ring arrived -- neither side's
 * props have changed, so both sides and the two FolderFilesChip/PathLinks
 * subtrees under them skip entirely. `pathAliases` is a useState value from
 * usePathAliases and `book` is a slice of the candidate object, so both hold
 * still; if either started churning this memo would go inert rather than wrong.
 */
const BookSide = memo(function BookSide({
  book,
  id,
  recommended,
  pathAliases,
}: {
  book: Book | null | undefined;
  id: string;
  recommended: boolean;
  pathAliases: PathAlias[];
}) {
  const missing = !book;
  const path = book?.file_path ?? '';
  const title = book?.title ?? '';
  // Same placeholder test as metadataQuality. A book titled "TITLE" or left
  // with a bare ULID is shown in warning colour so the reviewer can see WHY it
  // scored poorly, rather than being told it did.
  const isGarbageTitle =
    !title || title.toUpperCase() === 'TITLE' || /^[0-9A-Z]{26}$/.test(title.trim());

  return (
    <Stack direction="row" spacing={1.5} sx={SX_SIDE_ROOT}>
      <Box sx={SX_COVER}>
        {book?.cover_url && (
          <Box component="img" src={book.cover_url} alt="" loading="lazy" sx={SX_COVER_IMG} />
        )}
      </Box>

      <Stack spacing={0.4} sx={SX_SIDE_BODY}>
        <Stack direction="row" spacing={0.5} useFlexGap sx={SX_SIDE_TITLE_ROW}>
          {missing ? (
            <Typography variant="body1" sx={SX_MISSING}>
              (missing book — {id.slice(-8)})
            </Typography>
          ) : (
            <Link
              component={RouterLink}
              to={`/library/${id}`}
              underline="hover"
              onClick={stopPropagation}
              sx={isGarbageTitle ? SX_TITLE_LINK_GARBAGE : SX_TITLE_LINK}
            >
              {isGarbageTitle ? title || '(no title)' : title}
            </Link>
          )}
          <QualityChip book={book} />
          {recommended && (
            <Tooltip title="Richer metadata, so this side is recommended to keep">
              <Chip
                icon={<StarIcon />}
                label="Recommended"
                size="small"
                color="primary"
                variant="outlined"
                data-testid="recommended-keep"
              />
            </Tooltip>
          )}
        </Stack>

        {book?.author_name && (
          <Typography variant="caption" sx={SX_AUTHOR}>
            {book.author_name}
          </Typography>
        )}

        {path && <PathLinks path={path} aliases={pathAliases} />}

        {!missing && (
          <Box>
            <FolderFilesChip bookId={id} />
          </Box>
        )}
      </Stack>
    </Stack>
  );
});

/**
 * What one row needs. The three per-row STATE bits are resolved by the spine
 * and passed as plain booleans rather than looked up through the context, so a
 * row's props change only when that row's own state does -- which is what makes
 * the memo below able to skip.
 */
export interface CandidateRowProps {
  candidate: DedupCandidate;
  handlers: DupesSpineHandlers;
  selected: boolean;
  focused: boolean;
  /** Compact mode only; the two-column view always shows the evidence panel. */
  expanded: boolean;
  twoColumn: boolean;
  index: number;
  pathAliases: PathAlias[];
}

const CandidateRow = memo(function CandidateRow({
  candidate,
  handlers,
  selected,
  focused,
  expanded,
  twoColumn,
  index,
  pathAliases,
}: CandidateRowProps) {
  const rec = recommendedKeepSide(candidate);
  const decided = candidate.status !== 'pending';

  return (
    <Box
      data-testid={`dupes-row-${candidate.id}`}
      data-focused={focused ? 'true' : undefined}
      sx={{
        p: 1.5,
        mb: 1,
        border: 1,
        borderRadius: 1,
        borderColor: focused ? 'primary.main' : 'divider',
        // The focus ring is deliberately distinct from the selection tint: `j`
        // moving focus must not look like it selected anything, because
        // Shift+A and the bulk bar act on selection.
        boxShadow: focused ? (theme) => `0 0 0 2px ${theme.palette.primary.main}33` : undefined,
        opacity: decided ? 0.55 : 1,
      }}
    >
      <Stack direction="row" spacing={1} sx={SX_ROW_HEADER}>
        <Checkbox
          size="small"
          checked={selected}
          onChange={(e) =>
            handlers.onToggleSelect(
              candidate.id,
              index,
              // The React MouseEvent type is imported above and shadows the DOM
              // one, and a checkbox change can originate from the keyboard, so
              // this narrows structurally rather than by cast.
              (e.nativeEvent as Partial<{ shiftKey: boolean }>).shiftKey ?? false
            )
          }
          slotProps={{ input: { 'aria-label': `Select candidate ${candidate.id}` } }}
        />
        {candidate.band && (
          <Chip
            label={candidate.band}
            size="small"
            color={BAND_COLOR[candidate.band] ?? 'default'}
          />
        )}
        {candidate.score != null && (
          <Chip label={`Score ${candidate.score.toFixed(0)}`} size="small" variant="outlined" />
        )}
        <Chip label={candidate.layer} size="small" variant="outlined" />
        {/*
          Why the pair is here, without expanding anything. `layer` above names
          the collector that FOUND it; these name the evidence that justifies
          it, which is the question the reviewer is actually answering -- most
          of this queue is a single-signal pair, and none of the certain ones
          rest on a fuzzy title, so this is a read rather than a judgement.
          Supporting signals are deliberately absent -- see primarySignals.
        */}
        {primarySignals(candidate.score_breakdown).map((sig) => (
          <Chip
            key={sig.kind}
            label={sig.label}
            size="small"
            variant="outlined"
            color="info"
            data-testid={`signal-chip-${sig.kind}`}
          />
        ))}
        {decided && <Chip label={candidate.status} size="small" />}

        <Box sx={SX_PUSH_RIGHT}>
          <Button size="small" onClick={() => handlers.onOpenCompare(candidate.id)}>
            Compare
          </Button>
          {!twoColumn && (
            <Button size="small" onClick={() => handlers.onToggleExpand(candidate.id)}>
              {expanded ? 'Hide reasoning' : 'Why?'}
            </Button>
          )}
        </Box>
      </Stack>

      <Stack direction={SIDES_DIRECTION} spacing={2} sx={SX_SIDES}>
        <BookSide
          book={candidate.book_a}
          id={candidate.entity_a_id}
          recommended={rec?.label === 'A'}
          pathAliases={pathAliases}
        />
        <BookSide
          book={candidate.book_b}
          id={candidate.entity_b_id}
          recommended={rec?.label === 'B'}
          pathAliases={pathAliases}
        />
      </Stack>

      {!decided && (
        <Stack direction="row" spacing={1} sx={SX_ACTIONS} useFlexGap>
          <Button
            size="small"
            variant="outlined"
            onClick={() =>
              handlers.onAction({
                lane: 'dupes',
                type: 'merge',
                id: candidate.id,
                keepId: candidate.entity_a_id,
              })
            }
          >
            Keep A
          </Button>
          <Button
            size="small"
            variant="outlined"
            onClick={() =>
              handlers.onAction({
                lane: 'dupes',
                type: 'merge',
                id: candidate.id,
                keepId: candidate.entity_b_id,
              })
            }
          >
            Keep B
          </Button>
          <Button
            size="small"
            color="inherit"
            onClick={() => handlers.onAction({ lane: 'dupes', type: 'dismiss', id: candidate.id })}
          >
            Not a duplicate
          </Button>
        </Stack>
      )}

      {(twoColumn || expanded) && (
        <Box sx={SX_EVIDENCE} data-testid="evidence-section">
          <Typography variant="subtitle2" gutterBottom>
            How this score was reached
          </Typography>
          <EvidencePanel evidence={dedupEvidence(candidate.score_breakdown)} />
        </Box>
      )}
    </Box>
  );
});

export function DupesSpine({
  candidates,
  viewMode,
  ctx,
  emptyMessage,
  deepLinkedBookId,
}: DupesSpineProps) {
  const twoColumn = viewMode === 'two-column';
  // Called once here and threaded down to every row as a plain prop, matching
  // CompareSpine/RegroupSpine -- the render-only CandidateRow/BookSide pair
  // stays pure and doesn't each re-fetch config on its own.
  const pathAliases = usePathAliases();

  // The stable half of `ctx`. Keyed on the individual callbacks, NOT on `ctx`
  // itself: DupesPanel writes `ctx` as an object literal, so it has a new
  // identity on every panel render, while the callbacks inside it do not move.
  // This is what lets the memoized rows below actually skip.
  //
  // This MUST stay ABOVE the early return below. That return is conditional, so
  // a hook placed after it is skipped on the empty render and called on a
  // populated one -- React counts hooks by call order and throws "Rendered more
  // hooks than during the previous render" on the transition. tsc cannot see
  // this; only the order protects it, and DupesSpine.memo.test.tsx pins it.
  const handlers: DupesSpineHandlers = useMemo(
    () => ({
      onToggleSelect: ctx.onToggleSelect,
      onAction: ctx.onAction,
      onToggleExpand: ctx.onToggleExpand,
      onOpenCompare: ctx.onOpenCompare,
    }),
    [ctx.onToggleSelect, ctx.onAction, ctx.onToggleExpand, ctx.onOpenCompare]
  );

  if (candidates.length === 0) {
    return (
      <Box sx={{ p: 4, textAlign: 'center' }} data-testid="dupes-spine">
        <Typography color="text.secondary">
          {deepLinkedBookId
            ? // Truthful only because entity_id is filtered server-side. While
              // this was a client-side narrow of one page, an empty result here
              // meant "none on this page" and saying otherwise would have been
              // a lie the reviewer could not detect.
              'No duplicate candidates for this book.'
            : emptyMessage}
        </Typography>
      </Box>
    );
  }

  return (
    <Box data-testid="dupes-spine" data-view-mode={viewMode} sx={{ p: 1 }}>
      {candidates.map((c, i) => (
        // Resolved HERE, once per row, so each row receives plain values it can
        // be compared on rather than the whole churning context.
        <CandidateRow
          key={c.id}
          candidate={c}
          handlers={handlers}
          selected={ctx.isSelected(c.id)}
          focused={ctx.focusedId === c.id}
          expanded={ctx.expandedId === c.id}
          twoColumn={twoColumn}
          index={i}
          pathAliases={pathAliases}
        />
      ))}
    </Box>
  );
}
