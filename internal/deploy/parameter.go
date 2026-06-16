package deploy

import (
	"fmt"

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
