// file: web/src/components/common/ConfigurableTable.tsx
// version: 1.1.2
// guid: b2c3d4e5-f6a7-8b9c-0d1e-2f3a4b5c6d7e

import { useCallback, useEffect, useRef, useState } from 'react';
import {
  Box,
  Checkbox,
  Divider,
  FormControlLabel,
  IconButton,
  Popover,
  TableCell,
  TableSortLabel,
  Tooltip,
  Typography,
} from '@mui/material';
import ViewColumnIcon from '@mui/icons-material/ViewColumn';
import { STORAGE_KEYS } from '../../lib/storageKeys';

// --- Types ---

export interface ColumnDef<T> {
  /** Unique key for persistence */
  key: string;
  /** Display label */
  label: string;
  /** Whether this column is shown by default */
  defaultVisible?: boolean;
  /** Default width in pixels (min 50) */
  defaultWidth?: number;
  /** Min width in pixels */
  minWidth?: number;
  /** Alignment */
  align?: 'left' | 'right' | 'center';
  /** Whether this column can be sorted */
  sortable?: boolean;
  /** Render the cell content */
  render: (row: T) => React.ReactNode;
  /** Get a sortable value for comparison */
  sortValue?: (row: T) => string | number;
}

interface ColumnState {
  visible: string[];
  widths: Record<string, number>;
}

interface UseConfigurableTableOptions<T> {
  /** Unique key for localStorage persistence */
  storageKey: string;
  /** All available column definitions */
  columns: ColumnDef<T>[];
  /** Default sort field */
  defaultSortField?: string;
  /** Default sort direction */
  defaultSortDir?: 'asc' | 'desc';
}

interface UseConfigurableTableResult<T> {
  /** Currently visible columns in order */
  visibleColumns: ColumnDef<T>[];
  /** All available columns */
  allColumns: ColumnDef<T>[];
  /** Current sort field */
  sortField: string;
  /** Current sort direction */
  sortDir: 'asc' | 'desc';
  /** Column widths */
  columnWidths: Record<string, number>;
  /** Handle sort click on a column */
  handleSort: (field: string) => void;
  /** Toggle column visibility */
  toggleColumn: (key: string) => void;
  /** Check if a column is visible */
  isColumnVisible: (key: string) => boolean;
  /** Start resizing a column */
  startResize: (key: string, startX: number) => void;
  /** Sort rows by current sort settings */
  sortRows: (rows: T[]) => T[];
  /** Reset to default columns */
  resetColumns: () => void;
}

// --- Hook ---

// eslint-disable-next-line react-refresh/only-export-components
export function useConfigurableTable<T>({
  storageKey,
  columns,
  defaultSortField,
  defaultSortDir = 'asc',
}: UseConfigurableTableOptions<T>): UseConfigurableTableResult<T> {
  const getDefaultState = useCallback((): ColumnState => ({
    visible: columns.filter((c) => c.defaultVisible !== false).map((c) => c.key),
    widths: Object.fromEntries(columns.map((c) => [c.key, c.defaultWidth ?? 150])),
  }), [columns]);

  const loadState = useCallback((): ColumnState => {
    try {
      const raw = localStorage.getItem(`${STORAGE_KEYS.TABLE_CONFIG}_${storageKey}`);
      if (raw) {
        const parsed = JSON.parse(raw) as ColumnState;
        // Ensure all columns have widths
        for (const col of columns) {
          if (!parsed.widths[col.key]) {
            parsed.widths[col.key] = col.defaultWidth ?? 150;
          }
        }
        // Remove stale column keys
        parsed.visible = parsed.visible.filter((k) => columns.some((c) => c.key === k));
        return parsed;
      }
    } catch { /* use defaults */ }
    return getDefaultState();
  }, [storageKey, columns, getDefaultState]);

  const [colState, setColState] = useState<ColumnState>(loadState);
  const [sortField, setSortField] = useState(defaultSortField ?? columns[0]?.key ?? '');
  const [sortDir, setSortDir] = useState<'asc' | 'desc'>(defaultSortDir);

  // Persist column state
  useEffect(() => {
    localStorage.setItem(`${STORAGE_KEYS.TABLE_CONFIG}_${storageKey}`, JSON.stringify(colState));
  }, [colState, storageKey]);

  const visibleColumns = columns.filter((c) => colState.visible.includes(c.key));

  const handleSort = useCallback((field: string) => {
    setSortField((prev) => {
      if (prev === field) {
        setSortDir((d) => (d === 'asc' ? 'desc' : 'asc'));
        return prev;
      }
      setSortDir('asc');
      return field;
    });
  }, []);

  const toggleColumn = useCallback((key: string) => {
    setColState((prev) => {
      const isVisible = prev.visible.includes(key);
      if (isVisible && prev.visible.length <= 1) return prev; // keep at least 1
      return {
        ...prev,
        visible: isVisible
          ? prev.visible.filter((k) => k !== key)
          : [...prev.visible, key],
      };
    });
  }, []);

  const isColumnVisible = useCallback(
    (key: string) => colState.visible.includes(key),
    [colState.visible]
  );

  // Resize handling
  const resizeRef = useRef<{ key: string; startX: number; startWidth: number } | null>(null);

  const startResize = useCallback((key: string, startX: number) => {
    const startWidth = colState.widths[key] ?? 150;
    resizeRef.current = { key, startX, startWidth };

    const onMouseMove = (e: MouseEvent) => {
      if (!resizeRef.current) return;
      const diff = e.clientX - resizeRef.current.startX;
      const col = columns.find((c) => c.key === resizeRef.current!.key);
      const minW = col?.minWidth ?? 50;
      const newWidth = Math.max(minW, resizeRef.current.startWidth + diff);
      setColState((prev) => ({
        ...prev,
        widths: { ...prev.widths, [resizeRef.current!.key]: newWidth },
      }));
    };

    const onMouseUp = () => {
      resizeRef.current = null;
      document.removeEventListener('mousemove', onMouseMove);
      document.removeEventListener('mouseup', onMouseUp);
      document.body.style.cursor = '';
      document.body.style.userSelect = '';
    };

    document.addEventListener('mousemove', onMouseMove);
    document.addEventListener('mouseup', onMouseUp);
    document.body.style.cursor = 'col-resize';
    document.body.style.userSelect = 'none';

    // Return cleanup function for potential early termination
    return () => {
      document.removeEventListener('mousemove', onMouseMove);
      document.removeEventListener('mouseup', onMouseUp);
    };
  }, [colState.widths, columns]);

  const sortRows = useCallback(
    (rows: T[]): T[] => {
      const col = columns.find((c) => c.key === sortField);
      if (!col?.sortValue) return rows;
      return [...rows].sort((a, b) => {
        const va = col.sortValue!(a);
        const vb = col.sortValue!(b);
        let cmp: number;
        if (typeof va === 'string' && typeof vb === 'string') {
          cmp = va.localeCompare(vb);
        } else {
          cmp = (va as number) - (vb as number);
        }
        return sortDir === 'asc' ? cmp : -cmp;
      });
    },
    [columns, sortField, sortDir]
  );

  const resetColumns = useCallback(() => {
    setColState(getDefaultState());
  }, [getDefaultState]);

  return {
    visibleColumns,
    allColumns: columns,
    sortField,
    sortDir,
    columnWidths: colState.widths,
    handleSort,
    toggleColumn,
    isColumnVisible,
    startResize,
    sortRows,
    resetColumns,
  };
}

// --- Reusable Header Cell ---

interface ResizableHeaderCellProps {
  columnKey: string;
  label: string;
  width: number;
  align?: 'left' | 'right' | 'center';
  sortable?: boolean;
  sortActive: boolean;
  sortDirection: 'asc' | 'desc';
  onSort: () => void;
  onStartResize: (key: string, x: number) => void;
}

export function ResizableHeaderCell({
  columnKey,
  label,
  width,
  align = 'left',
  sortable = true,
  sortActive,
  sortDirection,
  onSort,
  onStartResize,
}: ResizableHeaderCellProps) {
  return (
    <TableCell
      align={align}
      sx={{
        width,
        minWidth: 50,
        maxWidth: width,
        position: 'relative',
        userSelect: 'none',
        whiteSpace: 'nowrap',
        overflow: 'hidden',
        textOverflow: 'ellipsis',
        pr: 2,
      }}
    >
      {sortable ? (
        <TableSortLabel
          active={sortActive}
          direction={sortActive ? sortDirection : 'asc'}
          onClick={onSort}
        >
          {label}
        </TableSortLabel>
      ) : (
        label
      )}
      {/* Resize handle */}
      <Box
        sx={{
          position: 'absolute',
          right: 0,
          top: 0,
          bottom: 0,
          width: 6,
          cursor: 'col-resize',
          '&:hover': { bgcolor: 'primary.main', opacity: 0.3 },
        }}
        onMouseDown={(e) => {
          e.preventDefault();
          e.stopPropagation();
          onStartResize(columnKey, e.clientX);
        }}
      />
    </TableCell>
  );
}

// --- Column Picker Button ---

interface ColumnPickerProps {
  columns: { key: string; label: string }[];
  isVisible: (key: string) => boolean;
  onToggle: (key: string) => void;
  onReset: () => void;
}

export function ColumnPicker({ columns, isVisible, onToggle, onReset }: ColumnPickerProps) {
  const [anchorEl, setAnchorEl] = useState<HTMLButtonElement | null>(null);

  return (
    <>
      <Tooltip title="Show/Hide Columns">
        <IconButton size="small" onClick={(e) => setAnchorEl(e.currentTarget)}>
          <ViewColumnIcon />
        </IconButton>
      </Tooltip>
      <Popover
        open={!!anchorEl}
        anchorEl={anchorEl}
        onClose={() => setAnchorEl(null)}
        anchorOrigin={{ vertical: 'bottom', horizontal: 'right' }}
        transformOrigin={{ vertical: 'top', horizontal: 'right' }}
      >
        <Box sx={{ p: 2, minWidth: 220, maxHeight: 400, overflow: 'auto' }}>
          <Box sx={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', mb: 1 }}>
            <Typography variant="subtitle2">Columns</Typography>
            <Typography
              variant="caption"
              color="primary"
              sx={{ cursor: 'pointer', '&:hover': { textDecoration: 'underline' } }}
              onClick={onReset}
            >
              Reset
            </Typography>
          </Box>
          <Divider sx={{ mb: 1 }} />
          {columns.map((col) => (
            <FormControlLabel
              key={col.key}
              control={
                <Checkbox
                  size="small"
                  checked={isVisible(col.key)}
                  onChange={() => onToggle(col.key)}
                />
              }
              label={<Typography variant="body2">{col.label}</Typography>}
              sx={{ display: 'flex', m: 0, py: 0.25 }}
            />
          ))}
        </Box>
      </Popover>
    </>
  );
}
