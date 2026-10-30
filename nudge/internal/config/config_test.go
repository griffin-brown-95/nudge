package config

import (
	"strings"
	"testing"
)

func TestLoadShippedConfig(t *testing.T) {
	c, err := Load("../../experiments.yaml")
	if err != nil {
		t.Fatalf("shipped experiments.yaml is invalid: %v", err)
	}
	if len(c.ByScope(ScopeOption)) == 0 || len(c.ByScope(ScopeTrial)) == 0 || len(c.ByScope(ScopeVisitor)) == 0 {
		t.Errorf("expected factors in every scope, got %+v", c.Factors)
	}
}

const validTail = `
  - name: color
    scope: option
    salt: s1
    levels: [red, blue]
`

func TestParseInvalid(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr []string // substrings that must all appear
	}{
		{
			name:    "zero version",
			yaml:    "version: 0\nblock_size: 10\nfactors:" + validTail,
			wantErr: []string{"version must be > 0"},
		},
		{
			name:    "zero block size",
			yaml:    "version: 1\nblock_size: 0\nfactors:" + validTail,
			wantErr: []string{"block_size must be > 0"},
		},
		{
			name:    "no factors",
			yaml:    "version: 1\nblock_size: 10\nfactors: []\n",
			wantErr: []string{"at least one factor"},
		},
		{
			name: "duplicate factor",
			yaml: "version: 1\nblock_size: 10\nfactors:" + validTail + `
  - name: color
    scope: trial
    salt: s2
    levels: [a, b]
`,
			wantErr: []string{`factor "color": duplicate factor name`},
		},
		{
			name: "empty levels",
			yaml: `version: 1
block_size: 10
factors:
  - name: color
    scope: option
    salt: s1
    levels: []
`,
			wantErr: []string{"needs at least 2 levels, got 0"},
		},
		{
			name: "single level",
			yaml: `version: 1
block_size: 10
factors:
  - name: color
    scope: option
    salt: s1
    levels: [red]
`,
			wantErr: []string{"needs at least 2 levels, got 1"},
		},
		{
			name: "missing salt",
			yaml: `version: 1
block_size: 10
factors:
  - name: color
    scope: option
    levels: [red, blue]
`,
			wantErr: []string{"missing salt"},
		},
		{
			name: "shared salt",
			yaml: "version: 1\nblock_size: 10\nfactors:" + validTail + `
  - name: size
    scope: option
    salt: s1
    levels: [base, plus8]
`,
			wantErr: []string{`salt is already used by factor "color"`},
		},
		{
			name: "duplicate level",
			yaml: `version: 1
block_size: 10
factors:
  - name: color
    scope: option
    salt: s1
    levels: [red, red]
`,
			wantErr: []string{`duplicate level "red"`},
		},
		{
			name: "bad scope",
			yaml: `version: 1
block_size: 10
factors:
  - name: color
    scope: page
    salt: s1
    levels: [red, blue]
`,
			wantErr: []string{`unknown scope "page"`},
		},
		{
			name: "missing scope and name",
			yaml: `version: 1
block_size: 10
factors:
  - salt: s1
    levels: [red, blue]
`,
			wantErr: []string{"factors[0]: missing name", "missing scope"},
		},
		{
			name: "name not a safe identifier",
			yaml: `version: 1
block_size: 10
factors:
  - name: "Color; DROP TABLE"
    scope: option
    salt: s1
    levels: [red, blue]
`,
			wantErr: []string{"name must be lower_snake_case"},
		},
		{
			name: "level not a safe identifier",
			yaml: `version: 1
block_size: 10
factors:
  - name: color
    scope: option
    salt: s1
    levels: [Red, blue]
`,
			wantErr: []string{`level "Red" must be lower_snake_case`},
		},
		{
			name:    "unknown key (typo)",
			yaml:    "version: 1\nblock_size: 10\nfactorz: []\n",
			wantErr: []string{"field factorz not found"},
		},
		{
			name:    "not yaml",
			yaml:    "version: [1\n",
			wantErr: []string{"decode"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, err := Parse([]byte(tc.yaml))
			if err == nil {
				t.Fatalf("expected error, got config %+v", c)
			}
			for _, want := range tc.wantErr {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not mention %q", err, want)
				}
			}
		})
	}
}

func TestValidateReportsAllProblems(t *testing.T) {
	_, err := Parse([]byte(`version: 0
block_size: 0
factors:
  - name: color
    scope: option
    levels: [red]
`))
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"version", "block_size", "missing salt", "at least 2 levels"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}
