// Package taskstate defines a versioned, evidence-linked task projection.
// It records agent claims, never authorization or a substitute for verification.
package taskstate

import (
	"fmt"
	"regexp"
	"strings"
)

const SchemaVersion = 1
const MaxItems = 64
const MaxStateBytes = 24 << 10

var validID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

type Reference struct {
	Sequence uint64 `json:"sequence"`
	Quote    string `json:"quote,omitempty"`
}

type Item struct {
	ID                string      `json:"id"`
	Kind              string      `json:"kind"` // goal, constraint, work, question, artifact
	Text              string      `json:"text"`
	Status            string      `json:"status"` // active, blocked, done, verified, superseded
	Sources           []Reference `json:"sources"`
	Evidence          []Reference `json:"evidence,omitempty"`
	WorkspaceRevision string      `json:"workspace_revision,omitempty"`
	Verification      string      `json:"verification,omitempty"`
}

type State struct {
	SchemaVersion int    `json:"schema_version"`
	Revision      uint64 `json:"revision"`
	Items         []Item `json:"items"`
}

type Patch struct {
	ExpectedRevision uint64 `json:"expected_revision"`
	Upsert           []Item `json:"upsert"`
}

// Apply is a CAS update. Entries are retained until explicitly superseded;
// resummarizing or omitting an item cannot silently remove a constraint.
func Apply(current State, patch Patch) (State, error) {
	if current.Revision != patch.ExpectedRevision {
		return State{}, fmt.Errorf("task state revision conflict: read revision %d before updating", current.Revision)
	}
	if len(patch.Upsert) == 0 || len(patch.Upsert) > MaxItems {
		return State{}, fmt.Errorf("task update requires 1..%d items", MaxItems)
	}
	out := State{SchemaVersion: SchemaVersion, Revision: current.Revision + 1, Items: append([]Item(nil), current.Items...)}
	positions := make(map[string]int, len(out.Items))
	for i, item := range out.Items {
		positions[item.ID] = i
	}
	seen := map[string]bool{}
	for _, item := range patch.Upsert {
		if !validID.MatchString(item.ID) || seen[item.ID] {
			return State{}, fmt.Errorf("task IDs must be unique stable identifiers")
		}
		seen[item.ID] = true
		if strings.TrimSpace(item.Text) == "" || len(item.Text) > 2000 || len(item.WorkspaceRevision) > 200 || len(item.Verification) > 1000 {
			return State{}, fmt.Errorf("task item text is empty or exceeds limits")
		}
		switch item.Kind {
		case "goal", "constraint", "work", "question", "artifact", "effect":
		default:
			return State{}, fmt.Errorf("unsupported task kind %q", item.Kind)
		}
		switch item.Status {
		case "active", "blocked", "done", "verified", "superseded", "unknown", "reconciled":
		default:
			return State{}, fmt.Errorf("unsupported task status %q", item.Status)
		}
		if len(item.Sources) == 0 || len(item.Sources) > 8 || len(item.Evidence) > 8 {
			return State{}, fmt.Errorf("task items require 1..8 sources and at most 8 evidence references")
		}
		for _, ref := range append(append([]Reference(nil), item.Sources...), item.Evidence...) {
			if ref.Sequence == 0 || len(ref.Quote) > 2000 {
				return State{}, fmt.Errorf("invalid evidence reference")
			}
		}
		if (item.Status == "verified" || item.Status == "reconciled") && (len(item.Evidence) == 0 || strings.TrimSpace(item.Verification) == "") {
			return State{}, fmt.Errorf("verified claims require tool evidence and a verification description")
		}
		if item.Kind == "effect" {
			if _, exists := positions[item.ID]; !exists {
				return State{}, fmt.Errorf("uncertain effects are created only by the runtime")
			}
			if item.Status != "unknown" && item.Status != "reconciled" {
				return State{}, fmt.Errorf("effects require explicit reconciliation")
			}
		} else if item.Status == "unknown" || item.Status == "reconciled" {
			return State{}, fmt.Errorf("effect status requires an effect item")
		}
		if pos, ok := positions[item.ID]; ok {
			old := out.Items[pos]
			if old.Kind == "effect" {
				if item.Text != old.Text || len(item.Sources) != len(old.Sources) {
					return State{}, fmt.Errorf("runtime effect identity and sources are immutable")
				}
				for i, ref := range old.Sources {
					if item.Sources[i] != ref {
						return State{}, fmt.Errorf("runtime effect sources are immutable")
					}
				}
			}
			if (old.Kind == "goal" || old.Kind == "constraint") && (old.Text != item.Text || old.Status != item.Status) {
				var previous, newest uint64
				for _, ref := range old.Sources {
					previous = max(previous, ref.Sequence)
				}
				for _, ref := range item.Sources {
					newest = max(newest, ref.Sequence)
				}
				if newest <= previous {
					return State{}, fmt.Errorf("changing a user goal or constraint requires a later user source")
				}
			}
			if old.Kind != item.Kind {
				return State{}, fmt.Errorf("task kind cannot change for %q", item.ID)
			}
			if old.Status == "superseded" && item.Status != "superseded" {
				return State{}, fmt.Errorf("superseded items cannot be revived; create a new item")
			}
			out.Items[pos] = item
		} else {
			positions[item.ID] = len(out.Items)
			out.Items = append(out.Items, item)
		}
	}
	if len(out.Items) > MaxItems {
		return State{}, fmt.Errorf("task state exceeds %d items; start a new session for an unrelated task", MaxItems)
	}
	return out, nil
}
