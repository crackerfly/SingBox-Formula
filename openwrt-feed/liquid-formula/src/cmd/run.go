package cmd

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/haierkeys/singbox-subscribe-convert/global"
	"github.com/haierkeys/singbox-subscribe-convert/pkg/fileurl"

	"github.com/spf13/cobra"
	"go.uber.org/zap"
)

type runFlags struct {
	dir     string // 项目根目录
	port    string // 启动端口
	runMode string // 启动模式
	config  string // 指定要使用的配置文件路径
}

var (
	runEnv = new(runFlags)
)

func init() {
	runCommand := &cobra.Command{
		Use:   "run [-c config_file] [-d working_dir] [-p port]",
		Short: "Run service",
		PreRunE: func(cmd *cobra.Command, args []string) error {
			return initConfig()
		},
		RunE: runServer,
	}

	rootCmd.AddCommand(runCommand)

	fs := runCommand.Flags()
	fs.StringVarP(&runEnv.dir, "dir", "d", "", "working directory")
	fs.StringVarP(&runEnv.port, "port", "p", "", "server port")
	fs.StringVarP(&runEnv.runMode, "mode", "m", "", "run mode (dev/prod)")
	fs.StringVarP(&runEnv.config, "config", "c", "", "config file path")
}

func runServer(cmd *cobra.Command, args []string) error {
	canonicalConfigPath, err := fileurl.GetAbsPath(runEnv.config, "")
	if err != nil {
		return fmt.Errorf("resolve config path: %w", err)
	}

	hooks := runServerHooks{
		load: global.LoadCandidate,
		newServer: func(cfg *global.Config, realpath string) (serverInstance, error) {
			server, err := newServerFromConfig(cfg, realpath, productionServerRuntime())
			if err != nil {
				return nil, err
			}

			server.logger.Info("✓ Server is running",
				zap.Int("port", cfg.Server.Port),
				zap.String("address", fmt.Sprintf("http://localhost:%d", cfg.Server.Port)),
			)
			server.printServerInfo(cfg.Server.Port)
			return server, nil
		},
		logger: func() *zap.Logger {
			if global.Logger == nil {
				return zap.NewNop()
			}
			return global.Logger
		},
		startConfigWatcher: func(ctx context.Context, configPath string, reload func() error, logger *zap.Logger) (configWatcherCloser, error) {
			return startConfigWatcher(ctx, configPath, reload, logger)
		},
		notifySignals: func(ch chan<- os.Signal) {
			signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
		},
		stopSignals: signal.Stop,
	}

	return runServerWithHooks(runEnv.config, canonicalConfigPath, hooks)
}

type configWatcherCloser interface {
	Close()
}

type runServerHooks struct {
	load               candidateLoader
	newServer          serverFactory
	logger             func() *zap.Logger
	startConfigWatcher func(context.Context, string, func() error, *zap.Logger) (configWatcherCloser, error)
	notifySignals      func(chan<- os.Signal)
	stopSignals        func(chan<- os.Signal)
}

func runServerWithHooks(configPath, canonicalConfigPath string, hooks runServerHooks) (result error) {
	if hooks.load == nil {
		return errors.New("server candidate loader is nil")
	}
	if hooks.newServer == nil {
		return errors.New("server factory is nil")
	}
	if hooks.logger == nil {
		return errors.New("server logger provider is nil")
	}
	if hooks.startConfigWatcher == nil {
		return errors.New("config watcher starter is nil")
	}
	if hooks.notifySignals == nil || hooks.stopSignals == nil {
		return errors.New("signal hooks are nil")
	}

	signalChannel := make(chan os.Signal, 1)
	hooks.notifySignals(signalChannel)
	defer hooks.stopSignals(signalChannel)

	supervisor := newServerSupervisor(configPath, hooks.load, hooks.newServer)
	if err := supervisor.Reload(); err != nil {
		_ = supervisor.Shutdown()
		log.Printf("Failed to initialize server: %v\n", err)
		return fmt.Errorf("server initialization failed: %w", err)
	}

	logger := hooks.logger()
	if logger == nil {
		logger = zap.NewNop()
	}
	ctx, cancel := context.WithCancel(context.Background())
	var rootWatcher configWatcherCloser
	defer func() {
		cancel()
		if rootWatcher != nil {
			rootWatcher.Close()
		}
		if err := supervisor.Shutdown(); result == nil && err != nil {
			result = fmt.Errorf("server shutdown failed: %w", err)
		}
	}()

	var err error
	rootWatcher, err = hooks.startConfigWatcher(ctx, canonicalConfigPath, supervisor.Reload, logger)
	if err != nil {
		log.Printf("Failed to start config watcher: %v\n", err)
		logger.Error("Failed to start config watcher", zap.Error(err))
		rootWatcher = nil
	}

	select {
	case receivedSignal := <-signalChannel:
		log.Printf("Received signal: %v\n", receivedSignal)
		logger.Info("Received shutdown signal", zap.String("signal", receivedSignal.String()))
		logger.Info("Server shutting down...")
		cancel()
		if rootWatcher != nil {
			rootWatcher.Close()
			rootWatcher = nil
		}
		shutdownErr := supervisor.Shutdown()
		// A server may become terminal while the signal path is synchronously
		// closing the root watcher. Supervisor.Err is the stable lifecycle result
		// and must take precedence over an otherwise clean signal shutdown.
		if terminalErr := supervisor.Err(); terminalErr != nil {
			return fmt.Errorf("server terminated during shutdown: %w", terminalErr)
		}
		if shutdownErr != nil {
			return fmt.Errorf("server shutdown failed: %w", shutdownErr)
		}
		logger.Info("Server shutdown complete")
		return nil
	case <-supervisor.Done():
		if err := supervisor.Err(); err != nil {
			return fmt.Errorf("server terminated: %w", err)
		}
		return nil
	}
}

const configWatcherDebounceInterval = 100 * time.Millisecond

// ConfigWatcher owns only the root config subscription and reload callback.
// The ServerSupervisor remains the sole owner of every concrete Server.
type ConfigWatcher struct {
	watcher    *fsnotify.Watcher
	ctx        context.Context
	cancel     context.CancelFunc
	configPath string
	reload     func() error
	logger     *zap.Logger
	done       chan struct{}
	stopOnce   sync.Once
}

// startConfigWatcher watches the canonical file's parent directory so atomic
// replacement cannot detach the subscription from the configured filename.
func startConfigWatcher(parentCtx context.Context, canonicalConfigPath string, reload func() error, logger *zap.Logger) (*ConfigWatcher, error) {
	if reload == nil {
		return nil, errors.New("config reload callback is nil")
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	configPath, err := filepath.Abs(canonicalConfigPath)
	if err != nil {
		return nil, fmt.Errorf("resolve config watcher path: %w", err)
	}
	configPath = filepath.Clean(configPath)

	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("create config watcher: %w", err)
	}
	if err := w.Add(filepath.Dir(configPath)); err != nil {
		_ = w.Close()
		return nil, fmt.Errorf("watch config parent directory: %w", err)
	}

	ctx, cancel := context.WithCancel(parentCtx)
	cw := &ConfigWatcher{
		watcher:    w,
		ctx:        ctx,
		cancel:     cancel,
		configPath: configPath,
		reload:     reload,
		logger:     logger,
		done:       make(chan struct{}),
	}
	go cw.handleEvents()

	logger.Info("Config file watcher started", zap.String("file", configPath))
	return cw, nil
}

func (cw *ConfigWatcher) handleEvents() {
	defer close(cw.done)
	defer cw.stop()

	var debounceTimer *time.Timer
	var debounce <-chan time.Time
	defer func() {
		if debounceTimer != nil {
			debounceTimer.Stop()
		}
	}()

	for {
		select {
		case <-cw.ctx.Done():
			cw.logger.Info("Config watcher stopped")
			return
		case event, ok := <-cw.watcher.Events:
			if !ok {
				return
			}
			eventPath, err := filepath.Abs(event.Name)
			if err != nil || filepath.Clean(eventPath) != cw.configPath {
				continue
			}
			if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename) == 0 {
				continue
			}
			cw.logger.Debug("Config file change detected",
				zap.String("event", event.Op.String()),
				zap.String("file", eventPath))
			if debounceTimer == nil {
				debounceTimer = time.NewTimer(configWatcherDebounceInterval)
			} else {
				if !debounceTimer.Stop() {
					select {
					case <-debounceTimer.C:
					default:
					}
				}
				debounceTimer.Reset(configWatcherDebounceInterval)
			}
			debounce = debounceTimer.C
		case <-debounce:
			debounce = nil
			cw.logger.Info("Config file changed, reloading...", zap.String("file", cw.configPath))
			if err := cw.reload(); err != nil {
				cw.logger.Error("Failed to reload server", zap.Error(err))
			} else {
				cw.logger.Info("Server reloaded successfully")
			}
		case err, ok := <-cw.watcher.Errors:
			if !ok {
				return
			}
			cw.logger.Error("Config watcher error", zap.Error(err))
		}
	}
}

func (cw *ConfigWatcher) stop() {
	cw.stopOnce.Do(func() {
		cw.cancel()
		_ = cw.watcher.Close()
	})
}

// Close is idempotent and returns only after event handling and the underlying
// fsnotify watcher have stopped.
func (cw *ConfigWatcher) Close() {
	if cw == nil {
		return
	}
	cw.stop()
	<-cw.done
}

// initConfig 初始化配置文件
func initConfig() error {
	// 切换工作目录
	if err := changeWorkingDir(runEnv.dir); err != nil {
		return err
	}

	// 查找或创建配置文件
	configPath, err := findOrCreateConfig()
	if err != nil {
		return err
	}

	runEnv.config = configPath
	log.Printf("Using config file: %s\n", configPath)
	return nil
}

// changeWorkingDir 切换工作目录
func changeWorkingDir(dir string) error {
	if dir == "" {
		return nil
	}

	if err := os.Chdir(dir); err != nil {
		return fmt.Errorf("failed to change working directory to %s: %w", dir, err)
	}

	absDir, _ := filepath.Abs(dir)
	log.Printf("Working directory changed to: %s\n", absDir)
	return nil
}

// findOrCreateConfig 查找或创建配置文件
func findOrCreateConfig() (string, error) {
	// 如果指定了配置文件,直接使用
	if runEnv.config != "" {
		if fileurl.IsExist(runEnv.config) {
			return runEnv.config, nil
		}
		log.Printf("Warning: Specified config file not found: %s\n", runEnv.config)
	}

	// 按优先级查找配置文件
	configPaths := []string{
		"config/config-dev.yaml",
		"config.yaml",
		"config/config.yaml",
	}

	for _, path := range configPaths {
		if fileurl.IsExist(path) {
			return path, nil
		}
	}

	// 如果都不存在,创建默认配置
	defaultPath := "config/config.yaml"
	if err := createDefaultConfig(defaultPath); err != nil {
		return "", err
	}

	return defaultPath, nil
}

// createDefaultConfig 创建默认配置文件
func createDefaultConfig(path string) error {
	log.Printf("Config file not found, creating default: %s\n", path)

	// 创建目录
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	// 创建文件
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("failed to create config file: %w", err)
	}
	defer file.Close()

	// 写入默认配置
	if _, err := file.WriteString(configDefault); err != nil {
		return fmt.Errorf("failed to write default config: %w", err)
	}

	log.Printf("✓ Default config file created: %s\n", path)
	printConfigInstructions()

	return nil
}

// printConfigInstructions 打印配置说明
func printConfigInstructions() {
	fmt.Println("\n" + strings.Repeat("=", 60))
	fmt.Println("⚠️  IMPORTANT: Please configure the following settings")
	fmt.Println(strings.Repeat("=", 60))
	fmt.Println("\n📝 Edit the config file and set:")
	fmt.Println("   1. Server password for authentication")
	fmt.Println("   2. node_file_url - URL to your node subscription")
	fmt.Println("   3. template_url - URL to your configuration template")
	fmt.Println("\n💡 After editing, restart the server")
	fmt.Println(strings.Repeat("=", 60) + "\n")
}
