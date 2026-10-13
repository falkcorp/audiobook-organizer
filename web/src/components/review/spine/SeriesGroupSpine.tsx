// file: web/src/components/review/spine/SeriesGroupSpine.tsx
// version: 1.0.0
// guid: 8c14e7a2-d935-4b06-a1f8-62b0d5e93c47
// last-edited: 2026-10-13
//
// The Series sub-view's renderer: one card per group of series that share a name.
//
// A SERIES CARD IS NOT AN AUTHOR CARD. An author group has a canonical author and
// variants folded into it; a series group is N peer rows, each of which can be
// renamed, left out of the merge, or credited to an author who is really the
// narrator, and the merge keeps the FIRST row that is still in. That is a
// different shape, so it is a different component rather than a `kind` branch in
// AuthorGroupSpine -- but it shares that file's pieces that are not about authors
// (the role chip and the inline name editor).
//
// MEMOIZATION. Same shape as AuthorGroupSpine, for the same reason: a checkbox
// tick re-renders the workspace, and a card that took the whole lane as a prop
// would repaint all 25-100 of them to show one tick. The spine resolves every
// PER-ROW value outside the row and hands the row those plus one stable
// `handlers` object. `keptRowOf` is called exactly once per card render (it picks
// the Merge button's target), which is what the memo test counts.

import { memo, useCallback, useLayoutEffect, useMemo, useRef } from 'react';
import {
  Box,
  Button,
  Card,
  CardActions,
  CardContent,
  Checkbox,
  Chip,
  CircularProgress,
  Divider,
  IconButton,
  Stack,
  Tooltip,
  Typography,
} from '@mui/material';
import MergeIcon from '@mui/icons-material/MergeType';
import EditIcon from '@mui/icons-material/Edit';
import SearchIcon from '@mui/icons-material/Search';
import CloseIcon from '@mui/icons-material/Close';
import UndoIcon from '@mui/icons-material/Undo';
import type { SeriesEntityAction } from '../reviewActions';
import type { AuthorsLane, SeriesEditing, SeriesRenameRecord, ValidationState } from '../lanes/useAuthorsLane';
import {
  hasSeriesMergeWork,
  keptRowOf,
  sharedBookIds,
  type SeriesGroup,
  type SeriesRow,
} from '../lanes/seriesGroups';
import { NameEditor, RoleChip } from './AuthorGroupSpine';

const EMPTY_NUMBERS: ReadonlySet<number> = new Set<number>();

/** The callback half of the lane, stable across selection ticks. */
interface Handlers {
  onToggleSelect: (key: string, shiftKey: boolean) => void;
  onAction: (action: SeriesEntityAction) => void;
  onStartEdit: (groupKey: string, seriesId: SeriesRow['id'], initial: string) => void;
  onCancelEdit: () => void;
  onValidate: (key: string) => void;
}

interface RowProps {
  group: SeriesGroup;
  selected: boolean;
  selectable: boolean;
  focused: boolean;
  /** Series ids left out of this group's merge. */
  excluded: ReadonlySet<number>;
  /** Author ids flagged as narrators in this group. */
  narrators: ReadonlySet<number>;
  validation: ValidationState | undefined;
  /** This group has a write queued or running. */
  pending: boolean;
  /** Writes are paused for everyone (a series scan is running). */
  frozen: boolean;
  /** Non-null while one of THIS group's series names is being edited. */
  editing: SeriesEditing | null;
  /**
   * Renames that can still be undone, by series id: only THIS group's, and
   * undefined when it has none, so a rename elsewhere does not repaint the card.
   */
  renames: Readonly<Record<number, SeriesRenameRecord>> | undefined;
  handlers: Handlers;
}

function ValidationResult({ validation }: { validation: ValidationState }) {
  if (validation.state === 'loading') {
    return (
      <Typography variant="caption" sx={{ color: 'text.secondary' }} data-testid="series-validation-loading">
        Searching the metadata providers…
      </Typography>
    );
  }
  if (validation.state === 'error') {
    return (
      <Typography variant="caption" color="error" role="alert" data-testid="series-validation-error">
        Validation failed: {validation.message}
      </Typography>
    );
  }
  if (validation.results.length === 0) {
    return (
      <Typography variant="caption" sx={{ color: 'text.secondary' }} data-testid="series-validation-empty">
        No external matches found
      </Typography>
    );
  }
  return (
    <Stack spacing={0.5} sx={{ mt: 0.5 }} data-testid="series-validation-results">
      <Typography variant="caption" sx={{ color: 'text.secondary' }}>
        Found {validation.results.length} result(s) from metadata sources:
      </Typography>
      {validation.results.map((r, i) => (
        <Box
          key={`${r.source}:${r.title}:${r.author}:${i}`}
          sx={{ display: 'flex', alignItems: 'center', gap: 1, p: 0.5, borderRadius: 1, bgcolor: 'action.hover' }}
        >
          <Box sx={{ flex: 1, minWidth: 0 }}>
            <Typography variant="body2" noWrap>
              {r.title}
            </Typography>
            <Typography variant="caption" noWrap sx={{ color: 'text.secondary' }}>
              {r.author}
              {r.series ? ` — Series: ${r.series}${r.series_position ? ` #${r.series_position}` : ''}` : ''}
            </Typography>
          </Box>
          <Chip label={r.source} size="small" variant="outlined" />
        </Box>
      ))}
    </Stack>
  );
}

function authorLabel(row: SeriesRow): string {
  if (row.authorName) return `${row.authorName} (id: ${row.authorId})`;
  return row.authorId !== null ? `author ${row.authorId}` : 'no author';
}

const SeriesCard = memo(function SeriesCard({
  group,
  selected,
  selectable,
  focused,
  excluded,
  narrators,
  validation,
  pending,
  frozen,
  editing,
  renames,
  handlers,
}: RowProps) {
  const locked = pending || frozen;
  const dispatch = handlers.onAction;
  const base = { lane: 'authors', entity: 'series' } as const;
  // The row the merge keeps, and how many rows it takes part in.
  const kept = keptRowOf(group, excluded);
  const canMerge = kept !== undefined && hasSeriesMergeWork(group, excluded);
  const shared = sharedBookIds(group);

  return (
    <Card
      variant="outlined"
      data-testid={`series-group-${group.key}`}
      data-focused={focused ? 'true' : undefined}
      aria-busy={pending || undefined}
      sx={{
        opacity: pending ? 0.7 : 1,
        outline: focused ? '2px solid' : 'none',
        outlineColor: 'primary.main',
      }}
    >
      <CardContent>
        <Box sx={{ display: 'flex', alignItems: 'center', gap: 1, mb: 1, flexWrap: 'wrap' }}>
          <Checkbox
            size="small"
            checked={selected}
            disabled={!selectable || locked}
            onChange={(e) => handlers.onToggleSelect(group.key, Boolean((e.nativeEvent as MouseEvent).shiftKey))}
            slotProps={{ input: { 'aria-label': `Select group ${group.name}` } }}
          />
          <Typography variant="subtitle1" component="h3" sx={{ fontWeight: 'bold' }}>
            {group.name}
          </Typography>
          {/* From the rows the lane holds now, not the server's count: a partial
              merge or a left-out row changes what this card has to say. */}
          <Chip label={`${group.series.length} series`} size="small" color="warning" variant="outlined" />
          {group.matchKind === 'subseries' && (
            <Chip label="sub-series" size="small" color="info" variant="outlined" />
          )}
          {group.suggestedName && kept && (
            <Tooltip title={`Rename "${kept.name}", the series this merge keeps, to the suggested name`}>
              <span>
                <Chip
                  label={`Suggested: "${group.suggestedName}"`}
                  size="small"
                  color="primary"
                  variant="outlined"
                  onClick={locked ? undefined : () => handlers.onStartEdit(group.key, kept.id, group.suggestedName ?? '')}
                  sx={{ cursor: locked ? 'default' : 'pointer' }}
                />
              </span>
            </Tooltip>
          )}
        </Box>
        <Divider sx={{ my: 1 }} />

        {group.series.map((row) => {
          const isOut = excluded.has(row.id);
          const isKept = kept?.id === row.id;
          const isNarrator = row.authorId !== null && narrators.has(row.authorId);
          const record = renames?.[row.id];
          const editingThis = editing !== null && editing.seriesId === row.id;
          return (
            <Box
              key={row.id}
              data-testid={`series-row-${row.id}`}
              sx={{ py: 0.5, opacity: isOut ? 0.55 : 1 }}
            >
              <Box sx={{ display: 'flex', alignItems: 'center', gap: 1, flexWrap: 'wrap' }}>
                <Tooltip title={isOut ? `Put "${row.name}" back in this merge` : `Leave "${row.name}" out of this merge`}>
                  <span>
                    <Checkbox
                      size="small"
                      checked={!isOut}
                      disabled={locked}
                      onChange={() =>
                        dispatch({ ...base, type: 'toggleSeriesIncluded', groupKey: group.key, seriesId: row.id })
                      }
                      slotProps={{
                        input: { 'aria-label': `Include series ${row.name} (ID ${row.id}) in the merge` },
                      }}
                    />
                  </span>
                </Tooltip>
                {editingThis ? (
                  <NameEditor
                    // A new edit (the suggested chip after a manual one) starts from
                    // its own initial value instead of the previous draft.
                    key={editing.initial}
                    initial={editing.initial}
                    disabled={locked}
                    label="Series name"
                    onSave={(name) =>
                      dispatch({ ...base, type: 'renameSeries', groupKey: group.key, seriesId: row.id, name })
                    }
                    onCancel={handlers.onCancelEdit}
                  />
                ) : (
                  <>
                    <Typography variant="body2">
                      ID {row.id}: &quot;{row.name}&quot;
                    </Typography>
                    <Tooltip title="Edit series name">
                      <span>
                        <IconButton
                          size="small"
                          aria-label={`Edit series name ${row.name}`}
                          disabled={locked}
                          onClick={() => handlers.onStartEdit(group.key, row.id, row.name)}
                        >
                          <EditIcon fontSize="small" />
                        </IconButton>
                      </span>
                    </Tooltip>
                  </>
                )}
                {isOut ? (
                  <Chip label="Left out" size="small" variant="outlined" />
                ) : isKept ? (
                  <Chip label="Kept" size="small" color="success" />
                ) : (
                  <Chip label="Merged in" size="small" color="warning" variant="outlined" />
                )}
                <Chip
                  label={authorLabel(row)}
                  size="small"
                  color={isNarrator ? 'secondary' : 'success'}
                  variant="outlined"
                />
                {row.authorId !== null && (
                  <RoleChip
                    narrator={isNarrator}
                    disabled={locked || isOut}
                    onClick={() =>
                      dispatch({
                        ...base,
                        type: 'toggleSeriesNarrator',
                        groupKey: group.key,
                        authorId: row.authorId as NonNullable<SeriesRow['authorId']>,
                      })
                    }
                  />
                )}
                {record && (
                  <Button
                    size="small"
                    startIcon={<UndoIcon fontSize="small" />}
                    disabled={locked}
                    onClick={() => dispatch({ ...base, type: 'undoRenameSeries', seriesId: row.id })}
                    aria-label={`Undo rename of ${record.newName}`}
                  >
                    Undo rename
                  </Button>
                )}
              </Box>
              {row.books.length > 0 && (
                <Box sx={{ display: 'flex', flexWrap: 'wrap', gap: 0.5, pl: 4.5, pt: 0.25 }}>
                  {row.books.map((b) => (
                    <Chip
                      key={b.id}
                      label={b.title}
                      size="small"
                      variant="outlined"
                      color={shared.has(b.id) ? 'warning' : 'default'}
                      // A book listed under two of the rows is the clearest sign
                      // they are one series.
                      title={shared.has(b.id) ? 'This book is listed under more than one of these series' : undefined}
                    />
                  ))}
                </Box>
              )}
            </Box>
          );
        })}

        <Box sx={{ mt: 1 }}>
          <Button
            size="small"
            variant="text"
            startIcon={validation?.state === 'loading' ? <CircularProgress size={12} /> : <SearchIcon />}
            disabled={validation?.state === 'loading'}
            onClick={() => handlers.onValidate(group.key)}
          >
            {validation?.state === 'loading' ? 'Validating…' : 'Validate'}
          </Button>
          {validation && <ValidationResult validation={validation} />}
        </Box>
      </CardContent>
      <CardActions>
        <Button
          startIcon={<MergeIcon />}
          variant="contained"
          size="small"
          disabled={locked || !canMerge}
          onClick={() => dispatch({ ...base, type: 'mergeSeriesGroup', groupKey: group.key })}
        >
          {kept ? `Merge into "${kept.name}"` : 'Merge'}
        </Button>
        {!canMerge && !locked && (
          <Typography variant="caption" sx={{ color: 'text.secondary' }} role="status">
            <CloseIcon fontSize="inherit" sx={{ verticalAlign: 'middle', mr: 0.5 }} />
            Include at least two series to merge.
          </Typography>
        )}
      </CardActions>
    </Card>
  );
});

export interface SeriesGroupSpineProps {
  lane: AuthorsLane;
}

export function SeriesGroupSpine({ lane }: SeriesGroupSpineProps) {
  const { dispatch } = lane;
  const { selection, startEdit, cancelEdit, validate } = lane.series;
  const { toggle } = selection;

  // `selection.toggle` is rebuilt whenever the set of selectable keys changes,
  // which an edit to ONE group's choices does (leaving out all but one series
  // makes it unselectable). Handing it to the rows directly would give every card
  // a new handler for that one card's change, so the rows get a stable forwarder.
  const toggleRef = useRef(toggle);
  useLayoutEffect(() => {
    toggleRef.current = toggle;
  }, [toggle]);
  const onToggleSelect = useCallback((key: string, shiftKey: boolean) => toggleRef.current(key, shiftKey), []);

  // Stable across selection ticks and choice edits: every member is a
  // useCallback/setState in the lane, or the forwarder above.
  const handlers = useMemo<Handlers>(
    () => ({
      onToggleSelect,
      onAction: dispatch,
      onStartEdit: startEdit,
      onCancelEdit: cancelEdit,
      onValidate: validate,
    }),
    [onToggleSelect, dispatch, startEdit, cancelEdit, validate]
  );

  const { choices, editing, renames } = lane.series;
  // Each card gets only the undoable renames of its own series. Groups with none
  // get undefined (the same value every render), so the lane's one `renames`
  // record changing repaints only the cards it concerns.
  const renamesByGroup = useMemo(() => {
    const out = new Map<string, Record<number, SeriesRenameRecord>>();
    for (const g of lane.series.pageGroups) {
      let own: Record<number, SeriesRenameRecord> | undefined;
      for (const r of g.series) {
        const rec = renames[r.id];
        if (rec) (own ??= {})[r.id] = rec;
      }
      if (own) out.set(g.key, own);
    }
    return out;
  }, [renames, lane.series.pageGroups]);
  return (
    <Stack spacing={2} data-testid="series-group-spine">
      {lane.series.pageGroups.map((group) => {
        const out = choices.excluded[group.key] ?? EMPTY_NUMBERS;
        return (
          <SeriesCard
            key={group.key}
            group={group}
            selected={selection.isSelected(group.key)}
            selectable={hasSeriesMergeWork(group, out)}
            focused={lane.series.focusedKey === group.key}
            excluded={out}
            narrators={choices.narrators[group.key] ?? EMPTY_NUMBERS}
            validation={lane.series.validation[group.key]}
            pending={lane.pendingKeys.has(group.key)}
            frozen={lane.scanning}
            editing={editing?.groupKey === group.key ? editing : null}
            renames={renamesByGroup.get(group.key)}
            handlers={handlers}
          />
        );
      })}
    </Stack>
  );
}
