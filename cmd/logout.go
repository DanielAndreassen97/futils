package cmd

import (
	"fmt"
	"strings"

	"github.com/DanielAndreassen97/futils/internal/config"
	"github.com/DanielAndreassen97/futils/internal/fabric"
)

// Logout clears OAuth tokens cached in the OS keychain for every known
// customer plus the "default" profile (used by cmd/fetch-nb when run
// without a customer context). Doesn't delete the config itself — just
// the credentials. Users log back in on their next run.
//
// Keyring delete failures are aggregated and returned rather than swallowed:
// printing "cleared" when a long-lived refresh token actually survived would
// give false credential-hygiene assurance on a shared or handed-off machine.
func Logout(configPath string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("load config for token cleanup: %w", err)
	}

	var failures []string
	if err := fabric.ClearCachedTokens("default"); err != nil {
		failures = append(failures, fmt.Sprintf("default: %v", err))
	}
	for name := range cfg.Customers {
		if err := fabric.ClearCachedTokens(name); err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", name, err))
		}
	}

	if len(failures) > 0 {
		return fmt.Errorf("some tokens may not have been cleared — your credentials may still be in the OS keyring:\n  %s",
			strings.Join(failures, "\n  "))
	}
	fmt.Println("Cached tokens cleared.")
	return nil
}
