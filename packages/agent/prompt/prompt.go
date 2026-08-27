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
	parts := make([]string, 0, len(ordered))
	for _, fragment := range ordered {
		if content := strings.TrimSpace(fragment.Content); content != "" {
			parts = append(parts, content)
		}
	}
	content := strings.Join(parts, "\n\n")
	sum := sha256.Sum256([]byte(content))
	return Composition{Content: content, Fragments: ordered, Hash: hex.EncodeToString(sum[:])}, nil
}
