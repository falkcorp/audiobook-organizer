// file: internal/audiobooks/delete_primary_handoff_test.go
// version: 1.0.0
// guid: 1e6b9d42-7c38-4a05-b2f1-8d4e6a3c9b70
// last-edited: 2026-09-24

package audiobooks

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary/vptest"
)

func handoffFixture(t *testing.T) (*vptest.Fixture, *AudiobookService) {
	t.Helper()
	f := vptest.New(t)
	prev := config.AppConfig.RootDir
	config.AppConfig.RootDir = f.Root
	t.Cleanup(func() { config.AppConfig.RootDir = prev })
	return f, NewAudiobookService(f.S)
}

// Soft-deleting a group's primary hands the flag on; restoring it does not
// bring back a second primary.
func TestDeleteAudiobook_SoftDeletedPrimaryHandsOnAndRestoreKeepsOne(t *testing.T) {
	f, svc := handoffFixture(t)
	inc := f.Book(t, vptest.Spec{ID: "inc", Group: "g", Primary: "true"})
	next := f.Book(t, vptest.Spec{ID: "next", Group: "g", Primary: "false"})

	_, err := svc.DeleteAudiobook(context.Background(), inc, &DeleteAudiobookOptions{SoftDelete: true})
	require.NoError(t, err)
	f.RequireSinglePrimary(t, "g", next)

	_, err = svc.RestoreAudiobook(context.Background(), inc)
	require.NoError(t, err)
	f.RequireSinglePrimary(t, "g", next)
	require.Equal(t, "false", f.Flag(t, inc))
}

// Hard-deleting a group's (fileless) primary hands the flag on.
func TestDeleteAudiobook_HardDeletedPrimaryHandsOn(t *testing.T) {
	f, svc := handoffFixture(t)
	inc := f.Book(t, vptest.Spec{ID: "inc", Group: "g", Primary: "true", NoFile: true})
	next := f.Book(t, vptest.Spec{ID: "next", Group: "g", Primary: "false"})

	_, err := svc.DeleteAudiobook(context.Background(), inc, &DeleteAudiobookOptions{})
	require.NoError(t, err)
	f.RequireSinglePrimary(t, "g", next)
}
