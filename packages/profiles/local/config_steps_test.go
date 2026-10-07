package local

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadStepBudgetPreservesExplicitUnlimited(t *testing.T) {
	for _, test := range []struct {
		value, want int
		invalid     bool
	}{
		{value: -1, want: -1}, {value: 0, want: 256}, {value: 12, want: 12}, {value: -2, invalid: true},
	} {
		t.Run(fmt.Sprint(test.value), func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(file, []byte(fmt.Sprintf("max_steps = %d\n", test.value)), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := LoadConfig(file)
			if (err != nil) != test.invalid {
				t.Fatalf("config=%+v err=%v", cfg, err)
			}
			if !test.invalid && cfg.MaxSteps != test.want {
				t.Fatalf("max_steps=%d want=%d", cfg.MaxSteps, test.want)
			}
		})
	}
}
