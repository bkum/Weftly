package schema

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// The declared type of an input. Omitting `type:` means InputString, so
// every workflow written before this system existed keeps its meaning.
const (
	InputEnum     InputType = "enum"
	InputInt      InputType = "int"
	InputDuration InputType = "duration"
	InputJSON     InputType = "json"
	InputPath     InputType = "path"
	InputList     InputType = "list"
)

// KnownInputTypes is the closed set, used for validation diagnostics.
var KnownInputTypes = []InputType{
	InputString, InputInt, InputNumber, InputBool,
	InputEnum, InputDuration, InputJSON, InputPath, InputList,
}

// Preset is a named bundle of input values. Presets are validated against
// the input schema at compile time — the whole point is that a typo like
// `domain: retial` fails in review, not in front of a customer.
type Preset struct {
	Description string         `yaml:"description" json:"description,omitempty"`
	Values      map[string]any `yaml:"values"      json:"values,omitempty"`
}

// InputError is one problem with one input. Collected rather than
// returned one-at-a-time so a form with three bad fields produces three
// errors in a single report.
type InputError struct {
	Input   string   `json:"input"`
	Message string   `json:"message"`
	Hint    string   `json:"hint,omitempty"`    // did-you-mean
	Allowed []string `json:"allowed,omitempty"` // enum values
	Line    int      `json:"line,omitempty"`
}

func (e InputError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "input %q — %s", e.Input, e.Message)
	if e.Hint != "" {
		fmt.Fprintf(&b, "\n  did you mean: %s?", e.Hint)
	}
	if len(e.Allowed) > 0 {
		fmt.Fprintf(&b, "\n  valid values: %s", strings.Join(e.Allowed, ", "))
	}
	return b.String()
}

// InputErrors is the multi-error returned by input resolution.
type InputErrors []InputError

func (es InputErrors) Error() string {
	parts := make([]string, len(es))
	for i, e := range es {
		parts[i] = "error: " + e.Error()
	}
	return strings.Join(parts, "\n\n")
}

// ExitCode matches schema.Errors so the CLI treats a bad input the same
// way it treats a bad workflow: a usage problem, not a run failure.
func (es InputErrors) ExitCode() int { return 2 }

// EffectiveType returns the declared type, defaulting to string.
func (in Input) EffectiveType() InputType {
	if in.Type == "" {
		return InputString
	}
	return in.Type
}

// AllowedValues merges the newer `values:` (the spec's enum vocabulary)
// with the older `enum:` field, which shipped first and is still used by
// existing workflows and by the SPA's picklist. Either spelling works;
// `values:` wins when both are present.
func (in Input) AllowedValues() []string {
	if len(in.Values) > 0 {
		return in.Values
	}
	out := make([]string, 0, len(in.Enum))
	for _, v := range in.Enum {
		out = append(out, fmt.Sprintf("%v", v))
	}
	return out
}

// Coerce converts a raw supplied value (a string from the CLI, or an
// already-typed value from JSON/YAML) into the input's declared type,
// then checks its constraints. Returns the typed value.
//
// Coercion is strict on purpose: accepting "3.0" for an int is how
// `cases: 2500.7` silently becomes 2500 and someone loses an afternoon.
func (in Input) Coerce(name string, raw any) (any, *InputError) {
	fail := func(msg string) (any, *InputError) {
		e := &InputError{Input: name, Message: msg}
		// Never echo a secret's value, at any length. Report the
		// constraint and nothing else.
		if in.Secret {
			e.Message = redactValue(msg)
		}
		return nil, e
	}
	// An input with NO declared type that receives a structured value
	// passes through unchanged. `type:` omitted means "untyped", not
	// "string" — stringifying here would turn a `default: [a, b, c]`
	// into "[a b c]" and break `for-each:` over it. An input declared
	// `type: string` explicitly does get stringified.
	if in.Type == "" {
		switch raw.(type) {
		case []any, []string, map[string]any:
			return raw, nil
		}
	}
	t := in.EffectiveType()
	switch t {
	case InputString:
		s := stringify(raw)
		// `enum:` predates `type: enum` and was enforced on untyped
		// inputs. Declaring an allowed set means it is enforced, whatever
		// the declared type — silently ignoring it on a `type: string`
		// input would quietly drop a constraint existing workflows rely
		// on.
		if len(in.AllowedValues()) > 0 {
			return in.checkChoice(name, s)
		}
		if in.Pattern != "" {
			re, err := regexp.Compile(in.Pattern)
			if err != nil {
				return fail(fmt.Sprintf("declared pattern is not a valid regexp: %v", err))
			}
			if !re.MatchString(s) {
				return fail(fmt.Sprintf("value %q does not match pattern %s", s, in.Pattern))
			}
		}
		if in.MinLen != nil && len(s) < *in.MinLen {
			return fail(fmt.Sprintf("fails min_length (%d)", *in.MinLen))
		}
		if in.MaxLen != nil && len(s) > *in.MaxLen {
			return fail(fmt.Sprintf("fails max_length (%d)", *in.MaxLen))
		}
		return s, nil

	case InputInt:
		n, ok := toInt(raw)
		if !ok {
			return fail(fmt.Sprintf("value %q is not an integer", stringify(raw)))
		}
		if len(in.AllowedValues()) > 0 {
			if _, cerr := in.checkChoice(name, stringify(n)); cerr != nil {
				return nil, cerr
			}
		}
		if in.Min != nil && float64(n) < *in.Min {
			return fail(fmt.Sprintf("%d is below the minimum (%s)", n, trimFloat(*in.Min)))
		}
		if in.Max != nil && float64(n) > *in.Max {
			return fail(fmt.Sprintf("%d is above the maximum (%s)", n, trimFloat(*in.Max)))
		}
		return n, nil

	case InputNumber:
		f, ok := toFloat(raw)
		if !ok {
			return fail(fmt.Sprintf("value %q is not a number", stringify(raw)))
		}
		if in.Min != nil && f < *in.Min {
			return fail(fmt.Sprintf("%s is below the minimum (%s)", trimFloat(f), trimFloat(*in.Min)))
		}
		if in.Max != nil && f > *in.Max {
			return fail(fmt.Sprintf("%s is above the maximum (%s)", trimFloat(f), trimFloat(*in.Max)))
		}
		return f, nil

	case InputBool:
		b, ok := toBool(raw)
		if !ok {
			return fail(fmt.Sprintf("value %q is not a boolean (accepted: true/false, 1/0, yes/no, on/off)", stringify(raw)))
		}
		return b, nil

	case InputEnum:
		return in.checkChoice(name, stringify(raw))

	case InputDuration:
		switch v := raw.(type) {
		case time.Duration:
			return v, nil
		}
		d, err := time.ParseDuration(stringify(raw))
		if err != nil {
			return fail(fmt.Sprintf("value %q is not a duration (e.g. 30s, 5m, 1h30m)", stringify(raw)))
		}
		if in.Min != nil && float64(d) < *in.Min {
			return fail(fmt.Sprintf("%s is below the minimum (%s)", d, time.Duration(*in.Min)))
		}
		if in.Max != nil && float64(d) > *in.Max {
			return fail(fmt.Sprintf("%s is above the maximum (%s)", d, time.Duration(*in.Max)))
		}
		return d, nil

	case InputJSON:
		// Already-structured values (from the API or an input file)
		// pass through; strings are parsed.
		switch raw.(type) {
		case map[string]any, []any:
			return raw, nil
		}
		var out any
		if err := json.Unmarshal([]byte(stringify(raw)), &out); err != nil {
			return fail(fmt.Sprintf("value is not valid JSON: %v", err))
		}
		return out, nil

	case InputPath:
		// Traversal checking happens in the engine, which knows the
		// workspace. Here we only normalise to a string.
		return stringify(raw), nil

	case InputList:
		items, ierr := in.coerceList(name, raw)
		if ierr != nil {
			return nil, ierr
		}
		if in.MinItems != nil && len(items) < *in.MinItems {
			return fail(fmt.Sprintf("has %d items, below min_items (%d)", len(items), *in.MinItems))
		}
		if in.MaxItems != nil && len(items) > *in.MaxItems {
			return fail(fmt.Sprintf("has %d items, above max_items (%d)", len(items), *in.MaxItems))
		}
		return items, nil
	}
	return raw, nil
}

// coerceList normalises a raw value into a slice and coerces each element
// to the declared `items:` type. Accepts a real slice, a JSON array, or a
// comma-separated string (the CLI form).
func (in Input) coerceList(name string, raw any) ([]any, *InputError) {
	var elems []any
	switch v := raw.(type) {
	case []any:
		elems = v
	case []string:
		for _, s := range v {
			elems = append(elems, s)
		}
	case string:
		s := strings.TrimSpace(v)
		if strings.HasPrefix(s, "[") {
			if err := json.Unmarshal([]byte(s), &elems); err != nil {
				return nil, &InputError{Input: name, Message: fmt.Sprintf("value is not a valid JSON array: %v", err)}
			}
		} else if s == "" {
			elems = nil
		} else {
			for _, part := range strings.Split(s, ",") {
				elems = append(elems, strings.TrimSpace(part))
			}
		}
	default:
		return nil, &InputError{Input: name, Message: fmt.Sprintf("value %q is not a list", stringify(raw))}
	}
	// Each element is coerced through a synthetic scalar input carrying
	// the element type and the shared value/range constraints, so
	// `items: enum` gets the same did-you-mean treatment as a scalar enum.
	elemIn := Input{
		Type:   in.Items,
		Values: in.Values,
		Enum:   in.Enum,
		Secret: in.Secret,
		Min:    in.Min,
		Max:    in.Max,
	}
	if elemIn.Type == "" {
		elemIn.Type = InputString
	}
	out := make([]any, 0, len(elems))
	for i, e := range elems {
		v, ierr := elemIn.Coerce(fmt.Sprintf("%s[%d]", name, i), e)
		if ierr != nil {
			return nil, ierr
		}
		out = append(out, v)
	}
	return out, nil
}

// EnvString renders a resolved input value for injection into a `run`
// step's environment, where everything is a string.
//
// Scalars use their canonical form; json and list become COMPACT JSON so
// the script can pipe them straight into jq. This is the seam where
// authors expect a shell array and get a JSON string, so it is defined
// rather than left to fmt's default.
func EnvString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case float64:
		return trimFloat(t)
	case time.Duration:
		return t.String()
	case []any, map[string]any, []string:
		b, err := json.Marshal(t)
		if err == nil {
			return string(b)
		}
	}
	return fmt.Sprintf("%v", v)
}

// --- coercion helpers -------------------------------------------------

func stringify(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return trimFloat(t)
	case nil:
		return ""
	}
	return fmt.Sprintf("%v", v)
}

// trimFloat renders a float without a trailing ".0" so an int-valued
// number stringifies as "42" rather than "42.000000".
func trimFloat(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// toInt is strict: "3.0", "3abc", "1e3", and "" are all rejected. A
// float that happens to be integral is accepted only when it arrived as
// a real number from JSON/YAML (where 3 unmarshals as float64) — a
// STRING "3.0" is not.
func toInt(raw any) (int, bool) {
	switch v := raw.(type) {
	case int:
		return v, true
	case int64:
		return int(v), true
	case float64:
		if v == float64(int(v)) {
			return int(v), true
		}
		return 0, false
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return 0, false
		}
		return n, true
	}
	return 0, false
}

func toFloat(raw any) (float64, bool) {
	switch v := raw.(type) {
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case float64:
		return v, true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			return 0, false
		}
		return f, true
	}
	return 0, false
}

// toBool accepts the spellings an operator actually types.
func toBool(raw any) (bool, bool) {
	switch v := raw.(type) {
	case bool:
		return v, true
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "true", "1", "yes", "on":
			return true, true
		case "false", "0", "no", "off":
			return false, true
		}
	}
	return false, false
}

// redactValue strips anything that looks like a quoted value from a
// message destined for a secret input's error. Belt and braces on top of
// the per-branch handling above.
var quoted = regexp.MustCompile(`"[^"]*"`)

func redactValue(msg string) string { return quoted.ReplaceAllString(msg, "value") }

// didYouMean returns the closest candidate within edit distance 2.
func didYouMean(got string, candidates []string) string {
	best, bestD := "", 3
	sorted := append([]string(nil), candidates...)
	sort.Strings(sorted)
	for _, c := range sorted {
		if d := levenshtein(strings.ToLower(got), strings.ToLower(c)); d < bestD {
			best, bestD = c, d
		}
	}
	return best
}

func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min3(cur[j-1]+1, prev[j]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}

func min3(a, b, c int) int {
	if b < a {
		a = b
	}
	if c < a {
		a = c
	}
	return a
}

// UnmarshalYAML gives Input two things the default decoder can't:
//
//  1. HasDefault — yaml.v3 gives nil for both "absent" and "default:
//     null", and a plain zero-check would also swallow `default: false`
//     and `default: 0`, which are meaningful values.
//  2. Type-aware min/max — `max: 10m` on a duration and `max: 50000` on
//     an int are both valid but parse differently, and the type isn't
//     known until the mapping has been read.
func (in *Input) UnmarshalYAML(node *yaml.Node) error {
	type plain Input // shed the method set to avoid infinite recursion
	var p plain
	if err := node.Decode(&p); err != nil {
		return err
	}
	*in = Input(p)
	in.Line = node.Line

	for i := 0; i+1 < len(node.Content); i += 2 {
		key, val := node.Content[i].Value, node.Content[i+1]
		switch key {
		case "default":
			in.HasDefault = true
		case "min":
			in.rawMin = val.Value
		case "max":
			in.rawMax = val.Value
		}
	}
	// Resolve min/max now that Type is known. A malformed bound is left
	// nil here and reported by Validate, which can name the file and
	// line — returning an error from UnmarshalYAML would abort the whole
	// parse and lose the other diagnostics.
	if in.rawMin != "" {
		if f, ok := parseBound(in.rawMin, in.EffectiveType()); ok {
			in.Min = &f
		}
	}
	if in.rawMax != "" {
		if f, ok := parseBound(in.rawMax, in.EffectiveType()); ok {
			in.Max = &f
		}
	}
	return nil
}

// parseBound reads a min/max scalar in the units its input type implies:
// a duration bound is written as `10m` and stored as nanoseconds, every
// other numeric bound is a plain number.
func parseBound(raw string, t InputType) (float64, bool) {
	if t == InputDuration {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return 0, false
		}
		return float64(d), true
	}
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, false
	}
	return f, true
}

// RawBounds exposes the un-parsed min/max scalars so Validate can report
// a malformed bound against the text the author actually wrote.
func (in Input) RawBounds() (string, string) { return in.rawMin, in.rawMax }

// ValidateInputSchema implements the compile-time rules of the typed-input
// spec §7.1, rules 1–7: malformed declarations, constraints that don't
// apply to their type, inverted bounds, and defaults that violate their
// own declaration.
func ValidateInputSchema(wf *Workflow) Errors {
	var errs Errors
	for name, in := range wf.Inputs {
		path := "inputs." + name
		t := in.EffectiveType()
		if !isKnownType(t) {
			errs = append(errs, Error{Line: in.Line, Path: path + ".type",
				Message: fmt.Sprintf("unknown type %q (known: %s)", t, joinTypes(KnownInputTypes))})
			continue
		}
		// 1. enum without values
		if t == InputEnum && len(in.AllowedValues()) == 0 {
			errs = append(errs, Error{Line: in.Line, Path: path,
				Message: "type: enum requires a non-empty `values:` list"})
		}
		// 2. list without items
		if t == InputList && in.Items == "" {
			errs = append(errs, Error{Line: in.Line, Path: path,
				Message: "type: list requires `items:` naming the element type"})
		}
		if t == InputList && in.Items != "" && !isKnownType(in.Items) {
			errs = append(errs, Error{Line: in.Line, Path: path + ".items",
				Message: fmt.Sprintf("unknown item type %q", in.Items)})
		}
		// 3. constraints that don't apply to the declared type
		errs = append(errs, irrelevantConstraints(path, in, t)...)
		// Malformed bound scalars (parseBound gave up during unmarshal).
		rawMin, rawMax := in.RawBounds()
		if rawMin != "" && in.Min == nil {
			errs = append(errs, Error{Line: in.Line, Path: path + ".min",
				Message: fmt.Sprintf("%q is not valid for type %s", rawMin, t)})
		}
		if rawMax != "" && in.Max == nil {
			errs = append(errs, Error{Line: in.Line, Path: path + ".max",
				Message: fmt.Sprintf("%q is not valid for type %s", rawMax, t)})
		}
		// 4. inverted bounds
		if in.Min != nil && in.Max != nil && *in.Min > *in.Max {
			errs = append(errs, Error{Line: in.Line, Path: path, Message: "min is greater than max"})
		}
		if in.MinLen != nil && in.MaxLen != nil && *in.MinLen > *in.MaxLen {
			errs = append(errs, Error{Line: in.Line, Path: path, Message: "min_length is greater than max_length"})
		}
		if in.MinItems != nil && in.MaxItems != nil && *in.MinItems > *in.MaxItems {
			errs = append(errs, Error{Line: in.Line, Path: path, Message: "min_items is greater than max_items"})
		}
		// 5. a literal default must satisfy its own declaration. Skip
		//    expression defaults — they can't be evaluated until run time.
		if in.HasDefault && in.Default != nil && !isExprString(in.Default) {
			if _, ierr := in.Coerce(name, in.Default); ierr != nil {
				errs = append(errs, Error{Line: in.Line, Path: path + ".default", Message: ierr.Message})
			}
		}
		// 7. a default expression may not reference steps.* — inputs
		//    resolve before any step has run.
		if s, ok := in.Default.(string); ok && strings.Contains(s, "steps.") && strings.Contains(s, "${{") {
			errs = append(errs, Error{Line: in.Line, Path: path + ".default",
				Message: "default may not reference steps.* — inputs resolve before any step runs"})
		}
	}
	// 6. cycles among default expressions
	if cycle := inputDefaultCycle(wf.Inputs); len(cycle) > 0 {
		errs = append(errs, Error{Path: "inputs",
			Message: "cycle in default expressions: " + strings.Join(cycle, " -> ")})
	}
	errs = append(errs, validatePresets(wf)...)
	return errs
}

// irrelevantConstraints reports constraints declared on a type that has
// no use for them — `min:` on a bool, `pattern:` on an int. Silently
// ignoring them is how an author believes a bound is enforced when it
// isn't.
func irrelevantConstraints(path string, in Input, t InputType) Errors {
	var errs Errors
	bad := func(field string) {
		errs = append(errs, Error{Line: in.Line, Path: path + "." + field,
			Message: fmt.Sprintf("%s does not apply to type %s", field, t)})
	}
	numeric := t == InputInt || t == InputNumber || t == InputDuration
	if in.Min != nil && !numeric {
		bad("min")
	}
	if in.Max != nil && !numeric {
		bad("max")
	}
	if (in.MinLen != nil || in.MaxLen != nil) && t != InputString {
		if in.MinLen != nil {
			bad("min_length")
		}
		if in.MaxLen != nil {
			bad("max_length")
		}
	}
	if in.Pattern != "" && t != InputString {
		bad("pattern")
	}
	if (in.MinItems != nil || in.MaxItems != nil) && t != InputList {
		if in.MinItems != nil {
			bad("min_items")
		}
		if in.MaxItems != nil {
			bad("max_items")
		}
	}
	if in.MustExist && t != InputPath {
		bad("must_exist")
	}
	if in.Items != "" && t != InputList {
		bad("items")
	}
	if len(in.Values) > 0 && t != InputEnum && t != InputList {
		bad("values")
	}
	return errs
}

// validatePresets implements §7.1 rules 8–11.
func validatePresets(wf *Workflow) Errors {
	var errs Errors
	for pname, p := range wf.Presets {
		path := "presets." + pname
		if !idPattern.MatchString(pname) {
			errs = append(errs, Error{Path: path, Message: "preset name must match [a-z0-9_]+"})
		}
		for key, val := range p.Values {
			in, declared := wf.Inputs[key]
			if !declared {
				e := Error{Path: path + "." + key, Message: fmt.Sprintf("%q is not a declared input", key)}
				if sug := didYouMean(key, inputNames(wf.Inputs)); sug != "" {
					e.Message += fmt.Sprintf(" (did you mean %q?)", sug)
				}
				errs = append(errs, e)
				continue
			}
			// A preset may never supply a secret. Presets live in
			// committed YAML that GET /workflows/{id} exposes to every
			// principal permitted the workflow — a secret here is a
			// credential in version control. There is no legitimate
			// use: secrets come from the environment, a vault, or an
			// operator.
			if in.Secret {
				errs = append(errs, Error{Path: path + "." + key,
					Message: "preset may not supply a value for a secret input"})
				continue
			}
			if _, ierr := in.Coerce(key, val); ierr != nil {
				errs = append(errs, Error{Path: path + "." + key, Message: ierr.Message})
			}
		}
	}
	return errs
}

// inputDefaultCycle finds a reference cycle among inputs whose defaults
// are expressions referencing other inputs.
func inputDefaultCycle(inputs map[string]Input) []string {
	deps := map[string][]string{}
	for name, in := range inputs {
		s, ok := in.Default.(string)
		if !ok || !strings.Contains(s, "${{") {
			continue
		}
		for other := range inputs {
			if other != name && referencesInput(s, other) {
				deps[name] = append(deps[name], other)
			}
		}
	}
	const (
		white = 0
		grey  = 1
		black = 2
	)
	color := map[string]int{}
	var stack []string
	var visit func(string) []string
	visit = func(n string) []string {
		if color[n] == grey {
			for i, s := range stack {
				if s == n {
					return append(append([]string{}, stack[i:]...), n)
				}
			}
			return []string{n, n}
		}
		if color[n] == black {
			return nil
		}
		color[n] = grey
		stack = append(stack, n)
		for _, d := range deps[n] {
			if c := visit(d); c != nil {
				return c
			}
		}
		stack = stack[:len(stack)-1]
		color[n] = black
		return nil
	}
	names := inputNames(inputs)
	sort.Strings(names)
	for _, n := range names {
		if color[n] == white {
			if c := visit(n); c != nil {
				return c
			}
		}
	}
	return nil
}

// ResolutionOrder topologically sorts inputs so a default expression
// referencing another input is evaluated after that input resolves.
// Assumes ValidateInputSchema already rejected cycles; on an unexpected
// cycle it falls back to alphabetical order rather than looping.
func ResolutionOrder(inputs map[string]Input) []string {
	deps := map[string][]string{}
	for name, in := range inputs {
		s, ok := in.Default.(string)
		if !ok || !strings.Contains(s, "${{") {
			continue
		}
		for other := range inputs {
			if other != name && referencesInput(s, other) {
				deps[name] = append(deps[name], other)
			}
		}
	}
	names := inputNames(inputs)
	sort.Strings(names)
	var out []string
	seen := map[string]bool{}
	var visit func(string, map[string]bool)
	visit = func(n string, path map[string]bool) {
		if seen[n] || path[n] {
			return
		}
		path[n] = true
		ds := append([]string(nil), deps[n]...)
		sort.Strings(ds)
		for _, d := range ds {
			visit(d, path)
		}
		delete(path, n)
		seen[n] = true
		out = append(out, n)
	}
	for _, n := range names {
		visit(n, map[string]bool{})
	}
	return out
}

// referencesInput reports whether an expression mentions inputs.<name>
// as a whole word, so `inputs.transaction` doesn't match `inputs.trans`.
func referencesInput(expr, name string) bool {
	idx := 0
	needle := "inputs." + name
	for {
		i := strings.Index(expr[idx:], needle)
		if i < 0 {
			return false
		}
		end := idx + i + len(needle)
		if end >= len(expr) || !isIdentRune(expr[end]) {
			return true
		}
		idx = end
	}
}

func isIdentRune(b byte) bool {
	return b == '_' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

func isExprString(v any) bool {
	s, ok := v.(string)
	return ok && strings.Contains(s, "${{")
}

func isKnownType(t InputType) bool {
	for _, k := range KnownInputTypes {
		if k == t {
			return true
		}
	}
	return false
}

func joinTypes(ts []InputType) string {
	parts := make([]string, len(ts))
	for i, t := range ts {
		parts[i] = string(t)
	}
	return strings.Join(parts, ", ")
}

func inputNames(inputs map[string]Input) []string {
	out := make([]string, 0, len(inputs))
	for n := range inputs {
		out = append(out, n)
	}
	return out
}

// checkChoice enforces membership in the declared allowed set, with a
// did-you-mean suggestion for near misses.
//
// Suppressed entirely for a secret input: the suggestion and the echoed
// value would together leak the credential a character at a time, which
// is precisely the failure mode the secret flag exists to prevent.
func (in Input) checkChoice(name, s string) (any, *InputError) {
	allowed := in.AllowedValues()
	for _, a := range allowed {
		if a == s {
			return s, nil
		}
	}
	if in.Secret {
		return nil, &InputError{Input: name, Message: "value is not a valid choice"}
	}
	e := &InputError{
		Input:   name,
		Message: fmt.Sprintf("value %q is not a valid choice", s),
		Allowed: allowed,
	}
	if sug := didYouMean(s, allowed); sug != "" {
		e.Hint = sug
	}
	return nil, e
}
