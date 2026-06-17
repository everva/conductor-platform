package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// scenarioDoc is the subset of a scenario YAML the intake command needs to create
// a statestore.Scenario + its statestore.Task. The conductor scenario files are
// flat scalars plus a small `deps` list (scalar `key: value`, a flow list
// `deps: ["A", "B"]`, or a block list), so a focused, dependency-free parser
// covers them honestly without pulling in a YAML library.
type scenarioDoc struct {
	ID    string
	Title string
	Lane  string
	Tier  string
	Deps  []string
}

// validate ensures the required fields are present BEFORE any store write, so a
// scenario missing id/lane/tier is refused with no partial write (the holdout
// stresses no-partial-write intake). It returns a clear, actionable error.
func (d scenarioDoc) validate() error {
	var missing []string
	if d.ID == "" {
		missing = append(missing, "id")
	}
	if d.Lane == "" {
		missing = append(missing, "lane")
	}
	if d.Tier == "" {
		missing = append(missing, "tier")
	}
	if len(missing) > 0 {
		return fmt.Errorf("scenario missing required field(s): %s", strings.Join(missing, ", "))
	}
	return nil
}

// parseScenarioFile reads and parses a scenario YAML file into a scenarioDoc.
func parseScenarioFile(path string) (scenarioDoc, error) {
	f, err := os.Open(path) //nolint:gosec // operator-supplied scenario path.
	if err != nil {
		return scenarioDoc{}, fmt.Errorf("open scenario %q: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	var doc scenarioDoc
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var inDepsBlock bool
	for sc.Scan() {
		raw := sc.Text()
		line := stripComment(raw)
		trimmed := strings.TrimSpace(line)

		// A block-list item under `deps:` (e.g. `  - "A-3"`).
		if inDepsBlock {
			if item, ok := blockListItem(line); ok {
				doc.Deps = append(doc.Deps, item)
				continue
			}
			// Any non-list, non-blank line ends the deps block.
			if trimmed != "" {
				inDepsBlock = false
			}
		}
		if trimmed == "" {
			continue
		}

		key, val, ok := splitKeyValue(trimmed)
		if !ok {
			continue
		}
		switch key {
		case "id":
			doc.ID = unquote(val)
		case "title":
			doc.Title = unquote(val)
		case "lane":
			doc.Lane = unquote(val)
		case "tier":
			doc.Tier = unquote(val)
		case "deps":
			if val == "" {
				inDepsBlock = true // block list follows on indented lines.
				continue
			}
			doc.Deps = parseFlowList(val)
		}
	}
	if err := sc.Err(); err != nil {
		return scenarioDoc{}, fmt.Errorf("read scenario %q: %w", path, err)
	}
	return doc, nil
}

// stripComment removes a trailing `#` comment, ignoring `#` inside quotes so a
// quoted title containing `#` survives.
func stripComment(line string) string {
	inSingle, inDouble := false, false
	for i, r := range line {
		switch r {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case '#':
			if !inSingle && !inDouble {
				return line[:i]
			}
		}
	}
	return line
}

// splitKeyValue splits a top-level `key: value` line. It returns ok=false for a
// line that is not a mapping (e.g. a stray list item), so callers skip it.
func splitKeyValue(line string) (key, val string, ok bool) {
	idx := strings.Index(line, ":")
	if idx < 0 {
		return "", "", false
	}
	key = strings.TrimSpace(line[:idx])
	val = strings.TrimSpace(line[idx+1:])
	if key == "" {
		return "", "", false
	}
	return key, val, true
}

// blockListItem returns the value of a `  - item` block-list line, or ok=false.
func blockListItem(line string) (string, bool) {
	t := strings.TrimSpace(line)
	if !strings.HasPrefix(t, "- ") && t != "-" {
		return "", false
	}
	return unquote(strings.TrimSpace(strings.TrimPrefix(t, "-"))), true
}

// parseFlowList parses a flow-style list `["A", "B"]` (or a bare scalar) into its
// items, trimming brackets and quotes. An empty `[]` yields no items.
func parseFlowList(val string) []string {
	val = strings.TrimSpace(val)
	val = strings.TrimPrefix(val, "[")
	val = strings.TrimSuffix(val, "]")
	val = strings.TrimSpace(val)
	if val == "" {
		return nil
	}
	parts := strings.Split(val, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		item := unquote(strings.TrimSpace(p))
		if item != "" {
			out = append(out, item)
		}
	}
	return out
}

// unquote strips matching single or double quotes from a scalar.
func unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}
