// file: internal/util/narrator_credit_test.go
// version: 1.1.0
// guid: 2de4c360-8fcb-4097-8ecd-6150b1dd6090
// last-edited: 2026-10-27

package util

import (
	"reflect"
	"testing"
)

// Cases are the shapes found among production's joined narrator entries on
// 2026-09-23.
func TestCleanNarratorCredit(t *testing.T) {
	cases := []struct {
		name    string
		credit  string
		authors []string
		want    []string
		verdict NarratorCreditVerdict
	}{
		{"cast", "Bern Dean, Zachary Johnson, Annie Ellicott", []string{"Bruce Sentar"},
			[]string{"Bern Dean", "Zachary Johnson", "Annie Ellicott"}, NarratorCreditPeople},
		{"and-joined", "Kate Reading and Michael Kramer", nil,
			[]string{"Kate Reading", "Michael Kramer"}, NarratorCreditPeople},
		{"slash", "Jacob Dudman/Safiyya Ingar", nil, []string{"Jacob Dudman", "Safiyya Ingar"}, NarratorCreditPeople},
		{"author plus narrator", "Adrian Tchaikovsky, Ben Allen", []string{"Adrian Tchaikovsky"},
			[]string{"Ben Allen"}, NarratorCreditPeople},
		{"author match ignores case and spacing", "adrian  tchaikovsky, Ben Allen", []string{"Adrian Tchaikovsky"},
			[]string{"Ben Allen"}, NarratorCreditPeople},
		{"authors only", "Craig Martelle, Michael Anderle", []string{"Michael Anderle", "Craig Martelle"},
			nil, NarratorCreditAllAuthors},
		{"single self-read", "Neil Gaiman", []string{"Neil Gaiman"}, nil, NarratorCreditAllAuthors},
		{"translator suffix", "Alexey Osadchuk, Andrew Douglas Schmitt - translator", []string{"Alexey Osadchuk"},
			nil, NarratorCreditAllAuthors},
		{"translator suffix no space", "Boris Romanovsky, Zachary J. Lorang -translated by", nil,
			[]string{"Boris Romanovsky"}, NarratorCreditPeople},
		{"introduction suffix", "Amy Lea, Mindy Kaling - introduction, Natalie Naudus", []string{"Amy Lea"},
			[]string{"Natalie Naudus"}, NarratorCreditPeople},
		{"editor parenthesized", "Jonathan Maberry (editor), Ray Porter", nil, []string{"Ray Porter"}, NarratorCreditPeople},
		{"role prefix", "Translated by Jane Doe, Ray Porter", nil, []string{"Ray Porter"}, NarratorCreditPeople},
		{"By: prefix", "By: J.N. Chaney, Rick Partlow", []string{"J.N. Chaney"}, []string{"Rick Partlow"}, NarratorCreditPeople},
		{"By: prefix no author list", "By: J.N. Chaney, Rick Partlow", nil,
			[]string{"J.N. Chaney", "Rick Partlow"}, NarratorCreditPeople},
		{"url", "https://kickass.to/user/Morrogoth/", nil, nil, NarratorCreditJunk},
		{"surname first", "Le Guin, Ursula", nil, []string{"Le Guin, Ursula"}, NarratorCreditPeople},
		{"hyphenated name kept", "Emma Campbell-Jones, Paul McGann", nil,
			[]string{"Emma Campbell-Jones", "Paul McGann"}, NarratorCreditPeople},
		{"only a role", "Jane Doe - translator", nil, nil, NarratorCreditEmpty},
		{"blank", "  ", nil, nil, NarratorCreditEmpty},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, verdict := CleanNarratorCredit(c.credit, c.authors)
			if verdict != c.verdict || !reflect.DeepEqual(got, c.want) {
				t.Fatalf("CleanNarratorCredit(%q, %v) = %v, %v; want %v, %v", c.credit, c.authors, got, verdict, c.want, c.verdict)
			}
		})
	}
}

// NarratorCreditPiecePerson reads a piece as CleanNarratorCredit does, and says
// where the person stands in it.
func TestNarratorCreditPiecePerson(t *testing.T) {
	cases := []struct {
		name       string
		piece      string
		person     string
		offset     int
		ok         bool
		cleanedAs  []string
		cleanedAsV NarratorCreditVerdict
	}{
		{"plain", "Renee Montoya", "Renee Montoya", 0, true, []string{"Renee Montoya"}, NarratorCreditPeople},
		{"read by prefix", "Read by Renee Montoya", "Renee Montoya", 8, true, []string{"Renee Montoya"}, NarratorCreditPeople},
		{"by colon prefix", "By: Harvey Dent", "Harvey Dent", 4, true, []string{"Harvey Dent"}, NarratorCreditPeople},
		{"narrated by, upper case", "NARRATED BY Harvey Dent", "Harvey Dent", 12, true, []string{"Harvey Dent"}, NarratorCreditPeople},
		{"surrounding space", "  Read by   Harvey Dent  ", "Harvey Dent", 12, true, []string{"Harvey Dent"}, NarratorCreditPeople},
		{"role suffix", "Harvey Dent - translator", "", 0, false, nil, NarratorCreditEmpty},
		{"role prefix", "Translated by Harvey Dent", "", 0, false, nil, NarratorCreditEmpty},
		{"role after read by", "Read by Harvey Dent (editor)", "", 0, false, nil, NarratorCreditEmpty},
		{"blank", "   ", "", 0, false, nil, NarratorCreditEmpty},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			person, offset, ok := NarratorCreditPiecePerson(c.piece)
			if person != c.person || offset != c.offset || ok != c.ok {
				t.Fatalf("NarratorCreditPiecePerson(%q) = %q, %d, %v; want %q, %d, %v", c.piece, person, offset, ok, c.person, c.offset, c.ok)
			}
			if ok && c.piece[offset:offset+len(person)] != person {
				t.Errorf("offset %d does not point at the person in %q", offset, c.piece)
			}
			// It is the reading CleanNarratorCredit makes of a one-piece credit.
			got, verdict := CleanNarratorCredit(c.piece, nil)
			if verdict != c.cleanedAsV || !reflect.DeepEqual(got, c.cleanedAs) {
				t.Errorf("CleanNarratorCredit(%q) = %v, %v; want %v, %v", c.piece, got, verdict, c.cleanedAs, c.cleanedAsV)
			}
		})
	}
}
