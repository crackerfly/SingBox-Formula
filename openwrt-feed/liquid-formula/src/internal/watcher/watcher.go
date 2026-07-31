package watcher

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/haierkeys/singbox-subscribe-convert/global"
	"go.uber.org/zap"
)

const defaultDebounce = time.Second

type Options struct {
	Debounce time.Duration
	Ready    chan<- struct{}
}

type callbackTarget struct {
	templateName string
	isNode       bool
}

func Start(
	ctx context.Context,
	cfg *global.Config,
	logger *zap.Logger,
	onNodeChange func(context.Context) error,
	onTemplateChange func(context.Context, string) error,
) error {
	return StartWithOptions(ctx, cfg, logger, onNodeChange, onTemplateChange, Options{Debounce: defaultDebounce})
}

// StartWithOptions watches only configured canonical cache paths. It uses one
// in-loop scheduler for trailing per-path debounce, so no callback goroutine
// can outlive Start after its context ends.
func StartWithOptions(
	ctx context.Context,
	cfg *global.Config,
	logger *zap.Logger,
	onNodeChange func(context.Context) error,
	onTemplateChange func(context.Context, string) error,
	options Options,
) error {
	if logger == nil {
		logger = zap.NewNop()
	}
	if options.Debounce <= 0 {
		options.Debounce = defaultDebounce
	}
	watch, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("create watcher: %w", err)
	}
	defer watch.Close()
	if err := watch.Add(cfg.Cache.Directory); err != nil {
		return fmt.Errorf("watch cache directory %s: %w", cfg.Cache.Directory, err)
	}

	targets := make(map[string]callbackTarget)
	nodePath, err := canonicalPath(cfg.GetNodeFilePath())
	if err != nil {
		return err
	}
	targets[nodePath] = callbackTarget{isNode: true}
	for name := range cfg.GetEnabledTemplates() {
		path, pathErr := canonicalPath(cfg.GetTemplateFilePathByName(name))
		if pathErr != nil {
			return pathErr
		}
		targets[path] = callbackTarget{templateName: name}
	}
	if options.Ready != nil {
		close(options.Ready)
	}
	logger.Info("✓ File watcher started", zap.String("monitoring", cfg.Cache.Directory))

	deadlines := make(map[string]time.Time)
	var timer *time.Timer
	var timerChannel <-chan time.Time
	resetTimer := func() {
		if len(deadlines) == 0 {
			if timer != nil && !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timerChannel = nil
			return
		}
		var earliest time.Time
		for _, deadline := range deadlines {
			if earliest.IsZero() || deadline.Before(earliest) {
				earliest = deadline
			}
		}
		delay := time.Until(earliest)
		if delay < 0 {
			delay = 0
		}
		if timer == nil {
			timer = time.NewTimer(delay)
		} else {
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(delay)
		}
		timerChannel = timer.C
	}
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()

	for {
		select {
		case <-ctx.Done():
			logger.Info("File watcher stopped")
			return nil
		case event, ok := <-watch.Events:
			if !ok {
				return nil
			}
			if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename) == 0 {
				continue
			}
			path, pathErr := canonicalPath(event.Name)
			if pathErr != nil {
				logger.Warn("Ignore non-canonical watcher event", zap.String("file", event.Name), zap.Error(pathErr))
				continue
			}
			if _, relevant := targets[path]; !relevant {
				continue
			}
			deadlines[path] = time.Now().Add(options.Debounce)
			resetTimer()
		case <-timerChannel:
			now := time.Now()
			due := make([]string, 0)
			for path, deadline := range deadlines {
				if !deadline.After(now) {
					due = append(due, path)
					delete(deadlines, path)
				}
			}
			sort.Strings(due)
			resetTimer()
			for _, path := range due {
				if err := ctx.Err(); err != nil {
					return nil
				}
				target := targets[path]
				if target.isNode {
					if onNodeChange != nil {
						if err := onNodeChange(ctx); err != nil {
							logger.Error("Error reloading node data", zap.String("file", path), zap.Error(err))
						}
					}
					continue
				}
				if onTemplateChange != nil {
					if err := onTemplateChange(ctx, target.templateName); err != nil {
						logger.Error("Error reloading template", zap.String("file", path), zap.String("template", target.templateName), zap.Error(err))
					}
				}
			}
		case watchErr, ok := <-watch.Errors:
			if !ok {
				return nil
			}
			logger.Error("Watcher error", zap.Error(watchErr))
		}
	}
}

func canonicalPath(path string) (string, error) {
	absPath, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", fmt.Errorf("resolve cache path %s: %w", path, err)
	}
	realDirectory, err := filepath.EvalSymlinks(filepath.Dir(absPath))
	if err != nil {
		return "", fmt.Errorf("resolve cache directory %s: %w", filepath.Dir(absPath), err)
	}
	return filepath.Join(realDirectory, filepath.Base(absPath)), nil
}
