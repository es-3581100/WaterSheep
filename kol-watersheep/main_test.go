package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func createXpra(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	if err := os.WriteFile(filepath.Join(d, "compose.yaml"), []byte("services: {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestParseFlags(t *testing.T) {
	o, err := parseArgs([]string{"start", "--skip-rust", "--skip-probe", "--xpra-root", "/a"})
	if err != nil || o.action != "start" || !o.skipRust || !o.skipProbe || o.xpraRoot != "/a" {
		t.Fatalf("bad parse: %+v %v", o, err)
	}
	for _, args := range [][]string{{"up", "--skip-pip"}, {"start", "--with-xpra"}, {"up", "extraneous"}, {"evil"}} {
		if _, err := parseArgs(args); err == nil {
			t.Fatalf("invalid flags accepted: %v", args)
		}
	}
}

func TestFindXpra(t *testing.T) {
	d := createXpra(t)
	found, err := findXpra(d, "", "", t.TempDir())
	if err != nil || found != d {
		t.Fatalf("found=%s err=%v", found, err)
	}
	_, err = findXpra(filepath.Join(d, "missing"), "", "", t.TempDir())
	if err == nil {
		t.Fatal("missing compose accepted")
	}
}

func TestGenerateConfigAndReuse(t *testing.T) {
	d := t.TempDir()
	xpra := createXpra(t)
	state := filepath.Join(d, "state")
	path := filepath.Join(d, "conf", "config.env")
	if err := createDefaultConfig(path, xpra, state); err != nil {
		t.Fatal(err)
	}
	cfg, err := parseConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{"XPRA_ROOT": xpra, "WS_USE_CONDA": "0", "WS_PYTHON": filepath.Join(state, "venv/bin/python"), "WS_TORCH_BACKEND": "cpu", "WS_ADAPTER_MODE": "dual"} {
		if cfg[k] != v {
			t.Fatalf("config %s=%q expected %q", k, cfg[k], v)
		}
	}
	stat, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if stat.Mode().Perm() != 0600 {
		t.Fatalf("config perms=%o", stat.Mode().Perm())
	}
	if err := createDefaultConfig(path, xpra, state); !os.IsExist(err) {
		t.Fatalf("existing config overwritten: %v", err)
	}
	src, _, err := configSource("", path, filepath.Join(d, "legacy"))
	if err != nil || src != path {
		t.Fatalf("config=%s err=%v", src, err)
	}
}

func TestMaterializeDoesNotReplaceModifiedLauncher(t *testing.T) {
	d := t.TempDir()
	dir, err := materialize(d, []byte("XPRA_ROOT=/tmp\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "kolstack")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "kolstack"), []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := materialize(d, []byte("XPRA_ROOT=/tmp\n")); err == nil {
		t.Fatal("modified launcher silently overwritten")
	}
}

func TestDryRunNoSideEffects(t *testing.T) {
	root := t.TempDir()
	xpra := createXpra(t)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XPRA_ROOT", "")
	t.Setenv("WS_STATE", "")
	var stdout, stderr bytes.Buffer
	err := run([]string{"start", "--xpra-root", xpra, "--dry-run"}, &stdout, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "DRY_RUN: action=start") {
		t.Fatalf("unexpected stdout: %s", stdout.String())
	}
	for _, sub := range []string{"config", "cache", "state"} {
		if _, err := os.Stat(filepath.Join(root, sub)); !os.IsNotExist(err) {
			t.Fatalf("dry run wrote %s", sub)
		}
	}
}

func TestNoClobberForeignKoLFork(t *testing.T) {
	xpra := createXpra(t)
	dest := filepath.Join(xpra, "watersheep/kol-watersheep")
	if err := os.MkdirAll(dest, 0700); err != nil {
		t.Fatal(err)
	}
	if err := ensureKoLFork(xpra, nil, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("foreign incomplete clone should be preserved")
	}
}
