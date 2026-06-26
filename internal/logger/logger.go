package logger

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"time"
)

// Level represents log level
type Level int

const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
)

// ParseLevel parses a string log level
func ParseLevel(s string) Level {
	switch s {
	case "debug":
		return LevelDebug
	case "info":
		return LevelInfo
	case "warn":
		return LevelWarn
	case "error":
		return LevelError
	default:
		return LevelInfo
	}
}

// Logger is the logger interface
type Logger struct {
	level  Level
	format string
	logger *log.Logger
	file   *os.File
}

// LogEntry represents a log entry
type LogEntry struct {
	Timestamp   string                 `json:"timestamp"`
	Level       string                 `json:"level"`
	Message     string                 `json:"message"`
	TaskID      string                 `json:"task_id,omitempty"`
	Provider    string                 `json:"provider,omitempty"`
	Event       string                 `json:"event,omitempty"`
	From        string                 `json:"from,omitempty"`
	To          string                 `json:"to,omitempty"`
	Error       string                 `json:"error,omitempty"`
	Score       float64                `json:"score,omitempty"`
	Threshold   float64                `json:"threshold,omitempty"`
	Duration    string                 `json:"duration,omitempty"`
	Extra       map[string]interface{} `json:"extra,omitempty"`
}

// New creates a new logger
func New(level, format, file string) (*Logger, error) {
	l := &Logger{
		level:  ParseLevel(level),
		format: format,
	}

	var output io.Writer = os.Stdout

	if file != "" {
		f, err := os.OpenFile(file, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			return nil, fmt.Errorf("failed to open log file: %w", err)
		}
		l.file = f
		output = io.MultiWriter(os.Stdout, f)
	}

	l.logger = log.New(output, "", 0)
	return l, nil
}

// Close closes the logger
func (l *Logger) Close() {
	if l.file != nil {
		l.file.Close()
	}
}

func (l *Logger) log(level Level, msg string, entry *LogEntry) {
	if level < l.level {
		return
	}

	if entry == nil {
		entry = &LogEntry{}
	}

	entry.Timestamp = time.Now().Format(time.RFC3339)
	entry.Level = level.String()
	entry.Message = msg

	if l.format == "json" {
		data, _ := json.Marshal(entry)
		l.logger.Println(string(data))
	} else {
		l.logger.Printf("[%s] %s: %s", entry.Level, entry.Timestamp, msg)
	}
}

// Debug logs a debug message
func (l *Logger) Debug(msg string, entry ...*LogEntry) {
	var e *LogEntry
	if len(entry) > 0 {
		e = entry[0]
	}
	l.log(LevelDebug, msg, e)
}

// Info logs an info message
func (l *Logger) Info(msg string, entry ...*LogEntry) {
	var e *LogEntry
	if len(entry) > 0 {
		e = entry[0]
	}
	l.log(LevelInfo, msg, e)
}

// Warn logs a warning message
func (l *Logger) Warn(msg string, entry ...*LogEntry) {
	var e *LogEntry
	if len(entry) > 0 {
		e = entry[0]
	}
	l.log(LevelWarn, msg, e)
}

// Error logs an error message
func (l *Logger) Error(msg string, entry ...*LogEntry) {
	var e *LogEntry
	if len(entry) > 0 {
		e = entry[0]
	}
	l.log(LevelError, msg, e)
}

// LogFallback logs a fallback event
func (l *Logger) LogFallback(from, to string, err error) {
	l.Warn("Provider fallback triggered", &LogEntry{
		Event: "fallback",
		From:  from,
		To:    to,
		Error: err.Error(),
	})
}

// LogProviderError logs a provider error
func (l *Logger) LogProviderError(provider string, err error) {
	l.Error("Provider error", &LogEntry{
		Event:    "provider_error",
		Provider: provider,
		Error:    err.Error(),
	})
}

// LogQualityPassed logs a quality check passed event
func (l *Logger) LogQualityPassed(provider string, score float64) {
	l.Info("Quality check passed", &LogEntry{
		Event:    "quality_passed",
		Provider: provider,
		Score:    score,
	})
}

// LogQualityFailed logs a quality check failed event
func (l *Logger) LogQualityFailed(provider string, score float64, reason string) {
	l.Warn("Quality check failed", &LogEntry{
		Event:    "quality_failed",
		Provider: provider,
		Score:    score,
		Error:    reason,
	})
}

// LogEvaluationRetry logs an evaluation retry event
func (l *Logger) LogEvaluationRetry(attempt int, err error) {
	l.Warn("Evaluation retry", &LogEntry{
		Event: "evaluation_retry",
		Error: err.Error(),
		Extra: map[string]interface{}{
			"attempt": attempt,
		},
	})
}

// LogEvaluationFailed logs an evaluation failure event
func (l *Logger) LogEvaluationFailed(err error) {
	l.Error("Evaluation failed after retries", &LogEntry{
		Event: "evaluation_failed",
		Error: err.Error(),
	})
}

// String returns the string representation of the level
func (l Level) String() string {
	switch l {
	case LevelDebug:
		return "debug"
	case LevelInfo:
		return "info"
	case LevelWarn:
		return "warn"
	case LevelError:
		return "error"
	default:
		return "info"
	}
}
