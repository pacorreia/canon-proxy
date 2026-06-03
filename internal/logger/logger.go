// Package logger provides a thin leveled-logging wrapper around the standard
// library's log package.  All output goes through log.Printf/log.Fatalf so
// that the LogBroadcaster tee set up in main continues to work unchanged.
//
// Log format (key=value pairs, consistent with the rest of the codebase):
//
//	level=<level> component=<component> msg="<message>" [key=value ...]
//
// The level is prepended automatically; callers supply everything after it.
package logger

import (
	"log"
	"strings"
	"sync/atomic"
)

// Level represents a log severity level.
type Level int32

const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
)

// currentLevel is the minimum level that will be emitted.  Defaults to Info.
var currentLevel atomic.Int32

func init() {
	currentLevel.Store(int32(LevelInfo))
}

// SetLevel sets the minimum log level from a string.
// Accepted values (case-insensitive): "debug", "info", "warn"/"warning", "error".
// Unknown values default to "info".
func SetLevel(s string) {
	currentLevel.Store(int32(ParseLevel(s)))
}

// ParseLevel converts a level name to a Level constant.
func ParseLevel(s string) Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return LevelDebug
	case "warn", "warning":
		return LevelWarn
	case "error":
		return LevelError
	default:
		return LevelInfo
	}
}

// LevelName returns the canonical name for a Level.
func LevelName(l Level) string {
	switch l {
	case LevelDebug:
		return "debug"
	case LevelWarn:
		return "warn"
	case LevelError:
		return "error"
	default:
		return "info"
	}
}

// CurrentLevel returns the active minimum level.
func CurrentLevel() Level { return Level(currentLevel.Load()) }

// Debug emits a log line at level=debug.  The format string and args are the
// same as log.Printf, starting after the level= prefix.
func Debug(format string, args ...any) {
	if Level(currentLevel.Load()) <= LevelDebug {
		log.Printf("level=debug "+format, args...)
	}
}

// Info emits a log line at level=info.
func Info(format string, args ...any) {
	if Level(currentLevel.Load()) <= LevelInfo {
		log.Printf("level=info "+format, args...)
	}
}

// Warn emits a log line at level=warn.
func Warn(format string, args ...any) {
	if Level(currentLevel.Load()) <= LevelWarn {
		log.Printf("level=warn "+format, args...)
	}
}

// Error emits a log line at level=error.
func Error(format string, args ...any) {
	if Level(currentLevel.Load()) <= LevelError {
		log.Printf("level=error "+format, args...)
	}
}

// Fatal emits a log line at level=fatal and then calls os.Exit(1) via
// log.Fatalf.  Fatal lines are always emitted regardless of the current level.
func Fatal(format string, args ...any) {
	log.Fatalf("level=fatal "+format, args...)
}
