// file: core/config/redact_test.go
// version: 1.0.2
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
// Apply's error. In a section holding a sensitive key, a FieldError on any of
// its keys, sensitive or plain, keeps its path and loses its message, and a
// bare error is replaced whole (fail closed). A sensitive key's flag text
// never reaches the error either.
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
	// A FieldError on a plain key of the same section keeps its path and
	// loses its message too: the section holds a sensitive key.
	plain := apply(map[config.Path]string{oracleFreq: `"jammed"`})
	if !errors.As(plain, &fe) || fe.Path != oracleFreq || strings.Contains(plain.Error(), "the frequency is jammed") || !strings.Contains(plain.Error(), "redacted") {
		t.Errorf("a plain key's Validator error = %v, want a redacted FieldError on %s", plain, oracleFreq)
	}
	// A sensitive key's flag text is never quoted (strconv echoes its input).
	if _, err := oracleStore(t, &logRecorder{}, map[config.Path]string{oraclePin: "joker-0451"}); err == nil || strings.Contains(err.Error(), "joker") {
		t.Errorf("a bad flag for a sensitive key = %v, want an error without the flag text", err)
	}
}

const (
	signalSection config.Path = "core.signal"
	signalOwner   config.Path = "core.signal.owner_email"
	signalDomain  config.Path = "core.signal.domain"
	beaconSection config.Path = "core.beacon"
	beaconFreq    config.Path = "core.beacon.frequency"
)

// signalIdent's Validator is a cross-field check: its FieldError names the
// plain key domain but quotes the sensitive owner_email beside it.
type signalIdent struct {
	OwnerEmail string `json:"owner_email" sensitive:"true"`
	Domain     string `json:"domain"`
}

func (s *signalIdent) Validate() error {
	if s.OwnerEmail != "" && s.Domain != "" && !strings.HasSuffix(s.OwnerEmail, "@"+s.Domain) {
		return &config.FieldError{Path: signalDomain, Err: fmt.Errorf("domain %q does not match the owner %q", s.Domain, s.OwnerEmail)}
	}
	return nil
}

// beaconIdent holds no sensitive key, so its Validator's message is kept.
type beaconIdent struct {
	Frequency string `json:"frequency"`
}

func (b *beaconIdent) Validate() error {
	if b.Frequency == "jammed" {
		return &config.FieldError{Path: beaconFreq, Err: fmt.Errorf("the beacon frequency %q is jammed by the Riddler", b.Frequency)}
	}
	return nil
}

// TestValidatorMessageDroppedInSensitiveSection (T4 re-verify NEW-1,
// 05-config §1.3.1): in a section that holds a sensitive key, a Validator's
// FieldError on a plain key keeps its path and loses its message, so a
// cross-field check that quotes the sensitive key beside it leaks the value
// to none of AttachReport.Quarantined, KeyState.Quarantined, the logs and
// Apply's error. A section with no sensitive key keeps its Validator's
// message.
func TestValidatorMessageDroppedInSensitiveSection(t *testing.T) {
	ctx := context.Background()
	logs := &logRecorder{}
	st, err := config.NewStore(config.Options{AppID: "gotham", Logger: slog.New(logs)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	if _, err := config.Register(st.Registry(), config.Define(signalSection, signalIdent{})); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Register(st.Registry(), config.Define(beaconSection, beaconIdent{})); err != nil {
		t.Fatal(err)
	}
	if err := st.Load(ctx); err != nil {
		t.Fatal(err)
	}
	mem := configtest.NewMemory()
	mem.Put(config.Override{Path: signalOwner, Value: json.RawMessage(`"selina.kyle@catwoman.example"`), By: "alfred"})
	mem.Put(config.Override{Path: signalDomain, Value: json.RawMessage(`"wayne.example"`), By: "alfred"})
	rep, err := st.Attach(ctx, mem, configtest.NewCommitter(mem))
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Quarantined) == 0 {
		t.Fatal("the rows failing the Validator were not quarantined")
	}
	surfaces := map[string]string{}
	for p, e := range rep.Quarantined {
		surfaces["AttachReport.Quarantined["+string(p)+"]"] = e.Error()
	}
	quarantined := 0
	for _, p := range []config.Path{signalOwner, signalDomain} {
		if ks, ok := st.Lookup(p); ok && ks.Quarantined != nil {
			quarantined++
			surfaces["KeyState.Quarantined["+string(p)+"]"] = ks.Quarantined.Error()
		}
	}
	if quarantined == 0 {
		t.Fatal("no KeyState.Quarantined is set")
	}
	if logs.count(string(signalSection)) == 0 {
		t.Fatal("the quarantine was not logged")
	}
	admin := config.Actor{Subject: "alfred", Interactive: true, Can: func(string) bool { return true }}
	_, applyErr := st.Apply(ctx, config.Patch{Set: map[config.Path]json.RawMessage{
		signalOwner:  json.RawMessage(`"harley.quinn@arkham.example"`),
		signalDomain: json.RawMessage(`"gotham.example"`),
	}}, admin)
	var fe *config.FieldError
	if !errors.Is(applyErr, config.ErrInvalid) || !errors.As(applyErr, &fe) || fe.Path != signalDomain || !strings.Contains(applyErr.Error(), "redacted") {
		t.Errorf("Apply = %v, want ErrInvalid with a redacted FieldError on %s", applyErr, signalDomain)
	}
	surfaces["Apply"] = fmt.Sprint(applyErr)
	surfaces["logs"] = logs.all()
	for name, s := range surfaces {
		for _, v := range []string{"selina.kyle", "catwoman.example", "harley.quinn", "arkham.example", "does not match the owner"} {
			if strings.Contains(s, v) {
				t.Errorf("%s carries Validator text %q: %s", name, v, s)
			}
		}
	}
	// A section with no sensitive key keeps its Validator's message.
	_, err = st.Apply(ctx, config.Patch{Set: map[config.Path]json.RawMessage{beaconFreq: json.RawMessage(`"jammed"`)}}, admin)
	if !errors.As(err, &fe) || fe.Path != beaconFreq || !strings.Contains(err.Error(), `the beacon frequency "jammed" is jammed by the Riddler`) {
		t.Errorf("Apply on a section without a sensitive key = %v, want its Validator's message on %s", err, beaconFreq)
	}
}

const gadgetSection config.Path = "core.gadgets"

// gadgetEntry is one entry of the map section core.gadgets.
type gadgetEntry struct {
	Code  string `json:"code" sensitive:"true"`
	Label string `json:"label"`
}

// gadgetMap's Validator builds a FieldError path from an entry's sensitive
// code, which is not a valid entry ID.
type gadgetMap map[string]gadgetEntry

func (m *gadgetMap) Validate() error {
	for _, e := range *m {
		if e.Label == "leak" {
			return &config.FieldError{Path: gadgetSection + "." + config.Path(strings.ToUpper(e.Code)) + ".label", Err: errors.New("the label leaks")}
		}
	}
	return nil
}

// TestValidatorPathWithInvalidEntryID (05-config §1.3.1): in a map section
// that holds a sensitive key, a Validator's FieldError whose entry ID is not
// a valid segment does not name a key, so its path is not kept either: the
// error becomes a fixed message on the section, and the text the Validator
// put in the path reaches no error.
func TestValidatorPathWithInvalidEntryID(t *testing.T) {
	ctx := context.Background()
	st, err := config.NewStore(config.Options{AppID: "gotham", Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	if _, err := config.Register(st.Registry(), config.Define(gadgetSection, gadgetMap{})); err != nil {
		t.Fatal(err)
	}
	if err := st.Load(ctx); err != nil {
		t.Fatal(err)
	}
	mem := configtest.NewMemory()
	if _, err := st.Attach(ctx, mem, configtest.NewCommitter(mem)); err != nil {
		t.Fatal(err)
	}
	admin := config.Actor{Subject: "alfred", Interactive: true, Can: func(string) bool { return true }}
	_, err = st.Apply(ctx, config.Patch{Set: map[config.Path]json.RawMessage{
		gadgetSection + ".batarang.code":  json.RawMessage(`"riddler"`),
		gadgetSection + ".batarang.label": json.RawMessage(`"leak"`),
	}}, admin)
	var fe *config.FieldError
	if !errors.As(err, &fe) || fe.Path != gadgetSection || strings.Contains(strings.ToLower(err.Error()), "riddler") {
		t.Errorf("Apply = %v, want a fixed FieldError on %s without the code", err, gadgetSection)
	}
}
