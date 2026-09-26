// file: internal/metadata/folder_cover_test.go
// version: 1.0.0
// guid: 0f4b7d2c-8e19-4a63-b5d7-3c1e9f6a2b84
// last-edited: 2026-09-26

package metadata

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"golang.org/x/image/bmp"
)

func testImage(w, h int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y += 7 {
		for x := 0; x < w; x += 7 {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 90, A: 255})
		}
	}
	return img
}

func writeImage(t *testing.T, path, format string, w, h int) {
	t.Helper()
	var buf bytes.Buffer
	var err error
	switch format {
	case "png":
		err = png.Encode(&buf, testImage(w, h))
	case "jpeg":
		err = jpeg.Encode(&buf, testImage(w, h), nil)
	case "bmp":
		err = bmp.Encode(&buf, testImage(w, h))
	default:
		t.Fatalf("unknown format %s", format)
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestFolderCoverRanking(t *testing.T) {
	dir := t.TempDir()
	writeImage(t, filepath.Join(dir, "big-scan.png"), "png", 1200, 1200)    // unnamed, largest
	writeImage(t, filepath.Join(dir, "FOLDER.JPG"), "jpeg", 400, 400)       // named "folder", upper case
	writeImage(t, filepath.Join(dir, "Cover.Bmp"), "bmp", 300, 300)         // named "cover", mixed-case bmp
	writeImage(t, filepath.Join(dir, "back cover.jpg"), "jpeg", 1500, 1500) // rejected: back
	writeImage(t, filepath.Join(dir, "AlbumArtSmall.jpg"), "jpeg", 75, 75)  // rejected: too small
	writeImage(t, filepath.Join(dir, "spine-strip.png"), "png", 900, 120)   // rejected: small side + reject word
	writeImage(t, filepath.Join(dir, "banner.png"), "png", 1500, 300)       // rejected: aspect 5:1
	writeImage(t, filepath.Join(dir, "._cover.jpg"), "jpeg", 500, 500)      // hidden AppleDouble
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "broken.jpg"), []byte("not an image"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := FolderCoverCandidates(dir, FolderCoverFilter{})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, c := range got {
		names = append(names, filepath.Base(c.Path))
	}
	want := []string{"Cover.Bmp", "FOLDER.JPG", "big-scan.png"}
	if len(names) != len(want) {
		t.Fatalf("candidates = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("candidates = %v, want %v", names, want)
		}
	}
	if got[0].Format != "bmp" || got[1].Format != "jpeg" {
		t.Fatalf("formats = %s,%s; want decoded formats bmp,jpeg", got[0].Format, got[1].Format)
	}
}

func TestFolderCoverUnnamedLargestWins(t *testing.T) {
	dir := t.TempDir()
	writeImage(t, filepath.Join(dir, "a.png"), "png", 300, 300)
	writeImage(t, filepath.Join(dir, "b.JPEG"), "jpeg", 600, 900)
	c, err := FindFolderCover(dir, FolderCoverFilter{})
	if err != nil || c == nil {
		t.Fatalf("FindFolderCover = %v, %v", c, err)
	}
	if filepath.Base(c.Path) != "b.JPEG" {
		t.Fatalf("got %s, want the larger b.JPEG", c.Path)
	}
}

func TestFolderCoverOnlyStems(t *testing.T) {
	dir := t.TempDir()
	writeImage(t, filepath.Join(dir, "cover.jpg"), "jpeg", 500, 500)
	writeImage(t, filepath.Join(dir, "My Book.png"), "png", 400, 400)
	c, err := FindFolderCover(dir, FolderCoverFilter{OnlyStems: map[string]bool{"my book": true}})
	if err != nil || c == nil || filepath.Base(c.Path) != "My Book.png" {
		t.Fatalf("got %+v, %v; want My Book.png only", c, err)
	}
	c, err = FindFolderCover(dir, FolderCoverFilter{OnlyStems: map[string]bool{"other": true}})
	if err != nil || c != nil {
		t.Fatalf("got %+v, %v; want nothing", c, err)
	}
}

func TestFolderCoverMissingDir(t *testing.T) {
	c, err := FindFolderCover(filepath.Join(t.TempDir(), "nope"), FolderCoverFilter{})
	if err != nil || c != nil {
		t.Fatalf("missing dir: %+v, %v", c, err)
	}
}

func TestLoadFolderCoverConvertsBMPToJPEG(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "COVER.BMP")
	writeImage(t, p, "bmp", 320, 480)
	c, err := FindFolderCover(dir, FolderCoverFilter{})
	if err != nil || c == nil {
		t.Fatalf("FindFolderCover: %+v %v", c, err)
	}
	img, err := LoadFolderCover(*c)
	if err != nil {
		t.Fatal(err)
	}
	if img.Ext != ".jpg" || img.MIMEType != "image/jpeg" {
		t.Fatalf("bmp stored as %s %s, want .jpg image/jpeg", img.Ext, img.MIMEType)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(img.Data))
	if err != nil || format != "jpeg" || cfg.Width != 320 || cfg.Height != 480 {
		t.Fatalf("converted image: %v %s %dx%d", err, format, cfg.Width, cfg.Height)
	}
}

func TestLoadFolderCoverUsesDecodedFormatNotExtension(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "cover.jpg")
	writeImage(t, p, "png", 300, 300) // a PNG with a .jpg name
	img, err := LoadFolderCover(FolderCover{Path: p})
	if err != nil {
		t.Fatal(err)
	}
	if img.Ext != ".png" {
		t.Fatalf("ext = %s, want .png from the decoded format", img.Ext)
	}
}

func TestStoreCoverImageContentAddressedAndConcurrent(t *testing.T) {
	root := t.TempDir()
	dir := t.TempDir()
	p := filepath.Join(dir, "cover.png")
	writeImage(t, p, "png", 300, 300)
	img, err := LoadFolderCover(FolderCover{Path: p})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	paths := make([]string, 8)
	errs := make([]error, 8)
	for i := range 8 {
		wg.Go(func() { paths[i], errs[i] = StoreCoverImage(root, img) })
	}
	wg.Wait()
	want := filepath.Join(root, ".covers", img.SHA256()+".png")
	for i := range 8 {
		if errs[i] != nil || paths[i] != want {
			t.Fatalf("store %d = %s, %v; want %s", i, paths[i], errs[i], want)
		}
	}
	entries, _ := os.ReadDir(filepath.Join(root, ".covers"))
	if len(entries) != 1 {
		t.Fatalf(".covers has %d entries, want exactly the one stored file (no temp leftovers)", len(entries))
	}
	got, _ := os.ReadFile(want)
	if !bytes.Equal(got, img.Data) {
		t.Fatal("stored bytes differ")
	}
	if u := LocalCoverURL(want); u != "/api/v1/covers/local/"+img.SHA256()+".png" {
		t.Fatalf("LocalCoverURL = %s", u)
	}
}
