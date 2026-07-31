package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/flosch/pongo2/v6"
	"github.com/haierkeys/singbox-subscribe-convert/internal/cache"
	"github.com/haierkeys/singbox-subscribe-convert/internal/fetcher"
	"github.com/haierkeys/singbox-subscribe-convert/internal/refresh"
	"github.com/haierkeys/singbox-subscribe-convert/internal/subscription"
	"go.uber.org/zap"
)

const (
	TriggerInitial  = "initial"
	TriggerManual   = "manual"
	TriggerQuery    = "query"
	TriggerAuto     = "auto"
	TriggerOnDemand = "on-demand"
	TriggerWatcher  = "watcher"
)

type dataSnapshot struct {
	nodeNames []string
	nodeData  []map[string]interface{}
	nodes     []string
	templates map[string]*pongo2.Template
}

type RefreshResult struct {
	URLs map[string]string
}

type notesFilterContext struct {
	Names  []string
	NoNode string
}

var (
	snapshotMutex   sync.RWMutex
	currentSnapshot = emptySnapshot()

	refreshManager = refresh.NewManager()
	remoteClient   *fetcher.Client

	fetchRemoteFn = func(ctx context.Context, rawURL string, limit int64) ([]byte, string, error) {
		if remoteClient == nil {
			return nil, rawURL, fmt.Errorf("fetch client is not initialized")
		}
		return remoteClient.FetchBytes(ctx, rawURL, limit)
	}
	commitBatchFn = func(batch *cache.Batch) error { return batch.Commit() }

	requestSnapshotHook = func() {}
	notesFilterOnce     sync.Once
)

func emptySnapshot() *dataSnapshot {
	return &dataSnapshot{templates: make(map[string]*pongo2.Template)}
}

func ensureNotesFilter() {
	notesFilterOnce.Do(func() {
		_ = pongo2.RegisterFilter("NotesName", func(in *pongo2.Value, param *pongo2.Value) (*pongo2.Value, *pongo2.Error) {
			filterContext, _ := param.Interface().(notesFilterContext)
			return pongo2.AsSafeValue(filterNodeNames(in.String(), filterContext.Names, filterContext.NoNode)), nil
		})
	})
}

func getSnapshot() *dataSnapshot {
	snapshotMutex.RLock()
	snapshot := currentSnapshot
	snapshotMutex.RUnlock()
	if snapshot == nil {
		return emptySnapshot()
	}
	return snapshot
}

func applySnapshot(snapshot *dataSnapshot) {
	if snapshot == nil {
		return
	}
	snapshotMutex.Lock()
	currentSnapshot = snapshot
	snapshotMutex.Unlock()
}

func buildSnapshot(nodeBytes []byte, templateBytes map[string][]byte) (*dataSnapshot, error) {
	ensureNotesFilter()

	var nodeFile NodeFile
	if err := json.Unmarshal(nodeBytes, &nodeFile); err != nil {
		return nil, fmt.Errorf("parse node file error: %w", err)
	}
	if len(nodeFile.Outbounds) == 0 {
		return nil, fmt.Errorf("no outbounds found in node file")
	}

	snapshot := &dataSnapshot{
		nodeData:  make([]map[string]interface{}, 0, len(nodeFile.Outbounds)),
		nodeNames: make([]string, 0, len(nodeFile.Outbounds)),
		nodes:     make([]string, 0, len(nodeFile.Outbounds)),
		templates: make(map[string]*pongo2.Template),
	}
	seenNames := make(map[string]struct{})
	for _, node := range nodeFile.Outbounds {
		tag, ok := node["tag"].(string)
		if !ok || tag == "" {
			continue
		}
		if _, exists := seenNames[tag]; exists {
			continue
		}
		seenNames[tag] = struct{}{}
		snapshot.nodeNames = append(snapshot.nodeNames, tag)
		snapshot.nodeData = append(snapshot.nodeData, node)
		encoded, err := json.Marshal(node)
		if err != nil {
			return nil, fmt.Errorf("marshal node %q: %w", tag, err)
		}
		snapshot.nodes = append(snapshot.nodes, string(encoded))
	}
	if len(snapshot.nodeData) == 0 {
		return nil, fmt.Errorf("no outbounds with a non-empty string tag found in node file")
	}

	for _, name := range enabledTemplateNames() {
		data, exists := templateBytes[name]
		if !exists {
			return nil, fmt.Errorf("template %q data is missing", name)
		}
		template, err := compileTemplate(data)
		if err != nil {
			return nil, fmt.Errorf("compile template %q: %w", name, err)
		}
		snapshot.templates[name] = template
	}
	return snapshot, nil
}

func compileTemplate(data []byte) (*pongo2.Template, error) {
	// NotesName historically read package globals while a request rendered.
	// Supplying its immutable request snapshot as an explicit filter argument
	// keeps existing template syntax while removing that cross-generation read.
	// Rewrite only real variable tags, never JSON strings, comments, or quoted
	// text inside a tag.
	source := injectNotesFilterContext(string(data))
	return pongo2.FromString(source)
}

func injectNotesFilterContext(source string) string {
	var out strings.Builder
	for pos := 0; pos < len(source); {
		start := strings.Index(source[pos:], "{{")
		if start < 0 {
			out.WriteString(source[pos:])
			break
		}
		start += pos
		out.WriteString(source[pos : start+2])
		end := strings.Index(source[start+2:], "}}")
		if end < 0 {
			out.WriteString(source[start+2:])
			break
		}
		end += start + 2
		out.WriteString(rewriteNotesFilters(source[start+2 : end]))
		out.WriteString("}}")
		pos = end + 2
	}
	return out.String()
}

func rewriteNotesFilters(expression string) string {
	var out strings.Builder
	quote := byte(0)
	escaped := false
	for i := 0; i < len(expression); {
		ch := expression[i]
		if quote != 0 {
			out.WriteByte(ch)
			if escaped {
				escaped = false
			} else if ch == '\\' {
				escaped = true
			} else if ch == quote {
				quote = 0
			}
			i++
			continue
		}
		if ch == '\'' || ch == '"' {
			quote = ch
			out.WriteByte(ch)
			i++
			continue
		}
		if ch != '|' {
			out.WriteByte(ch)
			i++
			continue
		}
		nameStart := i + 1
		for nameStart < len(expression) && (expression[nameStart] == ' ' || expression[nameStart] == '\t' || expression[nameStart] == '\r' || expression[nameStart] == '\n') {
			nameStart++
		}
		const filterName = "NotesName"
		nameEnd := nameStart + len(filterName)
		if nameEnd <= len(expression) && expression[nameStart:nameEnd] == filterName && (nameEnd == len(expression) || !isIdentifierByte(expression[nameEnd])) {
			out.WriteString(expression[i:nameEnd])
			lookahead := nameEnd
			for lookahead < len(expression) && (expression[lookahead] == ' ' || expression[lookahead] == '\t') {
				lookahead++
			}
			if lookahead >= len(expression) || expression[lookahead] != ':' {
				out.WriteString(":__refresh_filter")
			}
			i = nameEnd
			continue
		}
		out.WriteByte(ch)
		i++
	}
	return out.String()
}

func isIdentifierByte(ch byte) bool {
	return ch == '_' || ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9'
}

func enabledTemplateNames() []string {
	names := make([]string, 0)
	if cfg != nil {
		for name, template := range cfg.Templates {
			if template.Enabled {
				names = append(names, name)
			}
		}
	}
	sort.Strings(names)
	return names
}

func loadSnapshotFromDisk() (*dataSnapshot, error) {
	if cfg == nil {
		return nil, fmt.Errorf("handler config is not initialized")
	}
	nodeBytes, err := os.ReadFile(cfg.GetNodeFilePath())
	if err != nil {
		return nil, fmt.Errorf("read node file error: %w", err)
	}
	templateBytes := make(map[string][]byte)
	for _, name := range enabledTemplateNames() {
		data, err := os.ReadFile(cfg.GetTemplateFilePathByName(name))
		if err != nil {
			return nil, fmt.Errorf("read template %q: %w", name, err)
		}
		templateBytes[name] = data
	}
	return buildSnapshot(nodeBytes, templateBytes)
}

func Refresh(ctx context.Context, trigger string) (*RefreshResult, error) {
	result := &RefreshResult{URLs: make(map[string]string)}
	err := refreshManager.Do(ctx, trigger, func(workCtx context.Context) error {
		return refreshAll(workCtx, result)
	})
	return result, err
}

func refreshAll(ctx context.Context, result *RefreshResult) error {
	if cfg == nil {
		return fmt.Errorf("handler config is not initialized")
	}
	nodeBytes, actualURL, err := fetchRemoteFn(ctx, cfg.Subscription.URL, fetcher.NodeResponseLimit)
	result.URLs["node"] = actualURL
	logFetchURL("node", actualURL, err)
	if err != nil {
		return fmt.Errorf("fetch node from %s: %w", actualURL, err)
	}

	// 机场可能下发 sing-box JSON、base64 URI 列表或明文 URI 列表。统一归一化成
	// sing-box outbounds 之后再入缓存，后续所有环节（校验、快照、磁盘重载）都
	// 只需要面对一种格式。
	normalized, subInfo, normalizeErr := subscription.Normalize(nodeBytes)
	logNormalize(subInfo, normalizeErr)
	if normalizeErr != nil {
		return fmt.Errorf("normalize subscription payload from %s: %w", actualURL, normalizeErr)
	}
	nodeBytes = normalized

	templateBytes := make(map[string][]byte)
	for _, name := range enabledTemplateNames() {
		templateConfig := cfg.Templates[name]
		data, templateURL, fetchErr := fetchRemoteFn(ctx, templateConfig.URL, fetcher.TemplateResponseLimit)
		result.URLs["template:"+name] = templateURL
		logFetchURL("template:"+name, templateURL, fetchErr)
		if fetchErr != nil {
			return fmt.Errorf("fetch template %q from %s: %w", name, templateURL, fetchErr)
		}
		templateBytes[name] = data
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	candidate, err := buildSnapshot(nodeBytes, templateBytes)
	if err != nil {
		return fmt.Errorf("validate refresh snapshot: %w", err)
	}

	batch := cache.NewBatch()
	defer batch.Abort()
	if err := batch.Stage(cfg.GetNodeFilePath(), nodeBytes, 0o644, validateNodeStage); err != nil {
		return err
	}
	for _, name := range enabledTemplateNames() {
		if err := batch.Stage(cfg.GetTemplateFilePathByName(name), templateBytes[name], 0o644, validateTemplateStage); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := commitBatchFn(batch); err != nil {
		return fmt.Errorf("commit refresh batch: %w", err)
	}
	applySnapshot(candidate)
	return nil
}

func refreshTemplate(ctx context.Context, templateName string) error {
	return refreshManager.Do(ctx, TriggerOnDemand, func(workCtx context.Context) error {
		templateConfig, exists := cfg.GetTemplate(templateName)
		if !exists || !templateConfig.Enabled {
			return fmt.Errorf("template %q not found or disabled", templateName)
		}
		data, actualURL, err := fetchRemoteFn(workCtx, templateConfig.URL, fetcher.TemplateResponseLimit)
		logFetchURL("template:"+templateName, actualURL, err)
		if err != nil {
			return fmt.Errorf("fetch template %q from %s: %w", templateName, actualURL, err)
		}

		nodeBytes, err := os.ReadFile(cfg.GetNodeFilePath())
		if err != nil {
			return fmt.Errorf("read node cache: %w", err)
		}
		templateBytes := make(map[string][]byte)
		for _, name := range enabledTemplateNames() {
			if name == templateName {
				templateBytes[name] = data
				continue
			}
			cached, readErr := os.ReadFile(cfg.GetTemplateFilePathByName(name))
			if readErr != nil {
				return fmt.Errorf("read template %q: %w", name, readErr)
			}
			templateBytes[name] = cached
		}
		candidate, err := buildSnapshot(nodeBytes, templateBytes)
		if err != nil {
			return fmt.Errorf("validate on-demand snapshot: %w", err)
		}
		batch := cache.NewBatch()
		defer batch.Abort()
		if err := batch.Stage(cfg.GetTemplateFilePathByName(templateName), data, 0o644, validateTemplateStage); err != nil {
			return err
		}
		if err := workCtx.Err(); err != nil {
			return err
		}
		if err := commitBatchFn(batch); err != nil {
			return fmt.Errorf("commit on-demand template: %w", err)
		}
		applySnapshot(candidate)
		return nil
	})
}

func reloadSnapshot(ctx context.Context) error {
	return refreshManager.Do(ctx, TriggerWatcher, func(workCtx context.Context) error {
		candidate, err := loadSnapshotFromDisk()
		if err != nil {
			return err
		}
		if err := workCtx.Err(); err != nil {
			return err
		}
		applySnapshot(candidate)
		return nil
	})
}

func validateNodeStage(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var nodeFile NodeFile
	if err := json.Unmarshal(data, &nodeFile); err != nil {
		return err
	}
	if len(nodeFile.Outbounds) == 0 {
		return fmt.Errorf("no outbounds found")
	}
	for _, node := range nodeFile.Outbounds {
		if tag, ok := node["tag"].(string); ok && tag != "" {
			return nil
		}
	}
	return fmt.Errorf("no outbounds with a non-empty string tag found")
}

func validateTemplateStage(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	_, err = compileTemplate(data)
	return err
}

// logNormalize 记录订阅归一化结果，便于在 LuCI 的日志面板里直接看出
// 机场到底下发了什么格式、解析出多少节点、跳过了哪些行。
func logNormalize(info subscription.Info, err error) {
	if logger == nil {
		return
	}
	fields := []zap.Field{
		zap.String("format", string(info.Format)),
		zap.Int("node_count", info.NodeCount),
		zap.Int("skipped", info.Skipped),
	}
	if len(info.SkipReason) > 0 {
		fields = append(fields, zap.Strings("skip_samples", info.SkipReason))
	}
	if err != nil {
		logger.Error("Subscription normalize failed", append(fields, zap.Error(err))...)
		return
	}
	logger.Info("Subscription normalized", fields...)
}

func logFetchURL(name, actualURL string, err error) {
	if logger == nil {
		return
	}
	if err != nil {
		logger.Error("Refresh fetch failed", zap.String("name", name), zap.String("url", actualURL), zap.Error(err))
		return
	}
	logger.Info("Refresh fetch completed", zap.String("name", name), zap.String("url", actualURL))
}

func filterNodeNames(pattern string, nodeNames []string, noNode string) string {
	filtered := make([]string, 0)
	if pattern == "" {
		filtered = append(filtered, nodeNames...)
	} else {
		patterns := strings.Split(pattern, "|")
		for _, nodeName := range nodeNames {
			for _, name := range patterns {
				name = strings.TrimSpace(name)
				if name != "" && strings.Contains(nodeName, name) {
					filtered = append(filtered, nodeName)
					break
				}
			}
		}
	}
	if len(filtered) == 0 {
		if noNode == "" {
			noNode = "🎯 全球直连"
		}
		filtered = append(filtered, noNode)
	}
	encoded, _ := json.Marshal(filtered)
	if len(encoded) > 2 {
		return string(encoded[1 : len(encoded)-1])
	}
	return ""
}
