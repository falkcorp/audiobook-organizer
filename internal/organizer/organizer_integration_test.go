// file: internal/organizer/organizer_integration_test.go
// version: 1.0.1
// guid: d4e5f6a7-b8c9-0123-defa-456789012bcd

package organizer

import (
	"os"
	"path/filepath"
	"testing"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOrganizer_CopyStrategy(t *testing.T) {
	rootDir := t.TempDir()
	importDir := t.TempDir()

	cfg := &config.Config{
		RootDir:              rootDir,
		OrganizationStrategy: "copy",
		FolderNamingPattern:  "{author}/{title}",
		FileNamingPattern:    "{title}",
	}
	org := NewOrganizer(cfg)

	srcPath := filepath.Join(importDir, "test.m4b")
	require.NoError(t, os.WriteFile(srcPath, []byte("audiobook-content"), 0644))

	book := &database.Book{
		Title:    "The Hobbit",
		FilePath: srcPath,
		Format:   ".m4b",
		Author:   &database.Author{Name: "J.R.R. Tolkien"},
	}

	newPath, _, err := org.OrganizeBook(book)
	require.NoError(t, err)

	assert.Contains(t, newPath, rootDir)
	assert.Contains(t, newPath, "J.R.R. Tolkien")
	assert.Contains(t, newPath, "The Hobbit")

	// Verify file exists at new location
	_, err = os.Stat(newPath)
	assert.NoError(t, err)

	// Verify source file still exists (copy, not move)
	_, err = os.Stat(srcPath)
	assert.NoError(t, err)

	// Verify content matches
	srcData, _ := os.ReadFile(srcPath)
	dstData, _ := os.ReadFile(newPath)
	assert.Equal(t, srcData, dstData)
}

func TestOrganizer_HardlinkStrategy(t *testing.T) {
	rootDir := t.TempDir()
	importDir := t.TempDir()

	cfg := &config.Config{
		RootDir:              rootDir,
		OrganizationStrategy: "hardlink",
		FolderNamingPattern:  "{author}",
		FileNamingPattern:    "{title}",
	}
	org := NewOrganizer(cfg)

	srcPath := filepath.Join(importDir, "test.m4b")
	require.NoError(t, os.WriteFile(srcPath, []byte("hardlink-test"), 0644))

	book := &database.Book{
		Title:    "Dune",
		FilePath: srcPath,
		Format:   ".m4b",
		Author:   &database.Author{Name: "Frank Herbert"},
	}

	newPath, _, err := org.OrganizeBook(book)
	require.NoError(t, err)

	// Verify hardlink: both files share same inode
	srcInfo, err := os.Stat(srcPath)
	require.NoError(t, err)
	dstInfo, err := os.Stat(newPath)
	require.NoError(t, err)
	assert.True(t, os.SameFile(srcInfo, dstInfo), "should be hardlinked")
}

func TestOrganizer_ComplexPatterns(t *testing.T) {
	rootDir := t.TempDir()

	seriesSeq := 2
	tests := []struct {
		name          string
		folderPattern string
		filePattern   string
		book          *database.Book
		wantContains  []string
	}{
		{
			name:          "all fields populated",
			folderPattern: "{author}/{series}",
			filePattern:   "{series_number} - {title}",
			book: &database.Book{
				Title:          "The Two Towers",
				FilePath:       filepath.Join(t.TempDir(), "src.m4b"),
				Format:         ".m4b",
				Author:         &database.Author{Name: "J.R.R. Tolkien"},
				Series:         &database.Series{Name: "Lord of the Rings"},
				SeriesSequence: &seriesSeq,
			},
			wantContains: []string{"J.R.R. Tolkien", "Lord of the Rings", "2 - The Two Towers"},
		},
		{
			name:          "missing series falls back",
			folderPattern: "{author}/{series}",
			filePattern:   "{title}",
			book: &database.Book{
				Title:    "Standalone Novel",
				FilePath: filepath.Join(t.TempDir(), "src.m4b"),
				Format:   ".m4b",
				Author:   &database.Author{Name: "Some Author"},
			},
			wantContains: []string{"Some Author", "Standalone Novel"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.NoError(t, os.WriteFile(tt.book.FilePath, []byte("test"), 0644))

			cfg := &config.Config{
				RootDir:              rootDir,
				OrganizationStrategy: "copy",
				FolderNamingPattern:  tt.folderPattern,
				FileNamingPattern:    tt.filePattern,
			}
			org := NewOrganizer(cfg)

			newPath, _, err := org.OrganizeBook(tt.book)
			require.NoError(t, err)
			for _, want := range tt.wantContains {
				assert.Contains(t, newPath, want)
			}
		})
	}
}
