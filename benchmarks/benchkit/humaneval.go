package benchkit

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type HumanEvalOptions struct {
	Limit int
}

type humanEvalProblem struct {
	TaskID            string `json:"task_id"`
	Prompt            string `json:"prompt"`
	CanonicalSolution string `json:"canonical_solution"`
	Test              string `json:"test"`
	EntryPoint        string `json:"entry_point"`
}

func LoadHumanEvalSuite(path string, opts HumanEvalOptions) (Suite, error) {
	problems, err := readHumanEvalProblems(path, opts.Limit)
	if err != nil {
		return Suite{}, err
	}
	cases := make([]Case, 0, len(problems))
	for _, p := range problems {
		id := safeName(p.TaskID)
		cases = append(cases, Case{
			ID:       id,
			Category: "code-generation",
			Title:    p.TaskID,
			Prompt: strings.TrimSpace(fmt.Sprintf(`Implement the HumanEval task in solution.py.

Task ID: %s
Entry point: %s

Only edit solution.py. Keep the function name and signature compatible with the prompt. Verify by running python3 check.py.
`, p.TaskID, p.EntryPoint)),
			Files: map[string]string{
				"solution.py": humanEvalSolutionStub(p),
				"check.py":    humanEvalCheckFile(p),
			},
			TimeoutSeconds: 300,
			Graders: []Grader{{
				Type:           "command",
				Command:        []string{"python3", "check.py"},
				TimeoutSeconds: 30,
			}},
			Metadata: map[string]string{
				"source":      filepath.Base(path),
				"task_id":     p.TaskID,
				"entry_point": p.EntryPoint,
			},
		})
	}
	return Suite{
		Name:        "humaneval-compatible",
		Description: "HumanEval-compatible local execution suite generated from official-format JSONL.",
		Cases:       cases,
	}, nil
}

func readHumanEvalProblems(path string, limit int) ([]humanEvalProblem, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var reader io.Reader = file
	if strings.HasSuffix(path, ".gz") {
		gz, err := gzip.NewReader(file)
		if err != nil {
			return nil, err
		}
		defer gz.Close()
		reader = gz
	}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	var problems []humanEvalProblem
	for scanner.Scan() {
		if strings.TrimSpace(scanner.Text()) == "" {
			continue
		}
		var p humanEvalProblem
		if err := json.Unmarshal(scanner.Bytes(), &p); err != nil {
			return nil, err
		}
		if p.TaskID == "" || p.Prompt == "" || p.Test == "" || p.EntryPoint == "" {
			return nil, fmt.Errorf("invalid HumanEval problem: task_id, prompt, test, and entry_point are required")
		}
		problems = append(problems, p)
		if limit > 0 && len(problems) >= limit {
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(problems) == 0 {
		return nil, fmt.Errorf("no HumanEval problems found in %s", path)
	}
	return problems, nil
}

func humanEvalSolutionStub(p humanEvalProblem) string {
	prompt := strings.TrimRight(p.Prompt, "\n")
	return prompt + "\n    pass\n"
}

func humanEvalCheckFile(p humanEvalProblem) string {
	return strings.TrimRight(p.Test, "\n") + fmt.Sprintf(`

from solution import %s

if __name__ == "__main__":
    check(%s)
`, p.EntryPoint, p.EntryPoint)
}
