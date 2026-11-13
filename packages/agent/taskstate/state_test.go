package taskstate

import "testing"

func TestApplyRetainsConstraintsAndRejectsSilentRewrite(t *testing.T) {
	item := Item{ID: "api", Kind: "constraint", Text: "keep API", Status: "active", Sources: []Reference{{Sequence: 1, Quote: "keep API"}}}
	first, err := Apply(State{}, Patch{Upsert: []Item{item}})
	if err != nil {
		t.Fatal(err)
	}
	work := Item{ID: "work", Kind: "work", Text: "implement", Status: "active", Sources: []Reference{{Sequence: 1}}}
	next, err := Apply(first, Patch{ExpectedRevision: 1, Upsert: []Item{work}})
	if err != nil || len(next.Items) != 2 {
		t.Fatalf("state=%+v err=%v", next, err)
	}
	item.Status = "superseded"
	if _, err := Apply(next, Patch{ExpectedRevision: 2, Upsert: []Item{item}}); err == nil {
		t.Fatal("user constraint silently removed")
	}
	item.Sources = []Reference{{Sequence: 10, Quote: "replace that constraint"}}
	final, err := Apply(next, Patch{ExpectedRevision: 2, Upsert: []Item{item}})
	if err != nil || first.Items[0].Status != "active" || final.Items[0].Status != "superseded" {
		t.Fatalf("bad transition or mutation: %+v %v", final, err)
	}
}

func TestArchivalPreservesActiveConstraintsAndMakesRoom(t *testing.T) {
	state := State{SchemaVersion: SchemaVersion, Revision: 1, Items: []Item{{ID: "rule", Kind: "constraint", Text: "preserve", Status: "active", Sources: []Reference{{Sequence: 1}}}, {ID: "finished", Kind: "work", Text: "completed", Status: "done", Sources: []Reference{{Sequence: 1}}}}}
	if _, err := Apply(state, Patch{ExpectedRevision: 1, Archive: []string{"rule"}}); err == nil {
		t.Fatal("active constraint archived")
	}
	next, err := Apply(state, Patch{ExpectedRevision: 1, Archive: []string{"finished"}})
	if err != nil || len(next.Items) != 1 || next.Items[0].ID != "rule" || len(state.Items) != 2 {
		t.Fatalf("next=%+v original=%+v error=%v", next, state, err)
	}
	if _, err := Apply(next, Patch{ExpectedRevision: 1, Archive: []string{"rule"}}); err == nil {
		t.Fatal("stale revision accepted")
	}
}
