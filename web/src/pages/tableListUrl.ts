// file: web/src/pages/tableListUrl.ts
// version: 1.0.0
// guid: 6f2a8c15-3e90-4b74-a1d8-0c95e7b24f36
// last-edited: 2026-11-05
//
// The address-bar contract shared by the paged, server-searched list pages
// (Series, Authors): which query parameter carries which piece of the table's
// state, and the two pure functions between the page's state and the query
// string. A default is left out of the URL.
//
//   q       search text
//   filter  the page's row filter (a closed set per page)
//   sort    sort column key, `order` its direction (`desc`; asc is the default)
//   page    1-based page number (the pages themselves count from 0)
//   size    rows per page, one of the table's page-size options

import type { UrlValue } from '../hooks/useUrlState';

export const TABLE_PAGE_SIZES: readonly number[] = [10, 25, 50, 100];
export const TABLE_DEFAULT_PAGE_SIZE = 25;

export interface TableListSpec {
  filters: readonly string[];
  defaultFilter: string;
  /** Sort keys the table can sort by. */
  sortKeys: readonly string[];
  defaultSort: string;
}

export interface TableListUrlState {
  search: string;
  filter: string;
  sortField: string;
  sortDir: 'asc' | 'desc';
  /** 0-based, as the pages hold it. */
  page: number;
  rowsPerPage: number;
}

export function readTableListUrl(params: URLSearchParams, spec: TableListSpec): TableListUrlState {
  const page = Number(params.get('page'));
  const size = Number(params.get('size'));
  const filter = params.get('filter');
  const sort = params.get('sort');
  return {
    search: params.get('q') ?? '',
    filter: spec.filters.find((f) => f === filter) ?? spec.defaultFilter,
    sortField: spec.sortKeys.find((k) => k === sort) ?? spec.defaultSort,
    sortDir: params.get('order') === 'desc' ? 'desc' : 'asc',
    page: Number.isInteger(page) && page > 0 ? page - 1 : 0,
    rowsPerPage: TABLE_PAGE_SIZES.find((n) => n === size) ?? TABLE_DEFAULT_PAGE_SIZE,
  };
}

export function tableListUrlOwned(
  s: TableListUrlState,
  spec: TableListSpec
): Record<string, UrlValue> {
  return {
    q: s.search,
    filter: s.filter === spec.defaultFilter ? null : s.filter,
    sort: s.sortField === spec.defaultSort ? null : s.sortField,
    order: s.sortDir === 'desc' ? 'desc' : null,
    page: s.page > 0 ? String(s.page + 1) : null,
    size: s.rowsPerPage === TABLE_DEFAULT_PAGE_SIZE ? null : String(s.rowsPerPage),
  };
}
