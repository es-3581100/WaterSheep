package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/es-3581100/WaterSheep/go-bridge/internal/supervisor"
)

func main() {
	if len(os.Args) < 2 {
		fatal(errors.New("usage: watersheep-supervisor <serve|status|debug-ask|debug-logs>"))
	}
	var err error
	switch os.Args[1] {
	case "serve":
		err = serve(os.Args[2:])
	case "status":
		err = status(os.Args[2:])
	case "debug-ask":
		err = debugAsk(os.Args[2:])
	case "debug-logs":
		err = debugLogs(os.Args[2:])
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fatal(err)
	}
}

func serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	listen := fs.String("listen", envOr("WATERSHEEP_SUPERVISOR_LISTEN", "127.0.0.1:8777"), "supervisor listen address")
	upstream := fs.String("upstream", envOr("WATERSHEEP_BASE_URL", "http://127.0.0.1:8766"), "WaterSheep base URL")
	python := fs.String("python", envOr("WATERSHEEP_PYTHON", "python3"), "Python executable")
	model := fs.String("model", envOr("WATERSHEEP_MODEL", "samratduttaofficial/WaterSheep"), "WaterSheep model")
	dev := fs.Bool("dev", envBool("WATERSHEEP_DEV"), "enable dev-only read-only debug endpoints")
	childJSON := fs.String("child-command-json", os.Getenv("WATERSHEEP_CHILD_COMMAND_JSON"), "DEV ONLY: JSON array overriding child command")
	startup := fs.Duration("startup-timeout", 45*time.Second, "WaterSheep startup timeout")
	if err := fs.Parse(args); err != nil {
		return err
	}

	child, err := childCommand(*python, *model, *upstream, *childJSON, *dev || envBool("WATERSHEEP_SMOKE"))
	if err != nil {
		return err
	}
	s, err := supervisor.New(supervisor.Config{
		ListenAddr:     *listen,
		UpstreamBase:   *upstream,
		ChildCommand:   child,
		DevMode:        *dev,
		StartupTimeout: *startup,
	})
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return s.Run(ctx)
}

func status(args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	base := fs.String("base-url", "http://127.0.0.1:8777", "supervisor base URL")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return printGET(strings.TrimRight(*base, "/")+"/v1/status", false)
}

func debugLogs(args []string) error {
	fs := flag.NewFlagSet("debug-logs", flag.ContinueOnError)
	base := fs.String("base-url", "http://127.0.0.1:8777", "supervisor base URL")
	approve := fs.Bool("approve-readonly-direct-output", false, "explicitly approve this read-only debug output call")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !*approve {
		return errors.New("refusing direct debug output without --approve-readonly-direct-output")
	}
	return printGET(strings.TrimRight(*base, "/")+"/v1/debug/logs", true)
}

func debugAsk(args []string) error {
	fs := flag.NewFlagSet("debug-ask", flag.ContinueOnError)
	base := fs.String("base-url", "http://127.0.0.1:8777", "supervisor base URL")
	request := fs.String("request", "-", "request JSON file or - for stdin")
	approve := fs.Bool("approve-readonly-direct-output", false, "explicitly approve this read-only direct model output call")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !*approve {
		return errors.New("refusing direct debug output without --approve-readonly-direct-output")
	}
	body, err := readInput(*request)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(*base, "/")+"/v1/debug/direct", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(supervisor.DebugApprovalHeader, supervisor.DebugApprovalPhrase)
	return doAndPrint(req)
}

func childCommand(python, model, upstream, override string, allowOverride bool) ([]string, error) {
	if override != "" {
		if !allowOverride {
			return nil, errors.New("--child-command-json requires --dev or WATERSHEEP_SMOKE=1")
		}
		var command []string
		if err := json.Unmarshal([]byte(override), &command); err != nil {
			return nil, fmt.Errorf("invalid child command JSON: %w", err)
		}
		if len(command) == 0 {
			return nil, errors.New("child command override is empty")
		}
		return command, nil
	}
	port, err := upstreamPort(upstream)
	if err != nil {
		return nil, err
	}
	return []string{python, "-m", "watersheep.cli", "--model", model, "--serve", strconv.Itoa(port)}, nil
}

func upstreamPort(raw string) (int, error) {
	parts := strings.Split(strings.TrimPrefix(strings.TrimPrefix(raw, "http://"), "https://"), "/")
	host := parts[0]
	idx := strings.LastIndex(host, ":")
	if idx < 0 {
		return 0, fmt.Errorf("upstream %q must include an explicit port", raw)
	}
	p, err := strconv.Atoi(host[idx+1:])
	if err != nil {
		return 0, fmt.Errorf("invalid upstream port in %q", raw)
	}
	return p, nil
}

func printGET(url string, approved bool) error {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if approved {
		req.Header.Set(supervisor.DebugApprovalHeader, supervisor.DebugApprovalPhrase)
	}
	return doAndPrint(req)
}

func doAndPrint(req *http.Request) error {
	resp, err := (&http.Client{Timeout: 120 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	fmt.Println(string(body))
	return nil
}

func readInput(path string) ([]byte, error) {
	if path == "-" {
		return io.ReadAll(os.Stdin)
	}
	return os.ReadFile(path)
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

func envBool(name string) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(name)))
	return v == "1" || v == "true" || v == "yes" || v == "on"
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
