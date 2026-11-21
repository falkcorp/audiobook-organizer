// file: web/src/components/system/MaintenanceTab.tsx
// version: 1.19.0
// guid: c3d4e5f6-a7b8-9012-cdef-345678901234
// last-edited: 2026-10-30

// ABO's binding of the split Maintenance tab (F9-04 slice 1, 07 §7.11 FE8-6):
// the shell half (MaintenanceTabFrame) with the books jobs passed in. After
// shell adoption the books pieces are `widgets` entries (placement "system")
// and this file is deleted by F9-03.

import { OptimizeLibraryCard } from '../../modules/books/pages/OptimizeLibraryCard';
import { ManualFixesCard } from '../../modules/books/pages/ManualFixesCard';
import { ChapterConsolidationCard } from '../../modules/books/pages/ChapterConsolidationCard';
import { DuplicateAndHashJobs } from '../../modules/books/pages/DuplicateAndHashJobs';
import { MaintenanceTabFrame } from './MaintenanceTabFrame';
import { ThrottlePanel } from './ThrottlePanel';

export function MaintenanceTab() {
  return (
    <MaintenanceTabFrame leading={<OptimizeLibraryCard />}>
      <ManualFixesCard />

      <ThrottlePanel />

      <ChapterConsolidationCard />

      <DuplicateAndHashJobs />
    </MaintenanceTabFrame>
  );
}
