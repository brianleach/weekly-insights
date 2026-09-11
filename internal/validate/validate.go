// Package validate checks extracted facet files against the canonical
// vocabulary before they reach a snapshot.
//
// Extraction subagents write facet files directly, so nothing between the model
// and disk enforces the vocabulary. A single invented label or misspelled enum
// silently skews a week's counts and, worse, breaks comparability with every
// other week. Catching it here means a bad session can be re-extracted instead
// of quietly poisoning the trend.
package validate

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/brianleach/weekly-insights/internal/taxonomy"
)

// Problem is one validation failure, attributed to the file it came from.
type Problem struct {
	File    string
	Message string
}

// Result summarizes a validation pass over a directory.
type Result struct {
	Checked  int
	Problems []Problem
	Fixed    []string // files rewritten when fix was true
	// Unresolved is what is still wrong after a fix pass, re-checked against
	// what is now on disk. It is only populated when fix was true; a dry run
	// reports everything through Problems. Callers exit non-zero when it is
	// non-empty, because "--fix succeeded" and "the files are now valid" are
	// different claims: unparseable JSON, a non-object file and a missing
	// required field have no repair this tool can apply.
	Unresolved []Problem
}

// required lists the fields an extraction must emit. A facet file missing any
// of these cannot be aggregated, so absence is a problem even though we have no
// way to synthesize a value for it.
var required = []string{
	"session_id", "underlying_goal", "goal_categories", "outcome",
	"user_satisfaction_counts", "claude_helpfulness", "session_type",
	"friction_counts", "primary_success", "brief_summary",
}

// countField describes one of the label->count maps, pairing the canonical set
// with the normalizer that maps strays onto it.
type countField struct {
	name  string
	canon []string
	norm  func(string) string
}

var countFields = []countField{
	{"goal_categories", taxonomy.Goals, taxonomy.NormGoal},
	{"friction_counts", taxonomy.Frictions, taxonomy.NormFriction},
	{"user_satisfaction_counts", taxonomy.Satisfactions, taxonomy.NormSatisfaction},
}

// enumField describes one of the single-valued fields, with the fallback used
// when the extracted value is outside the allowed set.
type enumField struct {
	name     string
	allowed  []string
	fallback string
}

var enumFields = []enumField{
	{"outcome", taxonomy.Outcomes, "unclear_from_transcript"},
	{"claude_helpfulness", taxonomy.Helpfulness, "very_helpful"},
	{"session_type", taxonomy.SessionTypes, "multi_task"},
	{"primary_success", taxonomy.Successes, "none"},
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// Dir validates every *.json in dir. When fix is true, off-vocabulary labels
// are normalized in place and the file rewritten.
func Dir(dir string, fix bool) (Result, error) {
	matches, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return Result{}, fmt.Errorf("globbing facets in %s: %w", dir, err)
	}
	sort.Strings(matches)

	var res Result
	res.Checked = len(matches)
	for _, path := range matches {
		name := filepath.Base(path)
		problems, changed, err := checkFile(path, fix)
		if err != nil {
			return Result{}, err
		}
		for _, m := range problems {
			res.Problems = append(res.Problems, Problem{File: name, Message: m})
		}
		if changed {
			res.Fixed = append(res.Fixed, name)
		}
		if !fix {
			continue
		}
		// Re-read the file as it now stands. Re-running the same checks is the
		// only honest way to answer "is it valid now?": it covers both the
		// files we rewrote and the ones we could not touch, and it cannot
		// drift from the checks themselves the way a hand-kept list of
		// "fixable problems" would.
		remaining, _, err := checkFile(path, false)
		if err != nil {
			return Result{}, err
		}
		for _, m := range remaining {
			res.Unresolved = append(res.Unresolved, Problem{File: name, Message: m})
		}
	}
	return res, nil
}

// checkFile validates one facet file, rewriting it when fix is true and
// something actually changed. The returned error is reserved for I/O failures
// that the caller cannot work around; bad content is reported as problems.
func checkFile(path string, fix bool) (problems []string, changed bool, err error) {
	b, readErr := os.ReadFile(path)
	if readErr != nil {
		return nil, false, fmt.Errorf("reading facets %s: %w", path, readErr)
	}

	// A generic map rather than model.Facets: the typed maps would coerce the
	// very things we are looking for, silently dropping non-numeric counts and
	// hiding raw keys behind Go's map decoding. It also preserves fields the
	// extractor emitted that this tool does not model, so a fix round-trip
	// cannot quietly delete data.
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.UseNumber() // keep integer counts from becoming float64 on rewrite
	var raw any
	if err := dec.Decode(&raw); err != nil {
		return []string{fmt.Sprintf("unparseable JSON: %v", err)}, false, nil
	}
	f, ok := raw.(map[string]any)
	if !ok {
		return []string{"not a JSON object"}, false, nil
	}

	for _, k := range required {
		if _, present := f[k]; !present {
			problems = append(problems, fmt.Sprintf("missing field: %s", k))
		}
	}

	sid := strings.TrimSuffix(filepath.Base(path), ".json")
	if got, _ := f["session_id"].(string); got != sid {
		problems = append(problems, fmt.Sprintf("session_id mismatch (%v != %s)", f["session_id"], sid))
		if fix {
			f["session_id"] = sid
			changed = true
		}
	}

	for _, cf := range countFields {
		d, isObj := f[cf.name].(map[string]any)
		if !isObj {
			problems = append(problems, fmt.Sprintf("%s is not an object", cf.name))
			continue
		}
		out := map[string]float64{}
		order := []string{}
		for _, k := range sortedKeys(d) {
			n, bad := asCount(d[k])
			if bad != "" {
				// Dropped from the rewritten map: an unusable count cannot be
				// summed, and keeping it would re-trip this check forever.
				problems = append(problems, fmt.Sprintf("%s.%s %s", cf.name, k, bad))
				continue
			}
			c := k
			if !contains(cf.canon, k) {
				c = cf.norm(k)
			}
			if c != k {
				problems = append(problems, fmt.Sprintf("%s: off-vocabulary label '%s' -> '%s'", cf.name, k, c))
			}
			if _, seen := out[c]; !seen {
				order = append(order, c)
			}
			// Sum rather than overwrite: two stray labels can collapse onto the
			// same canonical key, and dropping one would understate the week.
			out[c] += n
		}
		if fix && countsDiffer(d, out) {
			f[cf.name] = numberMap(out, order)
			changed = true
		}
	}

	for _, ef := range enumFields {
		val, isStr := f[ef.name].(string)
		if !isStr || !contains(ef.allowed, val) {
			problems = append(problems, fmt.Sprintf("%s: invalid value '%v'", ef.name, f[ef.name]))
			if fix {
				f[ef.name] = taxonomy.NormEnum(val, ef.allowed, ef.fallback)
				changed = true
			}
		}
	}

	if changed {
		if err := writeJSON(path, f); err != nil {
			return problems, false, err
		}
	}
	return problems, changed, nil
}

func sortedKeys(m map[string]any) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// asCount accepts the shapes a valid count can take and rejects everything
// else with a reason phrase, or "" when the value is good.
//
// Booleans and strings are rejected even when they look numeric: "3" in a
// count field means the extractor emitted the wrong type. Fractions and
// negatives are rejected because these maps are summed into map[string]int
// downstream, where 1.5 fails to decode and -1 would silently cancel out a
// real event. All three are dropped rather than coerced, the same treatment
// non-numeric counts have always had: guessing at 1 or 2 for "1.5" invents a
// number the extractor never wrote.
func asCount(v any) (n float64, bad string) {
	switch x := v.(type) {
	case json.Number:
		f, err := x.Float64()
		if err != nil {
			return 0, "is not a number"
		}
		return checkCount(f)
	case float64:
		return checkCount(x)
	case int:
		return checkCount(float64(x))
	default:
		return 0, "is not a number"
	}
}

func checkCount(f float64) (float64, string) {
	switch {
	case math.IsNaN(f) || math.IsInf(f, 0):
		return 0, "is not a number"
	case f != math.Trunc(f):
		return 0, fmt.Sprintf("is not a whole number (%v)", f)
	case f < 0:
		return 0, fmt.Sprintf("is negative (%v)", f)
	default:
		return f, ""
	}
}

// countsDiffer reports whether the normalized map is materially different from
// what was on disk, so an already-clean file is never rewritten.
func countsDiffer(orig map[string]any, out map[string]float64) bool {
	if len(orig) != len(out) {
		return true
	}
	for k, v := range orig {
		n, bad := asCount(v)
		if bad != "" {
			return true
		}
		got, present := out[k]
		if !present || got != n {
			return true
		}
	}
	return false
}

// numberMap renders summed counts back as JSON numbers, keeping whole numbers
// whole so a rewrite does not turn 3 into 3.0.
func numberMap(out map[string]float64, order []string) map[string]any {
	m := make(map[string]any, len(out))
	for _, k := range order {
		m[k] = json.Number(strconv.FormatFloat(out[k], 'f', -1, 64))
	}
	return m
}

// writeJSON rewrites a facet file. HTML escaping is disabled because summaries
// and corrections are free text: the default encoder would rewrite a literal
// "&" or "<" in prose that the extractor never wrote that way.
func writeJSON(path string, v any) error {
	var sb strings.Builder
	enc := json.NewEncoder(&sb)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return fmt.Errorf("encoding %s: %w", path, err)
	}
	if err := os.WriteFile(path, []byte(sb.String()), 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}
