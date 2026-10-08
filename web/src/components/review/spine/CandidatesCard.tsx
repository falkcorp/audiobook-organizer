// file: web/src/components/review/spine/CandidatesCard.tsx
// version: 1.0.1
// guid: ceb6a375-997d-44e0-8c71-d83c1980ea33
// last-edited: 2026-10-07
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

import {
  Alert,
  Avatar,
  Box,
  Button,
  Checkbox,
  Chip,
  CircularProgress,
  Collapse,
  Stack,
  TextField,
  Typography,
} from '@mui/material';
import { memo, useCallback, useEffect, useRef, useState, useSyncExternalStore } from 'react';
import type { MetadataCandidate } from '../../../services/api';
import { EvidencePanel } from '../evidence/EvidencePanel';
import { metadataEvidence } from '../evidence/adapters';
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

/** What the candidates view needs beyond a row's props; built once by the panel. */
export interface CandidatesContext {
  loader: CandidateLoader;
  /** Apply one candidate to one book through the per-book background apply. */
  apply: (bookId: string, candidate: MetadataCandidate) => Promise<void>;
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
  actionable,
  applying,
  onApply,
  onReject,
}: {
  c: MetadataCandidate;
  isTop: boolean;
  actionable: boolean;
  /** An apply for this BOOK is running: every Apply in the card waits. */
  applying: boolean;
  onApply: () => void;
  onReject: () => void;
}) {
  const [showEvidence, setShowEvidence] = useState(false);
  return (
    <Box
      data-testid="candidate-item"
      sx={{ p: 1, border: 1, borderColor: isTop ? 'primary.main' : 'divider', borderRadius: 1 }}
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
          <Stack direction="row" spacing={0.5} sx={{ flexWrap: 'wrap', mt: 0.5, alignItems: 'center' }}>
            <Chip label={`${Math.round(c.score * 100)}`} size="small" color={scoreColor(c.score)} />
            <Chip
              label={c.source}
              size="small"
              variant="outlined"
              color={SOURCE_COLORS[c.source] || 'default'}
            />
            {c.year ? <Typography variant="caption">{c.year}</Typography> : null}
            {isTop && <Chip label="Cached pick" size="small" variant="outlined" />}
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
            <EvidencePanel evidence={metadataEvidence(c)} />
          </Collapse>
        </Box>
        {actionable && (
          <Stack spacing={0.5}>
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
          </Stack>
        )}
      </Stack>
    </Box>
  );
}

export const CandidatesCard = memo(function CandidatesCard({
  r,
  selected,
  rowState,
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
  const applyOne = (c: MetadataCandidate) => {
    setApplying(true);
    void cands.apply(bookId, c).finally(() => setApplying(false));
  };

  const { loader } = cands;
  const key = candidateKey(bookId, query);
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
      ? [...entry.results].sort((a, b) => b.score - a.score)
      : cached
        ? [cached]
        : [];
  const visible = list.filter((c) => !hidden.some((h) => sameCandidate(h, c)));

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
            {rowState === 'applied' && <Chip label="Applied" size="small" color="success" sx={{ mt: 1 }} />}
            {rowState === 'rejected' && (
              <Chip
                label="Rejected — click to undo"
                size="small"
                color="error"
                sx={{ mt: 1, cursor: 'pointer' }}
                onClick={() => handlers.onAction({ lane: 'metadata', type: 'unreject', id: bookId })}
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
                setQuery({ title: draft.title, author: draft.author });
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
                  isTop={!!cached && sameCandidate(cached, c)}
                  actionable={actionable}
                  applying={applying}
                  onApply={() => applyOne(c)}
                  onReject={() => reject(c)}
                />
              ))}
            </Stack>
          </Box>
        </Stack>
      </Box>
    </Box>
  );
});
