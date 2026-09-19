// file: internal/ai/aijobs_adapter.go
// version: 1.1.0
// guid: 7991182f-6718-4758-8ffa-14108704ae11
// last-edited: 2026-09-19

package ai

import "context"

// AIJobsBatchClient adapts OpenAIParser to the aijobs.BatchClient interface.
// (aijobs.BatchClient's UploadBatchFile signature is []byte rather than io.Reader.)
type AIJobsBatchClient struct {
	Parser *OpenAIParser
}

func (a *AIJobsBatchClient) UploadBatchFile(ctx context.Context, data []byte) (string, error) {
	return a.Parser.UploadBatchFileBytes(ctx, data)
}

func (a *AIJobsBatchClient) CreateBatchWithMetadata(ctx context.Context, fileID, batchType string, extra map[string]string) (string, error) {
	return a.Parser.CreateBatchWithMetadata(ctx, fileID, batchType, extra)
}
