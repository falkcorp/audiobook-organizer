#!/usr/bin/env python3
"""For each struct-based service in the target list, extract:
  - The store-field name and the struct it lives in
  - The set of methods called on that field
  - The Store interface methods those resolve to, mapped to sub-interfaces

Produces a per-file report the operator uses to write the narrowed interface.
Does NOT edit files — we do the edits by hand per file after reviewing output,
because transitive deps may force composite changes (see PR #380 / #388).
"""

from __future__ import annotations

import pathlib
import re

FILES = [
    "internal/server/ai_scan_pipeline.go",
    "internal/server/audiobook_update_service.go",
    "internal/server/author_series_service.go",
    "internal/server/batch_service.go",
    "internal/server/changelog_service.go",
    "internal/server/dashboard_service.go",
    "internal/server/diagnostics_service.go",
    "internal/server/import_path_service.go",
    "internal/server/import_service.go",
    "internal/server/isbn_enrichment.go",
    "internal/server/merge_service.go",
    "internal/server/metadata_upgrade.go",
    "internal/server/organize_preview_service.go",
    "internal/server/rename_service.go",
    "internal/server/revert_service.go",
    "internal/server/scan_service.go",
    "internal/server/system_service.go",
    "internal/server/work_service.go",
]

# Method → sub-interface map (matches iface_*.go on main)
METHOD_TO_IFACE = {
    # BookReader
    "GetBookByID": "BookReader",
    "GetAllBooks": "BookReader",
    "GetBookByFilePath": "BookReader",
    "GetBookByITunesPersistentID": "BookReader",
    "GetBookByFileHash": "BookReader",
    "GetBookByOriginalHash": "BookReader",
    "GetBookByOrganizedHash": "BookReader",
    "GetDuplicateBooks": "BookReader",
    "GetFolderDuplicates": "BookReader",
    "GetDuplicateBooksByMetadata": "BookReader",
    "GetBooksByTitleInDir": "BookReader",
    "GetBooksBySeriesID": "BookReader",
    "GetBooksByAuthorID": "BookReader",
    "GetBooksByVersionGroup": "BookReader",
    "SearchBooks": "BookReader",
    "CountBooks": "BookReader",
    "ListSoftDeletedBooks": "BookReader",
    "GetBookSnapshots": "BookReader",
    "GetBookAtVersion": "BookReader",
    "GetBookTombstone": "BookReader",
    "ListBookTombstones": "BookReader",
    "GetITunesDirtyBooks": "BookReader",
    # BookWriter
    "CreateBook": "BookWriter",
    "UpdateBook": "BookWriter",
    "DeleteBook": "BookWriter",
    "SetLastWrittenAt": "BookWriter",
    "MarkITunesSynced": "BookWriter",
    "RevertBookToVersion": "BookWriter",
    "PruneBookSnapshots": "BookWriter",
    "CreateBookTombstone": "BookWriter",
    "DeleteBookTombstone": "BookWriter",
    # AuthorReader
    "GetAllAuthors": "AuthorReader",
    "GetAuthorByID": "AuthorReader",
    "GetAuthorByName": "AuthorReader",
    "GetAuthorAliases": "AuthorReader",
    "GetAllAuthorAliases": "AuthorReader",
    "FindAuthorByAlias": "AuthorReader",
    "GetBookAuthors": "AuthorReader",
    "GetBooksByAuthorIDWithRole": "AuthorReader",
    "GetAllAuthorBookCounts": "AuthorReader",
    "GetAllAuthorFileCounts": "AuthorReader",
    "GetAuthorTombstone": "AuthorReader",
    # AuthorWriter
    "CreateAuthor": "AuthorWriter",
    "DeleteAuthor": "AuthorWriter",
    "UpdateAuthorName": "AuthorWriter",
    "CreateAuthorAlias": "AuthorWriter",
    "DeleteAuthorAlias": "AuthorWriter",
    "SetBookAuthors": "AuthorWriter",
    "CreateAuthorTombstone": "AuthorWriter",
    "ResolveTombstoneChains": "AuthorWriter",
    # SeriesReader
    "GetAllSeries": "SeriesReader",
    "GetSeriesByID": "SeriesReader",
    "GetSeriesByName": "SeriesReader",
    "GetAllSeriesBookCounts": "SeriesReader",
    "GetAllSeriesFileCounts": "SeriesReader",
    # SeriesWriter
    "CreateSeries": "SeriesWriter",
    "DeleteSeries": "SeriesWriter",
    "UpdateSeriesName": "SeriesWriter",
    # UserReader
    "GetUserByID": "UserReader",
    "GetUserByUsername": "UserReader",
    "GetUserByEmail": "UserReader",
    "ListUsers": "UserReader",
    "CountUsers": "UserReader",
    # UserWriter
    "CreateUser": "UserWriter",
    "UpdateUser": "UserWriter",
    # OperationStore (hot — lots of methods)
    "CreateOperation": "OperationStore",
    "GetOperationByID": "OperationStore",
    "GetRecentOperations": "OperationStore",
    "ListOperations": "OperationStore",
    "UpdateOperationStatus": "OperationStore",
    "UpdateOperationError": "OperationStore",
    "UpdateOperationResultData": "OperationStore",
    "SaveOperationState": "OperationStore",
    "GetOperationState": "OperationStore",
    "SaveOperationParams": "OperationStore",
    "GetOperationParams": "OperationStore",
    "DeleteOperationState": "OperationStore",
    "GetInterruptedOperations": "OperationStore",
    "CreateOperationChange": "OperationStore",
    "GetOperationChanges": "OperationStore",
    "GetBookChanges": "OperationStore",
    "RevertOperationChanges": "OperationStore",
    "AddOperationLog": "OperationStore",
    "GetOperationLogs": "OperationStore",
    "SaveOperationSummaryLog": "OperationStore",
    "GetOperationSummaryLog": "OperationStore",
    "ListOperationSummaryLogs": "OperationStore",
    "CreateOperationResult": "OperationStore",
    "GetOperationResults": "OperationStore",
    "GetRecentCompletedOperations": "OperationStore",
    "PruneOperationLogs": "OperationStore",
    "PruneOperationChanges": "OperationStore",
    "DeleteOperationsByStatus": "OperationStore",
    # Other single-interfaces
    "Close": "LifecycleStore",
    "Reset": "LifecycleStore",
    "CreateNarrator": "NarratorStore",
    "GetNarratorByID": "NarratorStore",
    "GetNarratorByName": "NarratorStore",
    "ListNarrators": "NarratorStore",
    "GetBookNarrators": "NarratorStore",
    "SetBookNarrators": "NarratorStore",
    "GetAllWorks": "WorkStore",
    "GetWorkByID": "WorkStore",
    "CreateWork": "WorkStore",
    "UpdateWork": "WorkStore",
    "DeleteWork": "WorkStore",
    "GetBooksByWorkID": "WorkStore",
    "CreateSession": "SessionStore",
    "GetSession": "SessionStore",
    "RevokeSession": "SessionStore",
    "ListUserSessions": "SessionStore",
    "DeleteExpiredSessions": "SessionStore",
    "GetRoleByID": "RoleStore",
    "GetRoleByName": "RoleStore",
    "ListRoles": "RoleStore",
    "CreateRole": "RoleStore",
    "UpdateRole": "RoleStore",
    "DeleteRole": "RoleStore",
    "CreateAPIKey": "APIKeyStore",
    "GetAPIKey": "APIKeyStore",
    "ListAPIKeysForUser": "APIKeyStore",
    "RevokeAPIKey": "APIKeyStore",
    "TouchAPIKeyLastUsed": "APIKeyStore",
    "CreateInvite": "InviteStore",
    "GetInvite": "InviteStore",
    "ListActiveInvites": "InviteStore",
    "DeleteInvite": "InviteStore",
    "ConsumeInvite": "InviteStore",
    "GetUserPreference": "UserPreferenceStore",
    "SetUserPreference": "UserPreferenceStore",
    "GetAllUserPreferences": "UserPreferenceStore",
    "SetUserPreferenceForUser": "UserPreferenceStore",
    "GetUserPreferenceForUser": "UserPreferenceStore",
    "GetAllPreferencesForUser": "UserPreferenceStore",
    "SetUserPosition": "UserPositionStore",
    "GetUserPosition": "UserPositionStore",
    "ListUserPositionsForBook": "UserPositionStore",
    "ClearUserPositions": "UserPositionStore",
    "SetUserBookState": "UserPositionStore",
    "GetUserBookState": "UserPositionStore",
    "ListUserBookStatesByStatus": "UserPositionStore",
    "ListUserPositionsSince": "UserPositionStore",
    "CreateBookVersion": "BookVersionStore",
    "GetBookVersion": "BookVersionStore",
    "GetBookVersionsByBookID": "BookVersionStore",
    "GetActiveVersionForBook": "BookVersionStore",
    "UpdateBookVersion": "BookVersionStore",
    "DeleteBookVersion": "BookVersionStore",
    "GetBookVersionByTorrentHash": "BookVersionStore",
    "ListTrashedBookVersions": "BookVersionStore",
    "ListPurgedBookVersions": "BookVersionStore",
    "CreateBookFile": "BookFileStore",
    "UpdateBookFile": "BookFileStore",
    "GetBookFiles": "BookFileStore",
    "GetBookFileByID": "BookFileStore",
    "GetBookFileByPID": "BookFileStore",
    "GetBookFileByPath": "BookFileStore",
    "DeleteBookFile": "BookFileStore",
    "DeleteBookFilesForBook": "BookFileStore",
    "UpsertBookFile": "BookFileStore",
    "BatchUpsertBookFiles": "BookFileStore",
    "MoveBookFilesToBook": "BookFileStore",
    "CreateBookSegment": "BookSegmentStore",
    "UpdateBookSegment": "BookSegmentStore",
    "ListBookSegments": "BookSegmentStore",
    "MergeBookSegments": "BookSegmentStore",
    "GetBookSegmentByID": "BookSegmentStore",
    "MoveSegmentsToBook": "BookSegmentStore",
    "CreatePlaylist": "PlaylistStore",
    "GetPlaylistByID": "PlaylistStore",
    "GetPlaylistBySeriesID": "PlaylistStore",
    "AddPlaylistItem": "PlaylistStore",
    "GetPlaylistItems": "PlaylistStore",
    "CreateUserPlaylist": "UserPlaylistStore",
    "GetUserPlaylist": "UserPlaylistStore",
    "GetUserPlaylistByName": "UserPlaylistStore",
    "GetUserPlaylistByITunesPID": "UserPlaylistStore",
    "ListUserPlaylists": "UserPlaylistStore",
    "UpdateUserPlaylist": "UserPlaylistStore",
    "DeleteUserPlaylist": "UserPlaylistStore",
    "ListDirtyUserPlaylists": "UserPlaylistStore",
    "GetAllImportPaths": "ImportPathStore",
    "GetImportPathByID": "ImportPathStore",
    "GetImportPathByPath": "ImportPathStore",
    "CreateImportPath": "ImportPathStore",
    "UpdateImportPath": "ImportPathStore",
    "DeleteImportPath": "ImportPathStore",
    # Tags
    "AddBookTag": "TagStore",
    "AddBookTagWithSource": "TagStore",
    "RemoveBookTag": "TagStore",
    "RemoveBookTagsByPrefix": "TagStore",
    "GetBookTags": "TagStore",
    "GetBookTagsDetailed": "TagStore",
    "SetBookTags": "TagStore",
    "ListAllTags": "TagStore",
    "GetBooksByTag": "TagStore",
    "AddAuthorTag": "TagStore",
    "AddAuthorTagWithSource": "TagStore",
    "RemoveAuthorTag": "TagStore",
    "RemoveAuthorTagsByPrefix": "TagStore",
    "GetAuthorTags": "TagStore",
    "GetAuthorTagsDetailed": "TagStore",
    "SetAuthorTags": "TagStore",
    "ListAllAuthorTags": "TagStore",
    "GetAuthorsByTag": "TagStore",
    "AddSeriesTag": "TagStore",
    "AddSeriesTagWithSource": "TagStore",
    "RemoveSeriesTag": "TagStore",
    "RemoveSeriesTagsByPrefix": "TagStore",
    "GetSeriesTags": "TagStore",
    "GetSeriesTagsDetailed": "TagStore",
    "SetSeriesTags": "TagStore",
    "ListAllSeriesTags": "TagStore",
    "GetSeriesByTag": "TagStore",
    "GetBookUserTags": "UserTagStore",
    "SetBookUserTags": "UserTagStore",
    "AddBookUserTag": "UserTagStore",
    "RemoveBookUserTag": "UserTagStore",
    # Metadata
    "GetMetadataFieldStates": "MetadataStore",
    "UpsertMetadataFieldState": "MetadataStore",
    "DeleteMetadataFieldState": "MetadataStore",
    "RecordMetadataChange": "MetadataStore",
    "GetMetadataChangeHistory": "MetadataStore",
    "GetBookChangeHistory": "MetadataStore",
    "GetBookAlternativeTitles": "MetadataStore",
    # Hash blocklist
    "IsHashBlocked": "HashBlocklistStore",
    "AddBlockedHash": "HashBlocklistStore",
    "RemoveBlockedHash": "HashBlocklistStore",
    "GetAllBlockedHashes": "HashBlocklistStore",
    "GetBlockedHashByHash": "HashBlocklistStore",
    # iTunes state
    "SaveLibraryFingerprint": "ITunesStateStore",
    "GetLibraryFingerprint": "ITunesStateStore",
    "CreateDeferredITunesUpdate": "ITunesStateStore",
    "GetPendingDeferredITunesUpdates": "ITunesStateStore",
    "MarkDeferredITunesUpdateApplied": "ITunesStateStore",
    "GetDeferredITunesUpdatesByBookID": "ITunesStateStore",
    # Path history
    "RecordPathChange": "PathHistoryStore",
    "GetBookPathHistory": "PathHistoryStore",
    # External IDs
    "CreateExternalIDMapping": "ExternalIDStore",
    "GetBookByExternalID": "ExternalIDStore",
    "GetExternalIDsForBook": "ExternalIDStore",
    "IsExternalIDTombstoned": "ExternalIDStore",
    "TombstoneExternalID": "ExternalIDStore",
    "ReassignExternalIDs": "ExternalIDStore",
    "BulkCreateExternalIDMappings": "ExternalIDStore",
    "MarkExternalIDRemoved": "ExternalIDStore",
    "SetExternalIDProvenance": "ExternalIDStore",
    "GetRemovedExternalIDs": "ExternalIDStore",
    # Raw KV
    "SetRaw": "RawKVStore",
    "GetRaw": "RawKVStore",
    "DeleteRaw": "RawKVStore",
    "ScanPrefix": "RawKVStore",
    # Playback
    "AddPlaybackEvent": "PlaybackStore",
    "ListPlaybackEvents": "PlaybackStore",
    "UpdatePlaybackProgress": "PlaybackStore",
    "GetPlaybackProgress": "PlaybackStore",
    "IncrementBookPlayStats": "PlaybackStore",
    "GetBookStats": "PlaybackStore",
    "IncrementUserListenStats": "PlaybackStore",
    "GetUserStats": "PlaybackStore",
    # Settings
    "GetSetting": "SettingsStore",
    "SetSetting": "SettingsStore",
    "GetAllSettings": "SettingsStore",
    "DeleteSetting": "SettingsStore",
    # Stats
    "CountFiles": "StatsStore",
    "CountAuthors": "StatsStore",
    "CountSeries": "StatsStore",
    "GetBookCountsByLocation": "StatsStore",
    "GetBookSizesByLocation": "StatsStore",
    "GetDashboardStats": "StatsStore",
    # Maintenance
    "Optimize": "MaintenanceStore",
    "GetScanCacheMap": "MaintenanceStore",
    "UpdateScanCache": "MaintenanceStore",
    "MarkNeedsRescan": "MaintenanceStore",
    "GetDirtyBookFolders": "MaintenanceStore",
    # System activity
    "AddSystemActivityLog": "SystemActivityStore",
    "GetSystemActivityLogs": "SystemActivityStore",
    "PruneSystemActivityLogs": "SystemActivityStore",
}

FIELD_RE = re.compile(r"^\s+([a-z][a-zA-Z0-9_]*)\s+database\.Store\b", re.MULTILINE)


def analyze(path: pathlib.Path) -> None:
    text = path.read_text()
    fields = list(dict.fromkeys(FIELD_RE.findall(text)))
    if not fields:
        print("  (no database.Store field found)")
        return
    print(f"  field(s): {', '.join(fields)}")
    for name in fields:
        pat = re.compile(
            rf"(?:^|[^a-zA-Z_0-9]){re.escape(name)}\.([A-Z][a-zA-Z0-9]+)\s*\(",
            re.MULTILINE,
        )
        methods = sorted(set(pat.findall(text)))
        if not methods:
            print(f"    {name}: (zero calls)")
            continue
        ifaces = set()
        unknown = []
        for m in methods:
            iface = METHOD_TO_IFACE.get(m)
            if iface:
                ifaces.add(iface)
            else:
                unknown.append(m)
        print(f"    {name}: {len(methods)} method(s), {len(ifaces)} iface(s)")
        print(f"      ifaces needed: {sorted(ifaces)}")
        if unknown:
            print(f"      UNKNOWN METHODS: {unknown}")


def main() -> int:
    repo = pathlib.Path(__file__).resolve().parent.parent
    for rel in FILES:
        print(f"=== {rel} ===")
        p = repo / rel
        if not p.exists():
            print("  MISSING")
            continue
        analyze(p)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
