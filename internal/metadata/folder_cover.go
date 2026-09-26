// file: internal/metadata/folder_cover.go
// version: 1.0.0
// guid: c18e7570-8f96-470e-afc2-20b0dfa98ddf
// last-edited: 2026-09-26

package metadata

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/gif" // register the gif decoder for image.DecodeConfig
	"image/jpeg"
	_ "image/png" // register the png decoder for image.DecodeConfig
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	_ "golang.org/x/image/bmp"  // register the bmp decoder
	_ "golang.org/x/image/webp" // register the webp decoder
)

// Folder cover images: a picture a user dropped into the book's folder.
//
// The rule, in full (FindFolderCover):
//
//  1. Candidates are the regular files directly in the folder whose extension,
//     compared case-insensitively, is .jpg, .jpeg, .png, .bmp, .webp or .gif.
//     Hidden files (a leading ".", which includes macOS "._" AppleDouble
//     sidecars) are ignored.
//  2. The image header must decode (image.DecodeConfig). The FORMAT used from
//     here on is the decoded one, never the extension, so a PNG named
//     "cover.jpg" is stored and served as the PNG it is.
//  3. Too small to be a cover: the short side is under FolderCoverMinSide
//     (200 px). Thumbnails such as AlbumArtSmall.jpg (75 px) and
//     Folder.jpg-style Windows Media Player thumbs fall out here.
//  4. Not a front cover: the filename contains one of the words in
//     folderCoverRejectWords (back, rear, spine, inlay, inside, tray,
//     booklet, disc, disk, cd, label, thumb, thumbnail), matched as whole
//     words so "Background.jpg" is not "back"; or the image is a strip more
//     than FolderCoverMaxAspect (2.5:1) long, which is a spine or a banner
//     and never a cover. Audiobook covers are square or 2:3.
//  5. Larger than FolderCoverMaxBytes (40 MiB) on disk: skipped rather than
//     read into memory.
//
// Ranking: a file whose name contains one of folderCoverPreferredWords
// (cover, folder, front, album/albumart, artwork) wins over any file that does
// not, earlier words first; within the same rank the larger image by pixel
// area wins; the name breaks a remaining tie so the choice is deterministic.

// FolderCoverMinSide is the smallest short side, in pixels, a folder image may
// have and still be considered a cover.
const FolderCoverMinSide = 200

// FolderCoverMaxAspect is the largest long-side/short-side ratio a cover may
// have. Covers are square (1:1) or book-shaped (2:3).
const FolderCoverMaxAspect = 2.5

// FolderCoverMaxBytes caps the size of an image file that is considered.
const FolderCoverMaxBytes = 40 << 20

// folderCoverExts are the image extensions a folder cover may carry. Compared
// against the lowercased extension.
var folderCoverExts = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".bmp": true, ".webp": true, ".gif": true,
}

// folderCoverPreferredWords ranks named covers; the index is the rank.
var folderCoverPreferredWords = []string{"cover", "folder", "front", "album", "albumart", "artwork"}

// folderCoverRejectWords mark an image that is some other part of the package.
var folderCoverRejectWords = map[string]bool{
	"back": true, "rear": true, "spine": true, "inlay": true, "inside": true, "tray": true,
	"booklet": true, "disc": true, "disk": true, "cd": true, "label": true, "thumb": true, "thumbnail": true,
}

// FolderCover is one ranked candidate image found in a book's folder.
type FolderCover struct {
	Path   string `json:"path"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	// Format is the decoded image format: jpeg, png, gif, webp or bmp.
	Format string `json:"format"`
	// NameRank is the index of the preferred word the name carries, or
	// len(folderCoverPreferredWords) for an unnamed image.
	NameRank int `json:"name_rank"`
}

// Area is the image's pixel area.
func (c FolderCover) Area() int { return c.Width * c.Height }

// FolderCoverFilter narrows the candidates FindFolderCover may return.
type FolderCoverFilter struct {
	// OnlyStems, when non-nil, admits only images whose lowercased filename
	// stem (the name without its extension) is in the set. Used for a folder
	// shared by several books: there, only "<book file stem>.jpg" can be
	// attributed to one book.
	OnlyStems map[string]bool
}

// nameWords splits a filename stem into lowercase alphanumeric words.
func nameWords(stem string) []string {
	return strings.FieldsFunc(strings.ToLower(stem), func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	})
}

// folderCoverNameRank returns the preferred-word rank for a stem and whether
// the stem names a part of the package that is not the front cover.
func folderCoverNameRank(stem string) (rank int, reject bool) {
	rank = len(folderCoverPreferredWords)
	for _, w := range nameWords(stem) {
		if folderCoverRejectWords[w] {
			return rank, true
		}
		for i, p := range folderCoverPreferredWords {
			if w == p && i < rank {
				rank = i
			}
		}
	}
	return rank, false
}

// IsFolderCoverExt reports whether ext (any case) is a folder-cover image
// extension.
func IsFolderCoverExt(ext string) bool { return folderCoverExts[strings.ToLower(ext)] }

// FolderCoverCandidates returns every image in dir that passes the rule above,
// best first. A missing directory is not an error: it has no candidates.
func FolderCoverCandidates(dir string, filter FolderCoverFilter) ([]FolderCover, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read folder %s: %w", dir, err)
	}
	var out []FolderCover
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") || !e.Type().IsRegular() {
			continue
		}
		ext := filepath.Ext(name)
		if !IsFolderCoverExt(ext) {
			continue
		}
		stem := strings.TrimSuffix(name, ext)
		if filter.OnlyStems != nil && !filter.OnlyStems[strings.ToLower(stem)] {
			continue
		}
		rank, reject := folderCoverNameRank(stem)
		if reject {
			continue
		}
		info, err := e.Info()
		if err != nil || info.Size() <= 0 || info.Size() > FolderCoverMaxBytes {
			continue
		}
		full := filepath.Join(dir, name)
		cfg, format, err := decodeImageConfigFile(full)
		if err != nil {
			continue // not a readable image; nothing to rank
		}
		short, long := min(cfg.Width, cfg.Height), max(cfg.Width, cfg.Height)
		if short < FolderCoverMinSide || float64(long) > FolderCoverMaxAspect*float64(short) {
			continue
		}
		out = append(out, FolderCover{Path: full, Width: cfg.Width, Height: cfg.Height, Format: format, NameRank: rank})
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.NameRank != b.NameRank {
			return a.NameRank < b.NameRank
		}
		if a.Area() != b.Area() {
			return a.Area() > b.Area()
		}
		return a.Path < b.Path
	})
	return out, nil
}

// FindFolderCover returns the best cover image in dir, or nil when there is
// none. See the rule at the top of this file.
func FindFolderCover(dir string, filter FolderCoverFilter) (*FolderCover, error) {
	c, err := FolderCoverCandidates(dir, filter)
	if err != nil || len(c) == 0 {
		return nil, err
	}
	return &c[0], nil
}

func decodeImageConfigFile(path string) (image.Config, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return image.Config{}, "", err
	}
	defer f.Close()
	return image.DecodeConfig(f)
}

// CoverImage is a cover's bytes in the form they are stored and served.
type CoverImage struct {
	Data []byte
	// Ext is the stored extension (.jpg, .png, .gif or .webp).
	Ext string
	// MIMEType matches Ext.
	MIMEType string
}

// SHA256 returns the hex SHA-256 of the image bytes: the stored file's name
// and the cover-text key.
func (ci CoverImage) SHA256() string {
	h := sha256.Sum256(ci.Data)
	return hex.EncodeToString(h[:])
}

// LoadFolderCover reads a folder cover and returns it in stored form. BMP is
// converted to JPEG: browsers and the ABS clients do not all render BMP, and
// an uncompressed bitmap is many times the size of the same cover as JPEG.
// The format is re-detected from the bytes read, never taken from c.
func LoadFolderCover(c FolderCover) (CoverImage, error) {
	f, err := os.Open(c.Path)
	if err != nil {
		return CoverImage{}, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, FolderCoverMaxBytes+1))
	if err != nil {
		return CoverImage{}, fmt.Errorf("read %s: %w", c.Path, err)
	}
	if len(data) > FolderCoverMaxBytes {
		return CoverImage{}, fmt.Errorf("%s is larger than %d bytes", c.Path, FolderCoverMaxBytes)
	}
	return normalizeCoverBytes(data)
}

// normalizeCoverBytes detects the image format of data and returns it in
// stored form, converting BMP to JPEG.
func normalizeCoverBytes(data []byte) (CoverImage, error) {
	_, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return CoverImage{}, fmt.Errorf("not a readable image: %w", err)
	}
	switch format {
	case "jpeg":
		return CoverImage{Data: data, Ext: ".jpg", MIMEType: "image/jpeg"}, nil
	case "png":
		return CoverImage{Data: data, Ext: ".png", MIMEType: "image/png"}, nil
	case "gif":
		return CoverImage{Data: data, Ext: ".gif", MIMEType: "image/gif"}, nil
	case "webp":
		return CoverImage{Data: data, Ext: ".webp", MIMEType: "image/webp"}, nil
	case "bmp":
		img, _, err := image.Decode(bytes.NewReader(data))
		if err != nil {
			return CoverImage{}, fmt.Errorf("decode bmp: %w", err)
		}
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
			return CoverImage{}, fmt.Errorf("encode bmp as jpeg: %w", err)
		}
		return CoverImage{Data: buf.Bytes(), Ext: ".jpg", MIMEType: "image/jpeg"}, nil
	default:
		return CoverImage{}, fmt.Errorf("unsupported cover format %q", format)
	}
}

// LocalCoverURLPrefix is the URL prefix of a cover stored under the covers
// directories and served by handleLocalCover.
const LocalCoverURLPrefix = "/api/v1/covers/local/"

// StoreCoverImage writes ci to {rootDir}/.covers/<sha256><ext>, the store
// extracted embedded art uses, and returns the path. Content-addressed, so the
// same image stored twice (two books, two workers) is one file. The write goes
// to a temp file in the same directory and is renamed into place, so a reader
// never sees a partial file and two concurrent writers of the same image both
// end with one complete file.
func StoreCoverImage(rootDir string, ci CoverImage) (string, error) {
	if rootDir == "" {
		return "", errors.New("store cover: root dir is empty")
	}
	if len(ci.Data) == 0 {
		return "", errors.New("store cover: no image data")
	}
	allowed := false
	for _, e := range coverExtensions {
		if e == ci.Ext {
			allowed = true
			break
		}
	}
	if !allowed {
		return "", fmt.Errorf("store cover: extension %q is not allowed", ci.Ext)
	}
	coverDir := filepath.Join(rootDir, ".covers")
	if err := os.MkdirAll(coverDir, 0o775); err != nil {
		return "", fmt.Errorf("failed to create covers directory: %w", err)
	}
	name := ci.SHA256() + ci.Ext
	dest := filepath.Join(coverDir, name)
	if fi, err := os.Stat(dest); err == nil && !fi.IsDir() {
		return dest, nil
	}
	tmp, err := os.CreateTemp(coverDir, "."+name+".*.tmp")
	if err != nil {
		return "", fmt.Errorf("failed to create cover file: %w", err)
	}
	tmpPath := tmp.Name()
	_, werr := tmp.Write(ci.Data)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("failed to write cover: %w", errors.Join(werr, cerr))
	}
	if err := os.Chmod(tmpPath, 0o664); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("failed to set cover permissions: %w", err)
	}
	if err := os.Rename(tmpPath, dest); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("failed to move cover into place: %w", err)
	}
	return dest, nil
}

// LocalCoverURL is the cover_url for a file StoreCoverImage wrote.
func LocalCoverURL(storedPath string) string {
	return LocalCoverURLPrefix + filepath.Base(storedPath)
}
