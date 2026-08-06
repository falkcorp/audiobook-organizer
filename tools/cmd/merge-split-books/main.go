// file: tools/cmd/merge-split-books/main.go
// version: 1.0.0
// last-edited: 2026-05-29
//
// merge-split-books is the operator-facing CLI for MAYDEPLOY-G2 + G4.
// It triggers the split-book backfill scan on a running server, lists
// the resulting candidate clusters, and (in --execute mode) issues the
// merge API call for each cluster.
//
// Default mode is --dry-run: candidates are printed to stdout and no
// mutations are performed. --execute flips the switch.
//
// Usage examples:
//
//	merge-split-books -server http://localhost:8484 -api-token-file .api-token
//	merge-split-books -server https://<server>:8484 -api-key $TOKEN --execute --limit 5
package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// Candidate mirrors internal/dedup.SplitBookCandidate. Kept inline so
// the CLI doesn't import internal/ packages.
type Candidate struct {
	ID                string   `json:"id"`
	ParentFolder      string   `json:"parent_folder"`
	BookIDs           []string `json:"book_ids"`
	SuggestedTitle    string   `json:"suggested_title"`
	SuggestedAuthor   string   `json:"suggested_author"`
	SequentialPattern string   `json:"sequential_pattern"`
	Shape             string   `json:"shape"`
}

type listResponse struct {
	Data *struct {
		Candidates []Candidate `json:"candidates"`
		Total      int         `json:"total"`
	} `json:"data"`
	Candidates []Candidate `json:"candidates"`
	Total      int         `json:"total"`
}

type scanResponse struct {
	Data *struct {
		OpID string `json:"op_id"`
	} `json:"data"`
	OpID string `json:"op_id"`
}

func main() {
	server := flag.String("server", "http://localhost:8484", "API base URL")
	apiKey := flag.String("api-key", "", "API key (or AUDIOBOOK_API_KEY env)")
	apiKeyFile := flag.String("api-token-file", "", "Read API key from file (default: empty)")
	dryRun := flag.Bool("dry-run", true, "List candidates without merging (default true)")
	execute := flag.Bool("execute", false, "Execute the merges (overrides --dry-run)")
	minGroupSize := flag.Int("min-group-size", 3, "Skip clusters smaller than this")
	limit := flag.Int("limit", 0, "Cap how many groups to merge in one run (0 = no limit)")
	skipScan := flag.Bool("skip-scan", false, "Don't trigger a fresh scan; read existing candidates only")
	scanTimeout := flag.Duration("scan-timeout", 5*time.Minute, "How long to poll for scan completion before listing")
	verbose := flag.Bool("verbose", false, "Verbose logging")
	flag.Parse()

	key := *apiKey
	if key == "" && *apiKeyFile != "" {
		data, err := os.ReadFile(*apiKeyFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "merge-split-books: read api-token-file: %v\n", err)
			os.Exit(1)
		}
		key = strings.TrimSpace(string(data))
	}
	if key == "" {
		key = os.Getenv("AUDIOBOOK_API_KEY")
	}
	if key == "" {
		fmt.Fprintln(os.Stderr, "merge-split-books: API key required: use -api-key, -api-token-file, or AUDIOBOOK_API_KEY env")
		os.Exit(1)
	}

	client := &http.Client{
		Timeout: 60 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
		},
	}

	if !*skipScan {
		if *verbose {
			fmt.Fprintln(os.Stderr, "Triggering split-book scan...")
		}
		opID, err := triggerScan(client, *server, key)
		if err != nil {
			fmt.Fprintf(os.Stderr, "merge-split-books: trigger scan: %v\n", err)
			os.Exit(1)
		}
		if *verbose {
			fmt.Fprintf(os.Stderr, "Scan op enqueued: %s\n", opID)
		}
		// Best-effort wait — we don't poll op status, just give the
		// server a few seconds to complete. The detector runs in
		// memory and even on a 100K-book library typically completes
		// in <30s. Operator can re-run with --skip-scan if needed.
		waitForScan(*scanTimeout, *verbose)
	}

	cands, err := listCandidates(client, *server, key)
	if err != nil {
		fmt.Fprintf(os.Stderr, "merge-split-books: list candidates: %v\n", err)
		os.Exit(1)
	}

	// Apply --min-group-size filter.
	filtered := cands[:0]
	for _, c := range cands {
		if len(c.BookIDs) >= *minGroupSize {
			filtered = append(filtered, c)
		}
	}
	cands = filtered

	if *limit > 0 && len(cands) > *limit {
		cands = cands[:*limit]
	}

	fmt.Fprintf(os.Stderr, "Found %d candidate cluster(s) (min-group-size=%d, limit=%d)\n",
		len(cands), *minGroupSize, *limit)

	doExecute := *execute && !*dryRun
	if *execute {
		doExecute = true // explicit --execute always wins
	}

	for i, c := range cands {
		fmt.Printf("\n=== Cluster %d/%d ===\n", i+1, len(cands))
		fmt.Printf("  ID:        %s\n", c.ID)
		fmt.Printf("  Shape:     %s\n", c.Shape)
		fmt.Printf("  Folder:    %s\n", c.ParentFolder)
		fmt.Printf("  Title:     %s\n", c.SuggestedTitle)
		fmt.Printf("  Author:    %s\n", c.SuggestedAuthor)
		fmt.Printf("  Pattern:   %s\n", c.SequentialPattern)
		fmt.Printf("  Books:     %d (keep %s, merge %d others)\n",
			len(c.BookIDs), c.BookIDs[0], len(c.BookIDs)-1)
		if *verbose {
			fmt.Printf("  All IDs:   %s\n", strings.Join(c.BookIDs, ", "))
		}
		if !doExecute {
			continue
		}
		res, err := mergeCluster(client, *server, key, c.ID)
		if err != nil {
			fmt.Printf("  MERGE FAILED: %v\n", err)
			continue
		}
		fmt.Printf("  MERGED: %s\n", res)
	}

	if !doExecute {
		fmt.Fprintln(os.Stderr, "\nDRY RUN — no merges performed. Re-run with --execute to apply.")
	}
}

func triggerScan(client *http.Client, server, key string) (string, error) {
	req, err := http.NewRequest("POST", server+"/api/v1/dedup/split-book-scan", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	resp, body, err := doRetry(client, req, 3)
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("scan returned %d: %s", resp.StatusCode, body)
	}
	var sr scanResponse
	_ = json.Unmarshal(body, &sr)
	if sr.Data != nil {
		return sr.Data.OpID, nil
	}
	return sr.OpID, nil
}

func waitForScan(d time.Duration, verbose bool) {
	if d <= 0 {
		return
	}
	if verbose {
		fmt.Fprintf(os.Stderr, "Waiting %s for scan to complete...\n", d)
	}
	time.Sleep(d)
}

func listCandidates(client *http.Client, server, key string) ([]Candidate, error) {
	req, err := http.NewRequest("GET", server+"/api/v1/dedup/split-book-candidates", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	resp, body, err := doRetry(client, req, 3)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("list returned %d: %s", resp.StatusCode, body)
	}
	var lr listResponse
	if err := json.Unmarshal(body, &lr); err != nil {
		return nil, fmt.Errorf("decode list: %w", err)
	}
	if lr.Data != nil {
		return lr.Data.Candidates, nil
	}
	return lr.Candidates, nil
}

func mergeCluster(client *http.Client, server, key, id string) (string, error) {
	url := server + "/api/v1/dedup/split-book-candidates/" + id + "/merge"
	req, err := http.NewRequest("POST", url, bytes.NewReader([]byte("{}")))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	resp, body, err := doRetry(client, req, 3)
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("merge returned %d: %s", resp.StatusCode, body)
	}
	return string(body), nil
}

// doRetry runs req once, and re-runs on 5xx up to maxAttempts total.
// The request body must be re-readable (we only send empty / small JSON).
func doRetry(client *http.Client, req *http.Request, maxAttempts int) (*http.Response, []byte, error) {
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			time.Sleep(time.Duration(attempt) * time.Second)
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode >= 500 && attempt < maxAttempts {
			time.Sleep(time.Duration(attempt) * time.Second)
			continue
		}
		return resp, body, nil
	}
	return nil, nil, lastErr
}
