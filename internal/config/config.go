package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Interval      time.Duration
	Warn          float64
	Danger        float64
	Providers     map[string]bool
	NotifyEnabled bool
	Warnings      []string
}

const DefaultInterval = 3 * time.Minute

func Default() Config {
	return Config{
		Interval:  DefaultInterval,
		Warn:      75,
		Danger:    90,
		Providers: map[string]bool{"codex": true, "claude": true, "opencode": true},
	}
}

func (c Config) ProviderEnabled(name string) bool {
	return c.Providers != nil && c.Providers[name]
}

func Load(home string) (Config, error) {
	result := Default()
	path := configPath(home)
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return result, fmt.Errorf("open config: %w", err)
	}
	defer file.Close()

	section := ""
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(stripComment(scanner.Text()))
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(line, "["), "]"))
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		known, err := apply(&result, section, strings.TrimSpace(key), strings.TrimSpace(value))
		if err != nil {
			return result, err
		}
		if !known {
			result.Warnings = append(result.Warnings, "ignored config field "+strings.TrimSpace(section+"."+key))
		}
	}
	if err := scanner.Err(); err != nil {
		return result, fmt.Errorf("read config: %w", err)
	}
	if result.Interval <= 0 {
		return result, errors.New("config interval must be positive")
	}
	if result.Warn < 0 || result.Danger < 0 || result.Warn > result.Danger {
		return result, errors.New("config thresholds must satisfy 0 <= warn <= danger")
	}
	return result, nil
}

func configPath(home string) string {
	if root := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); root != "" {
		return filepath.Join(root, "aiusage", "config.toml")
	}
	return filepath.Join(home, ".config", "aiusage", "config.toml")
}

func apply(result *Config, section, key, raw string) (bool, error) {
	value := strings.TrimSpace(raw)
	value = strings.Trim(value, "\"")
	switch section + "." + key {
	case ".interval":
		parsed, err := time.ParseDuration(value)
		if err != nil {
			return true, fmt.Errorf("invalid config interval: %w", err)
		}
		result.Interval = parsed
		return true, nil
	case "thresholds.warn":
		parsed, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return true, fmt.Errorf("invalid warn threshold: %w", err)
		}
		result.Warn = parsed
		return true, nil
	case "thresholds.danger":
		parsed, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return true, fmt.Errorf("invalid danger threshold: %w", err)
		}
		result.Danger = parsed
		return true, nil
	case "providers.codex", "providers.claude", "providers.opencode":
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return true, fmt.Errorf("invalid provider setting %s.%s: %w", section, key, err)
		}
		result.Providers[strings.TrimPrefix(section+"."+key, "providers.")] = parsed
		return true, nil
	case "notify.enabled":
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return true, fmt.Errorf("invalid notify.enabled: %w", err)
		}
		result.NotifyEnabled = parsed
		return true, nil
	}
	return false, nil
}

func stripComment(line string) string {
	quoted := false
	for index, character := range line {
		switch character {
		case '"':
			quoted = !quoted
		case '#':
			if !quoted {
				return line[:index]
			}
		}
	}
	return line
}
