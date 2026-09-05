package main

import (
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

	"aiusage/internal/auth"
	"aiusage/internal/collector"
	"aiusage/internal/config"
	"aiusage/internal/model"
	"aiusage/internal/provider"
	"aiusage/internal/provider/claude"
	"aiusage/internal/provider/codex"
	"aiusage/internal/provider/opencode"
	"aiusage/internal/security"
	"aiusage/internal/ui"
)

const version = "0.1.0"

const (
	enterAlternateScreen = "\x1b[?1049h\x1b[2J\x1b[H\x1b[?25l"
	leaveAlternateScreen = "\x1b[?25h\x1b[?1049l"
)

func main() {
	jsonOutput := flag.Bool("json", false, "print one JSON snapshot and exit")
	nonInteractive := flag.Bool("non-interactive", false, "read a JSON request from stdin without credential prompts")
	verbose := flag.Bool("verbose", false, "print diagnostic messages")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return
	}

	home, err := os.UserHomeDir()
	if err != nil {
		fatal(err)
	}
	cfg, err := config.Load(home)
	if err != nil {
		fatal(err)
	}
	if *verbose {
		fmt.Fprintf(os.Stderr, "interval=%s\n", cfg.Interval)
		for _, warning := range cfg.Warnings {
			fmt.Fprintln(os.Stderr, warning)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	app := &application{
		home:   home,
		config: cfg,
		store:  auth.NewKeychainStore(),
		client: &http.Client{Timeout: 10 * time.Second},
	}
	if *nonInteractive {
		if !*jsonOutput {
			os.Exit(2)
		}
		os.Exit(app.runBridge(ctx, os.Stdin, os.Stdout))
	}
	promptOutput := io.Writer(os.Stdout)
	if *jsonOutput {
		promptOutput = os.Stderr
	}
	app.prompt = auth.TerminalPrompt{In: os.Stdin, Out: promptOutput}.Ask
	app.prepareKey(ctx)

	if *jsonOutput {
		code := app.runJSON(ctx, os.Stdout)
		if code != 0 {
			os.Exit(code)
		}
		return
	}
	if err := app.runTUI(ctx, os.Stdout); err != nil {
		fatal(err)
	}
}

type application struct {
	home   string
	config config.Config
	store  auth.Store
	prompt auth.PromptFunc
	client *http.Client

	key              string
	keyResolveErr    error
	claudeOAuthToken string
	claudeEndpoint   string
	opencodeEndpoint string
	collector        *collector.Collector
}

func (a *application) prepareKey(ctx context.Context) {
	if !a.config.ProviderEnabled("opencode") {
		return
	}
	key, err := auth.ResolveKey(ctx, a.store, a.prompt, false)
	if err != nil {
		a.keyResolveErr = err
		return
	}
	a.key = key
}

func (a *application) buildCollector() {
	root := os.DirFS(a.home)
	providers := make([]provider.Provider, 0, 3)
	if a.config.ProviderEnabled("codex") {
		providers = append(providers, codex.NewWithLive(root))
	}
	if a.config.ProviderEnabled("claude") {
		providers = append(providers, claude.NewWithOAuth(root, a.claudeOAuthToken, a.claudeEndpoint, a.client))
	}
	if a.config.ProviderEnabled("opencode") {
		if a.opencodeEndpoint != "" {
			providers = append(providers, opencode.NewWithEndpoint(a.key, a.opencodeEndpoint, a.client))
		} else {
			providers = append(providers, opencode.New(a.key, a.client))
		}
	}
	a.collector = collector.New(providers, a.config.Interval)
}

func (a *application) collect(ctx context.Context, manual bool) collector.Snapshot {
	if a.collector == nil {
		a.buildCollector()
	}
	var snapshot collector.Snapshot
	if manual {
		snapshot = a.collector.Refresh(ctx)
	} else {
		snapshot = a.collector.Collect(ctx)
	}
	if containsUnauthorized(snapshot) {
		replacement, err := auth.ResolveKey(ctx, a.store, a.prompt, true)
		if err != nil {
			attachOpenCodeError(&snapshot, fmt.Errorf("OpenCode Go key replacement required: %w", err))
			return snapshot
		}
		a.key = replacement
		a.keyResolveErr = nil
		a.buildCollector()
		snapshot = a.collector.Collect(ctx)
	}
	if a.keyResolveErr != nil {
		attachMissingKeyError(&snapshot, a.keyResolveErr)
	}
	return snapshot
}

func containsUnauthorized(snapshot collector.Snapshot) bool {
	for _, status := range snapshot.Statuses {
		if errors.Is(status.Err, opencode.ErrUnauthorized) {
			return true
		}
	}
	return false
}

func attachOpenCodeError(snapshot *collector.Snapshot, err error) {
	for index := range snapshot.Statuses {
		if snapshot.Statuses[index].Name == "opencode" {
			snapshot.Statuses[index].Err = err
			return
		}
	}
}

func attachMissingKeyError(snapshot *collector.Snapshot, err error) {
	for index := range snapshot.Statuses {
		if snapshot.Statuses[index].Name == "opencode" && errors.Is(snapshot.Statuses[index].Err, opencode.ErrMissingKey) {
			snapshot.Statuses[index].Err = fmt.Errorf("OpenCode Go key unavailable: %w", err)
			return
		}
	}
}

func (a *application) runJSON(ctx context.Context, out io.Writer) int {
	snapshot := a.collect(ctx, false)
	output := jsonSnapshot{UpdatedAt: snapshot.UpdatedAt, Statuses: make([]jsonStatus, 0, len(snapshot.Statuses))}
	hasError := false
	for _, status := range snapshot.Statuses {
		item := jsonStatus{
			Name:      status.Name,
			Available: status.Available,
			Usages:    status.Usages,
		}
		if status.Err != nil {
			item.Error = security.Mask(status.Err.Error())
			hasError = true
		}
		output.Statuses = append(output.Statuses, item)
	}
	if err := json.NewEncoder(out).Encode(output); err != nil {
		fmt.Fprintln(os.Stderr, security.Mask(err.Error()))
		return 1
	}
	if hasError {
		return 1
	}
	return 0
}

type jsonSnapshot struct {
	UpdatedAt time.Time    `json:"updatedAt"`
	Statuses  []jsonStatus `json:"statuses"`
}

type jsonStatus struct {
	Name      string        `json:"name"`
	Available bool          `json:"available"`
	Error     string        `json:"error,omitempty"`
	Usages    []model.Usage `json:"usages,omitempty"`
}

func (a *application) runTUI(ctx context.Context, out io.Writer) error {
	_, _ = io.WriteString(out, enterAlternateScreen)
	defer func() { _, _ = io.WriteString(out, leaveAlternateScreen) }()

	snapshot := a.collect(ctx, false)
	ticker := time.NewTicker(a.config.Interval)
	defer ticker.Stop()

	for {
		width := terminalWidth()
		_, _ = io.WriteString(out, "\x1b[H\x1b[2J")
		_, _ = io.WriteString(out, clearLines(ui.RenderWithThresholds(
			snapshot,
			time.Now(),
			width,
			a.config.Interval,
			ui.Thresholds{Warn: a.config.Warn, Danger: a.config.Danger},
		))+"\n")
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			snapshot = a.collect(ctx, false)
		}
	}
}

func terminalWidth() int {
	if width, err := strconv.Atoi(os.Getenv("COLUMNS")); err == nil && width > 0 {
		return width
	}
	return 80
}

func clearLines(value string) string {
	return "\x1b[K" + strings.ReplaceAll(value, "\n", "\x1b[K\n")
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, security.Mask(err.Error()))
	os.Exit(1)
}
