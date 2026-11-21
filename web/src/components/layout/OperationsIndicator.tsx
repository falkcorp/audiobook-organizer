// file: web/src/components/layout/OperationsIndicator.tsx
// version: 4.14.2
// guid: 3b4c5d6e-7f8a-9b0c-1d2e-3f4a5b6c7d8e
// last-edited: 2026-11-07

import { useBooksRowActions } from '../../modules/books/layout/useBooksRowActions';
import { BooksOperationDetails } from '../../modules/books/layout/OperationDetails';
import { booksRunStatus, useBooksOperationsFeed } from '../../modules/books/layout/operationsFeed';
import { cancelOperation } from '../../services/api';
import { OperationActivityPanel } from '../OperationActivityPanel';
import { operationDisplayName } from './operationsFormat';
import { OperationsIndicatorFrame } from './OperationsIndicatorFrame';

/** The operations bell bound to the legacy store, the books op labels, cancel, the activity panel and the Review/Undo row actions; F9-03 re-points importers to the shell and deletes this file. */
export function OperationsIndicator() {
  const rowActions = useBooksRowActions();
  return (
    <OperationsIndicatorFrame
      useFeed={useBooksOperationsFeed}
      status={booksRunStatus}
      nameOf={operationDisplayName}
      onCancel={cancelOperation}
      renderActivity={(target) => (
        <OperationActivityPanel operationId={target.id} memberIds={target.memberIds} />
      )}
      renderDetails={(op) => <BooksOperationDetails op={op} />}
      rowActions={rowActions}
      allOperationsPath="/operations"
      activityPath="/activity"
    />
  );
}
