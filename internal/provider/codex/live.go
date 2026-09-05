package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"aiusage/internal/model"
)

// Codex app-server performs startup work before it can answer the first
// account request. Keep the probe bounded, but allow that startup to finish;
// the Swift helper has a separate, longer overall timeout.
const liveAppServerTimeout = 8 * time.Second

const codexCollectionTimeout = 10 * time.Second

var (
	errLiveUnavailable = errors.New("Codex app-server unavailable")
	errLiveNoResponse  = errors.New("Codex app-server returned no rate-limit response")
)

type liveRateLimitResponse struct {
	RateLimits          *liveRateLimitSnapshot           `json:"rateLimits"`
	RateLimitsByLimitID map[string]liveRateLimitSnapshot `json:"rateLimitsByLimitId"`
}

type liveRateLimitSnapshot struct {
	LimitID   string               `json:"limitId"`
	LimitName string               `json:"limitName"`
	Primary   *liveRateLimitWindow `json:"primary"`
	Secondary *liveRateLimitWindow `json:"secondary"`
}

type liveRateLimitWindow struct {
	UsedPercent        *float64 `json:"usedPercent"`
	WindowDurationMins *float64 `json:"windowDurationMins"`
	ResetsAt           *float64 `json:"resetsAt"`
}

func fetchLiveRateLimits(ctx context.Context, fetchedAt time.Time) ([]model.Usage, error) {
	command, args, ok := findCodexCommand()
	if !ok {
		return nil, errLiveUnavailable
	}

	request := strings.Join([]string{
		`{"id":1,"method":"initialize","params":{"clientInfo":{"name":"aiusage","version":"0.10"},"capabilities":{"experimentalApi":true,"optOutNotificationMethods":null}}}`,
		`{"method":"initialized"}`,
		`{"id":2,"method":"account/rateLimits/read","params":null}`,
	}, "\n") + "\n"
	requestContext, cancel := context.WithTimeout(ctx, liveAppServerTimeout)
	defer cancel()
	commandProcess := exec.CommandContext(requestContext, command, append(args, "app-server", "--listen", "stdio://")...)
	stdin, err := commandProcess.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("%w: stdin unavailable", errLiveUnavailable)
	}
	stdout, err := commandProcess.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("%w: stdout unavailable", errLiveUnavailable)
	}
	stderr, err := commandProcess.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("%w: stderr unavailable", errLiveUnavailable)
	}
	if err := commandProcess.Start(); err != nil {
		return nil, fmt.Errorf("%w: process failed", errLiveUnavailable)
	}
	if _, err := io.WriteString(stdin, request); err != nil {
		_ = stdin.Close()
		_ = commandProcess.Process.Kill()
		_ = commandProcess.Wait()
		return nil, fmt.Errorf("%w: request failed", errLiveUnavailable)
	}
	go func() { _, _ = io.Copy(io.Discard, stderr) }()
	resultCh := make(chan liveReadResult, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 16<<10), 1<<20)
		for scanner.Scan() {
			usages, parseErr := parseLiveRateLimitResponse(scanner.Bytes(), fetchedAt)
			if parseErr == nil {
				resultCh <- liveReadResult{usages: usages}
				return
			}
		}
		if err := scanner.Err(); err != nil {
			resultCh <- liveReadResult{err: fmt.Errorf("%w: output read failed", errLiveUnavailable)}
			return
		}
		resultCh <- liveReadResult{err: errLiveNoResponse}
	}()

	select {
	case result := <-resultCh:
		_ = stdin.Close()
		if result.err == nil {
			_ = commandProcess.Process.Kill()
		}
		_ = commandProcess.Wait()
		return result.usages, result.err
	case <-requestContext.Done():
		_ = stdin.Close()
		_ = commandProcess.Process.Kill()
		_ = commandProcess.Wait()
		return nil, requestContext.Err()
	}
}

type liveReadResult struct {
	usages []model.Usage
	err    error
}

func parseLiveRateLimitResponse(data []byte, fetchedAt time.Time) ([]model.Usage, error) {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 16<<10), 1<<20)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}

		var envelope struct {
			Result     json.RawMessage `json:"result"`
			Error      json.RawMessage `json:"error"`
			RateLimits json.RawMessage `json:"rateLimits"`
		}
		if err := json.Unmarshal(line, &envelope); err != nil {
			continue
		}
		if len(envelope.Error) > 0 && string(envelope.Error) != "null" {
			return nil, fmt.Errorf("%w: request failed", errLiveUnavailable)
		}

		payload := envelope.Result
		if len(payload) == 0 || string(payload) == "null" {
			if len(envelope.RateLimits) == 0 {
				continue
			}
			payload = line
		}
		var response liveRateLimitResponse
		if err := json.Unmarshal(payload, &response); err != nil {
			return nil, fmt.Errorf("%w: invalid response", errLiveUnavailable)
		}
		usages := liveRateLimitUsages(response, fetchedAt)
		if len(usages) == 0 {
			return nil, errLiveNoResponse
		}
		return usages, nil
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("%w: read failed", errLiveUnavailable)
	}
	return nil, errLiveNoResponse
}

func liveRateLimitUsages(response liveRateLimitResponse, fetchedAt time.Time) []model.Usage {
	byWindow := make(map[string]model.Usage)
	if response.RateLimits != nil {
		snapshot := *response.RateLimits
		// The backward-compatible top-level field is normally the regular
		// Codex bucket, but some responses expose gpt-reserve here instead.
		// Classify by identity before using the duration-based 5h/7d aliases;
		// otherwise a reserve primary window of 10080 minutes becomes 7d.
		forcedWindow := ""
		if isReserveLiveSnapshot("", snapshot) {
			forcedWindow = "reserve"
		}
		addLiveSnapshot(byWindow, snapshot, forcedWindow, fetchedAt)
	}

	keys := make([]string, 0, len(response.RateLimitsByLimitID))
	for key := range response.RateLimitsByLimitID {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		snapshot := response.RateLimitsByLimitID[key]
		if isReserveLiveSnapshot(key, snapshot) {
			addLiveSnapshot(byWindow, snapshot, "reserve", fetchedAt)
		} else if isCodexLiveSnapshot(key, snapshot) {
			addLiveSnapshot(byWindow, snapshot, "", fetchedAt)
		}
	}

	windows := make([]string, 0, len(byWindow))
	for window := range byWindow {
		windows = append(windows, window)
	}
	sort.Strings(windows)
	result := make([]model.Usage, 0, len(windows))
	for _, window := range windows {
		result = append(result, byWindow[window])
	}
	return result
}

func addLiveSnapshot(byWindow map[string]model.Usage, snapshot liveRateLimitSnapshot, forcedWindow string, fetchedAt time.Time) {
	addWindow := func(alias string, window *liveRateLimitWindow) bool {
		if window == nil || window.UsedPercent == nil {
			return false
		}
		used, ok := validLivePercent(*window.UsedPercent)
		if !ok {
			return false
		}
		name := forcedWindow
		if name == "" {
			name = liveWindowForDuration(alias, window.WindowDurationMins)
		}
		usage := model.Usage{
			Provider:  "codex",
			Window:    name,
			Used:      used,
			Limit:     100,
			Unit:      "percent",
			Source:    model.SourceEndpoint,
			FetchedAt: fetchedAt,
			Note:      "from Codex app-server account/rateLimits/read",
		}
		if window.ResetsAt != nil && !isInvalidNumber(*window.ResetsAt) {
			resetAt := time.Unix(int64(*window.ResetsAt), 0)
			usage.ResetAt = &resetAt
		}
		byWindow[name] = usage
		return true
	}

	if forcedWindow != "" {
		// A reserve bucket represents one meter. Prefer its primary window and
		// use secondary only when primary is absent or has no valid percentage;
		// never let secondary overwrite the reserve value.
		if addWindow("5h", snapshot.Primary) {
			return
		}
		addWindow("7d", snapshot.Secondary)
		return
	}
	addWindow("5h", snapshot.Primary)
	addWindow("7d", snapshot.Secondary)
}

func liveWindowForDuration(alias string, duration *float64) string {
	if duration != nil && !isInvalidNumber(*duration) {
		switch *duration {
		case 300:
			return "5h"
		case 10080:
			return "7d"
		}
	}
	return alias
}

func isReserveLiveSnapshot(key string, snapshot liveRateLimitSnapshot) bool {
	return isReserveLimitName(key) || isReserveLimitName(snapshot.LimitID) || isReserveLimitName(snapshot.LimitName)
}

func isCodexLiveSnapshot(key string, snapshot liveRateLimitSnapshot) bool {
	return strings.EqualFold(key, "codex") || strings.EqualFold(snapshot.LimitID, "codex")
}

func isInvalidNumber(value float64) bool {
	return math.IsNaN(value) || math.IsInf(value, 0)
}

func validLivePercent(percent float64) (float64, bool) {
	if math.IsNaN(percent) || math.IsInf(percent, 0) || percent < 0 || percent > 100 {
		return 0, false
	}
	return percent, true
}

type boundedOutput struct {
	data     bytes.Buffer
	limit    int
	exceeded bool
}

func (b *boundedOutput) Write(data []byte) (int, error) {
	if b.exceeded {
		return 0, errors.New("output limit exceeded")
	}
	if b.data.Len()+len(data) > b.limit {
		b.exceeded = true
		return 0, errors.New("output limit exceeded")
	}
	return b.data.Write(data)
}

func findCodexCommand() (string, []string, bool) {
	if command, err := exec.LookPath("codex"); err == nil {
		return command, nil, true
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", nil, false
	}
	paths := []string{
		filepath.Join(home, ".npm", "bin", "codex"),
		filepath.Join(home, ".local", "bin", "codex"),
		"/opt/homebrew/bin/codex",
		"/usr/local/bin/codex",
	}
	for _, path := range paths {
		if !isExecutable(path) {
			continue
		}
		if node, ok := findNodeCommand(); ok && isNodeScript(path) {
			return node, []string{path}, true
		}
		return path, nil, true
	}
	return "", nil, false
}

func findNodeCommand() (string, bool) {
	if node, err := exec.LookPath("node"); err == nil {
		return node, true
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", false
	}
	paths := []string{
		filepath.Join(home, ".volta", "bin", "node"),
		filepath.Join(home, ".asdf", "shims", "node"),
		"/opt/homebrew/bin/node",
		"/usr/local/bin/node",
		"/usr/bin/node",
	}
	if matches, _ := filepath.Glob(filepath.Join(home, ".nvm", "versions", "node", "*", "bin", "node")); len(matches) > 0 {
		sort.Sort(sort.Reverse(sort.StringSlice(matches)))
		paths = append(paths, matches...)
	}
	for _, path := range paths {
		if isExecutable(path) {
			return path, true
		}
	}
	return "", false
}

func isExecutable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Mode()&0o111 != 0
}

func isNodeScript(path string) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	data := make([]byte, 256)
	count, err := file.Read(data)
	if err != nil && !errors.Is(err, io.EOF) {
		return false
	}
	line := string(data[:count])
	if newline := strings.IndexByte(line, '\n'); newline >= 0 {
		line = line[:newline]
	}
	return strings.Contains(strings.ToLower(line), "node")
}
