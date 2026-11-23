package tests

import (
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const skillRel = "plugin/skills/checking-promtool-alert-tests/SKILL.md"

// TestInstalledPlugin copies plugin/ out of this module, the way Claude Code
// installs it, and builds it with the command the skill uses. The plugin
// has no go.mod, so it must build from its .go files alone.
func TestInstalledPlugin(t *testing.T) {
	const buildLine = `go build -o "${TMPDIR:-/tmp}/neverfired" $(ls "${CLAUDE_PLUGIN_ROOT}"/scripts/*.go | grep -v _test.go)`
	if !strings.Contains(string(readFile(t, skillRel)), buildLine) {
		t.Fatalf("SKILL.md doesn't contain the build line this test runs:\n%s", buildLine)
	}
	if runtime.GOOS == "windows" {
		t.Skip("the skill's build line is for a POSIX shell")
	}
	dir := t.TempDir()
	pluginRoot := filepath.Join(dir, "neverfired")
	copyTree(t, filepath.Join(root, "plugin"), pluginRoot)
	out := filepath.Join(dir, "tmp")
	if err := os.Mkdir(out, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "-c", buildLine)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "CLAUDE_PLUGIN_ROOT="+pluginRoot, "TMPDIR="+out)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building the installed plugin failed: %v\n%s", err, b)
	}
	run := exec.Command(filepath.Join(out, "neverfired"), "examples/healthy/alerts_test.yml")
	run.Dir = root
	b, err := run.CombinedOutput()
	if err != nil || !strings.Contains(string(b), "no problems found") {
		t.Fatalf("the installed plugin's binary gave %v:\n%s", err, b)
	}
}

func copyTree(t *testing.T, from, to string) {
	t.Helper()
	err := filepath.WalkDir(from, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(from, p)
		dst := filepath.Join(to, rel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestPluginFiles(t *testing.T) {
	var plugin struct{ Name, Version string }
	var market struct {
		Name    string
		Plugins []struct{ Name, Source string }
	}
	for p, v := range map[string]any{"plugin/.claude-plugin/plugin.json": &plugin, ".claude-plugin/marketplace.json": &market} {
		if err := json.Unmarshal(readFile(t, p), v); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
	}
	if plugin.Name != "neverfired" || plugin.Version != "0.1.0" {
		t.Errorf("plugin.json: %+v", plugin)
	}
	if market.Name != "neverfired-marketplace" || len(market.Plugins) != 1 || market.Plugins[0].Name != "neverfired" || market.Plugins[0].Source != "./plugin" {
		t.Errorf("marketplace.json: %+v", market)
	}
	if !strings.Contains(string(readFile(t, "plugin/scripts/neverfired.go")), `const version = "0.1.0"`) {
		t.Error("neverfired.go version doesn't match plugin.json")
	}
	if !strings.Contains(string(readFile(t, "README.md")), "@v0.1.0") || !strings.Contains(string(readFile(t, "CHANGELOG.md")), "## [0.1.0]") {
		t.Error("README or CHANGELOG doesn't match version 0.1.0")
	}
}
