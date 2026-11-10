// file: core/config/redact_test.go
// version: 1.0.1
// guid: 0d8e3a51-6b2f-4c97-a1e4-5f7b9c2d8e63
// last-edited: 2026-11-10

package config_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/config/configtest"
)

const (
	oracleSection config.Path = "core.oracle"
	oracleOwner   config.Path = "core.oracle.owner_email"
	oracleFreq    config.Path = "core.oracle.frequency"
	oraclePin     config.Path = "core.oracle.pin"
)

// oracleIdent's Validator quotes the values it rejects, as a careless
// declarer's would: once in a FieldError on the sensitive key, once in a
// FieldError on a plain key, and once in a bare error.
type oracleIdent struct {
	OwnerEmail string `json:"owner_email" sensitive:"true"`
	Frequency  string `json:"frequency"`
	Bare       bool   `json:"bare"`
	Pin        int    `json:"pin" sensitive:"true"`
}

func (o *oracleIdent) Validate() error {
	var errs []error
	if o.OwnerEmail != "" && !strings.HasSuffix(o.OwnerEmail, "@wayne.example") {
		errs = append(errs, &config.FieldError{Path: oracleOwner, Err: fmt.Errorf("%q is not a wayne.example address", o.OwnerEmail)})
	}
	if o.Frequency == "jammed" {
		errs = append(errs, &config.FieldError{Path: oracleFreq, Err: errors.New("the frequency is jammed")})
	}
	if o.Bare && o.OwnerEmail != "" {
		errs = append(errs, fmt.Errorf("the bare check quotes %s", o.OwnerEmail))
	}
	return errors.Join(errs...)
}

// oracleStore is a store over core.oracle alone, logging into logs.
func oracleStore(t *testing.T, logs *logRecorder, flags map[config.Path]string) (*config.Store, error) {
	t.Helper()
	st, err := config.NewStore(config.Options{AppID: "gotham", Logger: slog.New(logs), Flags: flags})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	if _, err := config.Register(st.Registry(), config.Define(oracleSection, oracleIdent{})); err != nil {
		t.Fatal(err)
	}
	return st, st.Load(context.Background())
}

// TestSensitiveValueNeverInValidatorErrors (T4 review MAJOR-2, 05-config
// §1.3.1): a section Validator's message can quote a sensitive value, and it
// reaches AttachReport.Quarantined, KeyState.Quarantined, the logs and
// Apply's error. In a section holding a sensitive key, a FieldError on the
// sensitive key keeps its path and loses its message, a bare error is
// replaced whole, and a FieldError on a plain key keeps its detail. A
// sensitive key's flag text never reaches the error either.
func TestSensitiveValueNeverInValidatorErrors(t *testing.T) {
	ctx := context.Background()
	logs := &logRecorder{}
	st, err := oracleStore(t, logs, nil)
	if err != nil {
		t.Fatal(err)
	}
	mem := configtest.NewMemory()
	mem.Put(config.Override{Path: oracleOwner, Value: json.RawMessage(`"selina.kyle@catwoman.example"`), By: "alfred"})
	rep, err := st.Attach(ctx, mem, configtest.NewCommitter(mem))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Quarantined[oracleOwner] == nil {
		t.Fatal("the row failing the Validator was not quarantined")
	}
	ks, _ := st.Lookup(oracleOwner)
	if ks.Quarantined == nil {
		t.Fatal("KeyState.Quarantined is empty")
	}
	if logs.count("core.oracle") == 0 {
		t.Fatal("the quarantine was not logged")
	}
	admin := config.Actor{Subject: "alfred", Interactive: true, Can: func(string) bool { return true }}
	apply := func(set map[config.Path]string) error {
		p := config.Patch{Set: map[config.Path]json.RawMessage{}}
		for k, v := range set {
			p.Set[k] = json.RawMessage(v)
		}
		_, err := st.Apply(ctx, p, admin)
		return err
	}
	onKey := apply(map[config.Path]string{oracleOwner: `"harley.quinn@arkham.example"`})
	var fe *config.FieldError
	if !errors.Is(onKey, config.ErrInvalid) || !errors.As(onKey, &fe) || fe.Path != oracleOwner {
		t.Fatalf("Apply = %v, want ErrInvalid with a FieldError on %s", onKey, oracleOwner)
	}
	bare := apply(map[config.Path]string{oracleOwner: `"alfred.pennyworth@wayne.example"`, "core.oracle.bare": "true"})
	if bare == nil || !errors.As(bare, &fe) || fe.Path != oracleSection || !strings.Contains(bare.Error(), "redacted") {
		t.Fatalf("bare Validator error = %v, want a redacted FieldError on %s", bare, oracleSection)
	}
	for name, s := range map[string]string{
		"AttachReport.Quarantined": rep.Quarantined[oracleOwner].Error(),
		"KeyState.Quarantined":     ks.Quarantined.Error(),
		"logs":                     logs.all(),
		"Apply (sensitive key)":    onKey.Error(),
		"Apply (bare error)":       bare.Error(),
	} {
		for _, v := range []string{"selina.kyle", "harley.quinn", "alfred.pennyworth"} {
			if strings.Contains(s, v) {
				t.Errorf("%s carries the sensitive value %s: %s", name, v, s)
			}
		}
	}
	// A FieldError on a plain key of the same section keeps its detail.
	if err := apply(map[config.Path]string{oracleFreq: `"jammed"`}); err == nil || !strings.Contains(err.Error(), "the frequency is jammed") {
		t.Errorf("a plain key's Validator error lost its detail: %v", err)
	}
	// A sensitive key's flag text is never quoted (strconv echoes its input).
	if _, err := oracleStore(t, &logRecorder{}, map[config.Path]string{oraclePin: "joker-0451"}); err == nil || strings.Contains(err.Error(), "joker") {
		t.Errorf("a bad flag for a sensitive key = %v, want an error without the flag text", err)
	}
}
