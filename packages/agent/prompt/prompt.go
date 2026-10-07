// Package prompt composes versioned prompt fragments deterministically.
package prompt

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

type Layer int

const (
	LayerCore Layer = iota + 1
	LayerProduct
	LayerEnvironment
	LayerProject
	LayerUser
	LayerSkill
	LayerTurn
)

type Fragment struct {
	ID      string `json:"id"`
	Version string `json:"version,omitempty"`
	Layer   Layer  `json:"layer"`
	Content string `json:"content"`
	Order   int    `json:"order,omitempty"`
}

type Composition struct {
	Content   string     `json:"content"`
	Fragments []Fragment `json:"fragments"`
	Hash      string     `json:"hash"`
}

func Compose(fragments []Fragment) (Composition, error) {
	ordered := append([]Fragment(nil), fragments...)
	for _, fragment := range ordered {
		if fragment.ID == "" {
			return Composition{}, fmt.Errorf("prompt fragment id is required")
		}
		if fragment.Layer < LayerCore || fragment.Layer > LayerTurn {
			return Composition{}, fmt.Errorf("prompt fragment %q has invalid layer", fragment.ID)
		}
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Layer != ordered[j].Layer {
			return ordered[i].Layer < ordered[j].Layer
		}
		if ordered[i].Order != ordered[j].Order {
			return ordered[i].Order < ordered[j].Order
		}
		return ordered[i].ID < ordered[j].ID
	})
	// Only the static core, product, and environment layers belong above the
	// cache breakpoint. Project, user, skill, and turn-specific instructions
	// sit below the boundary so multi-turn and multi-user requests maximize
	// prefix cache reuse on providers such as Anthropic.
	staticParts := make([]string, 0, len(ordered))
	dynamicParts := make([]string, 0, len(ordered))
	for _, fragment := range ordered {
		content := strings.TrimSpace(fragment.Content)
		if content == "" {
			continue
		}
		if fragment.Layer <= LayerEnvironment {
			staticParts = append(staticParts, content)
		} else {
			dynamicParts = append(dynamicParts, content)
		}
	}
	var content string
	switch {
	case len(staticParts) > 0 && len(dynamicParts) > 0:
		content = strings.Join(staticParts, "\n\n") + "\n\n---DYNAMIC_CONTEXT_BELOW---\n\n" + strings.Join(dynamicParts, "\n\n")
	case len(staticParts) > 0:
		content = strings.Join(staticParts, "\n\n")
	default:
		content = strings.Join(dynamicParts, "\n\n")
	}
	sum := sha256.Sum256([]byte(content))
	return Composition{Content: content, Fragments: ordered, Hash: hex.EncodeToString(sum[:])}, nil
}
