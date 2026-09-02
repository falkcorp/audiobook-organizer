// file: internal/metadata/mocks/mock_metadata_extractor_coverage_test.go
// version: 1.1.0
// guid: a1370ec4-a370-4039-b5d3-738ac582e206
// last-edited: 2026-09-02

package mocks

import (
	"reflect"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/stretchr/testify/mock"
)

func TestMockMetadataExtractor_Coverage(t *testing.T) {
	m := NewMockMetadataExtractor(t)
	exp := m.EXPECT()
	ifaceType := reflect.TypeFor[metadata.MetadataExtractor]()

	for method := range ifaceType.Methods() {
		name := method.Name

		matcherArgs := make([]reflect.Value, 0, method.Type.NumIn())
		for range method.Type.Ins() {
			matcherArgs = append(matcherArgs, reflect.ValueOf(mock.Anything))
		}

		expMethod := reflect.ValueOf(exp).MethodByName(name)
		if !expMethod.IsValid() {
			t.Fatalf("EXPECT() missing method %s", name)
		}
		callVals := expMethod.Call(matcherArgs)
		if len(callVals) != 1 {
			t.Fatalf("EXPECT().%s returned %d values, expected 1", name, len(callVals))
		}
		call := callVals[0]

		runMethod := call.MethodByName("Run")
		if runMethod.IsValid() {
			runFuncType := runMethod.Type().In(0)
			runFn := reflect.MakeFunc(runFuncType, func(_ []reflect.Value) []reflect.Value { return nil })
			runMethod.Call([]reflect.Value{runFn})
		}

		returnMethod := call.MethodByName("Return")
		if !returnMethod.IsValid() {
			t.Fatalf("EXPECT().%s missing Return(...)", name)
		}
		returnArgs := make([]reflect.Value, 0, method.Type.NumOut())
		for out := range method.Type.Outs() {
			returnArgs = append(returnArgs, reflect.Zero(out))
		}
		returnMethod.Call(returnArgs)

		methodVal := reflect.ValueOf(m).MethodByName(name)
		if !methodVal.IsValid() {
			t.Fatalf("MockMetadataExtractor missing method %s", name)
		}
		callArgs := make([]reflect.Value, 0, method.Type.NumIn())
		for in := range method.Type.Ins() {
			callArgs = append(callArgs, reflect.Zero(in))
		}
		_ = methodVal.Call(callArgs)
	}
}
