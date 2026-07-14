package logger

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/zap/zapcore"
)

func TestLoggerRoutesLowPriorityToStdoutAndErrorsToStderr(t *testing.T) {
	var stdout, stderr bytes.Buffer
	logger, err := newLoggerWithSinks(
		Config{Level: "debug"},
		zapcore.AddSync(&stdout),
		zapcore.AddSync(&stderr),
		nil,
	)
	if err != nil {
		t.Fatalf("newLoggerWithSinks() error = %v", err)
	}
	logger.Debug("debug-message")
	logger.Info("info-message")
	logger.Warn("warn-message")
	logger.Error("error-message")

	for _, message := range []string{"debug-message", "info-message", "warn-message"} {
		if got := strings.Count(stdout.String(), message); got != 1 {
			t.Errorf("stdout count for %q = %d, want 1\n%s", message, got, stdout.String())
		}
		if strings.Contains(stderr.String(), message) {
			t.Errorf("stderr unexpectedly contains %q\n%s", message, stderr.String())
		}
	}
	if strings.Contains(stdout.String(), "error-message") {
		t.Fatalf("stdout unexpectedly contains error\n%s", stdout.String())
	}
	if got := strings.Count(stderr.String(), "error-message"); got != 1 {
		t.Fatalf("stderr error count = %d, want 1\n%s", got, stderr.String())
	}
}

func TestLoggerFileReceivesEveryEnabledLevelOnce(t *testing.T) {
	var stdout, stderr, file bytes.Buffer
	config := Config{Level: "debug", File: filepath.Join(t.TempDir(), "server.log")}
	logger, err := newLoggerWithSinks(
		config,
		zapcore.AddSync(&stdout),
		zapcore.AddSync(&stderr),
		func(got Config) (zapcore.WriteSyncer, error) {
			return zapcore.AddSync(&file), nil
		},
	)
	if err != nil {
		t.Fatalf("newLoggerWithSinks() error = %v", err)
	}
	for _, item := range []struct {
		level   zapcore.Level
		message string
	}{{zapcore.DebugLevel, "file-debug"}, {zapcore.InfoLevel, "file-info"}, {zapcore.WarnLevel, "file-warn"}, {zapcore.ErrorLevel, "file-error"}} {
		logger.Log(item.level, item.message)
		if got := strings.Count(file.String(), item.message); got != 1 {
			t.Errorf("file count for %q = %d, want 1\n%s", item.message, got, file.String())
		}
	}
}

func TestLoggerMapsRotationSettingsToFileSink(t *testing.T) {
	var captured Config
	config := Config{
		Level:      "info",
		File:       filepath.Join(t.TempDir(), "server.log"),
		MaxSize:    17,
		MaxBackups: 4,
		MaxAge:     9,
	}
	_, err := newLoggerWithSinks(
		config,
		zapcore.AddSync(&bytes.Buffer{}),
		zapcore.AddSync(&bytes.Buffer{}),
		func(got Config) (zapcore.WriteSyncer, error) {
			captured = got
			return zapcore.AddSync(&bytes.Buffer{}), nil
		},
	)
	if err != nil {
		t.Fatalf("newLoggerWithSinks() error = %v", err)
	}
	if captured.File != config.File || captured.MaxSize != 17 || captured.MaxBackups != 4 || captured.MaxAge != 9 {
		t.Fatalf("file sink config = %+v, want %+v", captured, config)
	}
}

func TestNewLoggerWritesAllEnabledLevelsToRotatingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.log")
	logger, err := NewLogger(Config{
		Level:      "debug",
		File:       path,
		MaxSize:    1,
		MaxBackups: 2,
		MaxAge:     3,
	})
	if err != nil {
		t.Fatalf("NewLogger() error = %v", err)
	}
	logger.Debug("real-file-debug")
	logger.Error("real-file-error")
	_ = logger.Sync()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read rotating log file: %v", err)
	}
	for _, message := range []string{"real-file-debug", "real-file-error"} {
		if got := strings.Count(string(data), message); got != 1 {
			t.Errorf("rotating file count for %q = %d, want 1\n%s", message, got, data)
		}
	}
}
