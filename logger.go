package main

import (
	"log"
	"strings"
)

type LogLevel int

const (
	LogLevelInfo LogLevel = iota
	LogLevelDebug
)

var currentLogLevel = LogLevelInfo

// SetLogLevel configures the logging level
func SetLogLevel(level string) {
	switch strings.ToLower(level) {
	case "debug":
		currentLogLevel = LogLevelDebug
	case "info", "":
		currentLogLevel = LogLevelInfo
	default:
		currentLogLevel = LogLevelInfo
	}
}

// LogInfo logs high-level operations (always shown)
func LogInfo(format string, args ...interface{}) {
	log.Printf("[INFO] "+format, args...)
}

// LogDebug logs detailed protocol exchanges (only shown in debug mode)
func LogDebug(format string, args ...interface{}) {
	if currentLogLevel >= LogLevelDebug {
		log.Printf("[DEBUG] "+format, args...)
	}
}

// LogError logs errors (always shown)
func LogError(format string, args ...interface{}) {
	log.Printf("[ERROR] "+format, args...)
}

// LogStats logs statistics and summaries (always shown)
func LogStats(format string, args ...interface{}) {
	log.Printf("[STATS] "+format, args...)
}
