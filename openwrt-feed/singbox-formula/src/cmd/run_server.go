package cmd

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/haierkeys/singbox-subscribe-convert/global"
	"github.com/haierkeys/singbox-subscribe-convert/internal/handler"
	"github.com/haierkeys/singbox-subscribe-convert/internal/watcher"
	"github.com/haierkeys/singbox-subscribe-convert/pkg/fileurl"
	"github.com/haierkeys/singbox-subscribe-convert/pkg/logger"

	"go.uber.org/zap"
)

// ListenFunc opens the listener owned by a Server.
type ListenFunc func(network, address string) (net.Listener, error)

const defaultShutdownTimeout = 5 * time.Second

// serverRuntime contains the effects needed to start a Server. Tests replace
// these functions so lifecycle behavior can be driven without network or file
// system side effects.
type serverRuntime struct {
	listen     ListenFunc
	initialize func(*Server, *global.Config, string) error
	runUpdater func(context.Context, *global.Config)
	runWatcher func(context.Context, *global.Config)
}

// Server 服务器主结构体
type Server struct {
	logger     *zap.Logger        // 日志记录器
	httpServer *http.Server       // HTTP 服务器实例
	listener   net.Listener       // Server 唯一拥有的监听器
	ctx        context.Context    // 上下文，用于控制后台任务
	cancel     context.CancelFunc // 取消函数，用于停止后台任务

	workers sync.WaitGroup

	done chan struct{}

	terminalMu  sync.RWMutex
	terminalErr error

	shutdownOnce    sync.Once
	shutdownErr     error
	shutdownTimeout time.Duration
}

// NewServer 加载候选配置并创建服务器实例。
func NewServer(runEnv *runFlags) (*Server, error) {
	// 初始化临时 logger（在配置加载前使用）
	tempLogger, err := zap.NewProduction()
	if err != nil {
		return nil, fmt.Errorf("failed to create temp logger: %w", err)
	}

	// 确保临时 logger 在函数返回前刷新缓冲
	defer func() {
		_ = tempLogger.Sync()
	}()

	// 只加载候选配置；真实服务器成功启动前不提交全局配置。
	cfg, configRealpath, err := global.LoadCandidate(runEnv.config)
	if err != nil {
		tempLogger.Error("Error loading config", zap.Error(err))
		return nil, fmt.Errorf("failed to load config: %w", err)
	}

	s, err := newServerFromConfig(cfg, configRealpath, productionServerRuntime())
	if err != nil {
		tempLogger.Error("Failed to initialize server", zap.Error(err))
		return nil, err
	}

	s.logger.Info("✓ Server is running",
		zap.Int("port", cfg.Server.Port),
		zap.String("address", fmt.Sprintf("http://localhost:%d", cfg.Server.Port)),
	)
	s.printServerInfo(cfg.Server.Port)
	return s, nil
}

func productionServerRuntime() serverRuntime {
	var server *Server
	return serverRuntime{
		listen: net.Listen,
		initialize: func(s *Server, cfg *global.Config, configPath string) error {
			server = s
			return s.initialize(cfg, configPath)
		},
		runUpdater: func(ctx context.Context, cfg *global.Config) {
			server.startAutoUpdate(ctx, cfg)
		},
		runWatcher: func(ctx context.Context, cfg *global.Config) {
			if err := watcher.Start(ctx, cfg, server.logger, handler.ReloadDataContext, handler.ReloadTemplateByNameContext); err != nil {
				server.logger.Error("Cache watcher stopped with error", zap.Error(err))
			}
		},
	}
}

func newServerFromConfig(cfg *global.Config, realpath string, runtime serverRuntime) (*Server, error) {
	if cfg == nil {
		return nil, errors.New("server config is nil")
	}
	if runtime.listen == nil {
		return nil, errors.New("server listen function is nil")
	}
	if runtime.initialize == nil {
		return nil, errors.New("server initialize function is nil")
	}
	if runtime.runUpdater == nil {
		return nil, errors.New("server updater function is nil")
	}
	if runtime.runWatcher == nil {
		return nil, errors.New("server watcher function is nil")
	}

	address := fmt.Sprintf(":%d", cfg.Server.Port)
	listener, err := runtime.listen("tcp", address)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{
		listener:        listener,
		ctx:             ctx,
		cancel:          cancel,
		done:            make(chan struct{}),
		shutdownTimeout: defaultShutdownTimeout,
	}
	s.httpServer = newHTTPServer(cfg)

	if err := runtime.initialize(s, cfg, realpath); err != nil {
		cancel()
		if closeErr := listener.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close listener after initialization failure: %w", closeErr))
		}
		if s.logger != nil {
			_ = s.logger.Sync()
		}
		return nil, err
	}

	// Initialization is complete and the listener is already bound. Commit the
	// candidate only now, immediately before starting the owned goroutines.
	global.Cfg = cfg
	global.ConfigFile = realpath
	global.Logger = s.logger

	s.workers.Add(2)
	go func() {
		defer s.workers.Done()
		runtime.runUpdater(ctx, cfg)
	}()
	go func() {
		defer s.workers.Done()
		runtime.runWatcher(ctx, cfg)
	}()

	go s.serve()
	return s, nil
}

func newHTTPServer(cfg *global.Config) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/", handler.HandleRequest)
	mux.HandleFunc("/health", handler.HandleHealth)
	mux.HandleFunc("/refresh", handler.HandleRefresh)

	return &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Server.Port),
		Handler:      mux,
		ReadTimeout:  cfg.GetServerReadTimeout(),
		WriteTimeout: cfg.GetServerWriteTimeout(),
		IdleTimeout:  cfg.GetServerIdleTimeout(),
	}
}

func (s *Server) initialize(cfg *global.Config, configPath string) error {
	if err := s.initLogger(cfg); err != nil {
		return err
	}

	s.logStartupInfo(configPath, cfg)
	if err := fileurl.CreatePath(cfg.Cache.Directory, 0755); err != nil {
		return err
	}

	if err := handler.Init(cfg, s.logger); err != nil {
		s.logger.Error("Failed to initialize handler", zap.Error(err))
		return fmt.Errorf("handler init failed: %w", err)
	}
	if err := s.initializeData(cfg); err != nil {
		s.logger.Warn("Data initialization had issues", zap.Error(err))
		fmt.Println("⚠ Warning: Data initialization incomplete, server may not work properly")
	}
	return nil
}

// initLogger 初始化日志系统
func (s *Server) initLogger(cfg *global.Config) error {
	// 如果配置了日志文件且目录不存在，则创建目录
	if cfg.Logging.File != "" && !fileurl.IsExist(cfg.Logging.File) {
		if err := fileurl.CreatePath(cfg.Logging.File, 0755); err != nil {
			return err
		}
	}

	// 根据配置创建 logger
	lg, err := logger.NewLogger(loggerConfigFromGlobal(cfg.Logging))
	if err != nil {
		return fmt.Errorf("failed to init logger: %w", err)
	}

	s.logger = lg
	return nil
}

func loggerConfigFromGlobal(config global.LoggingConfig) logger.Config {
	return logger.Config{
		Level:      config.Level,
		File:       config.File,
		Production: config.Production,
		MaxSize:    config.MaxSize,
		MaxBackups: config.MaxBackups,
		MaxAge:     config.MaxAge,
	}
}

// initializeData 初始化数据（启动时总是获取最新远程文件）
func (s *Server) initializeData(cfg *global.Config) error {
	s.logger.Info("Starting initial fetch of remote files...")
	fmt.Println("Fetching remote files...")

	// 执行首次文件获取
	if err := s.performInitialFetch(cfg); err != nil {
		s.logger.Warn("Initial fetch failed", zap.Error(err))
		return err
	}

	s.logger.Info("✓ Initial fetch completed successfully")
	fmt.Println("✓ Initial fetch completed successfully")
	return nil
}

// logStartupInfo 记录服务器启动信息
func (s *Server) logStartupInfo(configPath string, cfg *global.Config) {
	s.logger.Info("=== Singbox Subscribe Convert Server Starting ===")
	s.logger.Info("Server information",
		zap.String("name", global.Name),
		zap.String("version", global.Version),
		zap.String("git_tag", global.GitTag),
		zap.String("build_time", global.BuildTime),
	)

	enabledTemplates := cfg.GetEnabledTemplates()
	templateNames := make([]string, 0, len(enabledTemplates))
	for name := range enabledTemplates {
		templateNames = append(templateNames, name)
	}

	s.logger.Info("Configuration loaded",
		zap.String("config_file", configPath),
		zap.Int("server_port", cfg.Server.Port),
		zap.String("subscription_url", cfg.Subscription.URL),
		zap.String("default_template", cfg.DefaultTemplate),
		zap.Strings("enabled_templates", templateNames),
		zap.String("cache_directory", cfg.Cache.Directory),
		zap.Duration("auto_refresh_interval", cfg.GetRefreshInterval()),
	)
}

// printServerInfo 在控制台打印服务器信息
func (s *Server) printServerInfo(port int) {
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Printf("✓ Server is running on http://localhost:%d\n", port)
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("\nAvailable endpoints:")
	fmt.Printf("  • Main:    http://localhost:%d/?password=xxx&type=xxx&_t=%d\n", port, time.Now().Unix())
	fmt.Printf("  • Health:  http://localhost:%d/health\n", port)
	fmt.Printf("  • Refresh: http://localhost:%d/refresh?password=xxx\n", port)
	fmt.Println("\nPress Ctrl+C to stop")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
}

func (s *Server) serve() {
	err := s.httpServer.Serve(s.listener)
	if errors.Is(err, http.ErrServerClosed) {
		err = nil
	}
	if err != nil && s.logger != nil {
		s.logger.Error("Server error", zap.Error(err))
	}

	// A fatal Serve return and a clean Shutdown converge here. Cancel workers,
	// drain every accepted HTTP connection, then wait for the remaining owned
	// work before publishing the original Serve result.
	s.cancel()
	if cleanupErr := s.httpServer.Shutdown(context.Background()); cleanupErr != nil && s.logger != nil {
		s.logger.Error("Server HTTP cleanup error", zap.Error(cleanupErr))
	}
	s.workers.Wait()

	s.terminalMu.Lock()
	s.terminalErr = err
	s.terminalMu.Unlock()

	if s.logger != nil {
		_ = s.logger.Sync()
	}
	close(s.done)
}

// Done returns the one close-only terminal broadcast for this Server.
func (s *Server) Done() <-chan struct{} {
	return s.done
}

// Err returns the stable terminal Serve result. It is guaranteed stable after
// Done closes and is safe for concurrent observers.
func (s *Server) Err() error {
	s.terminalMu.RLock()
	defer s.terminalMu.RUnlock()
	return s.terminalErr
}

// Shutdown is idempotent and waits for the same terminal state observed via
// Done. It cancels workers before stopping HTTP so both can drain together.
func (s *Server) Shutdown() error {
	s.shutdownOnce.Do(func() {
		s.cancel()

		ctx, cancel := context.WithTimeout(context.Background(), s.shutdownTimeout)
		s.shutdownErr = s.httpServer.Shutdown(ctx)
		cancel()

		// Shutdown has already closed the listener even if its context expired.
		// serve's background Shutdown still owns draining active handlers.
		<-s.done
		if s.logger != nil {
			if s.shutdownErr != nil {
				s.logger.Error("Server shutdown error", zap.Error(s.shutdownErr))
			} else {
				s.logger.Info("Server stopped gracefully")
			}
		}
	})
	return s.shutdownErr
}

// performInitialFetch 执行首次文件获取
func (s *Server) performInitialFetch(cfg *global.Config) error {
	s.logger.Info("Starting initial fetch of remote files")
	_, err := handler.Refresh(s.ctx, handler.TriggerInitial)
	return err
}

// startAutoUpdate 启动定期自动更新服务
func (s *Server) startAutoUpdate(ctx context.Context, cfg *global.Config) {
	// 创建定时器
	ticker := time.NewTicker(cfg.GetRefreshInterval())
	defer ticker.Stop()

	s.logger.Info("✓ Auto-update service started",
		zap.Duration("interval", cfg.GetRefreshInterval()),
	)

	updateCount := 0

	for {
		select {
		case <-ctx.Done():
			// 收到停止信号，退出自动更新
			s.logger.Info("Auto-update service stopped",
				zap.Int("total_updates", updateCount),
			)
			return

		case <-ticker.C:
			// 定时器触发，执行自动更新
			updateCount++
			s.performAutoUpdate(ctx, updateCount, cfg)
		}
	}
}

// performAutoUpdate 执行自动更新
func (s *Server) performAutoUpdate(ctx context.Context, updateNum int, cfg *global.Config) {
	s.logger.Info("Auto-update triggered", zap.Int("update_number", updateNum))
	fmt.Printf("\n[%s] Auto-updating files (#%d)...\n",
		time.Now().Format("2006-01-02 15:04:05"), updateNum)

	_, refreshErr := handler.Refresh(ctx, handler.TriggerAuto)
	hasError := refreshErr != nil

	// 记录更新结果
	if hasError {
		s.logger.Warn("Auto-update completed with errors",
			zap.Int("update_number", updateNum),
		)
		fmt.Printf("⚠ Auto-update #%d completed with errors\n", updateNum)
	} else {
		s.logger.Info("Auto-update completed successfully",
			zap.Int("update_number", updateNum),
		)
		fmt.Printf("✓ Auto-update #%d completed\n", updateNum)
	}

	// 计算并显示下次更新时间
	nextUpdate := time.Now().Add(cfg.GetRefreshInterval())
	s.logger.Info("Next update scheduled",
		zap.Time("next_update", nextUpdate),
		zap.Duration("interval", cfg.GetRefreshInterval()),
	)
	fmt.Printf("Next update: %s\n", nextUpdate.Format("2006-01-02 15:04:05"))
}
