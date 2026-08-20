// file: web/src/components/audiobooks/MetadataDiffTable.tsx
// version: 1.0.2
// guid: 8f6a7b5c-9d0e-4a70-b8c5-3d7e0f1b9a99
//
// Side-by-side metadata diff for dedup cluster cards (backlog 1.5).
// Shows fields that differ between two books, highlighting
// discrepancies so the user can decide which to keep.

import {
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  Typography,
} from '@mui/material';
import type { Book } from '../../services/api';

interface MetadataDiffTableProps {
  bookA: Book;
  bookB: Book;
}

interface DiffField {
  label: string;
  valueA: string;
  valueB: string;
}

const DIFF_FIELDS: Array<{ label: string; accessor: (b: Book) => string }> = [
  { label: 'Title', accessor: (b) => b.title || '' },
  {
    label: 'Author',
    accessor: (b) => b.authors?.map((a: { name: string }) => a.name).join(', ') || '',
  },
  { label: 'Narrator', accessor: (b) => b.narrator || '' },
  { label: 'Series', accessor: (b) => b.series_name || '' },
  { label: 'Year', accessor: (b) => String(b.print_year || b.audiobook_release_year || '') },
  { label: 'Format', accessor: (b) => b.format || '' },
  { label: 'Duration', accessor: (b) => (b.duration ? `${Math.round(b.duration / 60)}m` : '') },
  { label: 'Publisher', accessor: (b) => b.publisher || '' },
  { label: 'Language', accessor: (b) => b.language || '' },
  { label: 'ISBN', accessor: (b) => b.isbn13 || b.isbn10 || '' },
  { label: 'Library State', accessor: (b) => b.library_state || '' },
  { label: 'File Path', accessor: (b) => b.file_path || '' },
];

export default function MetadataDiffTable({ bookA, bookB }: MetadataDiffTableProps) {
  const diffs: DiffField[] = [];

  for (const field of DIFF_FIELDS) {
    const a = field.accessor(bookA);
    const b = field.accessor(bookB);
    if (a !== b) {
      diffs.push({ label: field.label, valueA: a || '—', valueB: b || '—' });
    }
  }

  if (diffs.length === 0) {
    return (
      <Typography
        variant="body2"
        sx={{
          color: 'text.secondary',
          py: 1,
        }}
      >
        No metadata differences detected.
      </Typography>
    );
  }

  return (
    <TableContainer sx={{ maxHeight: 300 }}>
      <Table size="small" stickyHeader>
        <TableHead>
          <TableRow>
            <TableCell sx={{ fontWeight: 'bold', width: 120 }}>Field</TableCell>
            <TableCell sx={{ fontWeight: 'bold', bgcolor: 'action.hover' }}>Book A</TableCell>
            <TableCell sx={{ fontWeight: 'bold', bgcolor: 'action.hover' }}>Book B</TableCell>
          </TableRow>
        </TableHead>
        <TableBody>
          {diffs.map((d) => (
            <TableRow key={d.label}>
              <TableCell>
                <Typography
                  variant="caption"
                  sx={{
                    fontWeight: 'bold',
                  }}
                >
                  {d.label}
                </Typography>
              </TableCell>
              <TableCell>
                <Typography variant="body2" noWrap title={d.valueA}>
                  {d.valueA}
                </Typography>
              </TableCell>
              <TableCell>
                <Typography variant="body2" noWrap title={d.valueB}>
                  {d.valueB}
                </Typography>
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </TableContainer>
  );
}
