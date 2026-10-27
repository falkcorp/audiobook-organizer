// file: internal/server/candidate_feedback_judges_test.go
// version: 1.0.0
// guid: 8e3d1a47-2c90-4b6f-a1d5-7f04c9be3a62
// last-edited: 2026-10-26

package server

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// TestCandidateFeedbackExport_OtherJudgesNeedUsersManage: the export serves the
// labels to an editor but names only the editor's own judgements. Another
// judge's account id and username are an account's data (the route is
// library.edit_metadata, which the editor role holds); an admin reads them all.
func TestCandidateFeedbackExport_OtherJudgesNeedUsersManage(t *testing.T) {
	f := newRepairsOpAccessFixture(t)
	sess, err := f.srv.storeForWiring().GetSession(f.editor)
	require.NoError(t, err)
	require.NotNil(t, sess)
	editorID := sess.UserID

	put := func(book, asin, userID, username string) {
		_, err := f.srv.candidateFeedbackStore().Put(database.CandidateFeedback{
			BookID: book, Label: database.CandidateFeedbackNegative,
			Query:     database.CandidateFeedbackQuery{Title: "Year One", Mode: database.CandidateFeedbackModeBook},
			Candidate: database.CandidateFeedbackCandidate{Source: "audible", ASIN: asin, Title: "Batman: Year One"},
			UserID:    userID, Username: username,
		}, time.Now())
		require.NoError(t, err)
	}
	put("book-year-one", "B0INVENTED01", "judge-harvey-dent", "harvey.dent")
	put("book-year-two", "B0INVENTED02", editorID, "editor-self")
	put("book-hush", "B0INVENTED03", "", "")

	read := func(token string) []database.CandidateFeedback {
		w := getAs(f.srv, token, "/api/v1/metadata/candidate-feedback")
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		var out []database.CandidateFeedback
		sc := bufio.NewScanner(bytes.NewReader(w.Body.Bytes()))
		for sc.Scan() {
			var fb database.CandidateFeedback
			require.NoError(t, json.Unmarshal(sc.Bytes(), &fb))
			out = append(out, fb)
		}
		return out
	}
	byBook := func(rows []database.CandidateFeedback) map[string]database.CandidateFeedback {
		m := map[string]database.CandidateFeedback{}
		for _, r := range rows {
			m[r.BookID] = r
		}
		return m
	}

	ed := byBook(read(f.editor))
	require.Len(t, ed, 3, "the labels themselves are still served")
	assert.Empty(t, ed["book-year-one"].UserID, "another judge's id is blanked")
	assert.Empty(t, ed["book-year-one"].Username, "another judge's username is blanked")
	assert.Equal(t, editorID, ed["book-year-two"].UserID, "the caller's own judgement keeps its id")
	assert.Equal(t, "editor-self", ed["book-year-two"].Username)

	ad := byBook(read(f.admin))
	require.Len(t, ad, 3)
	assert.Equal(t, "judge-harvey-dent", ad["book-year-one"].UserID)
	assert.Equal(t, "harvey.dent", ad["book-year-one"].Username)
}
