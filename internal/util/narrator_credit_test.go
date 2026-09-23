// file: internal/util/narrator_credit_test.go
// version: 1.0.0
// guid: 2de4c360-8fcb-4097-8ecd-6150b1dd6090
// last-edited: 2026-09-23

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
