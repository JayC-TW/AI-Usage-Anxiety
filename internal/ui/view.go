package ui

import (
	"context"
	"fmt"
	"math"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"aiusage/internal/collector"
	"aiusage/internal/model"
	"aiusage/internal/security"
)

func Render(snapshot collector.Snapshot, now time.Time, width int, interval time.Duration) string {
	return RenderWithThresholds(snapshot, now, width, interval, Thresholds{Warn: 75, Danger: 90})
}

type Thresholds struct {
	Warn   float64
	Danger float64
}

func RenderWithThresholds(snapshot collector.Snapshot, now time.Time, width int, interval time.Duration, thresholds Thresholds) string {
	if width <= 0 {
		width = 80
	}
	if interval <= 0 {
		interval = 3 * time.Minute
	}
	lines := []string{fmt.Sprintf("AI Usage Anxiety %s · auto update %s", now.Format("15:04:05"), formatInterval(interval))}
	for _, status := range snapshot.Statuses {
		if !status.Available && status.Err == nil {
			continue
		}
		lines = append(lines, "  "+strings.ToUpper(status.Name))
		if status.Err != nil {
			if len(status.Usages) == 0 {
				lines = append(lines, "  n/a ("+safeMessage(status.Err)+")")
			} else {
				lines = append(lines, "  stale: "+safeMessage(status.Err))
				for _, usage := range status.Usages {
					lines = append(lines, renderUsage(usage, now, width, interval, thresholds, true))
				}
			}
			continue
		}
		if len(status.Usages) == 0 {
			lines = append(lines, "  n/a")
			continue
		}
		for _, usage := range status.Usages {
			lines = append(lines, renderUsage(usage, now, width, interval, thresholds, false))
		}
	}
	for index := range lines {
		lines[index] = truncateTerminalLine(lines[index], width)
	}
	return strings.Join(lines, "\n")
}

func renderUsage(usage model.Usage, now time.Time, width int, interval time.Duration, thresholds Thresholds, staleOverride bool) string {
	stale := staleOverride || (!usage.FetchedAt.IsZero() && usage.FetchedAt.Before(now.Add(-3*interval)))
	reset := "unknown"
	if usage.ResetAt != nil {
		reset = usage.ResetAt.Local().Format("Jan 2 15:04")
	}
	if !usage.Known() {
		note := safeText(usage.Note)
		if note == "" {
			note = "quota unknown"
		} else if !strings.Contains(strings.ToLower(note), "quota") {
			note = "quota unknown; " + note
		}
		amount := formatUsageAmount(usage)
		if amount == "" {
			return fmt.Sprintf("  %-8s n/a (%s)%s", usage.Window, note, staleSuffix(stale))
		}
		return fmt.Sprintf("  %-8s %s (%s)%s", usage.Window, amount, note, staleSuffix(stale))
	}
	percent := usage.Used / usage.Limit * 100
	if width < 40 {
		return fmt.Sprintf("  %-8s %5.1f%%%s reset %s", usage.Window, percent, staleSuffix(stale), reset)
	}
	barWidth := 20
	if width < 60 {
		barWidth = 10
	}
	value := RenderBar(usage.Used, usage.Limit, barWidth)
	if os.Getenv("NO_COLOR") == "" {
		value = colorize(value, percent, thresholds)
	}
	return fmt.Sprintf("  %-8s [%s] %5.1f%%%s reset %s", usage.Window, value, percent, staleSuffix(stale), reset)
}

func formatUsageAmount(usage model.Usage) string {
	if math.IsNaN(usage.Used) || math.IsInf(usage.Used, 0) {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(usage.Unit)) {
	case "token", "tokens":
		return formatCompact(usage.Used) + " tokens"
	case "percent":
		return fmt.Sprintf("%s%%", formatCompact(usage.Used))
	case "request", "requests":
		return formatCompact(usage.Used) + " requests"
	default:
		if strings.TrimSpace(usage.Unit) == "" {
			return ""
		}
		return formatCompact(usage.Used) + " " + strings.TrimSpace(usage.Unit)
	}
}

func formatCompact(value float64) string {
	abs := math.Abs(value)
	switch {
	case abs >= 1_000_000_000:
		return fmt.Sprintf("%.1fB", value/1_000_000_000)
	case abs >= 1_000_000:
		return fmt.Sprintf("%.1fM", value/1_000_000)
	case abs >= 1_000:
		return fmt.Sprintf("%.1fK", value/1_000)
	case value == math.Trunc(value):
		return fmt.Sprintf("%.0f", value)
	default:
		return fmt.Sprintf("%.1f", value)
	}
}

func formatInterval(interval time.Duration) string {
	if interval%time.Minute == 0 {
		return fmt.Sprintf("%dm", int(interval/time.Minute))
	}
	return interval.Round(time.Second).String()
}

func staleSuffix(stale bool) string {
	if stale {
		return "~"
	}
	return ""
}

func colorize(value string, percent float64, thresholds Thresholds) string {
	color := "32"
	if percent >= thresholds.Danger {
		color = "31"
	} else if percent >= thresholds.Warn {
		color = "33"
	}
	return "\x1b[" + color + "m" + value + "\x1b[0m"
}

func safeMessage(err error) string {
	if err == nil {
		return ""
	}
	if err == context.Canceled || err == context.DeadlineExceeded {
		return "cancelled"
	}
	return safeText(err.Error())
}

func safeText(value string) string {
	value = security.Mask(value)
	value = strings.Join(strings.Fields(value), " ")
	if len(value) > 100 {
		return value[:100] + "..."
	}
	return value
}

func truncateTerminalLine(value string, width int) string {
	if width <= 0 {
		return ""
	}
	if terminalVisibleWidth(value) <= width {
		return value
	}
	var result strings.Builder
	result.Grow(len(value))
	visible := 0
	for index := 0; index < len(value); {
		if value[index] == '\x1b' {
			end := ansiSequenceEnd(value, index)
			result.WriteString(value[index:end])
			index = end
			continue
		}
		_, size := utf8.DecodeRuneInString(value[index:])
		if visible >= width-1 {
			result.WriteRune('…')
			for index < len(value) {
				if value[index] == '\x1b' {
					end := ansiSequenceEnd(value, index)
					result.WriteString(value[index:end])
					index = end
					continue
				}
				_, size = utf8.DecodeRuneInString(value[index:])
				index += size
			}
			return result.String()
		}
		result.WriteString(value[index : index+size])
		visible++
		index += size
	}
	return result.String()
}

func terminalVisibleWidth(value string) int {
	visible := 0
	for index := 0; index < len(value); {
		if value[index] == '\x1b' {
			index = ansiSequenceEnd(value, index)
			continue
		}
		_, size := utf8.DecodeRuneInString(value[index:])
		visible++
		index += size
	}
	return visible
}

func ansiSequenceEnd(value string, start int) int {
	if start+1 >= len(value) || value[start+1] != '[' {
		return min(start+1, len(value))
	}
	for index := start + 2; index < len(value); index++ {
		if value[index] >= '@' && value[index] <= '~' {
			return index + 1
		}
	}
	return len(value)
}
