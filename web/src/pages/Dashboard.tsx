// file: web/src/pages/Dashboard.tsx
// version: 1.17.1
// guid: 2f3a4b5c-6d7e-8f9a-0b1c-2d3e4f5a6b7c
// last-edited: 2026-11-07

// ABO's binding of the split Dashboard (F9-04 slice 2): the shell frame with books' library
// widget in it. F9-03 deletes this file when the shell's home hosts the widget.

import { DashboardFrame } from '../components/dashboard/DashboardFrame';
import { LibraryHomeWidget } from '../modules/books/pages/LibraryHomeWidget';

export function Dashboard() {
  return (
    <DashboardFrame>
      <LibraryHomeWidget />
    </DashboardFrame>
  );
}
