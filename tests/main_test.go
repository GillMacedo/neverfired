// Package tests runs neverfired as a built binary against real promtool test
// files and rule files.
package tests

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

var (
	tool string // path of the built neverfired binary
	root string // repository root
)

func TestMain(m *testing.M) {
	os.Exit(setup(m))
}

func setup(m *testing.M) int {
	wd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	root = filepath.Dir(wd)
	dir, err := os.MkdirTemp("", "neverfired-test")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer os.RemoveAll(dir)
	tool = filepath.Join(dir, "neverfired"+exeSuffix())
	cmd := exec.Command("go", "build", "-o", tool, "./plugin/scripts")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "building neverfired: %v\n%s", err, out)
		return 1
	}
	return m.Run()
}

func exeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

type result struct {
	stdout, stderr string
	code           int
}

// neverfired runs the tool from the repository root.
func neverfired(t *testing.T, args ...string) result {
	t.Helper()
	return neverfiredIn(t, root, args...)
}

func neverfiredIn(t *testing.T, dir string, args ...string) result {
	t.Helper()
	cmd := exec.Command(tool, args...)
	cmd.Dir = dir
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	code := 0
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatalf("running neverfired: %v", err)
		}
		code = exit.ExitCode()
	}
	return result{out.String(), errb.String(), code}
}

// codes returns the sorted finding codes in text output, such as
// "ERROR NF104".
func codes(out string) []string {
	var cs []string
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && (f[0] == "ERROR" || f[0] == "WARN" || f[0] == "NOTE") && strings.HasPrefix(f[1], "NF") {
			cs = append(cs, f[0]+" "+f[1])
		}
	}
	sort.Strings(cs)
	return cs
}

func readFile(t *testing.T, rel string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// writeFiles writes files into a new temporary directory and returns it.
func writeFiles(t *testing.T, files map[string][]byte) string {
	t.Helper()
	dir := t.TempDir()
	for name, data := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// refused checks that a run exited 2 with nothing on stdout and a reason on
// stderr.
func refused(t *testing.T, name string, r result) {
	t.Helper()
	if r.code != 2 || r.stdout != "" || !strings.Contains(r.stderr, "can't be trusted") {
		t.Errorf("%s: want exit 2, empty stdout and a reason; got exit %d\nstdout: %q\nstderr: %q", name, r.code, r.stdout, r.stderr)
	}
}
