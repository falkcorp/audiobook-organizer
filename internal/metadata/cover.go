// file: internal/metadata/cover.go
// version: 1.3.0
// guid: 4efaa7b8-e29a-47f3-84f7-39b46bfc9a01

package metadata

import (
	"context"
	"fmt"
	"github.com/falkcorp/audiobook-organizer/internal/metadata/providerhttp"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ErrSSRFBlocked is returned when a cover URL resolves to a private/reserved address.
var ErrSSRFBlocked = fmt.Errorf("cover URL resolves to a blocked (private/reserved) address")

// privateCIDRs lists address ranges that must not be reachable via cover downloads.
var privateCIDRs = func() []*net.IPNet {
	blocks := []string{
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
		"127.0.0.0/8",    // loopback
		"169.254.0.0/16", // link-local (AWS IMDSv1)
		"::1/128",        // IPv6 loopback
		"fc00::/7",       // IPv6 unique-local
		"fe80::/10",      // IPv6 link-local
	}
	nets := make([]*net.IPNet, 0, len(blocks))
	for _, b := range blocks {
		_, n, _ := net.ParseCIDR(b)
		if n != nil {
			nets = append(nets, n)
		}
	}
	return nets
}()

// isPrivateIP returns true if ip falls within any reserved/private range.
func isPrivateIP(ip net.IP) bool {
	for _, cidr := range privateCIDRs {
		if cidr.Contains(ip) {
			return true
		}
	}
	return false
}

// validateCoverURL enforces scheme and rejects obviously-internal hostnames.
// A full IP-block happens in the custom DialContext used by safeCoverClient.
func validateCoverURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid cover URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("cover URL scheme %q not allowed (only http/https)", u.Scheme)
	}
	return nil
}

// safeCoverDialContext is a DialContext hook that blocks connections to
// private/reserved IP ranges, preventing SSRF via cover-art downloads.
func safeCoverDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	addrs, err := net.DefaultResolver.LookupHost(ctx, host)
	if err != nil {
		return nil, err
	}
	for _, a := range addrs {
		ip := net.ParseIP(a)
		if ip == nil {
			continue
		}
		if isPrivateIP(ip) {
			return nil, ErrSSRFBlocked
		}
	}
	return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, net.JoinHostPort(host, port))
}

// DownloadCoverArt downloads a cover image from coverURL and saves it to
// {destDir}/covers/{bookID}.{ext}. Returns the local file path on success.
// Skips download if the file already exists. Only accepts image/* content types.
// Rejects non-http(s) URLs and URLs that resolve to private/reserved IPs.
func DownloadCoverArt(coverURL string, destDir string, bookID string) (string, error) {
	// Rate-limited, but the SSRF guard is preserved: safeCoverDialContext refuses
	// private/reserved IPs and MUST stay on the transport. providerhttp wraps the
	// caller's transport rather than replacing it, so throttling is added without
	// dropping the security control.
	client := providerhttp.ClientWithTransport("cover", &http.Transport{
		DialContext: safeCoverDialContext,
	})
	return downloadCoverArtWithClient(client, coverURL, destDir, bookID)
}

// downloadCoverArtWithClient is the internal implementation — accepts a custom
// client so tests can substitute a plain http.Client pointing to localhost.
func downloadCoverArtWithClient(client *http.Client, coverURL string, destDir string, bookID string) (string, error) {
	if coverURL == "" {
		return "", fmt.Errorf("empty cover URL")
	}
	if bookID == "" {
		return "", fmt.Errorf("empty book ID")
	}

	if err := validateCoverURL(coverURL); err != nil {
		return "", err
	}

	coversDir := filepath.Join(destDir, "covers")

	// Check if cover already exists (any extension)
	matches, _ := filepath.Glob(filepath.Join(coversDir, bookID+".*"))
	for _, m := range matches {
		ext := strings.ToLower(filepath.Ext(m))
		if ext == ".jpg" || ext == ".jpeg" || ext == ".png" || ext == ".webp" || ext == ".gif" {
			return m, nil
		}
	}

	// Create covers directory
	if err := os.MkdirAll(coversDir, 0775); err != nil {
		return "", fmt.Errorf("failed to create covers directory: %w", err)
	}

	resp, err := client.Get(coverURL) //nolint:noctx // URL already validated above
	if err != nil {
		return "", fmt.Errorf("failed to download cover: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("cover download returned status %d", resp.StatusCode)
	}

	contentType := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(contentType, "image/") {
		return "", fmt.Errorf("unexpected content type: %s", contentType)
	}

	ext := extensionFromContentType(contentType)
	destPath := filepath.Join(coversDir, bookID+ext)

	// Limit to 10 MB
	limitedReader := io.LimitReader(resp.Body, 10*1024*1024)

	f, err := os.Create(destPath)
	if err != nil {
		return "", fmt.Errorf("failed to create cover file: %w", err)
	}
	defer f.Close()

	if _, err := io.Copy(f, limitedReader); err != nil {
		os.Remove(destPath)
		return "", fmt.Errorf("failed to write cover file: %w", err)
	}

	return destPath, nil
}

// CoverPathForBook returns the local cover file path if it exists, empty string otherwise.
func CoverPathForBook(destDir string, bookID string) string {
	// filepath.Base strips any directory traversal from the bookID segment.
	safeID := filepath.Base(bookID)
	if safeID == "." || safeID == "/" {
		return ""
	}
	coversDir := filepath.Join(destDir, "covers")
	matches, _ := filepath.Glob(filepath.Join(coversDir, safeID+".*"))
	for _, m := range matches {
		ext := strings.ToLower(filepath.Ext(m))
		if ext == ".jpg" || ext == ".jpeg" || ext == ".png" || ext == ".webp" || ext == ".gif" {
			return m
		}
	}
	return ""
}

// HasExistingCoverArt checks if an audio file already has cover art, either
// embedded in the file or as a common image file in the same directory
// (e.g., cover.jpg, folder.jpg, etc.).
func HasExistingCoverArt(audioPath string) bool {
	// Check for embedded cover art
	if audioPath != "" {
		if coverPath, err := ExtractCoverArt(audioPath); err == nil && coverPath != "" {
			return true
		}
	}

	// Check for common cover image files in the same directory
	dir := filepath.Dir(audioPath)
	coverNames := []string{
		"cover", "folder", "front", "album", "artwork",
	}
	imageExts := []string{".jpg", ".jpeg", ".png", ".webp", ".gif"}
	for _, name := range coverNames {
		for _, ext := range imageExts {
			candidate := filepath.Join(dir, name+ext)
			if _, err := os.Stat(candidate); err == nil {
				return true
			}
			// Also check uppercase
			candidate = filepath.Join(dir, strings.ToUpper(name)+ext)
			if _, err := os.Stat(candidate); err == nil {
				return true
			}
		}
	}
	return false
}

func extensionFromContentType(ct string) string {
	ct = strings.ToLower(ct)
	switch {
	case strings.Contains(ct, "png"):
		return ".png"
	case strings.Contains(ct, "gif"):
		return ".gif"
	case strings.Contains(ct, "webp"):
		return ".webp"
	default:
		return ".jpg"
	}
}
