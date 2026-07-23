/*
 * Copyright (C) 2020-2022, IrineSistiana
 *
 * This file is part of mosdns.
 *
 * mosdns is free software: you can redistribute it and/or modify
 * it under the terms of the GNU General Public License as published by
 * the Free Software Foundation, either version 3 of the License, or
 * (at your option) any later version.
 *
 * mosdns is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with this program.  If not, see <https://www.gnu.org/licenses/>.
 */

package logger

import (
	"fmt"
	"os"

	"github.com/haierkeys/singbox-subscribe-convert/pkg/fileurl"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"
)

type Config struct {
	// Level, See also zapcore.ParseLevel.
	Level string `yaml:"level"`

	// File that logger will be writen into.
	// Default is stderr.
	File string `yaml:"file"`

	// Production enables json output.
	Production bool `yaml:"production"`

	MaxSize    int `yaml:"max_size"`
	MaxBackups int `yaml:"max_backups"`
	MaxAge     int `yaml:"max_age"`
}

var (
	stdout = zapcore.Lock(os.Stdout)
	stderr = zapcore.Lock(os.Stderr)
	lvl    = zap.NewAtomicLevelAt(zap.InfoLevel)
	l      = zap.New(zapcore.NewCore(zapcore.NewConsoleEncoder(zap.NewDevelopmentEncoderConfig()), stderr, lvl))
	s      = l.Sugar()

	nop = zap.NewNop()
)

type fileSinkFactory func(Config) (zapcore.WriteSyncer, error)

func NewLogger(lc Config) (*zap.Logger, error) {
	return newLoggerWithSinks(lc, stdout, stderr, func(config Config) (zapcore.WriteSyncer, error) {
		writer := &lumberjack.Logger{
			Filename:   config.File,
			MaxSize:    config.MaxSize,
			MaxBackups: config.MaxBackups,
			MaxAge:     config.MaxAge,
			Compress:   false,
		}
		return zapcore.Lock(zapcore.AddSync(writer)), nil
	})
}

func newLoggerWithSinks(lc Config, stdoutSink, stderrSink zapcore.WriteSyncer, newFileSink fileSinkFactory) (*zap.Logger, error) {
	level, err := zapcore.ParseLevel(lc.Level)
	if err != nil {
		return nil, fmt.Errorf("invalid log level: %w", err)
	}
	if stdoutSink == nil || stderrSink == nil {
		return nil, fmt.Errorf("stdout and stderr log sinks are required")
	}

	consoleEncoderConfig := zap.NewDevelopmentEncoderConfig()
	lowPriority := zap.LevelEnablerFunc(func(candidate zapcore.Level) bool {
		return candidate >= level && candidate < zapcore.ErrorLevel
	})
	highPriority := zap.LevelEnablerFunc(func(candidate zapcore.Level) bool {
		return candidate >= level && candidate >= zapcore.ErrorLevel
	})
	cores := []zapcore.Core{
		zapcore.NewCore(zapcore.NewConsoleEncoder(consoleEncoderConfig), zapcore.Lock(stdoutSink), lowPriority),
		zapcore.NewCore(zapcore.NewConsoleEncoder(consoleEncoderConfig), zapcore.Lock(stderrSink), highPriority),
	}

	if lc.File != "" {
		if err := fileurl.CreatePath(lc.File, 0o755); err != nil {
			return nil, fmt.Errorf("create log directory: %w", err)
		}
		if newFileSink == nil {
			return nil, fmt.Errorf("file log sink factory is required")
		}
		fileSink, err := newFileSink(lc)
		if err != nil {
			return nil, fmt.Errorf("open log file: %w", err)
		}
		var fileEncoder zapcore.Encoder
		if lc.Production {
			fileEncoder = zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig())
		} else {
			fileEncoder = zapcore.NewConsoleEncoder(zap.NewDevelopmentEncoderConfig())
		}
		fileLevels := zap.LevelEnablerFunc(func(candidate zapcore.Level) bool {
			return candidate >= level
		})
		cores = append(cores, zapcore.NewCore(fileEncoder, fileSink, fileLevels))
	}

	return zap.New(zapcore.NewTee(cores...)), nil
}

// L is a global logger.
func L() *zap.Logger {
	return l
}

// SetLevel sets the log level for the global logger.
func SetLevel(l zapcore.Level) {
	lvl.SetLevel(l)
}

// S is a global logger.
func S() *zap.SugaredLogger {
	return s
}

// Nop is a logger that never writes out logs.
func Nop() *zap.Logger {
	return nop
}
