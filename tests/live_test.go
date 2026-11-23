package tests

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLiveKubernetesMixin reruns the kubernetes-mixin result in the README.
// Set NEVERFIRED_K8S_MIXIN to a checkout of kubernetes-monitoring/
// kubernetes-mixin at 2002557 with prometheus_alerts.yaml and
// prometheus_rules.yaml generated (jb install, then the two jsonnet
// commands in its Makefile).
func TestLiveKubernetesMixin(t *testing.T) {
	dir := os.Getenv("NEVERFIRED_K8S_MIXIN")
	if dir == "" {
		t.Skip("NEVERFIRED_K8S_MIXIN not set")
	}
	files, _ := filepath.Glob(filepath.Join(dir, "tests", "*.yaml"))
	var rel []string
	for _, f := range files {
		r, _ := filepath.Rel(dir, f)
		rel = append(rel, r)
	}
	r := neverfiredIn(t, dir, append([]string{"--fail-on", "never"}, rel...)...)
	for _, want := range []string{
		"WARN NF107 tests/tests.yaml:402 [test group 17, KubePersistentVolumeInodesFillingUp at 1h1m]",
		"WARN NF107 tests/tests.yaml:418 [test group 18, KubePersistentVolumeInodesFillingUp at 1h1m]",
	} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("missing %q\n%s%s", want, r.stdout, r.stderr)
		}
	}
	if strings.Contains(r.stdout, "ERROR ") {
		t.Errorf("errors on kubernetes-mixin:\n%s", r.stdout)
	}
}

// TestLiveCeph reruns the Ceph result: every alert is shown firing and
// nothing is reported. Set NEVERFIRED_CEPH to a checkout of ceph/ceph.
func TestLiveCeph(t *testing.T) {
	dir := os.Getenv("NEVERFIRED_CEPH")
	if dir == "" {
		t.Skip("NEVERFIRED_CEPH not set")
	}
	r := neverfiredIn(t, filepath.Join(dir, "monitoring", "ceph-mixin"), "tests_alerts/test_alerts.yml", "tests_alerts/test_nvmeof_cpu.yml")
	if r.code != 0 || !strings.Contains(r.stdout, "no problems found") {
		t.Errorf("exit %d\n%s%s", r.code, r.stdout, r.stderr)
	}
}
