// file: web/src/components/common/ConfigurableTable.tsx
// version: 1.2.0
// guid: b2c3d4e5-f6a7-8b9c-0d1e-2f3a4b5c6d7e
// last-edited: 2026-11-05

// Moved to @mediacore/ui-kit (F9-05). The ui-kit hook uses its storageKey as given;
// this wrapper keeps the legacy table_config_<name> key until the shell
// adoption (F9-03) moves it to storageKey().

import {
  useConfigurableTable as useUiKitConfigurableTable,
  type UseConfigurableTableOptions,
  type UseConfigurableTableResult,
} from '@mediacore/ui-kit';
import { STORAGE_KEYS } from '../../lib/storageKeys';

export { ColumnPicker, ResizableHeaderCell, type ColumnDef } from '@mediacore/ui-kit';

// eslint-disable-next-line react-refresh/only-export-components
export function useConfigurableTable<T>(options: UseConfigurableTableOptions<T>): UseConfigurableTableResult<T> {
  return useUiKitConfigurableTable({
    ...options,
    storageKey: `${STORAGE_KEYS.TABLE_CONFIG}_${options.storageKey}`,
  });
}
