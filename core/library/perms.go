// file: core/library/perms.go
// version: 1.1.0
// guid: a2b3c4d5-e6f7-8901-ab23-cd45ef678901
// last-edited: 2026-11-19

package library

import "os"

// Standard permission modes for production use on Linux with POSIX ACLs.
//
// ACLs use the group permission bits as the ACL mask. Without group-write
// in the base mode, ACL entries granting write are silently masked out.
// Always use these constants instead of raw octal literals.

// DirMode is the standard permission for directories (rwxrwxr-x).
const DirMode os.FileMode = 0o775

// LedgerDirMode is the creation mode of the append-only ledger directory
// (rwxr-x---). Unlike DirMode it has no group write: a ledger's value is that
// it is never rewritten, and a directory the group can write is one where any
// group member can unlink or rename the ledger file. Only the user the service
// runs as writes there. The umask can only narrow it further.
const LedgerDirMode os.FileMode = 0o750

// FileMode is the standard permission for regular files (rw-rw-r--).
const FileMode os.FileMode = 0o664

// SecretFileMode is for sensitive files like encryption keys (rw-------).
const SecretFileMode os.FileMode = 0o600
