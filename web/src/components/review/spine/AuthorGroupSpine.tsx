// file: web/src/components/review/spine/AuthorGroupSpine.tsx
// version: 1.0.0
// guid: 2e8f6b04-d1a7-4c93-85e0-9b4c7a1f3d62
// last-edited: 2026-10-13
//
// The Authors sub-view's renderer: one card per duplicate-author group.
//
// MEMOIZATION. Same shape as DupesSpine / CompareSpine, for the same reason: a
// checkbox tick re-renders the workspace (the lane's state lives there), and a
// card that took the whole lane as a prop would repaint all 25-100 of them to
// show one tick. So the spine resolves every PER-ROW value outside the row (is
// it selected, focused, pending, which variants are excluded, what validation
// said) and hands the row only those values plus one stable `handlers` object.
// A row re-renders when one of ITS values changes.
//
// The card carries over DedupAuthorTab's behaviour: canonical name with an inline
// editor and a suggested-name chip, per-variant Author/Narrator toggle and
// remove-from-merge, the book-count chip that opens the books popover, Validate,
// and the one verb the group's kind calls for (Merge, Split, Find Real Author).

import { memo, useMemo, useState, type KeyboardEvent } from 'react';
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
  TextField,
  Tooltip,
  Typography,
} from '@mui/material';
import MergeIcon from '@mui/icons-material/MergeType';
import MenuBookIcon from '@mui/icons-material/MenuBook';
import EditIcon from '@mui/icons-material/Edit';
import SaveIcon from '@mui/icons-material/Save';
import CloseIcon from '@mui/icons-material/Close';
import UndoIcon from '@mui/icons-material/Undo';
import SearchIcon from '@mui/icons-material/Search';
import type { AuthorsAction } from '../reviewActions';
import type { AuthorGroup, AuthorsLane, ValidationState } from '../lanes/useAuthorsLane';
import { isMergeable } from '../lanes/useAuthorsLane';

const EMPTY_NUMBERS: ReadonlySet<number> = new Set<number>();
const EMPTY_STRINGS: ReadonlySet<string> = new Set<string>();

/** The callback half of the lane, stable across selection ticks. */
interface Handlers {
  onToggleSelect: (key: string, shiftKey: boolean) => void;
  onAction: (action: AuthorsAction) => void;
  onStartEdit: (key: string, initial: string) => void;
  onCancelEdit: () => void;
  onOpenBooks: (key: string, anchor: HTMLElement | null) => void;
  onValidate: (key: string) => void;
}

interface RowProps {
  group: AuthorGroup;
  selected: boolean;
  selectable: boolean;
  focused: boolean;
  excluded: ReadonlySet<number>;
  narrators: ReadonlySet<string>;
  validation: ValidationState | undefined;
  /** This group has a write queued or running. */
  pending: boolean;
  /** Writes are paused for everyone (an author scan is running). */
  frozen: boolean;
  /** Non-null while this group's canonical name is being edited. */
  editingInitial: string | null;
  handlers: Handlers;
}

function RoleChip({ narrator, onClick, disabled }: { narrator: boolean; onClick: () => void; disabled: boolean }) {
  return (
    <Chip
      label={narrator ? 'Narrator' : 'Author'}
      size="small"
      color={narrator ? 'secondary' : 'default'}
      variant={narrator ? 'filled' : 'outlined'}
      onClick={disabled ? undefined : onClick}
      disabled={disabled}
      sx={{ cursor: disabled ? 'default' : 'pointer', minWidth: 60 }}
    />
  );
}

function NameEditor({
  initial,
  disabled,
  onSave,
  onCancel,
}: {
  initial: string;
  disabled: boolean;
  onSave: (name: string) => void;
  onCancel: () => void;
}) {
  // The draft is local: typing must not re-render the workspace per keystroke.
  const [draft, setDraft] = useState(initial);
  const keyDown = (e: KeyboardEvent) => {
    if (e.key === 'Enter') onSave(draft);
    if (e.key === 'Escape') onCancel();
  };
  return (
    <>
      <TextField
        size="small"
        value={draft}
        onChange={(e) => setDraft(e.target.value)}
        onKeyDown={keyDown}
        autoFocus
        sx={{ minWidth: 300 }}
        slotProps={{ htmlInput: { 'aria-label': 'Canonical name' } }}
      />
      <Tooltip title="Save name">
        <span>
          <IconButton
            size="small"
            color="primary"
            aria-label="Save name"
            disabled={disabled || draft.trim() === ''}
            onClick={() => onSave(draft)}
          >
            <SaveIcon fontSize="small" />
          </IconButton>
        </span>
      </Tooltip>
      <Tooltip title="Cancel">
        <IconButton size="small" aria-label="Cancel editing" onClick={onCancel}>
          <CloseIcon fontSize="small" />
        </IconButton>
      </Tooltip>
    </>
  );
}

function ValidationResult({ validation }: { validation: ValidationState }) {
  if (validation.state === 'loading') {
    return (
      <Typography variant="caption" sx={{ color: 'text.secondary' }} data-testid="author-validation-loading">
        Searching the metadata providers…
      </Typography>
    );
  }
  if (validation.state === 'error') {
    return (
      <Typography variant="caption" color="error" role="alert" data-testid="author-validation-error">
        Validation failed: {validation.message}
      </Typography>
    );
  }
  if (validation.results.length === 0) {
    return (
      <Typography variant="caption" sx={{ color: 'text.secondary' }} data-testid="author-validation-empty">
        No external matches found
      </Typography>
    );
  }
  return (
    <Box data-testid="author-validation-results">
      {validation.results.map((r, i) => (
        <Chip
          key={`${r.source}:${r.author}:${r.title}:${i}`}
          label={`${r.source}: ${r.author || r.title}`}
          size="small"
          variant="outlined"
          sx={{ mr: 0.5, mb: 0.5 }}
        />
      ))}
    </Box>
  );
}

const GroupCard = memo(function GroupCard({
  group,
  selected,
  selectable,
  focused,
  excluded,
  narrators,
  validation,
  pending,
  frozen,
  editingInitial,
  handlers,
}: RowProps) {
  const { key, canonical } = group;
  const locked = pending || frozen;
  const dispatch = handlers.onAction;
  const base = { lane: 'authors', entity: 'author' } as const;
  const activeVariants = group.variants.filter((v) => !excluded.has(v.id));
  // Evidence from a different name than the card shows now would mislead.
  const shownValidation = validation && validation.forName === canonical.name ? validation : undefined;

  return (
    <Card
      variant="outlined"
      data-testid={`author-group-${key}`}
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
            onChange={(e) => handlers.onToggleSelect(key, Boolean((e.nativeEvent as MouseEvent).shiftKey))}
            slotProps={{ input: { 'aria-label': `Select group ${canonical.name}` } }}
          />
          {editingInitial !== null ? (
            <NameEditor
              // A new edit (the suggested chip after a manual one) starts from
              // its own initial value instead of the previous draft.
              key={editingInitial}
              initial={editingInitial}
              disabled={locked}
              onSave={(name) =>
                dispatch({ ...base, type: 'renameCanonical', canonicalId: canonical.id, name })
              }
              onCancel={handlers.onCancelEdit}
            />
          ) : (
            <>
              <Typography variant="subtitle1" component="h3" sx={{ fontWeight: 'bold' }}>
                {canonical.name}
              </Typography>
              <Tooltip title="Edit canonical name">
                <span>
                  <IconButton
                    size="small"
                    aria-label="Edit canonical name"
                    disabled={locked}
                    onClick={() => handlers.onStartEdit(key, canonical.name)}
                  >
                    <EditIcon fontSize="small" />
                  </IconButton>
                </span>
              </Tooltip>
              {group.suggestedName && group.suggestedName !== canonical.name && (
                <Tooltip title={`Use suggested: "${group.suggestedName}"`}>
                  <Chip
                    label={group.suggestedName}
                    size="small"
                    color="info"
                    variant="outlined"
                    onClick={locked ? undefined : () => handlers.onStartEdit(key, group.suggestedName ?? '')}
                    sx={{ cursor: 'pointer' }}
                  />
                </Tooltip>
              )}
            </>
          )}
          <Chip
            icon={<MenuBookIcon />}
            label={`${group.bookCount} book(s)`}
            size="small"
            variant="outlined"
            data-author-books-chip={key}
            onClick={(e) => handlers.onOpenBooks(key, e.currentTarget)}
            sx={{ cursor: 'pointer' }}
          />
          {group.isProductionCompany && <Chip label="Production Company" size="small" color="warning" />}
        </Box>

        {group.kind === 'composite' || group.kind === 'production' ? (
          group.splitNames.length > 1 && (
            <>
              <Divider sx={{ my: 1 }} />
              <Typography variant="body2" sx={{ color: 'text.secondary', mb: 1 }}>
                Composite author — will split into:
              </Typography>
              <Box sx={{ display: 'flex', flexWrap: 'wrap', gap: 1 }}>
                {group.splitNames.map((name) => (
                  <Box key={name} sx={{ display: 'flex', gap: 0.5, alignItems: 'center' }}>
                    <Chip label={name} color="warning" variant="outlined" size="small" />
                    <RoleChip
                      narrator={narrators.has(`s:${name}`)}
                      disabled={locked}
                      onClick={() =>
                        dispatch({
                          ...base,
                          type: 'toggleRole',
                          canonicalId: canonical.id,
                          target: { kind: 'split', name },
                        })
                      }
                    />
                  </Box>
                ))}
              </Box>
            </>
          )
        ) : group.variants.length > 0 ? (
          <>
            <Divider sx={{ my: 1 }} />
            <Box sx={{ display: 'flex', flexWrap: 'wrap', gap: 1, alignItems: 'center', mb: 1 }}>
              <Typography variant="body2" sx={{ color: 'text.secondary' }}>
                Merge target:
              </Typography>
              <Chip label={canonical.name} color="primary" size="small" variant="outlined" />
              <Typography variant="body2" sx={{ color: 'text.secondary', mx: 0.5 }}>
                ←
              </Typography>
              <Typography variant="body2" sx={{ color: 'text.secondary' }}>
                {activeVariants.length} variant(s) will be merged into it
                {excluded.size > 0 ? ` (${excluded.size} left out)` : ''}:
              </Typography>
            </Box>
            <Box sx={{ display: 'flex', flexWrap: 'wrap', gap: 1 }}>
              {group.variants.map((v) => {
                const isOut = excluded.has(v.id);
                const sameAsCanonical = v.name === canonical.name;
                return (
                  <Box
                    key={v.id}
                    data-testid={`author-variant-${v.id}`}
                    sx={{ display: 'flex', gap: 0.5, alignItems: 'center', opacity: isOut ? 0.5 : 1 }}
                  >
                    <Tooltip
                      title={
                        sameAsCanonical
                          ? `"${v.name}" is the current canonical name (ID ${v.id} will be merged)`
                          : `Click to use "${v.name}" as the merge target (canonical spelling)`
                      }
                    >
                      <Chip
                        label={v.name}
                        color={sameAsCanonical ? 'default' : 'warning'}
                        variant="outlined"
                        size="small"
                        onClick={
                          sameAsCanonical || locked || isOut
                            ? undefined
                            : () =>
                                dispatch({
                                  ...base,
                                  type: 'renameCanonical',
                                  canonicalId: canonical.id,
                                  name: v.name,
                                })
                        }
                        sx={{
                          cursor: sameAsCanonical ? 'default' : 'pointer',
                          textDecoration: isOut ? 'line-through' : 'none',
                        }}
                      />
                    </Tooltip>
                    {!isOut && (
                      <RoleChip
                        narrator={narrators.has(`v:${v.id}`)}
                        disabled={locked}
                        onClick={() =>
                          dispatch({
                            ...base,
                            type: 'toggleRole',
                            canonicalId: canonical.id,
                            target: { kind: 'variant', id: v.id },
                          })
                        }
                      />
                    )}
                    <Tooltip title={isOut ? `Put "${v.name}" back in this merge` : `Remove "${v.name}" from this merge`}>
                      <span>
                        <IconButton
                          size="small"
                          disabled={locked}
                          aria-label={isOut ? `Put "${v.name}" back in this merge` : `Remove "${v.name}" from this merge`}
                          onClick={() =>
                            dispatch({ ...base, type: 'excludeVariant', canonicalId: canonical.id, variantId: v.id })
                          }
                          sx={{ p: 0.25 }}
                        >
                          {isOut ? <UndoIcon fontSize="small" /> : <CloseIcon fontSize="small" />}
                        </IconButton>
                      </span>
                    </Tooltip>
                  </Box>
                );
              })}
            </Box>
          </>
        ) : null}

        <Box sx={{ mt: 1 }}>
          <Button
            size="small"
            variant="text"
            startIcon={shownValidation?.state === 'loading' ? <CircularProgress size={12} /> : <SearchIcon />}
            disabled={shownValidation?.state === 'loading'}
            onClick={() => handlers.onValidate(key)}
          >
            {shownValidation?.state === 'loading' ? 'Validating…' : 'Validate'}
          </Button>
          {shownValidation && <ValidationResult validation={shownValidation} />}
        </Box>
      </CardContent>
      <CardActions>
        {group.kind === 'production' ? (
          <Button
            startIcon={<SearchIcon />}
            variant="contained"
            size="small"
            color="warning"
            disabled={locked}
            onClick={() => dispatch({ ...base, type: 'resolveProduction', canonicalId: canonical.id })}
          >
            {pending ? 'Resolving…' : 'Find Real Author'}
          </Button>
        ) : group.kind === 'composite' ? (
          <Button
            startIcon={<MergeIcon />}
            variant="contained"
            size="small"
            color="warning"
            disabled={locked}
            onClick={() => dispatch({ ...base, type: 'splitComposite', canonicalId: canonical.id })}
          >
            Split into {group.splitNames.length} authors
          </Button>
        ) : (
          <Button
            startIcon={<MergeIcon />}
            variant="contained"
            size="small"
            disabled={locked || !isMergeable(group)}
            onClick={() => dispatch({ ...base, type: 'mergeGroup', canonicalId: canonical.id })}
          >
            {`Merge into "${canonical.name}"`}
          </Button>
        )}
      </CardActions>
    </Card>
  );
});

export interface AuthorGroupSpineProps {
  lane: AuthorsLane;
}

export function AuthorGroupSpine({ lane }: AuthorGroupSpineProps) {
  const { dispatch, openBooks, validate, startEdit, cancelEdit, selection } = lane;
  const { toggle } = selection;

  // Stable across selection ticks: every member is a useCallback/setState in the
  // lane, or `selection.toggle`, which the selection hook keeps stable by design.
  const handlers = useMemo<Handlers>(
    () => ({
      onToggleSelect: toggle,
      onAction: dispatch,
      onStartEdit: startEdit,
      onCancelEdit: cancelEdit,
      onOpenBooks: openBooks,
      onValidate: validate,
    }),
    [toggle, dispatch, startEdit, cancelEdit, openBooks, validate]
  );

  return (
    <Stack spacing={2} data-testid="author-group-spine">
      {lane.pageGroups.map((group) => (
        <GroupCard
          key={group.key}
          group={group}
          selected={selection.isSelected(group.key)}
          selectable={isMergeable(group)}
          focused={lane.focusedKey === group.key}
          excluded={lane.choices.excluded[group.key] ?? EMPTY_NUMBERS}
          narrators={lane.choices.narrators[group.key] ?? EMPTY_STRINGS}
          validation={lane.validation[group.key]}
          pending={lane.pendingKeys.has(group.key)}
          frozen={lane.scanning}
          editingInitial={lane.editing?.key === group.key ? lane.editing.initial : null}
          handlers={handlers}
        />
      ))}
    </Stack>
  );
}
