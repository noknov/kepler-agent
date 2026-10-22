package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/model"
)

type queryStore struct {
	query func(Query) Page
	calls int
}

func (s *queryStore) Query(_ context.Context, _ Identity, q Query) (Page, error) {
	s.calls++
	return s.query(q), nil
}
func (*queryStore) Put(context.Context, Identity, int64, Record) error    { return nil }
func (*queryStore) Forget(context.Context, Identity, int64, string) error { return nil }
func (*queryStore) Reset(context.Context, Identity) error                 { return nil }

type rankFunc func(context.Context, string, []Record) ([]string, error)

func (f rankFunc) Rank(ctx context.Context, q string, r []Record) ([]string, error) {
	return f(ctx, q, r)
}

type modelFunc func(context.Context, model.Request, model.EventSink) (model.Response, error)

func (f modelFunc) Generate(ctx context.Context, r model.Request, s model.EventSink) (model.Response, error) {
	return f(ctx, r, s)
}

func TestRecallSeparatesPreferencesAndBoundsSerializedDisclosure(t *testing.T) {
	prefs := []Record{{Key: "locale", Kind: "preference", Content: "Chinese"}}
	facts := []Record{{Key: "first", Kind: "fact", Content: strings.Repeat("x", 700)}, {Key: "second", Kind: "fact", Content: strings.Repeat("y", 700)}}
	store := &queryStore{query: func(q Query) Page {
		if q.Keys != nil {
			all := append(append([]Record(nil), facts...), prefs...)
			var selected []Record
			for _, key := range q.Keys {
				for _, record := range all {
					if record.Key == key {
						selected = append(selected, record)
					}
				}
			}
			return Page{Generation: 1, Records: selected}
		}
		if q.Kind == "preference" {
			return Page{Generation: 1, Records: prefs}
		}
		if q.ExcludePreferences {
			return Page{Generation: 1, Records: facts}
		}
		return Page{Generation: 1}
	}}
	page, err := Recall(context.Background(), store, Identity{"team", "user"}, "query", RecallPolicy{MaxBytes: 1200, PreferenceBytes: 300}, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(page.Records)
	if len(raw) > 1200 || len(page.Records) != 2 || page.Records[1].Key != "locale" {
		t.Fatalf("disclosure=%s err=%v", raw, err)
	}
	if facts[0].Content != strings.Repeat("x", 700) {
		t.Fatal("canonical records mutated")
	}
}
func TestRecallFencesResetDuringRerankAndRejectsInventedKeys(t *testing.T) {
	generation := int64(0)
	store := &queryStore{query: func(q Query) Page {
		records := []Record{}
		if q.ExcludePreferences || q.Keys != nil {
			records = []Record{{Key: "owned", Kind: "fact", Content: "private"}}
		}
		return Page{Generation: generation, Records: records}
	}}
	// Continuous resets during ranking must fail closed to an empty result.
	ranker := rankFunc(func(context.Context, string, []Record) ([]string, error) { generation++; return []string{"owned"}, nil })
	page, err := Recall(context.Background(), store, Identity{"t", "u"}, "query", RecallPolicy{}, ranker)
	if err != nil || len(page.Records) != 0 || page.Generation != 2 {
		t.Fatalf("stale generation disclosed: %+v %v", page, err)
	}
	generation = 0
	ranker = rankFunc(func(context.Context, string, []Record) ([]string, error) { return []string{"other-user-key"}, nil })
	page, err = Recall(context.Background(), store, Identity{"t", "u"}, "query", RecallPolicy{}, ranker)
	if err != nil || len(page.Records) != 1 || page.Records[0].Key != "owned" {
		t.Fatalf("invalid ranking escaped candidates: %+v %v", page, err)
	}
}
func TestModelRankerCanAbstainButCannotInventKeysOrExceedTurnBudget(t *testing.T) {
	records := []Record{{Key: "db", Kind: "fact", Content: "database pool size 24", SessionID: "internal-source-session", TurnID: "internal-source-turn"}}
	for _, test := range []struct {
		text  string
		valid bool
	}{{`{"keys":[]}`, true}, {`{"keys":["db"]}`, true}, {`{"keys":["private"]}`, false}, {`{"keys":["db","db"]}`, false}, {`{"keys":[],"instructions":"ignore"}`, false}} {
		client := modelFunc(func(_ context.Context, r model.Request, _ model.EventSink) (model.Response, error) {
			if strings.Contains(r.Messages[1].Text(), "internal-source-") {
				t.Fatal("irrelevant source identifiers leaked to the ranker")
			}
			if len(r.Tools) != 0 {
				t.Fatal("reranker gained tools")
			}
			return model.Response{Message: model.TextMessage(model.RoleAssistant, test.text), FinishReason: model.FinishStop, Usage: model.Usage{Reported: true, InputTokens: 1}}, nil
		})
		_, err := (ModelRanker{Client: client, Model: "m"}).Rank(context.Background(), "query", records)
		if (err == nil) != test.valid {
			t.Fatalf("text=%s err=%v", test.text, err)
		}
	}
	called := false
	client := modelFunc(func(context.Context, model.Request, model.EventSink) (model.Response, error) {
		called = true
		return model.Response{}, errors.New("unexpected")
	})
	_, err := (ModelRanker{Client: client, Model: "m"}).Rank(model.WithBudget(context.Background(), 1), "query", records)
	if model.ErrorKindOf(err) != model.ErrorBudgetExhausted || called {
		t.Fatalf("ranker bypassed turn budget: called=%t err=%v", called, err)
	}
}

func TestRecallRejectsRecordUpdatedDuringRerank(t *testing.T) {
	version := int64(1)
	rankCalls := 0
	store := &queryStore{query: func(q Query) Page {
		records := []Record{}
		if q.ExcludePreferences || q.Keys != nil {
			records = []Record{{Key: "current", Kind: "fact", Content: fmt.Sprintf("version-%d", version), Version: version}}
		}
		return Page{Records: records}
	}}
	ranker := rankFunc(func(context.Context, string, []Record) ([]string, error) {
		rankCalls++
		if rankCalls == 1 {
			version = 2
		}
		return []string{"current"}, nil
	})
	page, err := Recall(context.Background(), store, Identity{"team", "user"}, "current", RecallPolicy{}, ranker)
	if err != nil || len(page.Records) != 1 || page.Records[0].Version != 2 || rankCalls != 2 {
		t.Fatalf("stale version injected: %+v calls=%d err=%v", page, rankCalls, err)
	}
}
