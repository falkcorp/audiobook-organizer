// file: web/src/pages/organizeRollback.test.ts
// version: 1.0.0
// guid: bedb3166-a95a-4995-a253-9de934aeb33c
// last-edited: 2026-10-04

import { describe, expect, it, vi } from 'vitest';
import { runOrganizeRollback } from './organizeRollback';
import type { Audiobook } from '../types';
import type { UpdateBookResult } from '../services/api';

const book = (id: string, title: string): Audiobook =>
  ({ id, title, library_state: 'imported', file_path: `/import/${id}.m4b` }) as unknown as Audiobook;

const ok = (id: string, warnings?: string[]) =>
  ({ id, title: id, ...(warnings ? { warnings } : {}) }) as unknown as UpdateBookResult;

describe('runOrganizeRollback (Library handleOrganizeRollback)', () => {
  it('restores every book and reports a plain success', async () => {
    const update = vi.fn((id: string) => Promise.resolve(ok(id)));
    const r = await runOrganizeRollback([book('a', 'A'), book('b', 'B')], update);
    expect(update).toHaveBeenCalledTimes(2);
    expect(update).toHaveBeenCalledWith('a', {
      library_state: 'imported',
      file_path: '/import/a.m4b',
      organized_file_hash: undefined,
    });
    expect(r).toEqual({ total: 2, restored: 2, message: 'Rollback complete.', severity: 'success' });
  });

  it('reports partial-save warnings as a warning', async () => {
    const update = vi.fn((id: string) =>
      Promise.resolve(id === 'b' ? ok(id, ['history not recorded']) : ok(id))
    );
    const r = await runOrganizeRollback([book('a', 'A'), book('b', 'B')], update);
    expect(r.severity).toBe('warning');
    expect(r.restored).toBe(2);
    expect(r.message).toBe('Rollback complete, but 1 book(s) not saved completely: B: history not recorded');
  });

  it('stops at a failure, says how far it got, and keeps the earlier warnings', async () => {
    const update = vi.fn((id: string) => {
      if (id === 'c') return Promise.reject(new Error('500 server error'));
      return Promise.resolve(id === 'a' ? ok(id, ['locks not saved']) : ok(id));
    });
    const r = await runOrganizeRollback([book('a', 'A'), book('b', 'B'), book('c', 'C'), book('d', 'D')], update);
    expect(update).toHaveBeenCalledTimes(3);
    expect(r.severity).toBe('error');
    expect(r.restored).toBe(2);
    expect(r.failed).toEqual({ label: 'C', error: '500 server error' });
    expect(r.message).toBe(
      'Rolled back 2 of 4; failed at C: 500 server error; 1 book(s) not saved completely: A: locks not saved'
    );
  });
});
