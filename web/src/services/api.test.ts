// file: src/services/api.test.ts
// version: 1.3.1
// guid: 0a1b2c3d-4e5f-6a7b-8c9d-0e1f2a3b4c5d
// last-edited: 2026-06-24

import { vi, describe, it, expect, beforeEach, afterEach } from 'vitest';
import {
  getImportPaths,
  addImportPath,
  addImportPathDetailed,
  removeImportPath,
  bulkFetchMetadata,
  batchWriteBackMetadata,
} from './api';

const mockFetch = vi.fn();

describe('api import paths', () => {
  beforeEach(() => {
    // Allow overriding fetch in tests
    global.fetch = mockFetch as unknown as typeof fetch;
  });

  afterEach(() => {
    mockFetch.mockReset();
  });

  it('getImportPaths returns import paths list', async () => {
    mockFetch.mockResolvedValueOnce(
      new Response(
        JSON.stringify({
          data: {
            importPaths: [
              {
                id: 1,
                path: '/tmp',
                name: 'Tmp',
                enabled: true,
                created_at: 'now',
                book_count: 0,
              },
            ],
          },
        }),
        {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        }
      )
    );

    const paths = await getImportPaths();
    expect(paths).toEqual([
      {
        id: 1,
        path: '/tmp',
        name: 'Tmp',
        enabled: true,
        created_at: 'now',
        book_count: 0,
      },
    ]);
    expect(mockFetch).toHaveBeenCalledWith('/api/v1/import-paths', expect.any(Object));
  });

  it('addImportPath returns created import path', async () => {
    mockFetch.mockResolvedValueOnce(
      new Response(
        JSON.stringify({
          data: {
            importPath: {
              id: 2,
              path: '/new',
              name: 'New',
              enabled: true,
              created_at: 'now',
              book_count: 0,
            },
          },
        }),
        {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        }
      )
    );

    const created = await addImportPath('/new', 'New');
    expect(created.path).toBe('/new');
    expect(mockFetch).toHaveBeenCalledWith(
      '/api/v1/import-paths',
      expect.any(Object)
    );
  });

  it('addImportPathDetailed returns detailed response', async () => {
    mockFetch.mockResolvedValueOnce(
      new Response(
        JSON.stringify({
          data: {
            importPath: {
              id: 3,
              path: '/detailed',
              name: 'Detailed',
              enabled: true,
              created_at: 'now',
              book_count: 0,
            },
            scan_operation_id: 'op-1',
          },
        }),
        {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        }
      )
    );

    const detailed = await addImportPathDetailed('/detailed', 'Detailed');
    expect(detailed.importPath.id).toBe(3);
    expect(detailed.scan_operation_id).toBe('op-1');
  });

  it('removeImportPath calls delete endpoint', async () => {
    mockFetch.mockResolvedValueOnce(new Response(null, { status: 200 }));

    await removeImportPath(4);
    expect(mockFetch).toHaveBeenCalledWith('/api/v1/import-paths/4', expect.objectContaining({ method: 'DELETE' }));
  });

  it('bulkFetchMetadata posts book ids and returns response', async () => {
    mockFetch.mockResolvedValueOnce(
      new Response(
        JSON.stringify({
          data: {
            updated_count: 1,
            total_count: 2,
            results: [
              {
                book_id: 'id-1',
                status: 'updated',
                applied_fields: ['publisher'],
                fetched_fields: ['publisher'],
              },
            ],
            source: 'Open Library',
          },
        }),
        {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        }
      )
    );

    const response = await bulkFetchMetadata(['id-1', 'id-2'], false);
    expect(response.updated_count).toBe(1);
    expect(response.total_count).toBe(2);
    expect(mockFetch).toHaveBeenCalledWith('/api/v1/metadata/bulk-fetch', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({ book_ids: ['id-1', 'id-2'], only_missing: false }),
    }));
  });

  it('batchWriteBackMetadata posts book ids and rename flag', async () => {
    mockFetch.mockResolvedValueOnce(
      new Response(
        JSON.stringify({
          data: {
            written: 2,
            written_files: 3,
            renamed: 1,
            failed: 0,
            errors: [],
          },
        }),
        {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        }
      )
    );

    const response = await batchWriteBackMetadata(['id-1', 'id-2'], true);
    expect(response.written).toBe(2);
    expect(response.renamed).toBe(1);
    expect(mockFetch).toHaveBeenCalledWith('/api/v1/audiobooks/batch-write-back', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({ book_ids: ['id-1', 'id-2'], organize: true, force: false }),
    }));
  });
});
