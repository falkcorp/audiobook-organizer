// file: core/auth/methodclass_test.go
// version: 0.1.0
// guid: 3e7a9b52-0d16-4c84-b2f9-6a1c8e45d037
// last-edited: 2026-11-22

package auth

import "testing"

// TestClassify_RegistryAnswersEachPropertyOnItsOwn: classify is core's list for
// core's methods and the registered MethodSpec for the rest, each property
// read from its own field, and a method nobody registered is neither (F4-08
// B10y).
func TestClassify_RegistryAnswersEachPropertyOnItsOwn(t *testing.T) {
	s := &Service{methods: map[Method]MethodSpec{
		"ext:both":     {Method: "ext:both", Interactive: true, MayChangeCredentials: true},
		"ext:interact": {Method: "ext:interact", Interactive: true},
		"ext:creds":    {Method: "ext:creds", MayChangeCredentials: true},
		"ext:neither":  {Method: "ext:neither"},
		"books.abs":    {Method: "books.abs"},
	}}
	cases := []struct {
		m                 Method
		interactive, cred bool
	}{
		{MethodSession, true, true},
		{MethodSessionDelegated, false, true},
		{MethodAPIKey, false, false},
		{MethodAuthDisabled, false, false},
		{MethodNone, false, false},
		{"ext:both", true, true},
		{"ext:interact", true, false},
		{"ext:creds", false, true},
		{"ext:neither", false, false},
		{"books.abs", false, false},
		{"ext:nobody", false, false},   // unregistered extension: fail closed
		{"books.nobody", false, false}, // unregistered compat method: fail closed
	}
	for _, tc := range cases {
		t.Run(string(tc.m), func(t *testing.T) {
			c := s.classify(tc.m)
			if c.method != tc.m || c.interactive != tc.interactive || c.mayChangeCredentials != tc.cred {
				t.Fatalf("classify(%q) = %+v, want interactive=%v mayChangeCredentials=%v", tc.m, c, tc.interactive, tc.cred)
			}
			if got := s.interactive(tc.m); got != tc.interactive {
				t.Fatalf("interactive(%q) = %v, want %v", tc.m, got, tc.interactive)
			}
		})
	}
}

// TestPrincipalClass_OnlyForTheMethodItWasReadFor: a principal answers its
// registered classification only while its Method is the one PrincipalFor read
// it for. A holder that rewrites Method gets core's list for the new name, and
// a principal core never classified (an unknown method) is neither.
func TestPrincipalClass_OnlyForTheMethodItWasReadFor(t *testing.T) {
	both := methodClass{method: "ext:both", interactive: true, mayChangeCredentials: true}
	cases := []struct {
		name              string
		p                 *Principal
		interactive, cred bool
	}{
		{"nil principal", nil, false, false},
		{"registered extension, as built", &Principal{Method: "ext:both", class: both}, true, true},
		{"method rewritten to another extension", &Principal{Method: "ext:neither", class: both}, false, false},
		{"method rewritten to an api key", &Principal{Method: MethodAPIKey, class: both}, false, false},
		{"method rewritten to none", &Principal{Method: MethodNone, class: both}, false, false},
		{"unknown method, never classified", &Principal{Method: "ext:nobody"}, false, false},
		{"zero class with no method", &Principal{class: methodClass{interactive: true, mayChangeCredentials: true}}, false, false},
		{"core session needs no class", &Principal{Method: MethodSession}, true, true},
		{"core delegated session needs no class", &Principal{Method: MethodSessionDelegated}, false, true},
		{"core api key", &Principal{Method: MethodAPIKey}, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.p.Interactive(); got != tc.interactive {
				t.Errorf("Interactive() = %v, want %v", got, tc.interactive)
			}
			if got := tc.p.MayChangeCredentials(); got != tc.cred {
				t.Errorf("MayChangeCredentials() = %v, want %v", got, tc.cred)
			}
		})
	}
}
