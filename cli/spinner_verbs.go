package cli

import "math/rand"

// Subset of claude-code/src/constants/spinnerVerbs.ts — same playful tone.
var spinnerVerbs = []string{
	"Brewing",
	"Combobulating",
	"Computing",
	"Contemplating",
	"Cooking",
	"Cogitating",
	"Conjuring",
	"Crafting",
	"Crunching",
	"Deliberating",
	"Discombobulating",
	"Doodling",
	"Dreaming",
	"Fermenting",
	"Finagling",
	"Forging",
	"Germinating",
	"Hatching",
	"Ideating",
	"Incubating",
	"Marinating",
	"Moseying",
	"Mulling",
	"Musing",
	"Noodling",
	"Percolating",
	"Pondering",
	"Processing",
	"Puttering",
	"Ruminating",
	"Scheming",
	"Simmering",
	"Sketching",
	"Spinning",
	"Thinking",
	"Tinkering",
	"Vibing",
	"Whirring",
	"Working",
	"Wrangling",
}

func randomSpinnerVerb() string {
	return spinnerVerbs[rand.Intn(len(spinnerVerbs))]
}
