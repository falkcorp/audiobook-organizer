// file: cmd/dedup_bench_status.go
// version: 1.1.0
// guid: 1a2b3c4d-5e6f-7890-abcd-111111111111
// last-edited: 2026-08-20

//go:build bench

package cmd

import (
	"fmt"
	"log/slog"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/spf13/cobra"
)

var dedupBenchStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show status of all OpenAI batch jobs",
	Long:  `Queries the OpenAI API to list all recent batch jobs and their status.`,
	RunE:  runDedupBenchStatus,
}

func runDedupBenchStatus(cmd *cobra.Command, args []string) error {
	config.InitConfig()
	apiKey := config.AppConfig.OpenAIAPIKey
	if apiKey == "" {
		return fmt.Errorf("OPENAI_API_KEY env var required")
	}

	clientOpts := []option.RequestOption{option.WithAPIKey(apiKey)}
	if baseURL := config.AppConfig.OpenAIBaseURL; baseURL != "" {
		clientOpts = append(clientOpts, option.WithBaseURL(baseURL))
	}
	client := openai.NewClient(clientOpts...)

	batches, err := client.Batches.List(cmd.Context(), openai.BatchListParams{
		Limit: openai.Int(100),
	})
	if err != nil {
		return fmt.Errorf("list batches: %w", err)
	}

	var pending, completed, failed int

	fmt.Printf("%-24s %-14s %6s %6s %6s\n", "BATCH ID", "STATUS", "OK", "FAIL", "TOTAL")
	fmt.Println("--------------------------------------------------------------")

	for _, b := range batches.Data {
		status := string(b.Status)
		ok := b.RequestCounts.Completed
		fail := b.RequestCounts.Failed
		total := b.RequestCounts.Total

		switch status {
		case "completed":
			completed++
		case "failed", "expired", "cancelled":
			failed++
		default:
			pending++
		}

		fmt.Printf("%-24s %-14s %6d %6d %6d\n", b.ID[:24], status, ok, fail, total)
	}

	slog.Info("Status summary", "completed", completed, "failed", failed, "pending", pending)
	return nil
}
