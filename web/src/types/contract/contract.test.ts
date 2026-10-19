// file: web/src/types/contract/contract.test.ts
// version: 1.1.1
// guid: 5d1a9e37-8c42-4b06-a7f3-2e6b0c84d915
// last-edited: 2026-10-18

import { describe, expect, it } from 'vitest';

import apply from './testdata/repairs_apply_result.json';
import bookCore from './testdata/database_book_core.json';
import bookAuthor from './testdata/database_book_author.json';
import bookFileCore from './testdata/database_book_file_core.json';
import provenance from './testdata/database_metadata_provenance_entry.json';
import planParams from './testdata/repairs_plan_params.json';
import planResult from './testdata/repairs_plan_result.json';
import row from './testdata/repairs_row.json';
import rowMember from './testdata/repairs_row_member.json';
import rowResult from './testdata/repairs_row_result.json';
import rowsPage from './testdata/repairs_rows_page.json';
import scanState from './testdata/database_scan_state.json';
import stageStatus from './testdata/database_stage_status.json';
import type {
  BookAuthor,
  BookCore,
  BookFileCore,
  MetadataProvenanceEntry,
  ScanState,
  StageStatus,
} from '../generated/database';
import type {
  ApplyResult,
  PlanParams,
  PlanResult,
  Row,
  RowMember,
  RowResult,
  RowsPage,
} from '../generated/repairs';

// The goldens are marshalled from a fully populated Go sample by
// internal/wirecontract/contract_test.go. Assigning them to the generated types
// is the compile-time half of the contract: a Go field the TypeScript lacks, or
// the reverse, fails tsc here. `satisfies` alone is not enough: an optional
// property renamed on the Go side leaves the golden with a key the interface
// lacks, and an excess key on a non-literal is not an error. SameKeys closes
// that: the golden is fully populated, so its keys must equal the interface's.
type SameKeys<G, T> = [keyof G] extends [keyof T] ? ([keyof T] extends [keyof G] ? true : never) : never;

describe('generated types match the Go wire format', () => {
  it('repairs.Row', () => {
    const v = row satisfies Row;
    const same: SameKeys<typeof row, Row> = true;
    const members: SameKeys<(typeof row.members)[number], RowMember> = true;
    expect([same, members]).toEqual([true, true]);
    expect(v.row_id).toBe('synthetic-RowID');
  });

  it('repairs.RowMember', () => {
    const same: SameKeys<typeof rowMember, RowMember> = true;
    expect(rowMember satisfies RowMember).toBeTruthy();
    expect(same).toBe(true);
  });

  it('repairs.RowsPage', () => {
    const v = rowsPage satisfies RowsPage;
    const same: SameKeys<typeof rowsPage, RowsPage> = true;
    expect(same).toBe(true);
    expect(v.rows[0].row_id).toBe(row.row_id);
  });

  it('repairs.RowResult', () => {
    const same: SameKeys<typeof rowResult, RowResult> = true;
    expect(rowResult satisfies RowResult).toBeTruthy();
    expect(same).toBe(true);
  });

  it('repairs.ApplyResult', () => {
    const v = apply satisfies ApplyResult;
    const same: SameKeys<typeof apply, ApplyResult> = true;
    expect(same).toBe(true);
    expect(v.rows).toHaveLength(1);
  });

  it('repairs.PlanParams and PlanResult', () => {
    const params: SameKeys<typeof planParams, PlanParams> = true;
    const result: SameKeys<typeof planResult, PlanResult> = true;
    expect(planParams satisfies PlanParams).toBeTruthy();
    expect(planResult satisfies PlanResult).toBeTruthy();
    expect([params, result]).toEqual([true, true]);
  });

  it('database.BookCore and BookFileCore', () => {
    const core: SameKeys<typeof bookCore, BookCore> = true;
    const fileCore: SameKeys<typeof bookFileCore, BookFileCore> = true;
    expect(bookCore satisfies BookCore).toBeTruthy();
    expect(bookFileCore satisfies BookFileCore).toBeTruthy();
    expect([core, fileCore]).toEqual([true, true]);
  });

  it('the structs BookCore reaches, nested key sets included', () => {
    const scan: SameKeys<typeof scanState, ScanState> = true;
    const stage: SameKeys<typeof stageStatus, StageStatus> = true;
    const author: SameKeys<typeof bookAuthor, BookAuthor> = true;
    const entry: SameKeys<typeof provenance, MetadataProvenanceEntry> = true;
    // The nested copies inside the parents' goldens must agree with the types too.
    const nestedScan: SameKeys<typeof bookFileCore.scan, ScanState> = true;
    const nestedEntry: SameKeys<(typeof bookCore.metadata_provenance)['synthetic-key'], MetadataProvenanceEntry> = true;
    const nestedAuthor: SameKeys<(typeof bookCore.authors)[number], BookAuthor> = true;
    expect(scanState satisfies ScanState).toBeTruthy();
    expect(stageStatus satisfies StageStatus).toBeTruthy();
    expect(bookAuthor satisfies BookAuthor).toBeTruthy();
    expect(provenance satisfies MetadataProvenanceEntry).toBeTruthy();
    expect([scan, stage, author, entry, nestedScan, nestedEntry, nestedAuthor]).toEqual(Array(7).fill(true));
  });
});
