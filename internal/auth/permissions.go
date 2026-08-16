// file: internal/auth/permissions.go
// version: 1.1.0
// guid: 2d8a1f4e-5c3b-4f90-a7d6-1e8c0f2b9a45
//
// Permission atoms for the multi-user model (spec 3.7). Permissions
// are Go string constants — not DB rows. Roles carry inline lists of
// these constants in `database.Role.Permissions`.
//
// Adding a new permission is a source change: add a const here, then
// reference it from a requirePerm(...) middleware call at the route
// where the check should run. The compile step catches typos.

package auth

// Permission is a named capability a user may be granted via role
// membership.
type Permission = string

const (
	// PermLibraryView gates read access to books, authors, series,
	// playlists, and activity. Baseline permission — every role in the
	// seed set includes it.
	PermLibraryView Permission = "library.view"

	// PermLibraryEditMetadata gates title / author / series / tag edits,
	// metadata fetch + apply, and batch metadata operations. Does NOT
	// grant file-mutation rights.
	PermLibraryEditMetadata Permission = "library.edit_metadata"

	// PermLibraryDelete gates deletion of books (soft or hard) and
	// related cascades (version rows, book_files).
	PermLibraryDelete Permission = "library.delete"

	// PermLibraryOrganize gates file-level organize operations: rename,
	// move, re-layout under the managed library tree.
	PermLibraryOrganize Permission = "library.organize"

	// PermScanTrigger gates starting library scans and imports.
	PermScanTrigger Permission = "scan.trigger"

	// PermIntegrationsManage gates configuring and triggering external
	// integrations: iTunes (library path, sync, writeback), deluge,
	// openai, openlibrary, hardcover, audible.
	PermIntegrationsManage Permission = "integrations.manage"

	// PermUsersManage gates creating/deleting users, generating invites,
	// assigning roles, regenerating passwords, managing API keys for
	// other users.
	PermUsersManage Permission = "users.manage"

	// PermSettingsManage gates editing global/system configuration.
	PermSettingsManage Permission = "settings.manage"

	// PermPlaylistsCreate gates creating and editing user-owned
	// playlists (3.4). Read access is covered by PermLibraryView.
	PermPlaylistsCreate Permission = "playlists.create"

	// PermCollectionsManage gates creating, editing and deleting
	// collections (static or dynamic). Collections are SERVER-WIDE and
	// shared by every user, which is why this is a separate atom from
	// PermPlaylistsCreate: a playlist is the creator's own, so granting
	// it affects nobody else, while a collection is part of what every
	// user sees. Read access is covered by PermLibraryView.
	PermCollectionsManage Permission = "collections.manage"

	// PermRequestsCreate — reserved for the future "request a book"
	// feature. A viewer without full library.edit_metadata can still
	// file a request for an admin to fulfill.
	PermRequestsCreate Permission = "requests.create"

	// PermRequestsApprove — reserved for the future request-approval
	// flow (admin-equivalent for the requests subsystem).
	PermRequestsApprove Permission = "requests.approve"
)

// All returns every permission constant defined in this package.
// Useful for seeding the admin role and for validating role JSON at
// registration time.
func All() []Permission {
	return []Permission{
		PermLibraryView,
		PermLibraryEditMetadata,
		PermLibraryDelete,
		PermLibraryOrganize,
		PermScanTrigger,
		PermIntegrationsManage,
		PermUsersManage,
		PermSettingsManage,
		PermPlaylistsCreate,
		PermCollectionsManage,
		PermRequestsCreate,
		PermRequestsApprove,
	}
}

// IsKnown reports whether p is a permission constant defined here.
// Used during role-JSON validation to reject typo'd or dropped
// permission strings before they reach the database.
func IsKnown(p Permission) bool {
	switch p {
	case PermLibraryView,
		PermLibraryEditMetadata,
		PermLibraryDelete,
		PermLibraryOrganize,
		PermScanTrigger,
		PermIntegrationsManage,
		PermUsersManage,
		PermSettingsManage,
		PermPlaylistsCreate,
		PermCollectionsManage,
		PermRequestsCreate,
		PermRequestsApprove:
		return true
	}
	return false
}
