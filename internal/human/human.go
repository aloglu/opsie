// Package human formats numbers for people.
package human

import (
	"fmt"
	"time"
)

// Bytes formats a size compactly: 512B, 3.4K, 21G.
func Bytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	value, exp := float64(n)/unit, 0
	for value >= unit && exp < 5 {
		value /= unit
		exp++
	}
	suffix := "KMGTPE"[exp : exp+1]
	if value < 10 {
		return fmt.Sprintf("%.1f%s", value, suffix)
	}
	return fmt.Sprintf("%.0f%s", value, suffix)
}

// Rate formats bytes per second.
func Rate(perSecond float64) string {
	if perSecond < 0 {
		perSecond = 0
	}
	return Bytes(uint64(perSecond)) + "/s"
}

// Duration formats a long span with its two largest units: 6d 3h, 4h 12m.
func Duration(d time.Duration) string {
	d = max(d, 0)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	minutes := int(d.Minutes()) % 60
	switch {
	case days > 0:
		return fmt.Sprintf("%dd %dh", days, hours)
	case hours > 0:
		return fmt.Sprintf("%dh %dm", hours, minutes)
	default:
		return fmt.Sprintf("%dm", minutes)
	}
}

// Plural returns "1 key" or "3 keys".
func Plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

// Seconds is a short wait, like "180 ms" or "4.2 s".
func Seconds(d time.Duration) string {
	switch {
	case d < time.Second:
		return fmt.Sprintf("%d ms", d.Milliseconds())
	case d < 10*time.Second:
		return fmt.Sprintf("%.1f s", d.Seconds())
	}
	return fmt.Sprintf("%.0f s", d.Seconds())
}
