package tests

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestReadme runs every "$ " line in the README's indented code blocks
// from the repository root and compares its output with the lines that
// follow, the way a reader would see them.
func TestReadme(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("README commands are for a POSIX shell")
	}
	lines := strings.Split(string(readFile(t, "README.md")), "\n")
	found := 0
	for i := 0; i < len(lines); i++ {
		if !strings.HasPrefix(lines[i], "    $ ") {
			continue
		}
		cmdline := strings.TrimPrefix(lines[i], "    $ ")
		var want []string
		for j := i + 1; j < len(lines); j++ {
			l := lines[j]
			if l == "" && j+1 < len(lines) && strings.HasPrefix(lines[j+1], "    ") && !strings.HasPrefix(lines[j+1], "    $ ") {
				want = append(want, "")
				continue
			}
			if !strings.HasPrefix(l, "    ") || strings.HasPrefix(l, "    $ ") {
				break
			}
			want = append(want, strings.TrimPrefix(l, "    "))
		}
		found++
		const prefix = "go run ./plugin/scripts "
		if !strings.HasPrefix(cmdline, prefix) {
			t.Errorf("README command this test doesn't know how to run: %s", cmdline)
			continue
		}
		cmd := exec.Command("sh", "-c", `"$NEVERFIRED" `+strings.TrimPrefix(cmdline, prefix))
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "NEVERFIRED="+tool)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Errorf("%s: %v (README commands must exit 0)", cmdline, err)
		}
		if got := strings.TrimRight(string(out), "\n"); got != strings.TrimRight(strings.Join(want, "\n"), "\n") {
			t.Errorf("%s: README output differs.\n--- got\n%s\n--- README\n%s", cmdline, got, strings.Join(want, "\n"))
		}
	}
	if found < 3 {
		t.Errorf("found %d commands in the README, expected at least 3", found)
	}
}

// TestReadmeRefusal checks the refusal message the README quotes.
func TestReadmeRefusal(t *testing.T) {
	full := readFile(t, "examples/healthy/alerts_test.yml")
	dir := writeFiles(t, map[string][]byte{
		"alerts.yml":      readFile(t, "examples/healthy/alerts.yml"),
		"alerts_test.yml": full[:len(full)-30],
	})
	r := neverfiredIn(t, dir, "alerts_test.yml")
	got := strings.TrimSpace(r.stderr)
	if r.code != 2 || !strings.Contains(string(readFile(t, "README.md")), "    "+got+"\n") {
		t.Errorf("exit %d; the README doesn't quote this message:\n%s", r.code, got)
	}
}

// TestReadmeCodes checks that every code the tool can print is in the
// README table and the skill.
func TestReadmeCodes(t *testing.T) {
	var src strings.Builder
	files, _ := filepath.Glob(filepath.Join(root, "plugin", "scripts", "*.go"))
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, _ := os.ReadFile(f)
		src.Write(b)
	}
	readme := string(readFile(t, "README.md"))
	skill := string(readFile(t, skillRel))
	n := 0
	for _, part := range strings.Split(src.String(), `Code: "NF`)[1:] {
		code := "NF" + part[:3]
		n++
		if !strings.Contains(readme, "| "+code+" |") {
			t.Errorf("%s isn't in the README table", code)
		}
		if !strings.Contains(skill, code) {
			t.Errorf("%s isn't in the skill", code)
		}
	}
	if n < 10 {
		t.Errorf("found only %d codes in the source", n)
	}
}
