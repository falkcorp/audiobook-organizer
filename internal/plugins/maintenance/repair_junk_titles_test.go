// file: internal/plugins/maintenance/repair_junk_titles_test.go
// version: 1.0.0
// guid: 136c25ea-f430-43b1-b31a-9d4c2dbf1edb
// last-edited: 2026-09-25

package maintenance

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// junkTitleFixture is one book per exclusion, plus one ordinary repair so a
// test that sees nothing written cannot pass because the op never ran.
//
//	bk-good       ordinary: folder "Good Book" -> retitled
//	bk-author     folder is the linked author's name ("C. T. Phipps") -> refused
//	bk-narrator   folder is the narrator's name ("Colin Baker") -> refused
//	bk-bf-path    book path under "Big Finish" -> skipped
//	bk-bf-series  series "Doctor Who: The Monthly Adventures" -> skipped
//	bk-tw-files   book path neutral, FILES under "Torchwood" -> skipped
//	bk-itunes     book path under books/itunes/, files elsewhere -> skipped
//	bk-itunes-f   book path neutral, FILES under books/itunes/ -> skipped
func junkTitleFixture() (books []database.BookCore, files map[string][]database.BookFile) {
	primary := 1
	colin := "Colin Baker, Nicola Bryant"
	series := 7
	books = []database.BookCore{
		{ID: "bk-good", Title: "read by narrator", AuthorID: &primary, FilePath: "/lib/Author A/Good Book"},
		{ID: "bk-author", Title: "read by narrator", FilePath: "/lib/C. T. Phipps/C. T. Phipps"},
		{ID: "bk-narrator", Title: "Intro", Narrator: &colin, FilePath: "/lib/Someone/Colin Baker"},
		{ID: "bk-bf-path", Title: "read by narrator", FilePath: "/lib/Big Finish/Real Title"},
		{ID: "bk-bf-series", Title: "read by narrator", SeriesID: &series, FilePath: "/lib/Plain/Series Title"},
		{ID: "bk-tw-files", Title: "read by narrator", FilePath: "/lib/Neutral/Stale Path"},
		{ID: "bk-itunes", Title: "read by narrator", FilePath: "/mnt/data/books/itunes/Some Artist/Some Title"},
		{ID: "bk-itunes-f", Title: "read by narrator", FilePath: "/lib/Neutral/Another"},
	}
	two := func(dir string) []database.BookFile {
		return []database.BookFile{{FilePath: dir + "/01.mp3"}, {FilePath: dir + "/02.mp3"}}
	}
	files = map[string][]database.BookFile{
		"bk-good":      two("/lib/Author A/Good Book"),
		"bk-author":    two("/lib/C. T. Phipps/C. T. Phipps"),
		"bk-narrator":  two("/lib/Someone/Colin Baker"),
		"bk-bf-path":   two("/lib/Big Finish/Real Title"),
		"bk-bf-series": two("/lib/Plain/Series Title"),
		"bk-tw-files":  two("/lib/Torchwood/Real Title"),
		// Book path under the iTunes tree, files elsewhere: only the pass-1
		// book-path check can exclude it.
		"bk-itunes":   two("/lib/Elsewhere/Some Title"),
		"bk-itunes-f": two("/mnt/data/books/itunes/Other/Title Here"),
	}
	return books, files
}

func runJunkTitles(t *testing.T, params string) (written []string, logs []string) {
	t.Helper()
	books, files := junkTitleFixture()
	var mu sync.Mutex
	store := &database.MockStore{
		GetAllBooksCoreFunc: func(_, _ int) ([]database.BookCore, error) { return books, nil },
		GetAllSeriesFunc: func() ([]database.Series, error) {
			return []database.Series{{ID: 7, Name: "Doctor Who: The Monthly Adventures"}, {ID: 8, Name: "Plain"}}, nil
		},
		GetBookFilesFunc: func(id string) ([]database.BookFile, error) { return files[id], nil },
		GetAuthorByIDFunc: func(id int) (*database.Author, error) {
			switch id {
			case 1:
				return &database.Author{ID: 1, Name: "Author A"}, nil
			case 2:
				return &database.Author{ID: 2, Name: "C. T. Phipps"}, nil
			}
			return nil, nil
		},
		GetBookAuthorsFunc: func(id string) ([]database.BookAuthor, error) {
			if id == "bk-author" {
				// Linked, but NOT the denormalized primary: the check must
				// read the junction, not only book.AuthorID.
				return []database.BookAuthor{{BookID: id, AuthorID: 2, Role: "author"}}, nil
			}
			return nil, nil
		},
		ModifyBookFunc: func(id string, fn func(*database.Book) error) (*database.Book, error) {
			b := &database.Book{ID: id}
			for _, c := range books {
				if c.ID == id {
					b.Title = c.Title
				}
			}
			if err := fn(b); err != nil {
				return b, nil
			}
			mu.Lock()
			written = append(written, id+"="+b.Title)
			mu.Unlock()
			return b, nil
		},
	}
	p := &Plugin{deps: &fakeDeps{store: store}}
	rep := &fakeReporter{}
	var raw json.RawMessage
	if params != "" {
		raw = json.RawMessage(params)
	}
	if err := p.runRepairJunkTitles(context.Background(), raw, rep); err != nil {
		t.Fatalf("runRepairJunkTitles: %v", err)
	}
	sort.Strings(written)
	return written, rep.logs
}

// 🔴 Big Finish / Doctor Who / Torchwood and the iTunes tree are hands-off,
// and a recovered title that is a person's name is refused. Only bk-good is
// retitled.
func TestRepairJunkTitles_ExclusionsAndPersonNameRefusal(t *testing.T) {
	written, logs := runJunkTitles(t, `{"apply":true}`)
	want := []string{"bk-good=Good Book"}
	if strings.Join(written, "|") != strings.Join(want, "|") {
		t.Fatalf("retitled %v, want exactly %v", written, want)
	}
	summary := strings.Join(logs, "\n")
	for _, frag := range []string{
		"3 (owner-manual series)",
		"2 (iTunes tree)",
		"2 (recovered title names the author or narrator)",
	} {
		if !strings.Contains(summary, frag) {
			t.Errorf("summary missing %q:\n%s", frag, summary)
		}
	}
}

func TestRepairJunkTitles_DryRunWritesNothing(t *testing.T) {
	written, logs := runJunkTitles(t, ``)
	if len(written) != 0 {
		t.Fatalf("dry run retitled %v", written)
	}
	if !strings.Contains(strings.Join(logs, "\n"), "would repair 1") {
		t.Errorf("dry run should report exactly one repair: %v", logs)
	}
}

func TestTitleNamesAPerson(t *testing.T) {
	people := splitCreditNames("Colin Baker, Nicola Bryant and India Fisher")
	for _, title := range []string{"colin baker", " Nicola Bryant ", "INDIA FISHER", "Colin Baker, Nicola Bryant and India Fisher"} {
		if !titleNamesAPerson(title, people) {
			t.Errorf("titleNamesAPerson(%q) = false, want true", title)
		}
	}
	for _, title := range []string{"The Colin Baker Years", "Good Book", ""} {
		if titleNamesAPerson(title, people) {
			t.Errorf("titleNamesAPerson(%q) = true, want false", title)
		}
	}
}
