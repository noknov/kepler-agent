package tool

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

type preparedFixture struct {
	state json.RawMessage
	err   error
}

func (*preparedFixture) Descriptor() Descriptor {
	return FunctionDescriptor("prepared", "help", ObjectSchema(nil, nil), WithEffects(EffectRead))
}
func (*preparedFixture) Execute(context.Context, Call) (Result, error) { return TextResult("ok"), nil }
func (f *preparedFixture) PrepareCall(call Call) (json.RawMessage, error) {
	if len(call.State) != 0 {
		return nil, errors.New("caller supplied state reached preparation")
	}
	return f.state, f.err
}

func TestPreparationSurvivesWrappersAndOwnsItsSnapshot(t *testing.T) {
	fixture := &preparedFixture{state: json.RawMessage(`{"proof":true}`)}
	catalog, _ := NewCatalog(BindSurface(fixture, "slack"))
	if err := catalog.SetPromptOverlay(func(d Descriptor) Descriptor {
		d.Description = "wording"
		return d
	}); err != nil {
		t.Fatal(err)
	}
	item, _ := catalog.Get("prepared")
	call, err := PrepareCall(item, Call{ID: "call", Arguments: json.RawMessage(`{}`), State: json.RawMessage(`{"forged":true}`)})
	if err != nil || string(call.State) != `{"proof":true}` || call.ID != "call" || string(call.Arguments) != `{}` {
		t.Fatalf("preparation changed identity or lost state: call=%+v err=%v", call, err)
	}
	fixture.state[0] = '!'
	if !json.Valid(call.State) {
		t.Fatal("captured state aliases adapter scratch memory")
	}
	fixture.err = errors.New("prerequisite failed")
	if _, err := PrepareCall(item, Call{}); !errors.Is(err, fixture.err) {
		t.Fatalf("preparation error lost through wrappers: %v", err)
	}
}

func TestPreparationRejectsInvalidAdapterState(t *testing.T) {
	if _, err := PrepareCall(&preparedFixture{state: json.RawMessage(`invalid`)}, Call{}); err == nil {
		t.Fatal("invalid state admitted to durable call")
	}
	call, err := PrepareCall(dynamicEffectFixture{}, Call{State: json.RawMessage(`{"forged":true}`)})
	if err != nil || len(call.State) != 0 {
		t.Fatalf("tool without preparation inherited supplied state: %+v %v", call, err)
	}
}
