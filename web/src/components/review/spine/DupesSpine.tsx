// file: web/src/components/review/spine/DupesSpine.tsx
// version: 1.2.0
// guid: 9c4e7b21-6a58-4d03-8b7f-1e5d2a9c6403
// last-edited: 2026-08-21
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

import type { MouseEvent } from 'react';
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
 */
function BookSide({
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
    <Stack direction="row" spacing={1.5} sx={{ alignItems: 'stretch', minWidth: 0, flex: 1 }}>
      <Box
        sx={{
          width: 56,
          flexShrink: 0,
          borderRadius: 0.5,
          overflow: 'hidden',
          alignSelf: 'stretch',
          minHeight: 68,
          bgcolor: 'action.selected',
        }}
      >
        {book?.cover_url && (
          <Box
            component="img"
            src={book.cover_url}
            alt=""
            loading="lazy"
            sx={{ width: 56, height: '100%', objectFit: 'cover', display: 'block' }}
          />
        )}
      </Box>

      <Stack spacing={0.4} sx={{ minWidth: 0, flex: 1 }}>
        <Stack direction="row" spacing={0.5} useFlexGap sx={{ alignItems: 'center', flexWrap: 'wrap' }}>
          {missing ? (
            <Typography variant="body1" sx={{ color: 'error.main', fontStyle: 'italic' }}>
              (missing book — {id.slice(-8)})
            </Typography>
          ) : (
            <Link
              component={RouterLink}
              to={`/library/${id}`}
              underline="hover"
              onClick={(e: MouseEvent) => e.stopPropagation()}
              sx={{
                fontWeight: 600,
                fontSize: '1rem',
                textAlign: 'left',
                wordBreak: 'break-word',
                color: isGarbageTitle ? 'warning.main' : 'primary.main',
                fontStyle: isGarbageTitle ? 'italic' : 'normal',
              }}
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
          <Typography variant="caption" sx={{ color: 'text.secondary' }}>
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
}

function CandidateRow({
  candidate,
  ctx,
  twoColumn,
  index,
  pathAliases,
}: {
  candidate: DedupCandidate;
  ctx: DupesSpineContext;
  twoColumn: boolean;
  index: number;
  pathAliases: PathAlias[];
}) {
  const rec = recommendedKeepSide(candidate);
  const decided = candidate.status !== 'pending';
  const focused = ctx.focusedId === candidate.id;
  const expanded = ctx.expandedId === candidate.id;

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
      <Stack direction="row" spacing={1} sx={{ alignItems: 'center', mb: 1, flexWrap: 'wrap' }}>
        <Checkbox
          size="small"
          checked={ctx.isSelected(candidate.id)}
          onChange={(e) =>
            ctx.onToggleSelect(
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

        <Box sx={{ ml: 'auto' }}>
          <Button size="small" onClick={() => ctx.onOpenCompare(candidate.id)}>
            Compare
          </Button>
          {!twoColumn && (
            <Button size="small" onClick={() => ctx.onToggleExpand(candidate.id)}>
              {expanded ? 'Hide reasoning' : 'Why?'}
            </Button>
          )}
        </Box>
      </Stack>

      <Stack
        direction={{ xs: 'column', md: 'row' }}
        spacing={2}
        sx={{ alignItems: 'flex-start', minWidth: 0 }}
      >
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
        <Stack direction="row" spacing={1} sx={{ mt: 1.5, flexWrap: 'wrap' }} useFlexGap>
          <Button
            size="small"
            variant="outlined"
            onClick={() =>
              ctx.onAction({
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
              ctx.onAction({
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
            onClick={() => ctx.onAction({ lane: 'dupes', type: 'dismiss', id: candidate.id })}
          >
            Not a duplicate
          </Button>
        </Stack>
      )}

      {(twoColumn || expanded) && (
        <Box sx={{ mt: 2 }} data-testid="evidence-section">
          <Typography variant="subtitle2" gutterBottom>
            How this score was reached
          </Typography>
          <EvidencePanel evidence={dedupEvidence(candidate.score_breakdown)} />
        </Box>
      )}
    </Box>
  );
}

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
        <CandidateRow
          key={c.id}
          candidate={c}
          ctx={ctx}
          twoColumn={twoColumn}
          index={i}
          pathAliases={pathAliases}
        />
      ))}
    </Box>
  );
}
