package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/flosch/pongo2/v6"
	"github.com/haierkeys/singbox-subscribe-convert/global"
	"github.com/haierkeys/singbox-subscribe-convert/internal/fetcher"
	"github.com/haierkeys/singbox-subscribe-convert/internal/refresh"

	"go.uber.org/zap"
)

var (
	cfg    *global.Config
	logger *zap.Logger
)

// NodeFile 节点文件结构
type NodeFile struct {
	Outbounds []map[string]interface{} `json:"outbounds"`
}

// Init 初始化 handler
func Init(c *global.Config, l *zap.Logger) error {
	cfg = c
	if l == nil {
		l = zap.NewNop()
	}
	logger = l
	ensureNotesFilter()
	remoteClient = fetcher.NewClient(c, l)
	refreshManager = refresh.NewManager()
	applySnapshot(emptySnapshot())
	if snapshot, err := loadSnapshotFromDisk(); err != nil {
		logger.Warn("Failed to load complete last-known-good snapshot", zap.Error(err))
	} else {
		applySnapshot(snapshot)
	}

	return nil
}

// ReloadData 重新加载节点数据
func ReloadData() error {
	return ReloadDataContext(context.Background())
}

func ReloadDataContext(ctx context.Context) error {
	return reloadSnapshot(ctx)
}

// ReloadTemplateByName 根据名称重新加载模板
func ReloadTemplateByName(templateName string) error {
	return ReloadTemplateByNameContext(context.Background(), templateName)
}

func ReloadTemplateByNameContext(ctx context.Context, _ string) error {
	return reloadSnapshot(ctx)
}

// ReloadAllTemplates 重新加载所有启用的模板
func ReloadAllTemplates() error {
	return reloadSnapshot(context.Background())
}

// HandleRequest 处理主请求
func HandleRequest(w http.ResponseWriter, r *http.Request) {
	// 如果路径不是根路径，则直接返回 404，不进入鉴权逻辑，避免干扰日志
	if r.URL.Path != "/" {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	fmt.Printf("\n[DEBUG] >>> New Request: %s\n", r.URL.String())
	logger.Info("Request received",
		zap.String("remote_addr", r.RemoteAddr),
		zap.String("path", r.URL.Path),
	)
	queryParams := r.URL.Query()
	setType := queryParams.Get("type")
	password := queryParams.Get("password")
	templateName := queryParams.Get("template")
	// 如果 template 参数为空，回退到 type 参数
	if templateName == "" {
		templateName = setType
	}
	refresh := queryParams.Get("refresh")

	if password != cfg.Auth.Password {
		fmt.Printf("[DEBUG] !!! Auth Failed: expected '%s', got '%s'\n", cfg.Auth.Password, password)
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte("Password Error"))
		logger.Warn("Unauthorized request",
			zap.String("remote_addr", r.RemoteAddr),
			zap.String("path", r.URL.Path),
		)
		return
	}
	fmt.Println("[DEBUG] <<< Auth Success")

	// 如果设置了 refresh 参数，则先拉取最新数据
	if refresh == "1" || refresh == "true" {
		logger.Info("Forced refresh via request parameter", zap.String("remote_addr", r.RemoteAddr))
		if _, err := Refresh(r.Context(), TriggerQuery); err != nil {
			// A query refresh is opportunistic: preserve and serve the complete
			// last-known-good snapshot when the transaction fails.
			logger.Error("Query refresh failed; serving last-known-good", zap.Error(err))
		}
	}

	// 获取要使用的模板
	if templateName == "" {
		templateName = cfg.DefaultTemplate
	}

	// 确保模板可用（检查存在性与更新）
	if err := ensureTemplate(r.Context(), templateName); err != nil {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(fmt.Sprintf("Template Error: %v", err)))
		logger.Warn("Template error",
			zap.String("template", templateName),
			zap.Error(err),
			zap.String("remote_addr", r.RemoteAddr),
		)
		return
	}

	snapshot := getSnapshot()
	requestSnapshotHook()
	var currentTemplate = snapshot.templates[templateName]
	var actualTemplateName string
	var noNodeName string

	// 检查模板是否启用
	if tplConfig, exists := cfg.GetTemplate(templateName); exists && tplConfig.Enabled {
		actualTemplateName = tplConfig.Name
		noNodeName = tplConfig.NoNode
	} else {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(fmt.Sprintf("Template '%s' not found or not enabled", templateName)))
		return
	}

	if currentTemplate == nil {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(fmt.Sprintf("Template '%s' not loaded", templateName)))
		return
	}

	// 构建模板上下文
	context := pongo2.Context{
		"Nodes":     pongo2.AsSafeValue(strings.Join(snapshot.nodes, ",\r\n")),
		"setType":   setType,
		"nodeCount": len(snapshot.nodes),
		"noNode":    noNodeName,
		"__refresh_filter": notesFilterContext{
			Names: snapshot.nodeNames, NoNode: noNodeName,
		},
	}

	output, err := currentTemplate.Execute(context)
	if err != nil {
		logger.Error("Error rendering template",
			zap.Error(err),
			zap.String("template", templateName),
		)
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(fmt.Sprintf("Server Error: %v", err)))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Profile-Update-Interval", "6")
	w.Header().Set("Subscription-Userinfo", fmt.Sprintf("upload=0; download=0; total=%d", len(snapshot.nodes)))
	// 添加防缓存 Header
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")

	w.WriteHeader(http.StatusOK)
	w.Write([]byte(output))

	logger.Info("Successfully served config",
		zap.String("remote_addr", r.RemoteAddr),
		zap.String("template", templateName),
		zap.String("template_name", actualTemplateName),
		zap.String("type", setType),
		zap.Int("node_count", len(snapshot.nodes)),
	)
}

// HandleHealth 健康检查
func HandleHealth(w http.ResponseWriter, r *http.Request) {
	snapshot := getSnapshot()
	hasData := len(snapshot.nodeData) > 0
	hasTemplate := len(snapshot.templates) > 0
	templateCount := len(snapshot.templates)
	nodeCount := len(snapshot.nodeData)

	status := "ok"
	code := http.StatusOK
	if !hasData || !hasTemplate {
		status = "degraded"
		code = http.StatusServiceUnavailable
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(struct {
		Service       string `json:"service"`
		Version       string `json:"version"`
		Status        string `json:"status"`
		HasData       bool   `json:"has_data"`
		HasTemplate   bool   `json:"has_template"`
		NodeCount     int    `json:"node_count"`
		TemplateCount int    `json:"template_count"`
	}{
		Service:       "singbox-subscribe-convert",
		Version:       global.Version,
		Status:        status,
		HasData:       hasData,
		HasTemplate:   hasTemplate,
		NodeCount:     nodeCount,
		TemplateCount: templateCount,
	})
}

// PurgeCloudflareCache 清理 Cloudflare 缓存
func PurgeCloudflareCache() error {
	if !cfg.Cloudflare.Enabled {
		logger.Debug("Cloudflare cache purge is disabled")
		return nil
	}

	if cfg.Cloudflare.PurgeURL == "" {
		return fmt.Errorf("cloudflare purge_url is not configured")
	}

	logger.Info("🧹 Starting Cloudflare cache purge...",
		zap.String("purge_url", cfg.Cloudflare.PurgeURL),
	)

	// 构建请求体 - 清理所有缓存
	requestBody := map[string]interface{}{
		"purge_everything": true,
	}

	jsonData, err := json.Marshal(requestBody)
	if err != nil {
		logger.Error("❌ Failed to marshal Cloudflare request body",
			zap.Error(err),
		)
		return fmt.Errorf("failed to marshal request body: %w", err)
	}

	logger.Debug("Cloudflare purge request body",
		zap.String("body", string(jsonData)),
	)

	// 创建 POST 请求
	req, err := http.NewRequest("POST", cfg.Cloudflare.PurgeURL, bytes.NewBuffer(jsonData))
	if err != nil {
		logger.Error("❌ Failed to create Cloudflare request",
			zap.Error(err),
		)
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	// 设置认证 Headers
	// 优先使用 API Token (推荐方式)
	if cfg.Cloudflare.APIToken != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.Cloudflare.APIToken)
		logger.Debug("Using Cloudflare API Token authentication")
	} else if cfg.Cloudflare.APIKey != "" && cfg.Cloudflare.APIEmail != "" {
		// 使用 API Key + Email 方式
		req.Header.Set("X-Auth-Key", cfg.Cloudflare.APIKey)
		req.Header.Set("X-Auth-Email", cfg.Cloudflare.APIEmail)
		logger.Debug("Using Cloudflare API Key + Email authentication")
	} else {
		logger.Error("❌ No Cloudflare authentication configured")
		return fmt.Errorf("cloudflare authentication not configured: either api_token or (api_key + api_email) is required")
	}

	// 发送请求
	client := &http.Client{
		Timeout: cfg.GetRequestTimeout(),
	}

	logger.Info("📤 Sending purge request to Cloudflare API...")

	resp, err := client.Do(req)
	if err != nil {
		logger.Error("❌ Failed to send request to Cloudflare",
			zap.Error(err),
			zap.String("url", cfg.Cloudflare.PurgeURL),
		)
		return fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	// 读取响应
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		logger.Error("❌ Failed to read Cloudflare response",
			zap.Error(err),
		)
		return fmt.Errorf("failed to read response: %w", err)
	}

	logger.Info("📥 Received response from Cloudflare",
		zap.Int("status_code", resp.StatusCode),
		zap.Int("body_size", len(body)),
	)

	// 检查响应状态
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		logger.Error("❌ Cloudflare API returned error",
			zap.Int("status_code", resp.StatusCode),
			zap.String("response", string(body)),
		)
		return fmt.Errorf("cloudflare API returned status %d: %s", resp.StatusCode, string(body))
	}

	// 尝试解析响应以获取更多信息
	var cfResponse map[string]interface{}
	if err := json.Unmarshal(body, &cfResponse); err == nil {
		logger.Info("✅ Cloudflare cache purged successfully!",
			zap.Int("status_code", resp.StatusCode),
			zap.Any("cloudflare_response", cfResponse),
		)
	} else {
		logger.Info("✅ Cloudflare cache purged successfully!",
			zap.Int("status_code", resp.StatusCode),
			zap.String("response", string(body)),
		)
	}

	return nil
}

// HandleRefresh 手动刷新
// Handle manual refresh request.
func HandleRefresh(w http.ResponseWriter, r *http.Request) {
	password := r.URL.Query().Get("password")
	if password != cfg.Auth.Password {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte("Password Error"))
		return
	}

	logger.Info("Manual refresh triggered", zap.String("remote_addr", r.RemoteAddr))
	errorsList := make([]string, 0, 2)
	if cfg.Cloudflare.Enabled {
		if err := PurgeCloudflareCache(); err != nil {
			errorsList = append(errorsList, fmt.Sprintf("cloudflare cache purge: %v", err))
		}
	}
	result, err := Refresh(r.Context(), TriggerManual)
	if err != nil {
		errorsList = append(errorsList, err.Error())
	}
	for name, actualURL := range result.URLs {
		logger.Info("Manual refresh URL", zap.String("name", name), zap.String("url", actualURL))
	}

	w.Header().Set("Content-Type", "application/json")
	if len(errorsList) > 0 {
		w.WriteHeader(http.StatusInternalServerError)
		errJSON, _ := json.Marshal(errorsList)
		fmt.Fprintf(w, `{"status":"error","errors":%s}`, string(errJSON))
		logger.Error("Manual refresh failed", zap.Strings("errors", errorsList))
		return
	}
	snapshot := getSnapshot()
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `{"status":"success","message":"Files refreshed successfully","node_count":%d,"template_count":%d}`, len(snapshot.nodeData), len(snapshot.templates))
}

// nodeNameFilter 过滤节点名称
func nodeNameFilter(param string) string {
	snapshot := getSnapshot()
	return filterNodeNames(param, snapshot.nodeNames, cfg.GetDefaultTemplateNoNode())
}

// EnsureTemplate 确保模板可用，如果不存在则下载，如果过期则更新
func EnsureTemplate(templateName string) error {
	return ensureTemplate(context.Background(), templateName)
}

func ensureTemplate(ctx context.Context, templateName string) error {
	tplConfig, exists := cfg.GetTemplate(templateName)
	if !exists {
		return fmt.Errorf("template '%s' not found in configuration", templateName)
	}

	if !tplConfig.Enabled {
		return fmt.Errorf("template '%s' is disabled", templateName)
	}

	templateFilePath := cfg.GetTemplateFilePathByName(templateName)
	snapshot := getSnapshot()
	_, loaded := snapshot.templates[templateName]
	needDownload := !fetcher.IsFileExists(templateFilePath)
	if !needDownload {
		modTime := fetcher.GetFileModTime(templateFilePath)
		needDownload = time.Since(modTime) > cfg.GetTemplateUpdateInterval(templateName)
	}
	if needDownload {
		if err := refreshTemplate(ctx, templateName); err != nil {
			if loaded {
				logger.Warn("Failed to refresh template, using loaded last-known-good",
					zap.String("template", templateName), zap.String("url", tplConfig.URL), zap.Error(err))
				return nil
			}
			return fmt.Errorf("failed to refresh template: %w", err)
		}
		return nil
	}
	if loaded {
		return nil
	}
	if err := reloadSnapshot(ctx); err != nil {
		return fmt.Errorf("failed to load template into memory: %w", err)
	}
	if _, ok := getSnapshot().templates[templateName]; !ok {
		return fmt.Errorf("template '%s' not loaded", templateName)
	}
	return nil
}
