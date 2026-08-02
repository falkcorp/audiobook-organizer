// file: internal/server/audio_sample.go
// version: 1.2.0
// guid: d4e5f6a7-b8c9-0d1e-2f3a-4b5c6d7e8f9a
// last-edited: 2026-05-11

package server

import (
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/falkcorp/audiobook-organizer/internal/audio"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/gin-gonic/gin"
)

// handleAudioSample streams a short MP3 clip from an audiobook file.
//
// GET /api/v1/audiobooks/:id/sample?start=0&duration=30
//
//   - start    — offset in seconds (default 0, clamped to [0, book duration])
//   - duration — clip length in seconds (default 30, capped at 60)
//
// Delegates to internal/audio.ExtractSample for the actual ffmpeg logic.
func (s *Server) handleAudioSample(c *gin.Context) {
	book, err := s.Store().GetBookByID(c.Param("id"))
	if err != nil || book == nil {
		httputil.RespondWithNotFound(c, "book", "")
		return
	}
	if book.FilePath == "" {
		httputil.RespondWithError(c, http.StatusUnprocessableEntity, "book has no file path", "UNPROCESSABLE_ENTITY")
		return
	}

	start := httputil.ParseQueryInt(c, "start", 0)
	dur := httputil.ParseQueryInt(c, "duration", audio.SampleDefault)

	req := &audio.SampleRequest{
		FilePath: book.FilePath,
		Start:    start,
		Duration: dur,
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 120)
	defer cancel()

	c.Header("Content-Type", "audio/mpeg")
	c.Header("Cache-Control", "no-store")
	c.Status(http.StatusOK)

	// Stream the audio sample via gin's streaming interface
	c.Stream(func(w io.Writer) bool {
		err := audio.ExtractSample(ctx, req, func(buf []byte) (int, error) {
			return w.Write(buf)
		})
		if err != nil {
			// Log error but don't try to write to response (headers already sent)
			c.Error(fmt.Errorf("audio sample extraction: %w", err))
			return false
		}
		return true
	})
}
