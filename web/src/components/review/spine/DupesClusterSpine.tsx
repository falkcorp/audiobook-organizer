// file: web/src/components/review/spine/DupesClusterSpine.tsx
// version: 1.1.0
// guid: 6e1b9d37-0c52-4a84-b7f6-4d8a2c0e9153
// last-edited: 2026-10-12
//
// The duplicate lane's Clusters view: one card per connected component of the
// loaded page's pairs.
//
// A sibling of DupesSpine, not a mode of it: a row there is one PAIR with Keep A
// / Keep B, a card here is a SET of books with a primary radio and set-level
// verbs (Link cluster, Reject cluster, Remove from cluster). The member renderer
// is shared (BookSide), so a book reads the same in both views.
//
// The page boundary is stated, not hidden. The server pages pairs, so a card is
// the cluster of THIS page's pairs; when a card's books also have pairs on other
// pages the card says how many (from useClusterOverflow) and the count opens the
// books it concerns.

import { useMemo, useState } from 'react';
import { Link as RouterLink } from 'react-router-dom';
import {
  Box,
  Button,
  Checkbox,
  Chip,
  Dialog,
  DialogActions,
  DialogContent,
  DialogContentText,
  DialogTitle,
  FormControlLabel,
  Link,
  Radio,
  RadioGroup,
  Stack,
  Tooltip,
  Typography,
} from '@mui/material';
import type { DedupCandidate } from '../../../services/api';
import { usePathVars } from '../../../utils/formatPath';
import { usePathAliases } from '../../common/PathLinks';
import type { DupesAction } from '../reviewActions';
import type { DupeCluster } from '../lanes/dupesClusters';
import { otherPagesLabel, recommendedPrimary } from '../lanes/dupesClusters';
import type { ClusterOverflow } from '../lanes/useDupesClusterData';
import { BookSide } from './DupesSpine';

export interface DupesClusterSpineProps {
  clusters: readonly DupeCluster[];
  /** The rows on screen: where a card's pairs are looked up for its detail list. */
  candidates: readonly DedupCandidate[];
  overflow: (cluster: DupeCluster) => ClusterOverflow;
  emptyMessage: string;
  deepLinkedBookId?: string | null;
  /** A cluster verb or bulk action is in flight: the card buttons wait. */
  busy: boolean;
  isSelected: (candidateId: number) => boolean;
  onSetPairsSelected: (candidateIds: readonly number[], on: boolean) => void;
  onAction: (action: DupesAction) => void;
  onOpenCompare: (candidateId: number) => void;
  /**
   * Extra query string (`&layer=...&band=...`) for the per-book links in the
   * other-pages list, so a link carries the filters the reviewer is looking
   * through. The link opens ALL of that book's pairs under those filters: the
   * page's own pairs cannot be excluded through a URL, so the link's list is
   * longer than the count beside it, and the label says so.
   */
  bookLinkQuery?: string;
}

function titleOf(cluster: DupeCluster, bookId: string): string {
  const m = cluster.members.find((x) => x.bookId === bookId);
  return m?.book?.title || `(${bookId.slice(-8)})`;
}

type DetailTarget = { clusterKey: string } | null;

export function DupesClusterSpine({
  clusters,
  candidates,
  overflow,
  emptyMessage,
  deepLinkedBookId,
  busy,
  isSelected,
  onSetPairsSelected,
  onAction,
  onOpenCompare,
  bookLinkQuery = '',
}: DupesClusterSpineProps) {
  const pathAliases = usePathAliases();
  const pathVars = usePathVars();
  // The reviewer's primary pick per cluster; absent means "use the recommended
  // one, or let the server pick"; '' is the explicit "let the server pick". Keyed by cluster key (its smallest book id),
  // which survives a refetch that reorders the page.
  const [picked, setPicked] = useState<ReadonlyMap<string, string>>(() => new Map());
  const [confirming, setConfirming] = useState<{ cluster: DupeCluster; primaryId: string } | null>(
    null
  );
  const [rejecting, setRejecting] = useState<DupeCluster | null>(null);
  const [detail, setDetail] = useState<DetailTarget>(null);

  const byId = useMemo(() => new Map(candidates.map((c) => [c.id, c])), [candidates]);

  if (clusters.length === 0) {
    return (
      <Box sx={{ p: 4, textAlign: 'center' }} data-testid="dupes-cluster-spine">
        <Typography color="text.secondary">
          {deepLinkedBookId ? 'No duplicate candidates for this book.' : emptyMessage}
        </Typography>
      </Box>
    );
  }

  // A pick is kept only while it still names a member (or is the explicit
  // "let the server pick"): after Remove from cluster the card keeps its key
  // but loses that book, and linking with a primary that is no longer in
  // book_ids would be refused by the server.
  const primaryFor = (c: DupeCluster): string => {
    const p = picked.get(c.key);
    if (p !== undefined && (p === '' || c.bookIds.includes(p))) return p;
    return recommendedPrimary(c) ?? '';
  };
  const detailCluster = detail ? clusters.find((c) => c.key === detail.clusterKey) : undefined;
  const detailOverflow = detailCluster ? overflow(detailCluster) : null;

  return (
    <Box data-testid="dupes-cluster-spine" sx={{ p: 1 }}>
      {clusters.map((cluster) => {
        const primaryId = primaryFor(cluster);
        const selectedPairs = cluster.candidateIds.filter((id) => isSelected(id)).length;
        const allSelected = selectedPairs === cluster.candidateIds.length;
        const other = overflow(cluster);
        const firstTitle = titleOf(cluster, cluster.bookIds[0]);
        return (
          <Box
            key={cluster.key}
            data-testid={`dupes-cluster-${cluster.key}`}
            sx={{
              p: 1.5,
              mb: 1,
              border: 1,
              borderRadius: 1,
              borderColor: 'divider',
              opacity: cluster.hasPending ? 1 : 0.55,
            }}
          >
            <Stack
              direction="row"
              spacing={1}
              useFlexGap
              sx={{ alignItems: 'center', flexWrap: 'wrap', mb: 1 }}
            >
              <Checkbox
                size="small"
                checked={allSelected}
                indeterminate={selectedPairs > 0 && !allSelected}
                onChange={() => onSetPairsSelected(cluster.candidateIds, !allSelected)}
                slotProps={{
                  input: {
                    'aria-label': `Select cluster ${firstTitle} (${cluster.bookIds.length} books)`,
                  },
                }}
              />
              <Chip label={cluster.layer} size="small" variant="outlined" />
              {cluster.maxSimilarity != null && (
                <Typography variant="caption" color="text.secondary">
                  {(cluster.maxSimilarity * 100).toFixed(1)}%
                </Typography>
              )}
              {/* Both counts open the same detail list: the books, the pairs that
                  join them, and the pairs on other pages. */}
              <Chip
                label={`${cluster.bookIds.length} books`}
                size="small"
                color={cluster.bookIds.length > 2 ? 'warning' : 'default'}
                variant="outlined"
                onClick={() => setDetail({ clusterKey: cluster.key })}
                data-testid={`cluster-books-count-${cluster.key}`}
              />
              <Chip
                label={`${cluster.candidateIds.length} pair${cluster.candidateIds.length === 1 ? '' : 's'}`}
                size="small"
                variant="outlined"
                onClick={() => setDetail({ clusterKey: cluster.key })}
                data-testid={`cluster-pairs-count-${cluster.key}`}
              />
              {!cluster.hasPending && <Chip label={cluster.overallStatus} size="small" />}
            </Stack>

            <RadioGroup
              value={primaryId}
              onChange={(e) => {
                // '' is a choice ("let the server pick"), kept as one: dropping
                // it would fall back to the recommended primary and undo it.
                const v = e.target.value;
                setPicked((prev) => new Map(prev).set(cluster.key, v));
              }}
              name={`primary-${cluster.key}`}
            >
              <Stack spacing={1.5}>
                {cluster.members.map((m) => (
                  <Stack
                    key={m.bookId}
                    direction="row"
                    spacing={1}
                    sx={{ alignItems: 'flex-start', minWidth: 0 }}
                  >
                    {cluster.hasPending && (
                      <Radio
                        size="small"
                        value={m.bookId}
                        slotProps={{
                          input: {
                            'aria-label': `Keep ${titleOf(cluster, m.bookId)} as the primary`,
                          },
                        }}
                      />
                    )}
                    <BookSide
                      book={m.book}
                      id={m.bookId}
                      recommended={false}
                      pathAliases={pathAliases}
                      pathVars={pathVars}
                    />
                    {cluster.hasPending && (
                      <Tooltip title="Dismiss this book's pairs with the rest of the cluster; the others stay a smaller cluster">
                        <span>
                          <Button
                            size="small"
                            color="inherit"
                            disabled={busy}
                            onClick={() =>
                              onAction({
                                lane: 'dupes',
                                type: 'removeFromCluster',
                                ids: cluster.bookIds,
                                removeIds: [m.bookId],
                              })
                            }
                          >
                            Remove from cluster
                          </Button>
                        </span>
                      </Tooltip>
                    )}
                  </Stack>
                ))}
                {cluster.hasPending && (
                  <FormControlLabel
                    value=""
                    control={<Radio size="small" />}
                    label={
                      <Typography variant="caption" color="text.secondary">
                        Let the server pick the primary
                      </Typography>
                    }
                  />
                )}
              </Stack>
            </RadioGroup>

            {cluster.llmInfo && (
              <Typography
                variant="caption"
                color="text.secondary"
                sx={{ display: 'block', mt: 0.5, fontStyle: 'italic' }}
              >
                LLM: {cluster.llmInfo}
              </Typography>
            )}

            {other.state === 'loading' && (
              <Typography variant="caption" color="text.secondary" data-testid="cluster-other-loading">
                Checking other pages…
              </Typography>
            )}
            {other.state === 'error' && (
              <Typography variant="caption" color="error" data-testid="cluster-other-error">
                Could not count pairs on other pages: {other.message}
              </Typography>
            )}
            {other.state === 'ready' && other.count > 0 && (
              <Box>
                <Link
                  component="button"
                  type="button"
                  variant="caption"
                  underline="hover"
                  onClick={() => setDetail({ clusterKey: cluster.key })}
                  data-testid={`cluster-other-pages-${cluster.key}`}
                >
                  {otherPagesLabel(other.count)}
                </Link>
              </Box>
            )}

            {cluster.hasPending && (
              <Stack direction="row" spacing={1} useFlexGap sx={{ mt: 1.5, flexWrap: 'wrap' }}>
                <Button
                  size="small"
                  variant="contained"
                  disabled={busy}
                  onClick={() => setConfirming({ cluster, primaryId })}
                >
                  Link cluster
                </Button>
                <Button
                  size="small"
                  color="inherit"
                  disabled={busy}
                  onClick={() => setRejecting(cluster)}
                >
                  Reject cluster
                </Button>
              </Stack>
            )}
          </Box>
        );
      })}

      {/* Reject retires every pair it names at once, so it asks first and says
          exactly which pairs: the ones on the card, not the whole queue. */}
      <Dialog
        open={rejecting !== null}
        onClose={() => setRejecting(null)}
        data-testid="cluster-reject-confirm"
      >
        <DialogTitle>
          Mark {rejecting?.candidateIds.length} pair
          {rejecting?.candidateIds.length === 1 ? '' : 's'} as not a duplicate?
        </DialogTitle>
        <DialogContent>
          <DialogContentText>
            The {rejecting?.candidateIds.length} pair
            {rejecting?.candidateIds.length === 1 ? '' : 's'} joining these{' '}
            {rejecting?.bookIds.length} books on this card are dismissed. Other pairs for the same
            books, such as ones on other pages or hidden by the current filter, are left alone. This
            cannot be undone from here.
          </DialogContentText>
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setRejecting(null)}>Cancel</Button>
          <Button
            variant="contained"
            color="warning"
            data-testid="cluster-reject-confirm-btn"
            onClick={() => {
              const target = rejecting;
              setRejecting(null);
              if (!target) return;
              onAction({
                lane: 'dupes',
                type: 'rejectCluster',
                ids: target.bookIds,
                candidateIds: target.candidateIds,
              });
            }}
          >
            Reject cluster
          </Button>
        </DialogActions>
      </Dialog>

      {/* Link is irreversible and folds N books into one, so it asks first. */}
      <Dialog
        open={confirming !== null}
        onClose={() => setConfirming(null)}
        data-testid="cluster-link-confirm"
      >
        <DialogTitle>
          Link {confirming?.cluster.bookIds.length} books into one group?
        </DialogTitle>
        <DialogContent>
          <DialogContentText>
            {confirming?.primaryId
              ? `"${titleOf(confirming.cluster, confirming.primaryId)}" is kept as the primary.`
              : 'The server picks the primary from path origin, curation, format, bitrate and size.'}{' '}
            Every pending pair among these books is marked merged. This cannot be undone.
          </DialogContentText>
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setConfirming(null)}>Cancel</Button>
          <Button
            variant="contained"
            color="warning"
            data-testid="cluster-link-confirm-btn"
            onClick={() => {
              const target = confirming;
              setConfirming(null);
              if (!target) return;
              onAction({
                lane: 'dupes',
                type: 'linkCluster',
                ids: target.cluster.bookIds,
                ...(target.primaryId ? { primaryId: target.primaryId } : {}),
              });
            }}
          >
            Link cluster
          </Button>
        </DialogActions>
      </Dialog>

      {/* What the counts open: the books, the pairs, and the other-page pairs. */}
      <Dialog
        open={detailCluster !== undefined}
        onClose={() => setDetail(null)}
        data-testid="cluster-detail"
        fullWidth
        maxWidth="sm"
      >
        <DialogTitle>Cluster of {detailCluster?.bookIds.length} books</DialogTitle>
        <DialogContent>
          {detailCluster && (
            <Stack spacing={2}>
              <Box>
                <Typography variant="overline" color="text.secondary">
                  Books
                </Typography>
                <Stack component="ul" spacing={0.5} sx={{ m: 0, pl: 2 }} data-testid="cluster-detail-books">
                  {detailCluster.members.map((m) => (
                    <li key={m.bookId}>
                      <Link component={RouterLink} to={`/library/${m.bookId}`} underline="hover">
                        {titleOf(detailCluster, m.bookId)}
                      </Link>
                      {m.book?.author_name ? ` — ${m.book.author_name}` : ''}
                    </li>
                  ))}
                </Stack>
              </Box>
              <Box>
                <Typography variant="overline" color="text.secondary">
                  Pairs on this page
                </Typography>
                <Stack component="ul" spacing={0.5} sx={{ m: 0, pl: 2 }} data-testid="cluster-detail-pairs">
                  {detailCluster.candidateIds.map((id) => {
                    const c = byId.get(id);
                    return (
                      <li key={id}>
                        {c
                          ? `${titleOf(detailCluster, c.entity_a_id)} / ${titleOf(detailCluster, c.entity_b_id)}`
                          : `Pair ${id}`}{' '}
                        <Button
                          size="small"
                          onClick={() => {
                            setDetail(null);
                            onOpenCompare(id);
                          }}
                        >
                          Compare
                        </Button>
                      </li>
                    );
                  })}
                </Stack>
              </Box>
              {detailOverflow?.state === 'ready' && detailOverflow.perBook.length > 0 && (
                <Box>
                  <Typography variant="overline" color="text.secondary">
                    On other pages (each link opens all pairs for that book)
                  </Typography>
                  <Stack component="ul" spacing={0.5} sx={{ m: 0, pl: 2 }} data-testid="cluster-detail-other">
                    {detailOverflow.perBook.map((b) => (
                      <li key={b.bookId}>
                        <Link
                          component={RouterLink}
                          to={`/review?lane=dupes&book=${encodeURIComponent(b.bookId)}${bookLinkQuery}`}
                          underline="hover"
                          onClick={() => setDetail(null)}
                        >
                          {titleOf(detailCluster, b.bookId)}
                        </Link>
                        {`: ${b.count} pair${b.count === 1 ? '' : 's'} off this page`}
                      </li>
                    ))}
                  </Stack>
                </Box>
              )}
            </Stack>
          )}
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setDetail(null)}>Close</Button>
        </DialogActions>
      </Dialog>
    </Box>
  );
}
