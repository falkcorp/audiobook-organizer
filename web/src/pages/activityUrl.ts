// file: web/src/pages/activityUrl.ts
// version: 1.1.0
// guid: 45954b0f-cd73-4c48-9acd-b356d25b83b2
// last-edited: 2026-11-05
//
// The bound version of the Activity URL contract (F9-05). The mapping itself is
// components/activity/activityUrlCore.ts, which takes the event types `?type=`
// accepts as a parameter; this file passes the audiobook list, so its importers
// and activityUrl.test.ts read the same names and signatures as before.

import {
  readActivityUrl as readActivityUrlWith,
  type ActivityUrlState,
} from '../components/activity/activityUrlCore';
import { ACTIVITY_EVENT_TYPES } from '../modules/books/activity/eventTypes';

export {
  ACTIVITY_DEFAULT_PAGE_SIZE,
  ACTIVITY_DEFAULT_TIERS,
  ACTIVITY_LEVELS,
  ACTIVITY_PAGE_SIZES,
  ACTIVITY_TIERS,
  activityFiltersDiffer,
  activityUrlOwned,
  isDefaultTiers,
  type ActivityUrlState,
} from '../components/activity/activityUrlCore';
export { ACTIVITY_EVENT_TYPES };

/** The state a query string names, with the audiobook event types. */
export const readActivityUrl = (params: URLSearchParams, defaultSince: string): ActivityUrlState =>
  readActivityUrlWith(params, defaultSince, ACTIVITY_EVENT_TYPES);
