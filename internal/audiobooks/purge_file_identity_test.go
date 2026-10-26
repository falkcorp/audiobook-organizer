// file: internal/audiobooks/purge_file_identity_test.go
// version: 1.0.0
// guid: c41e7a92-5d08-4b63-8f1a-2e9d0b6c7a35
// last-edited: 2026-10-25

package audiobooks

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func identityFromJSON(t *testing.T, raw string) fileIdentity {
	t.Helper()
	var id fileIdentity
	require.NoError(t, json.Unmarshal([]byte(raw), &id))
	return id
}

// B-1. The device number is not part of the identity: ZFS, NFS and btrfs assign
// st_dev at mount time, so a record read back after a reboot or a remount
// carries a different one for the very same file. A record that names another
// device (the records an earlier build wrote did) still matches.
func TestFileIdentity_DeviceNumberDoesNotDecideTheMatch(t *testing.T) {
	before := identityFromJSON(t, `{"known":true,"has_inode":true,"dev":41,"ino":9001,"size":1234,"mtime_ns":1700000000000000001}`)
	after := identityFromJSON(t, `{"known":true,"has_inode":true,"dev":57,"ino":9001,"size":1234,"mtime_ns":1700000000000000001}`)
	assert.True(t, before.matches(after, false), "same inode, size and mtime on a remounted device is the same file")
	assert.True(t, before.matches(after, true))
}

func TestFileIdentity_InodeSizeAndMtimeStillDecide(t *testing.T) {
	base := identityFromJSON(t, `{"known":true,"has_inode":true,"ino":9001,"size":1234,"mtime_ns":77}`)
	otherIno := identityFromJSON(t, `{"known":true,"has_inode":true,"ino":9002,"size":1234,"mtime_ns":77}`)
	otherSize := identityFromJSON(t, `{"known":true,"has_inode":true,"ino":9001,"size":1235,"mtime_ns":77}`)
	otherMtime := identityFromJSON(t, `{"known":true,"has_inode":true,"ino":9001,"size":1234,"mtime_ns":78}`)
	assert.False(t, base.matches(otherIno, false), "a different inode is a different file")
	assert.False(t, base.matches(otherSize, false))
	assert.False(t, base.matches(otherMtime, false))
	// An empty directory's size and mtime move with its entries; its inode decides.
	assert.False(t, base.matches(otherIno, true))
	assert.True(t, base.matches(otherSize, true))
	assert.True(t, base.matches(otherMtime, true))
	// An unknown identity never matches, not even itself.
	assert.False(t, fileIdentity{}.matches(fileIdentity{}, false))
	assert.False(t, base.matches(fileIdentity{}, false))
}
