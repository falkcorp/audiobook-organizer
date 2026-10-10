// file: web/src/components/review/spine/CandidatesCard.tsx
// version: 1.3.1
// guid: ceb6a375-997d-44e0-8c71-d83c1980ea33
// last-edited: 2026-10-10
//
// The Candidates view's card (owner-approved 2026-10-07): the book's full info
// on the LEFT, and on the RIGHT the full ranked candidate list the per-book
// Search Metadata dialog shows -- same endpoint -- with Apply / Reject per
// candidate, the recorded scoring derivation behind a toggle, and a per-book
// "Search again" whose title/author retarget the search.
//
// Requests go through the spine's CandidateLoader (at most 4 in flight, only
// for cards on screen) and applies through the panel's `apply` (the dialog's
// background per-book apply, at most 4 in flight). Until the list arrives the
// row's cached top candidate is shown, so the card is never empty while the
// search runs.
//
// "Search again" is a BROWSE search (browse: true): what was typed, not the
// book's identity. An author alone lists that author's books (the local
// author catalog first, shown at once, then the live answers), a partial
// title narrows them, and every answer is listed -- no filtering to the
// book's own title. The cached pick is not shown in its place while a browse
// search runs: it is the answer to a different question.
//
// Every candidate row carries a thumbs-down, "Not the best match" (owner
// request 2026-10-07). It is NOT a reject and NOT a skip: it never touches the
// book's review state and never hides the candidate. It records a labelled
// negative example (book, the query, the candidate, its score and derivation)
// as scorer training data, and marks the row; a second click undoes it. An
// Apply that lands records the applied candidate as the positive for the same
// book and query. See internal/database/candidate_feedback.go.

import {
  Alert,
  Avatar,
  Box,
  Button,
  Checkbox,
  Chip,
  CircularProgress,
  Collapse,
  IconButton,
  Stack,
  TextField,
  Tooltip,
  Typography,
} from '@mui/material';
import ThumbDownIcon from '@mui/icons-material/ThumbDown';
import ThumbDownOutlinedIcon from '@mui/icons-material/ThumbDownOutlined';
import { memo, useCallback, useEffect, useRef, useState, useSyncExternalStore } from 'react';
import * as api from '../../../services/api';
import type { MetadataCandidate } from '../../../services/api';
import { EvidencePanel } from '../evidence/EvidencePanel';
import { metadataEvidence, type CandidateDetailState } from '../evidence/adapters';
import { sameCandidate } from '../../audiobooks/stagedMetadataApply';
import { BookInfoPanel } from './BookInfoPanel';
import { candidateKey, type CandidateLoader, type CandidateQuery } from './candidateLoader';
import {
  SOURCE_COLORS,
  SPINE_TWO_COLUMN_MIN,
  getRowSx,
  isRowActionable,
  noCandidateLabel,
  scoreColor,
} from './rowState';
import type { SpineRowProps } from './CompareSpine';
import { rankScoreOf } from '../../audiobooks/rankScore';

/** What the candidates view needs beyond a row's props; built once by the panel. */
export interface CandidatesContext {
  loader: CandidateLoader;
  /**
   * Apply one candidate to one book through the per-book background apply.
   * Resolves true only when the apply landed.
   */
  apply: (bookId: string, candidate: MetadataCandidate) => Promise<boolean>;
}

/**
 * A candidate's identity for its thumbs-down mark: provider and the ids and
 * names it carries -- not its list position, which moves as results arrive.
 */
export function candidateIdentity(c: MetadataCandidate): string {
  return [
    c.source,
    c.asin ?? '',
    c.isbn13 ?? '',
    c.isbn10 ?? '',
    c.isbn ?? '',
    c.title,
    c.author ?? '',
  ]
    .map((v) => String(v).trim().toLowerCase())
    .join('\u0000');
}

/** A thumbs-down mark: `id` once the server has recorded it. */
interface FeedbackMark {
  id?: string;
}

/** True once the element is on screen; true at once where there is no observer. */
function useInView(ref: React.RefObject<HTMLElement | null>): boolean {
  const [inView, setInView] = useState(() => typeof IntersectionObserver === 'undefined');
  useEffect(() => {
    const el = ref.current;
    if (!el || typeof IntersectionObserver === 'undefined') return;
    const io = new IntersectionObserver(
      (entries) => {
        for (const e of entries) setInView(e.isIntersecting);
      },
      { rootMargin: '200px 0px' }
    );
    io.observe(el);
    return () => io.disconnect();
  }, [ref]);
  return inView;
}

function CandidateItem({
  c,
  isTop,
  isCached,
  detail,
  actionable,
  applying,
  onApply,
  onReject,
  thumbsDown,
  onThumbsDown,
}: {
  c: MetadataCandidate;
  isTop: boolean;
  isCached: boolean;
  /**
   * Whether `c` is a full candidate. The cached pick shown before the search
   * answers is the lane's INDEX row, which carries no breakdown until the
   * lane's detail fetch lands; a search result is always full.
   */
  detail: CandidateDetailState;
  actionable: boolean;
  /** An apply for this BOOK is running: every Apply in the card waits. */
  applying: boolean;
  onApply: () => void;
  onReject: () => void;
  /** This row's thumbs-down mark, when it has one. */
  thumbsDown?: FeedbackMark;
  onThumbsDown: () => void;
}) {
  const [showEvidence, setShowEvidence] = useState(false);
  const marked = !!thumbsDown;
  // Saving: the mark is shown but the server has not answered yet, so an
  // undo has no id to send. One click at a time.
  const saving = marked && !thumbsDown.id;
  return (
    <Box
      data-testid="candidate-item"
      data-thumbs-down={marked ? 'true' : undefined}
      sx={{
        p: 1,
        border: 1,
        borderColor: marked ? 'warning.main' : isTop ? 'primary.main' : 'divider',
        borderStyle: marked ? 'dashed' : 'solid',
        borderRadius: 1,
      }}
    >
      <Stack direction="row" spacing={1} sx={{ alignItems: 'flex-start' }}>
        <Avatar src={c.cover_url || ''} variant="rounded" sx={{ width: 48, height: 64 }} />
        <Box sx={{ minWidth: 0, flex: 1 }}>
          <Typography variant="body2" sx={{ fontWeight: 'bold' }}>
            {c.title}
          </Typography>
          {c.author && <Typography variant="body2">{c.author}</Typography>}
          {c.narrator && (
            <Typography variant="body2" sx={{ color: 'text.secondary' }}>
              Narrated by {c.narrator}
            </Typography>
          )}
          {c.series && (
            <Typography variant="caption" sx={{ display: 'block' }}>
              Series: {c.series}
              {c.series_position ? ` · Book ${c.series_position}` : ''}
            </Typography>
          )}
          <Stack
            direction="row"
            spacing={0.5}
            sx={{ flexWrap: 'wrap', mt: 0.5, alignItems: 'center' }}
          >
            <Chip label={`${Math.round(c.score * 100)}`} size="small" color={scoreColor(c.score)} />
            <Chip
              label={c.source}
              size="small"
              variant="outlined"
              color={SOURCE_COLORS[c.source] || 'default'}
            />
            {c.year ? <Typography variant="caption">{c.year}</Typography> : null}
            {isTop && <Chip label="Best match" size="small" color="primary" variant="outlined" />}
            {isCached && <Chip label="Cached pick" size="small" variant="outlined" />}
            {marked && (
              <Chip label="Not the best match" size="small" color="warning" variant="outlined" />
            )}
            {c.from_catalog && (
              <Chip
                label="Catalog"
                size="small"
                variant="outlined"
                title="From the local author catalog (harvested Audible listings)"
              />
            )}
          </Stack>
          <Button
            size="small"
            variant="text"
            sx={{ px: 0.5, minWidth: 0 }}
            onClick={() => setShowEvidence((v) => !v)}
          >
            {showEvidence ? 'Hide' : 'How this score was reached'}
          </Button>
          <Collapse in={showEvidence} unmountOnExit>
            <EvidencePanel evidence={metadataEvidence(c, detail)} />
          </Collapse>
        </Box>
        <Stack spacing={0.5} sx={{ alignItems: 'flex-end' }}>
          <Tooltip title={marked ? 'Not the best match (click to undo)' : 'Not the best match'}>
            <span>
              <IconButton
                size="small"
                aria-label="Not the best match"
                aria-pressed={marked}
                color={marked ? 'warning' : 'default'}
                disabled={saving}
                onClick={onThumbsDown}
              >
                {marked ? (
                  <ThumbDownIcon fontSize="small" />
                ) : (
                  <ThumbDownOutlinedIcon fontSize="small" />
                )}
              </IconButton>
            </span>
          </Tooltip>
          {actionable && (
            <>
              <Button
                size="small"
                variant="contained"
                color="success"
                disabled={applying}
                onClick={onApply}
              >
                Apply
              </Button>
              <Button size="small" variant="outlined" color="error" onClick={onReject}>
                Reject
              </Button>
            </>
          )}
        </Stack>
      </Stack>
    </Box>
  );
}

export const CandidatesCard = memo(function CandidatesCard({
  r,
  selected,
  rowState,
  detail,
  handlers,
  pathAliases,
  pathVars,
  cands,
}: SpineRowProps & { cands: CandidatesContext }) {
  const bookId = r.book.id;
  const rootRef = useRef<HTMLDivElement>(null);
  const inView = useInView(rootRef);

  const [query, setQuery] = useState<CandidateQuery>({
    title: r.book.title || '',
    author: r.book.author || '',
  });
  const [draft, setDraft] = useState<CandidateQuery>(query);
  // Rejected candidates this session (other than the cached pick, whose
  // reject is the lane's per-book reject): there is no candidate-level reject
  // endpoint, so these are hidden locally.
  const [hidden, setHidden] = useState<MetadataCandidate[]>([]);
  // One apply per book: the panel refuses a second while the first runs, and
  // the card disables every Apply until it settles.
  const [applying, setApplying] = useState(false);

  const { loader } = cands;
  const key = candidateKey(bookId, query);

  // Thumbs-down marks for THIS query (the label is per book + query +
  // candidate), keyed by candidateKey + candidateIdentity.
  const [marks, setMarks] = useState<Map<string, FeedbackMark>>(() => new Map());
  const [feedbackError, setFeedbackError] = useState<string | null>(null);
  const markKey = (c: MetadataCandidate) => `${key}\u0001${candidateIdentity(c)}`;
  const setMark = (k: string, m: FeedbackMark | undefined) =>
    setMarks((prev) => {
      const next = new Map(prev);
      if (m) next.set(k, m);
      else next.delete(k);
      return next;
    });
  const feedbackInput = (
    c: MetadataCandidate,
    label: api.CandidateFeedbackLabel,
    rank: number,
    count: number
  ): api.CandidateFeedbackInput => ({
    book_id: bookId,
    label,
    query: { title: query.title, author: query.author, browse: !!query.browse },
    candidate: c,
    rank,
    result_count: count,
  });

  const thumbsDown = (c: MetadataCandidate, rank: number, count: number) => {
    const k = markKey(c);
    const cur = marks.get(k);
    setFeedbackError(null);
    if (cur) {
      if (!cur.id) return; // still saving
      setMark(k, undefined);
      api.deleteCandidateFeedback(cur.id, 'negative').catch((err: unknown) => {
        setMark(k, cur);
        setFeedbackError(err instanceof Error ? err.message : 'Could not undo the feedback');
      });
      return;
    }
    setMark(k, {});
    api
      .recordCandidateFeedback(feedbackInput(c, 'negative', rank, count))
      // Only while the mark is still there: an Apply of the same candidate
      // may have cleared it (and turned the record positive) meanwhile.
      .then((res) =>
        setMarks((prev) => (prev.has(k) ? new Map(prev).set(k, { id: res.id }) : prev))
      )
      .catch((err: unknown) => {
        setMark(k, undefined);
        setFeedbackError(err instanceof Error ? err.message : 'Could not record the feedback');
      });
  };

  const applyOne = (c: MetadataCandidate, rank: number, count: number) => {
    setApplying(true);
    void cands
      .apply(bookId, c)
      .then((applied) => {
        if (!applied) return;
        // The applied candidate is the positive for this book + query. It
        // overwrites a thumbs-down on the same candidate server-side, so the
        // mark goes too.
        setMark(markKey(c), undefined);
        api
          .recordCandidateFeedback(feedbackInput(c, 'positive', rank, count))
          .catch((err: unknown) => {
            setFeedbackError(
              err instanceof Error ? err.message : 'Could not record the applied match'
            );
          });
      })
      .finally(() => setApplying(false));
  };
  const subscribe = useCallback((cb: () => void) => loader.subscribe(key, cb), [loader, key]);
  const entry = useSyncExternalStore(subscribe, () => loader.get(key));

  useEffect(() => {
    if (!inView) return;
    loader.request(bookId, query);
    // Leaving the viewport before its turn gives the slot back.
    return () => loader.cancel(bookId, query);
  }, [inView, loader, bookId, query]);

  const actionable = isRowActionable(rowState);
  const cached = r.candidate;
  const list: MetadataCandidate[] =
    entry.status === 'done'
      ? [...entry.results].sort((a, b) => rankScoreOf(b) - rankScoreOf(a))
      : entry.status === 'loading' && entry.partial
        ? [...entry.partial].sort((a, b) => rankScoreOf(b) - rankScoreOf(a))
        : cached && !query.browse
          ? [cached]
          : [];
  const visible = list.filter((c) => !hidden.some((h) => sameCandidate(h, c)));
  // The highlight follows the highest ranking score (rank_score; score for a
  // row that predates it) in the current list, not the
  // background scan's cached pick: a fresh search can find a better match
  // (owner report 2026-10-07: Audible 284 sat below a highlighted 213 pick).
  const best = visible.reduce<MetadataCandidate | undefined>(
    (b, c) => (b === undefined || rankScoreOf(c) > rankScoreOf(b) ? c : b),
    undefined
  );

  const reject = (c: MetadataCandidate) => {
    if (cached && sameCandidate(cached, c)) {
      handlers.onAction({ lane: 'metadata', type: 'reject', id: bookId });
      return;
    }
    setHidden((h) => [...h, c]);
  };

  const label = !cached ? noCandidateLabel(r) : null;

  return (
    <Box
      ref={rootRef}
      data-testid="spine-candidates-card"
      data-book-id={bookId}
      sx={{
        [`@container spine (max-width: ${SPINE_TWO_COLUMN_MIN - 1}px)`]: {
          '& .candidates-card-columns': { flexDirection: 'column' },
        },
      }}
    >
      <Box sx={{ p: 2, mb: 1, border: 1, borderColor: 'divider', ...getRowSx(rowState) }}>
        <Stack direction="row" spacing={2} className="candidates-card-columns">
          {/* Left: the book as it is now */}
          <Box sx={{ flex: 1, minWidth: 0 }}>
            <Stack direction="row" spacing={1} sx={{ alignItems: 'flex-start' }}>
              <Checkbox
                size="small"
                checked={selected}
                onChange={() => handlers.onToggleSelect(bookId)}
                disabled={!actionable}
              />
              <Avatar
                src={r.book.cover_url || ''}
                variant="rounded"
                sx={{ width: 60, height: 80, cursor: r.book.cover_url ? 'pointer' : 'default' }}
                onClick={() => r.book.cover_url && handlers.onPreviewCover(r.book.cover_url)}
              />
              <BookInfoPanel book={r.book} pathAliases={pathAliases} pathVars={pathVars} />
            </Stack>
            {label && (
              <Chip
                label={label.label}
                title={label.detail}
                color={label.color}
                size="small"
                sx={{ mt: 1 }}
                data-testid="no-candidate-chip"
              />
            )}
            {rowState === 'applied' && (
              <Chip label="Applied" size="small" color="success" sx={{ mt: 1 }} />
            )}
            {rowState === 'rejected' && (
              <Chip
                label="Rejected — click to undo"
                size="small"
                color="error"
                sx={{ mt: 1, cursor: 'pointer' }}
                onClick={() =>
                  handlers.onAction({ lane: 'metadata', type: 'unreject', id: bookId })
                }
              />
            )}
            {rowState === 'skipped' && (
              <Chip
                label="Skipped — click to undo"
                size="small"
                sx={{ mt: 1, cursor: 'pointer' }}
                onClick={() => handlers.onAction({ lane: 'metadata', type: 'unskip', id: bookId })}
              />
            )}
          </Box>

          {/* Right: every ranked candidate for this book */}
          <Box sx={{ flex: 1, minWidth: 0 }}>
            <Stack
              component="form"
              direction="row"
              spacing={1}
              sx={{ mb: 1, alignItems: 'center' }}
              onSubmit={(e: React.FormEvent) => {
                e.preventDefault();
                setHidden([]);
                const next: CandidateQuery = {
                  title: draft.title,
                  author: draft.author,
                  browse: true,
                };
                if (candidateKey(bookId, next) === key) {
                  // The same text again: run it again rather than replay
                  // the memoized answer.
                  loader.refresh(bookId, next);
                } else {
                  setQuery(next);
                }
              }}
            >
              <TextField
                size="small"
                label="Title"
                value={draft.title}
                onChange={(e) => setDraft((d) => ({ ...d, title: e.target.value }))}
                sx={{ flex: 2 }}
                slotProps={{ htmlInput: { 'data-testid': 'search-again-title' } }}
              />
              <TextField
                size="small"
                label="Author"
                value={draft.author}
                onChange={(e) => setDraft((d) => ({ ...d, author: e.target.value }))}
                sx={{ flex: 1 }}
                slotProps={{ htmlInput: { 'data-testid': 'search-again-author' } }}
              />
              <Button type="submit" size="small" variant="outlined">
                Search again
              </Button>
            </Stack>
            {(entry.status === 'queued' || entry.status === 'loading') && (
              <Stack direction="row" spacing={1} sx={{ alignItems: 'center', mb: 1 }}>
                <CircularProgress size={14} />
                <Typography variant="caption">
                  {entry.status === 'queued' ? 'Waiting to search…' : 'Searching…'}
                </Typography>
              </Stack>
            )}
            {feedbackError && (
              <Alert severity="warning" sx={{ mb: 1 }} onClose={() => setFeedbackError(null)}>
                {feedbackError}
              </Alert>
            )}
            {entry.status === 'error' && (
              <Alert severity="error" sx={{ mb: 1 }}>
                {entry.error}
              </Alert>
            )}
            {entry.status === 'done' && visible.length === 0 && (
              <Typography variant="body2" sx={{ color: 'text.secondary', fontStyle: 'italic' }}>
                No candidates for this search.
              </Typography>
            )}
            <Stack spacing={1} data-testid="candidate-list">
              {visible.map((c, i) => (
                <CandidateItem
                  key={`${c.source}:${c.asin ?? ''}:${c.isbn ?? ''}:${c.title}:${i}`}
                  c={c}
                  isTop={c === best}
                  isCached={!!cached && sameCandidate(cached, c)}
                  // Only the cached object ITSELF is the index row. A search
                  // result that is the same candidate is a full one.
                  detail={c === cached ? detail : 'loaded'}
                  actionable={actionable}
                  applying={applying}
                  onApply={() => applyOne(c, i + 1, visible.length)}
                  onReject={() => reject(c)}
                  thumbsDown={marks.get(markKey(c))}
                  onThumbsDown={() => thumbsDown(c, i + 1, visible.length)}
                />
              ))}
            </Stack>
          </Box>
        </Stack>
      </Box>
    </Box>
  );
});
