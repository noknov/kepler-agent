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
