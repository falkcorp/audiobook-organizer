package mocks

import (
	"reflect"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/mock"
)

func TestMockStore_Coverage(t *testing.T) {
	m := NewMockStore(t)
	exp := m.EXPECT()
	storeType := reflect.TypeFor[database.Store]()

	for method := range storeType.Methods() {
		method := method
		name := method.Name

		// Build expectation via typed expecter methods.
		matcherArgs := make([]reflect.Value, 0, method.Type.NumIn())
		for in := range method.Type.Ins() {
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

		// Exercise typed Run(...) wrapper.
		runMethod := call.MethodByName("Run")
		if runMethod.IsValid() {
			runFuncType := runMethod.Type().In(0)
			runFn := reflect.MakeFunc(runFuncType, func(_ []reflect.Value) []reflect.Value { return nil })
			runMethod.Call([]reflect.Value{runFn})
		}

		// Exercise typed Return(...) wrapper.
		returnMethod := call.MethodByName("Return")
		if !returnMethod.IsValid() {
			t.Fatalf("EXPECT().%s missing Return(...)", name)
		}
		returnArgs := make([]reflect.Value, 0, method.Type.NumOut())
		for out := range method.Type.Outs() {
			returnArgs = append(returnArgs, reflect.Zero(out))
		}
		returnMethod.Call(returnArgs)

		// Exercise the actual mock method wrapper.
		methodVal := reflect.ValueOf(m).MethodByName(name)
		if !methodVal.IsValid() {
			t.Fatalf("MockStore missing method %s", name)
		}
		callArgs := make([]reflect.Value, 0, method.Type.NumIn())
		for in := range method.Type.Ins() {
			callArgs = append(callArgs, reflect.Zero(in))
		}
		_ = methodVal.Call(callArgs)
	}
}
