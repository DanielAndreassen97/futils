package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/DanielAndreassen97/futils/internal/config"
)

// DemoSeed writes everything demo mode needs into dir (default
// /tmp/futils-demo): a config.json with the fictional Fabrikam customer and a
// git repo of Fabric items whose GUIDs match the fake tenant in demo.go. The
// repo gets a local bare "origin" so the deploy flow's fetch-from-origin works
// fully offline. Idempotent: re-seeding overwrites content and re-commits only
// when something changed.
func DemoSeed(dir string) error {
	if dir == "" {
		// A stable, typeable default beats os.TempDir(): on macOS that is a
		// /var/folders/... path nobody can retype from the docs. /tmp exists
		// on every unix; Windows falls back to the real temp dir.
		dir = filepath.Join("/tmp", "futils-demo")
		if runtime.GOOS == "windows" {
			dir = filepath.Join(os.TempDir(), "futils-demo")
		}
	}
	repo := filepath.Join(dir, "fabrikam-repo")
	originDir := filepath.Join(dir, "fabrikam-origin.git")

	for path, content := range demoRepoFiles() {
		full := filepath.Join(repo, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			return err
		}
	}

	git := func(args ...string) error {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git %v: %v\n%s", args, err, out)
		}
		return nil
	}
	if _, err := os.Stat(filepath.Join(repo, ".git")); os.IsNotExist(err) {
		if err := git("init", "-q", "-b", "main"); err != nil {
			return err
		}
	}
	if _, err := os.Stat(originDir); os.IsNotExist(err) {
		cmd := exec.Command("git", "init", "-q", "--bare", originDir)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git init --bare: %v\n%s", err, out)
		}
		if err := git("remote", "add", "origin", originDir); err != nil {
			return err
		}
	}
	if err := git("add", "-A"); err != nil {
		return err
	}
	// Commit only when the tree changed; a no-op re-seed shouldn't fail.
	if err := exec.Command("git", "-C", repo, "diff", "--cached", "--quiet").Run(); err != nil {
		if err := git("-c", "user.email=demo@fabrikam.example", "-c", "user.name=Fabrikam Demo",
			"commit", "-q", "-m", "Seed Fabrikam demo content"); err != nil {
			return err
		}
	}
	if err := git("push", "-q", "origin", "main"); err != nil {
		return err
	}

	cfgPath := filepath.Join(dir, "config.json")
	if err := writeDemoConfig(cfgPath, repo); err != nil {
		return err
	}

	fmt.Println("Seeded demo sandbox:")
	fmt.Println("  config:  " + cfgPath)
	fmt.Println("  repo:    " + repo)
	fmt.Println()
	fmt.Println("Run futils against it with:")
	fmt.Println("  export FUTILS_DEMO=1 FUTILS_CONFIG=" + cfgPath)
	return nil
}

// writeDemoConfig renders the Fabrikam customer: DEV is the baseline, TEST and
// PROD carry folder→workspace deployment mappings matching the seeded repo.
func writeDemoConfig(path, repo string) error {
	mappings := func(env string) []config.DeployMapping {
		return []config.DeployMapping{
			{Folder: "Backend", Workspace: demoConfigWS(env)},
			{Folder: "Frontend", Workspace: demoSemModWS(env)},
		}
	}
	cfg := config.Config{Customers: map[string]config.Customer{
		"Fabrikam": {
			Environments: []config.Environment{
				{Alias: "DEV", Workspaces: []string{demoConfigWS("DEV"), demoSemModWS("DEV")}},
				{Alias: "TEST", Workspaces: []string{demoConfigWS("TEST"), demoSemModWS("TEST")}, Deployments: mappings("TEST")},
				{Alias: "PROD", Workspaces: []string{demoConfigWS("PROD"), demoSemModWS("PROD")}, Deployments: mappings("PROD")},
			},
			RepoPath:            repo,
			BaselineEnvironment: "DEV",
			DeployHistoryPath:   "deploy-history",
			PostDeployRuns:      []string{"nb_ingest_sales"},
			Favorites: []config.NotebookFavorite{
				{Name: "nb_ingest_sales", Parameters: []string{"run_date", "full_reload"}},
			},
		},
	}}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}
