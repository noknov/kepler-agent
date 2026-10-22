package memory

import (
	"context"
	"encoding/json"
	"fmt"
)

type RecallPolicy struct {
	Version                                       int
	Candidates, MaxRecords, Preferences, MaxBytes int
	PreferenceBytes                               int
	Mode                                          string
}

func (p RecallPolicy) WithDefaults() RecallPolicy {
	if p.Version == 0 {
		p.Version = 1
	}
	if p.Candidates == 0 {
		p.Candidates = 32
	}
	if p.MaxRecords == 0 {
		p.MaxRecords = 8
	}
	if p.Preferences == 0 {
		p.Preferences = 2
	}
	if p.PreferenceBytes == 0 {
		p.PreferenceBytes = 1500
	}
	if p.MaxBytes == 0 {
		p.MaxBytes = 6000
	}
	if p.Mode == "" {
		p.Mode = "hybrid"
	}
	return p
}
func (p RecallPolicy) Validate() error {
	p = p.WithDefaults()
	if p.Version != 1 || p.MaxRecords < 1 || p.Candidates < p.MaxRecords || p.Candidates > MaxPageSize || p.Preferences < 0 || p.Preferences >= p.MaxRecords || p.MaxBytes < 1 || p.PreferenceBytes < 1 || (p.Mode != "hybrid" && p.Mode != "lexical") {
		return fmt.Errorf("invalid memory recall policy")
	}
	return nil
}

type Ranker interface {
	Rank(context.Context, string, []Record) ([]string, error)
}

// Recall separates persistent preferences from query-matched facts. It never
// mixes generations across reset, and re-rankers may select only supplied keys.
func Recall(ctx context.Context, store Store, identity Identity, text string, policy RecallPolicy, ranker Ranker) (Page, error) {
	policy = policy.WithDefaults()
	if err := policy.Validate(); err != nil {
		return Page{}, err
	}
	for attempt := 0; attempt < 2; attempt++ {
		relevant, err := store.Query(ctx, identity, Query{Text: text, Limit: policy.Candidates, ExcludePreferences: true, Mode: policy.Mode})
		if err != nil {
			return Page{}, err
		}
		preferences, err := store.Query(ctx, identity, Query{Kind: "preference", Limit: policy.Preferences})
		if err != nil {
			return Page{}, err
		}
		if relevant.Generation != preferences.Generation {
			continue
		}
		records := relevant.Records
		if ranker != nil && len(records) > 0 {
			keys, rankErr := ranker.Rank(ctx, text, records)
			if rankErr == nil {
				ordered, validErr := selectKeys(records, keys)
				if validErr == nil {
					records = ordered
				}
			}
		}
		prefs := Bound(preferences.Records[:min(policy.Preferences, len(preferences.Records))], min(policy.PreferenceBytes, policy.MaxBytes))
		slots := policy.MaxRecords - len(prefs)
		if len(records) > slots {
			records = records[:slots]
		}
		prefBytes := 0
		for _, record := range prefs {
			raw, _ := json.Marshal(record)
			prefBytes += len(raw) + 1
		}
		records = append(Bound(records, policy.MaxBytes-prefBytes), prefs...)

		// A reset during a slow semantic rank must not disclose pre-reset memory.
		keys := make([]string, 0, len(records))
		for _, record := range records {
			keys = append(keys, record.Key)
		}
		fence, err := store.Query(ctx, identity, Query{Limit: max(1, len(keys)), Keys: keys})
		if err != nil {
			return Page{}, err
		}
		if fence.Generation != relevant.Generation || !sameVersions(records, fence.Records) {
			continue
		}
		return Page{Generation: relevant.Generation, Records: Bound(records, policy.MaxBytes)}, nil
	}
	latest, err := store.Query(ctx, identity, Query{Limit: 1})
	if err != nil {
		return Page{}, err
	}
	return Page{Generation: latest.Generation, Records: []Record{}}, nil
}
func selectKeys(records []Record, keys []string) ([]Record, error) {
	byKey := make(map[string]Record, len(records))
	for _, record := range records {
		byKey[record.Key] = record
	}
	seen := map[string]bool{}
	out := make([]Record, 0, len(keys))
	for _, key := range keys {
		record, ok := byKey[key]
		if !ok || seen[key] {
			return nil, fmt.Errorf("ranker returned an unknown or duplicate memory key")
		}
		seen[key] = true
		out = append(out, record)
	}
	return out, nil
}

func sameVersions(selected, current []Record) bool {
	versions := make(map[string]int64, len(current))
	for _, record := range current {
		versions[record.Key] = record.Version
	}
	for _, record := range selected {
		if version, exists := versions[record.Key]; !exists || version != record.Version {
			return false
		}
	}
	return true
}
