package deploy

import (
	"fmt"
	"path"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// StringOrSlice unmarshals a YAML field that may be either a scalar string or
// a sequence of strings (parameter.yml allows both for item_type/item_name/
// file_path). It always presents as a []string.
type StringOrSlice []string

func (s *StringOrSlice) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		*s = StringOrSlice{value.Value}
		return nil
	case yaml.SequenceNode:
		var out []string
		if err := value.Decode(&out); err != nil {
			return err
		}
		*s = out
		return nil
	default:
		return fmt.Errorf("expected string or list, got yaml kind %d", value.Kind)
	}
}

// FindReplace is one literal/regex substitution rule.
type FindReplace struct {
	FindValue    string            `yaml:"find_value"`
	ReplaceValue map[string]string `yaml:"replace_value"`
	IsRegex      string            `yaml:"is_regex"`
	IgnoreCase   string            `yaml:"ignore_case"`
	ItemType     StringOrSlice     `yaml:"item_type"`
	ItemName     StringOrSlice     `yaml:"item_name"`
	FilePath     StringOrSlice     `yaml:"file_path"`
}

// Parameters is the parsed parameter.yml. KeyValueReplace and SparkPool are
// parsed (forward-compat) but not applied until Phase 3.
type Parameters struct {
	FindReplace     []FindReplace    `yaml:"find_replace"`
	KeyValueReplace []map[string]any `yaml:"key_value_replace"`
	SparkPool       []map[string]any `yaml:"spark_pool"`
}

// ApplyFindReplace runs every matching find_replace rule against one file's
// content for the chosen environment. resolve expands dynamic variables (e.g.
// "$items.Lakehouse.X.$id") in replacement values; pass an identity function
// when there are none. filePath is the item-relative path of the file.
func (p Parameters) ApplyFindReplace(env string, item LocalItem, filePath string, content []byte, resolve func(string) (string, error)) ([]byte, error) {
	out := content
	for _, fr := range p.FindReplace {
		if !matchesFilters(fr.ItemType, item.Type) ||
			!matchesFilters(fr.ItemName, item.DisplayName) ||
			!matchesGlob(fr.FilePath, filePath) {
			continue
		}
		repl, ok := replacementFor(fr.ReplaceValue, env)
		if !ok {
			continue
		}
		resolved, err := resolve(repl)
		if err != nil {
			return nil, fmt.Errorf("resolve %q: %w", repl, err)
		}
		if fr.IsRegex == "true" {
			expr := fr.FindValue
			if fr.IgnoreCase == "true" {
				expr = "(?i)" + expr
			}
			re, err := regexp.Compile(expr)
			if err != nil {
				return nil, fmt.Errorf("compile regex %q: %w", fr.FindValue, err)
			}
			out = re.ReplaceAll(out, []byte(resolved))
		} else {
			out = []byte(strings.ReplaceAll(string(out), fr.FindValue, resolved))
		}
	}
	return out, nil
}

// matchesFilters returns true if filter is empty (no constraint) or contains
// value. Used for item_type and item_name (exact match).
func matchesFilters(filter StringOrSlice, value string) bool {
	if len(filter) == 0 {
		return true
	}
	for _, f := range filter {
		if f == value {
			return true
		}
	}
	return false
}

// matchesGlob returns true if filter is empty or any pattern matches path
// (shell glob, via path.Match on the posix file path).
func matchesGlob(filter StringOrSlice, p string) bool {
	if len(filter) == 0 {
		return true
	}
	for _, pat := range filter {
		if ok, _ := path.Match(pat, p); ok {
			return true
		}
	}
	return false
}

// replacementFor picks the environment-specific value, falling back to the
// reserved _ALL_ key (case-insensitive) that applies to every environment.
func replacementFor(m map[string]string, env string) (string, bool) {
	if v, ok := m[env]; ok {
		return v, true
	}
	for k, v := range m {
		if strings.EqualFold(k, "_ALL_") {
			return v, true
		}
	}
	return "", false
}

// ParseParameters parses parameter.yml bytes. Empty input yields an empty
// (no-op) Parameters, since a repo may legitimately have no parameter file.
func ParseParameters(raw []byte) (Parameters, error) {
	var p Parameters
	if len(raw) == 0 {
		return p, nil
	}
	if err := yaml.Unmarshal(raw, &p); err != nil {
		return p, fmt.Errorf("parse parameter.yml: %w", err)
	}
	return p, nil
}
