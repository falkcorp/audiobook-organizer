// file: web/src/modules/books/layout/useBooksRowActions.tsx
// version: 0.1.0
// guid: 2f6b0d84-3e19-4a57-9c82-b1d7e5a0c346
// last-edited: 2026-11-21

import { useCallback, useState } from 'react';
import { useNavigate } from 'react-router';
import { Button } from '@mui/material';
import type { OperationsIndicatorFrameProps } from '../../../components/layout/OperationsIndicatorFrame';
import { getUndoPreflight, revertOperation as revertOp } from '../../../services/versionApi';
import {
  describeRevertResult,
  describeUndoPreflight,
  isUndoRefused,
} from '../../../utils/revertResult';

type RowActions = NonNullable<OperationsIndicatorFrameProps['rowActions']>;

const SMALL_BUTTON_SX = {
  textTransform: 'none',
  fontSize: '0.65rem',
  py: 0,
  minHeight: 20,
} as const;

/**
 * The books buttons at the end of a completed row in the operations bell: Review
 * for a finished metadata candidate fetch, Undo for a finished organize run.
 * Returned as the frame's `rowActions`.
 *
 * The refused-undo set lives HERE, one level above the rows: the Completed
 * section unmounts its rows when it is collapsed, and a refusal that will repeat
 * on every click must outlast that. (The bell kept it in its own state for the
 * same reason.)
 */
export function useBooksRowActions(): RowActions {
  const navigate = useNavigate();
  // Operations the server refused to preflight an undo for (403): their Undo
  // button is not offered again, since the same caller is refused every time.
  const [undoRefused, setUndoRefused] = useState<ReadonlySet<string>>(() => new Set());

  return useCallback<RowActions>(
    (op, { closePopover }) => (
      <>
        {op.type === 'metadata_candidate_fetch' && op.status === 'completed' && (
          <Button
            size="small"
            variant="outlined"
            sx={SMALL_BUTTON_SX}
            onClick={(e) => {
              e.stopPropagation();
              e.preventDefault();
              closePopover();
              // Was `window.location.href = '/library?reviewOp=' + op.id`,
              // which reloaded the whole SPA to open a modal over
              // the library. The op id was never used for anything
              // but its own presence -- Library read it as a
              // boolean -- so nothing is lost by dropping it.
              navigate('/review');
            }}
          >
            Review
          </Button>
        )}
        {(op.type === 'organize' || op.type === 'scan_and_organize') &&
          op.status === 'completed' &&
          !undoRefused.has(op.id) && (
            <Button
              size="small"
              variant="outlined"
              color="warning"
              sx={SMALL_BUTTON_SX}
              onClick={async (e) => {
                e.stopPropagation();
                e.preventDefault();
                try {
                  const preflight = await getUndoPreflight(op.id);
                  const plan = describeUndoPreflight(preflight);
                  if (!plan.canUndo) {
                    // Nothing restorable: say so rather than offer an Undo the server refuses.
                    alert(plan.message);
                    return;
                  }
                  if (confirm(plan.message)) {
                    const result = await revertOp(op.id);
                    alert(describeRevertResult(result));
                  }
                } catch (err: unknown) {
                  if (isUndoRefused(err)) {
                    setUndoRefused((prev) => new Set(prev).add(op.id));
                  }
                  const msg = (err as { message?: string })?.message || 'Undo failed';
                  alert(msg);
                }
              }}
            >
              Undo
            </Button>
          )}
      </>
    ),
    [navigate, undoRefused]
  );
}
