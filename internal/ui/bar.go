package ui

import (
	"math"
	"strings"
)

func RenderBar(used, limit float64, width int) string {
	if limit <= 0 || width <= 0 {
		return "n/a"
	}
	ratio := used / limit
	if ratio < 0 {
		ratio = 0
	}
	if ratio > 1 {
		ratio = 1
	}
	filled := int(math.Round(ratio * float64(width)))
	if filled > width {
		filled = width
	}
	return strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
}
