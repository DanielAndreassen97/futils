package cmd

import (
	"testing"

	"github.com/DanielAndreassen97/futils/internal/config"
)

func TestValidateNewAlias(t *testing.T) {
	existing := []config.Environment{
		{Alias: "DEV", Workspaces: []string{"DW - DEV - Config"}},
		{Alias: "PROD", Workspaces: []string{"DW - PROD - Config"}},
	}

	cases := []struct {
		name    string
		alias   string
		wantErr bool
	}{
		{"empty", "", true},
		{"whitespace only", "   ", true},
		{"duplicate exact match", "DEV", true},
		{"duplicate with different case", "dev", true},
		{"new alias", "feature", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateNewAlias(tc.alias, existing)
			if (err != nil) != tc.wantErr {
				t.Errorf("alias %q: got err=%v, wantErr=%v", tc.alias, err, tc.wantErr)
			}
		})
	}
}
