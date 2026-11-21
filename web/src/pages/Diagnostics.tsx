// file: web/src/pages/Diagnostics.tsx
// version: 1.12.1
// last-edited: 2026-11-07
// guid: f2323fc4-b3e7-4298-9ec5-759447cbd643

// ABO's binding of the split Diagnostics page (F9-04 slice 3): the shell frame with books'
// diagnostics pieces in the order the page always rendered them. F9-03 deletes this file when
// the shell's diagnostics frame hosts them as widgets.

import { AIJobsPanel } from '../components/AIJobsPanel';
import { CacheStatsPanel } from '../components/CacheStatsPanel';
import { DiagnosticsFrame } from '../components/diagnostics/DiagnosticsFrame';
import { AIDiagnosticsWidget } from '../modules/books/pages/AIDiagnosticsWidget';
import { DBHealthWidget } from '../modules/books/pages/DBHealthWidget';

export function Diagnostics() {
  return (
    <DiagnosticsFrame>
      <AIDiagnosticsWidget />

      <AIJobsPanel />

      <DBHealthWidget />

      <CacheStatsPanel />
    </DiagnosticsFrame>
  );
}
