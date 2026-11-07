// Command kol-watersheep bootstraps the KoL-specific, observation-only WaterSheep
// integration. It does not issue KoL gameplay commands or edit KoLmafia settings.
package main

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

//go:embed assets/kolstack assets/compat_adapter.py assets/environment.yml
var launcherAssets embed.FS

const (
	moduleName = "github.com/es-3581100/WaterSheep/kol-watersheep"
	kolForkURL = "https://github.com/donCannoli-burns/KOL-WaterSheep.git"
)

var allowedActions = map[string]bool{"start": true, "setup": true, "up": true, "status": true, "down": true, "rust-check": true}

type options struct {
	action     string
	xpraRoot   string
	configPath string
	skipRust   bool
	skipPip    bool
	skipProbe  bool
	withXpra   bool
	dryRun     bool
}

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "KOL_WATERSHEEP_ERROR:", err)
		os.Exit(1)
	}
}

func usage(w io.Writer) {
	fmt.Fprintf(w, `kol-watersheep — observation-only WaterSheep + KoLmafia/Xpra launcher

Usage:
  kol-watersheep start [--xpra-root PATH]  Bootstrap once, then start + verify
  kol-watersheep setup                   Install KoL inference + build Go supervisor
  kol-watersheep up                      Start an already-setup stack
  kol-watersheep status                  Show managed services and connectivity
  kol-watersheep down                    Stop only owned WaterSheep services
  kol-watersheep rust-check              Optional read-only Rust evidence check

Flags (placed after the command):
  --xpra-root PATH    Existing KoLmafia-Xpra repository root (contains compose.yaml)
  --config PATH       Explicit config.env path (must exist)
  --skip-rust         Do not run optional Rust tests during setup
  --skip-pip          Skip Python package install (setup; advanced)
  --skip-probe        Skip model inference probe (start/up; advanced)
  --with-xpra         Also stop Docker Xpra (down only)
  --dry-run           Print the plan without modifying files or starting processes

Requires Linux, Go >=1.22 to build supervisor, Python3 with venv or Conda,
Docker Compose, and an existing KoLmafia-Xpra checkout with Don Combo installed.
The program never executes KoL game actions, modifies game preferences, or
publishes inference endpoints to all interfaces.

Module: %s
`, moduleName)
}

func parseArgs(args []string) (options, error) {
	var o options
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		o.action = "help"
		return o, nil
	}
	o.action = args[0]
	if !allowedActions[o.action] {
		return o, fmt.Errorf("unknown command %q (use --help)", o.action)
	}
	f := flag.NewFlagSet(o.action, flag.ContinueOnError)
	f.SetOutput(io.Discard)
	f.StringVar(&o.xpraRoot, "xpra-root", "", "KoLmafia-Xpra root")
	f.StringVar(&o.configPath, "config", "", "existing config.env path")
	f.BoolVar(&o.skipRust, "skip-rust", false, "skip Rust tests")
	f.BoolVar(&o.skipPip, "skip-pip", false, "skip Python install")
	f.BoolVar(&o.skipProbe, "skip-probe", false, "skip model probe")
	f.BoolVar(&o.withXpra, "with-xpra", false, "also stop Xpra")
	f.BoolVar(&o.dryRun, "dry-run", false, "no writes/processes")
	if err := f.Parse(args[1:]); err != nil {
		return o, err
	}
	if f.NArg() > 0 {
		return o, fmt.Errorf("unexpected positional arguments: %v", f.Args())
	}
	if o.withXpra && o.action != "down" {
		return o, errors.New("--with-xpra applies only to down")
	}
	if o.skipPip && o.action != "setup" && o.action != "start" {
		return o, errors.New("--skip-pip applies only to setup/start")
	}
	if o.skipRust && o.action != "setup" && o.action != "start" {
		return o, errors.New("--skip-rust applies only to setup/start")
	}
	if o.skipProbe && o.action != "up" && o.action != "start" {
		return o, errors.New("--skip-probe applies only to up/start")
	}
	return o, nil
}

func run(args []string, stdout, stderr io.Writer) error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("Linux is currently required; detected %s", runtime.GOOS)
	}
	o, err := parseArgs(args)
	if err != nil {
		return err
	}
	if o.action == "help" {
		usage(stdout)
		return nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	userConfig := filepath.Join(configHome(home), "kol-watersheep", "config.env")
	// Existing user config may identify a root even outside conventional checkout paths.
	configuredRoot := ""
	if kv, e := parseConfig(userConfig); e == nil {
		configuredRoot = kv["XPRA_ROOT"]
	}
	if o.configPath != "" {
		kv, e := parseConfig(o.configPath)
		if e != nil {
			return fmt.Errorf("explicit config: %w", e)
		}
		configuredRoot = kv["XPRA_ROOT"]
	}
	xpra, err := findXpra(o.xpraRoot, os.Getenv("XPRA_ROOT"), configuredRoot, home)
	if err != nil {
		return err
	}
	source, target, err := configSource(o.configPath, userConfig, filepath.Join(xpra, "kolstack-watersheep-go-rust-kit", "config.env"))
	if err != nil {
		return err
	}
	state := filepath.Join(stateHome(home), "kolstack-watersheep")
	configured := map[string]string{}
	if source != "" {
		configured, err = parseConfig(source)
		if err != nil {
			return err
		}
		if configured["WS_STATE"] != "" {
			state = expandHome(configured["WS_STATE"], home)
		}
	}
	if val := os.Getenv("WS_STATE"); val != "" {
		state = expandHome(val, home)
	}
	if !filepath.IsAbs(state) {
		return errors.New("WS_STATE must be an absolute path")
	}

	cache := filepath.Join(cacheHome(home), "kol-watersheep", "launcher")
	if o.dryRun {
		fmt.Fprintf(stdout, "DRY_RUN: action=%s xpra_root=%s\n", o.action, xpra)
		if source != "" {
			fmt.Fprintf(stdout, "CONFIG_SOURCE: %s\n", source)
		} else {
			fmt.Fprintf(stdout, "GENERATE_CONFIG: %s\n", target)
		}
		fmt.Fprintf(stdout, "STATE_DIR: %s\nLAUNCHER_CACHE: %s\n", state, cache)
		if o.action == "start" || o.action == "setup" {
			fmt.Fprintln(stdout, "PLAN: ensure KoL-WaterSheep source exists; create managed Python venv if configured; provision dependencies/build Go if needed")
		}
		fmt.Fprintln(stdout, "PLAN: invoke bundled Python launcher; no KoL gameplay or preference mutation")
		return nil
	}

	if source == "" && o.action != "start" && o.action != "setup" {
		return errors.New("no KoL-WaterSheep config found; run kol-watersheep start first")
	}
	if o.action == "start" || o.action == "setup" {
		if err := ensureKoLFork(xpra, configured, stdout, stderr); err != nil {
			return err
		}
	}
	if source == "" {
		if err := createDefaultConfig(target, xpra, state); err != nil {
			return err
		}
		source = target
		configured, err = parseConfig(source)
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, "CREATED_CONFIG:", source)
	}
	// User-owned config is never overwritten. Explicit flags take priority via env.
	payload, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	dir, err := materialize(cache, payload)
	if err != nil {
		return err
	}
	exe := filepath.Join(dir, "kolstack")

	env := os.Environ()
	env = append(env, "XPRA_ROOT="+xpra)
	if o.action == "start" || o.action == "setup" {
		if err := ensurePythonEnv(configured, state, home, stdout, stderr); err != nil {
			return err
		}
	}

	execute := func(arguments ...string) error {
		interp, err := exec.LookPath("python3")
		if err != nil {
			return errors.New("python3 is required to run the bundled launcher")
		}
		cmd := exec.Command(interp, append([]string{exe}, arguments...)...)
		cmd.Env = env
		cmd.Stdin = os.Stdin
		cmd.Stdout = stdout
		cmd.Stderr = stderr
		fmt.Fprintf(stdout, "LAUNCH: kolstack %s\n", strings.Join(arguments, " "))
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("kolstack %s: %w", arguments[0], err)
		}
		return nil
	}
	switch o.action {
	case "start":
		// The checked-in Python launcher already manages PID ownership and health checks.
		bin := filepath.Join(state, "bin", "watersheep-supervisor")
		if _, err := os.Stat(bin); errors.Is(err, os.ErrNotExist) {
			setupArgs := []string{"setup", "--skip-rust"}
			if o.skipPip {
				setupArgs = append(setupArgs, "--skip-pip")
			}
			if err := execute(setupArgs...); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		upArgs := []string{"up"}
		if o.skipProbe {
			upArgs = append(upArgs, "--skip-probe")
		}
		return execute(upArgs...)
	case "setup":
		a := []string{"setup"}
		if o.skipRust {
			a = append(a, "--skip-rust")
		}
		if o.skipPip {
			a = append(a, "--skip-pip")
		}
		return execute(a...)
	case "up":
		a := []string{"up"}
		if o.skipProbe {
			a = append(a, "--skip-probe")
		}
		return execute(a...)
	case "down":
		a := []string{"down"}
		if o.withXpra {
			a = append(a, "--with-xpra")
		}
		return execute(a...)
	default:
		return execute(o.action)
	}
}

func expandHome(p, home string) string {
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(home, p[2:])
	}
	return p
}

func configHome(home string) string {
	if p := os.Getenv("XDG_CONFIG_HOME"); filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(home, ".config")
}
func cacheHome(home string) string {
	if p := os.Getenv("XDG_CACHE_HOME"); filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(home, ".cache")
}
func stateHome(home string) string {
	if p := os.Getenv("XDG_STATE_HOME"); filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(home, ".local", "state")
}

func validXpra(dir string) bool {
	if dir == "" {
		return false
	}
	s, err := os.Stat(filepath.Join(dir, "compose.yaml"))
	return err == nil && s.Mode().IsRegular()
}

func findXpra(explicit, fromEnv, fromConfig, home string) (string, error) {
	for _, p := range []string{explicit, fromEnv, fromConfig} {
		if p == "" {
			continue
		}
		dir, err := filepath.Abs(expandHome(p, home))
		if err != nil {
			return "", err
		}
		if !validXpra(dir) {
			return "", fmt.Errorf("XPRA_ROOT=%s has no compose.yaml", dir)
		}
		return dir, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for dir := cwd; ; dir = filepath.Dir(dir) {
		if validXpra(dir) {
			return dir, nil
		}
		if dir == filepath.Dir(dir) {
			break
		}
	}
	for _, p := range []string{
		filepath.Join(home, "repos/es-3581100/kolmafia-xpra"),
		filepath.Join(home, "repos/kolmafia-xpra"),
		filepath.Join(home, "kolmafia-xpra"),
		filepath.Join(home, "src/kolmafia-xpra"),
	} {
		if validXpra(p) {
			return p, nil
		}
	}
	return "", errors.New("could not find KoLmafia-Xpra (compose.yaml). Run from its checkout or pass --xpra-root /path/to/kolmafia-xpra")
}

// The explicit file wins; otherwise prefer modern user config, then the old
// working kit's config, then a new config under XDG_CONFIG_HOME.
func configSource(explicit, userConfig, legacy string) (source, target string, err error) {
	if explicit != "" {
		if s, e := os.Stat(explicit); e != nil || !s.Mode().IsRegular() {
			return "", "", fmt.Errorf("config %s not a readable regular file", explicit)
		}
		return explicit, explicit, nil
	}
	for _, p := range []string{userConfig, legacy} {
		if s, e := os.Stat(p); e == nil && s.Mode().IsRegular() {
			return p, p, nil
		}
	}
	return "", userConfig, nil
}

func parseConfig(file string) (map[string]string, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	v := map[string]string{}
	for i, raw := range strings.Split(string(b), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, found := strings.Cut(line, "=")
		if !found || strings.TrimSpace(key) == "" {
			return nil, fmt.Errorf("%s:%d: expected KEY=VALUE", file, i+1)
		}
		val = strings.TrimSpace(val)
		if len(val) >= 2 && ((val[0] == '\'' && val[len(val)-1] == '\'') || (val[0] == '"' && val[len(val)-1] == '"')) {
			val = val[1 : len(val)-1]
		}
		v[strings.TrimSpace(key)] = val
	}
	return v, nil
}

func createDefaultConfig(target, xpra, state string) error {
	// Reject embedded newlines, since config.env uses a line-oriented format.
	for _, v := range []string{xpra, state} {
		if strings.ContainsAny(v, "\n\r") {
			return errors.New("newline in path is not supported")
		}
	}
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return err
	}
	content := fmt.Sprintf(`# Generated by kol-watersheep. No KoL credentials are stored here.
# To use a Conda environment instead, set WS_USE_CONDA=1 and WS_CONDA_ENV=NAME.
XPRA_ROOT=%s
WS_CORE=%s
WS_KOL=%s
WS_USE_CONDA=0
WS_PYTHON=%s
WS_MODEL=samratduttaofficial/WaterSheep
WS_STATE=%s
WS_STARTUP_TIMEOUT=600
WS_ADAPTER_MODE=dual
WS_TORCH_BACKEND=cpu
WS_PREFS_FILE=%s
`, xpra, filepath.Join(xpra, "watersheep/watersheep-go-rust-core"), filepath.Join(xpra, "watersheep/kol-watersheep"), filepath.Join(state, "venv/bin/python"), state, filepath.Join(xpra, "data/settings/doncannoli_prefs.txt"))
	file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = io.WriteString(file, content)
	return err
}

func ensureKoLFork(xpra string, conf map[string]string, stdout, stderr io.Writer) error {
	dst := conf["WS_KOL"]
	if fromEnv := os.Getenv("WS_KOL"); fromEnv != "" {
		dst = fromEnv
	}
	if dst == "" {
		dst = filepath.Join(xpra, "watersheep/kol-watersheep")
	}
	if s, err := os.Stat(filepath.Join(dst, "pyproject.toml")); err == nil && s.Mode().IsRegular() {
		return nil
	}
	if _, err := os.Stat(dst); err == nil {
		return fmt.Errorf("KoL-WaterSheep destination %s already exists but lacks pyproject.toml; not overwriting it", dst)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "Cloning missing KoL-WaterSheep fork into", dst)
	cmd := exec.Command("git", "clone", "--depth", "1", kolForkURL, dst)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("clone KoL-WaterSheep: %w", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "pyproject.toml")); err != nil {
		return fmt.Errorf("KoL-WaterSheep checkout missing pyproject.toml: %w", err)
	}
	return nil
}

func ensurePythonEnv(conf map[string]string, state, home string, stdout, stderr io.Writer) error {
	useConda := strings.ToLower(conf["WS_USE_CONDA"])
	if useConda != "0" && useConda != "false" && useConda != "no" {
		return nil
	}
	selected := conf["WS_PYTHON"]
	if v := os.Getenv("WS_PYTHON"); v != "" {
		selected = v
	}
	if selected == "" {
		return errors.New("WS_USE_CONDA=0 requires WS_PYTHON")
	}
	selected = expandHome(selected, home)
	if _, err := os.Stat(selected); err == nil {
		return nil
	}
	managed := filepath.Join(state, "venv/bin/python")
	if filepath.Clean(selected) != filepath.Clean(managed) {
		return fmt.Errorf("custom WS_PYTHON %s missing; won't create/overwrite user-selected Python", selected)
	}
	fmt.Fprintln(stdout, "Creating dedicated Python virtualenv in", filepath.Join(state, "venv"))
	cmd := exec.Command("python3", "-m", "venv", filepath.Join(state, "venv"))
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("python3 -m venv: %w (is python3-venv installed?)", err)
	}
	if _, err := os.Stat(selected); err != nil {
		return fmt.Errorf("venv missing Python: %w", err)
	}
	return nil
}

func materialize(cache string, config []byte) (string, error) {
	files := []string{"kolstack", "compat_adapter.py", "environment.yml"}
	h := sha256.New()
	for _, file := range files {
		b, err := launcherAssets.ReadFile("assets/" + file)
		if err != nil {
			return "", err
		}
		_, _ = h.Write([]byte(file))
		_, _ = h.Write(b)
	}
	dir := filepath.Join(cache, hex.EncodeToString(h.Sum(nil))[:16])
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	for _, file := range files {
		content, err := launcherAssets.ReadFile("assets/" + file)
		if err != nil {
			return "", err
		}
		mode := fs.FileMode(0600)
		if file == "kolstack" {
			mode = 0700
		}
		if err := writeVerified(filepath.Join(dir, file), content, mode); err != nil {
			return "", err
		}
	}
	// config.env can change between runs, but the launcher binary is never overwritten.
	if err := os.WriteFile(filepath.Join(dir, "config.env"), config, 0600); err != nil {
		return "", err
	}
	return dir, nil
}

func writeVerified(path string, bytes []byte, mode fs.FileMode) error {
	if existing, err := os.ReadFile(path); err == nil {
		if string(existing) != string(bytes) {
			return fmt.Errorf("cache file %s differs from bundled version; refusing to overwrite", path)
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(bytes)
	return err
}
