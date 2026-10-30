// Package config loads and validates the experiment definition (experiments.yaml).
//
// Validation is deliberately strict: a malformed config would silently corrupt
// the experiment, so every problem fails startup with a descriptive error.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"regexp"

	"go.yaml.in/yaml/v3"
)

// Scope says how often a factor is re-drawn.
type Scope string

const (
	ScopeOption  Scope = "option"  // independently per option, per trial
	ScopeTrial   Scope = "trial"   // once per trial, shared by both options
	ScopeVisitor Scope = "visitor" // once per visitor
)

type Factor struct {
	Name   string   `yaml:"name"`
	Scope  Scope    `yaml:"scope"`
	Salt   string   `yaml:"salt"`
	Levels []string `yaml:"levels"`
}

type Config struct {
	Version   int      `yaml:"version"`
	BlockSize int      `yaml:"block_size"`
	Factors   []Factor `yaml:"factors"`
}

// Factor names become column names; level names become stored values.
var identRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

// Load reads and validates the config file at path.
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	return Parse(b)
}

// Parse decodes and validates a config. Unknown keys are rejected so typos
// like "salts:" can't silently drop a field.
func Parse(b []byte) (*Config, error) {
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	var c Config
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("config: decode: %w", err)
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

// Validate reports every problem at once rather than the first.
func (c *Config) Validate() error {
	var errs []error
	add := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }

	if c.Version <= 0 {
		add("version must be > 0, got %d", c.Version)
	}
	if c.BlockSize <= 0 {
		add("block_size must be > 0, got %d", c.BlockSize)
	}
	if len(c.Factors) == 0 {
		add("at least one factor is required")
	}

	names := map[string]bool{}
	salts := map[string]string{}
	for i, f := range c.Factors {
		where := fmt.Sprintf("factors[%d]", i)
		if f.Name != "" {
			where = fmt.Sprintf("factor %q", f.Name)
		}

		switch {
		case f.Name == "":
			add("%s: missing name", where)
		case !identRe.MatchString(f.Name):
			add("%s: name must be lower_snake_case", where)
		case names[f.Name]:
			add("%s: duplicate factor name", where)
		}
		names[f.Name] = true

		switch f.Scope {
		case ScopeOption, ScopeTrial, ScopeVisitor:
		case "":
			add("%s: missing scope", where)
		default:
			add("%s: unknown scope %q (want option, trial or visitor)", where, f.Scope)
		}

		// Shared salts would make two factors' assignments correlated.
		switch {
		case f.Salt == "":
			add("%s: missing salt", where)
		case salts[f.Salt] != "":
			add("%s: salt is already used by factor %q", where, salts[f.Salt])
		default:
			salts[f.Salt] = f.Name
		}

		if len(f.Levels) < 2 {
			add("%s: needs at least 2 levels, got %d", where, len(f.Levels))
		}
		seen := map[string]bool{}
		for _, l := range f.Levels {
			switch {
			case !identRe.MatchString(l):
				add("%s: level %q must be lower_snake_case", where, l)
			case seen[l]:
				add("%s: duplicate level %q", where, l)
			}
			seen[l] = true
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("config: invalid experiment definition:\n%w", errors.Join(errs...))
	}
	return nil
}

// Factor returns the named factor.
func (c *Config) Factor(name string) (Factor, bool) {
	for _, f := range c.Factors {
		if f.Name == name {
			return f, true
		}
	}
	return Factor{}, false
}

// ByScope returns the factors with the given scope, in config order.
func (c *Config) ByScope(s Scope) []Factor {
	var out []Factor
	for _, f := range c.Factors {
		if f.Scope == s {
			out = append(out, f)
		}
	}
	return out
}
