// Package subscription 把机场下发的各种订阅载荷统一归一化为 sing-box 的
// {"outbounds":[...]} JSON。支持三类输入：
//
//  1. 已经是 sing-box JSON（含 outbounds 数组）—— 原样通过；
//  2. base64 编码的 URI 列表（v2rayN 风格，机场默认下发格式）；
//  3. 明文 URI 列表（每行一个 ss:// vmess:// vless:// trojan:// ... ）。
//
// 若识别为 Clash/Clash.Meta YAML，会返回一个可读的错误，提示改用其它 UA，
// 而不是让上层拿到一堆无法解析的字节。
//
// 本包只依赖标准库，便于单独编译与测试。
package subscription

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Format 表示探测到的原始订阅格式。
type Format string

const (
	FormatSingBox   Format = "singbox-json"
	FormatBase64URI Format = "base64-uri-list"
	FormatPlainURI  Format = "plain-uri-list"
	FormatClashYAML Format = "clash-yaml"
	FormatUnknown   Format = "unknown"
)

// Info 描述一次归一化的结果，用于日志与健康展示。
type Info struct {
	Format     Format   // 探测到的原始格式
	NodeCount  int      // 成功转换的节点数
	Skipped    int      // 无法识别而跳过的行数
	SkipReason []string // 跳过原因样本（最多 5 条，便于排障）
}

// ErrClashPayload 表示拿到的是 Clash 系 YAML，本转换器不处理。
var ErrClashPayload = errors.New("订阅返回的是 Clash/Clash.Meta YAML，请把 User-Agent 改成 sing-box 或 v2rayN 系")

// ErrNoNode 表示解析完一个可用节点都没有。
var ErrNoNode = errors.New("订阅内容中没有解析出任何可用节点")

const maxSkipSamples = 5

// Normalize 把任意订阅载荷转成 sing-box outbounds JSON。
// 返回的字节可直接写入 node 缓存文件。
func Normalize(raw []byte) ([]byte, Info, error) {
	info := Info{Format: FormatUnknown}
	text := strings.TrimSpace(stripBOM(string(raw)))
	if text == "" {
		return nil, info, errors.New("订阅返回内容为空")
	}

	// 1) 已经是 sing-box JSON：直接透传，保持既有行为不变。
	if looksLikeJSON(text) {
		var probe struct {
			Outbounds []map[string]any `json:"outbounds"`
		}
		if err := json.Unmarshal([]byte(text), &probe); err == nil && len(probe.Outbounds) > 0 {
			info.Format = FormatSingBox
			info.NodeCount = len(probe.Outbounds)
			return raw, info, nil
		}
	}

	// 2) Clash 系 YAML：明确报错，让用户知道该换 UA，而不是报“解析失败”。
	if looksLikeClashYAML(text) {
		info.Format = FormatClashYAML
		return nil, info, ErrClashPayload
	}

	// 3) base64 或明文 URI 列表。
	body, decoded := decodeBase64Loose(text)
	if decoded {
		info.Format = FormatBase64URI
	} else {
		body = text
		info.Format = FormatPlainURI
	}

	// base64 解出来后仍可能是 sing-box JSON（少数面板会 base64 包一层）。
	if looksLikeJSON(strings.TrimSpace(body)) {
		var probe struct {
			Outbounds []map[string]any `json:"outbounds"`
		}
		if err := json.Unmarshal([]byte(strings.TrimSpace(body)), &probe); err == nil && len(probe.Outbounds) > 0 {
			info.Format = FormatSingBox
			info.NodeCount = len(probe.Outbounds)
			return []byte(strings.TrimSpace(body)), info, nil
		}
	}
	if looksLikeClashYAML(body) {
		info.Format = FormatClashYAML
		return nil, info, ErrClashPayload
	}

	outbounds := make([]map[string]any, 0, 32)
	for _, line := range splitLines(body) {
		node, err := ParseURI(line)
		if err != nil {
			info.Skipped++
			if len(info.SkipReason) < maxSkipSamples {
				info.SkipReason = append(info.SkipReason, err.Error())
			}
			continue
		}
		outbounds = append(outbounds, node)
	}
	if len(outbounds) == 0 {
		return nil, info, ErrNoNode
	}

	dedupeTags(outbounds)
	info.NodeCount = len(outbounds)

	encoded, err := json.MarshalIndent(map[string]any{"outbounds": outbounds}, "", "  ")
	if err != nil {
		return nil, info, fmt.Errorf("序列化 outbounds 失败: %w", err)
	}
	return append(encoded, '\n'), info, nil
}

// dedupeTags 保证 tag 唯一。上游 buildSnapshot 会直接丢弃重名节点，
// 这里改为追加序号，避免同名节点被静默吞掉。
func dedupeTags(outbounds []map[string]any) {
	seen := make(map[string]int, len(outbounds))
	for i, node := range outbounds {
		tag, _ := node["tag"].(string)
		if tag == "" {
			tag = fmt.Sprintf("node-%d", i+1)
		}
		if n, exists := seen[tag]; exists {
			n++
			seen[tag] = n
			candidate := fmt.Sprintf("%s #%d", tag, n)
			for {
				if _, clash := seen[candidate]; !clash {
					break
				}
				n++
				candidate = fmt.Sprintf("%s #%d", tag, n)
			}
			tag = candidate
		}
		seen[tag] = 0
		node["tag"] = tag
	}
}

func stripBOM(s string) string {
	return strings.TrimPrefix(s, "\ufeff")
}

func looksLikeJSON(s string) bool {
	return strings.HasPrefix(s, "{") || strings.HasPrefix(s, "[")
}

// looksLikeClashYAML 只在明文里判断，避免把 base64 误判成 YAML。
func looksLikeClashYAML(s string) bool {
	head := s
	if len(head) > 4096 {
		head = head[:4096]
	}
	if strings.Contains(head, "://") {
		return false
	}
	for _, marker := range []string{"\nproxies:", "proxies:\n", "\nproxy-groups:", "\nmixed-port:", "\nport:"} {
		if strings.Contains("\n"+head, marker) {
			return true
		}
	}
	return false
}

// decodeBase64Loose 宽松地尝试 base64 解码：兼容标准/URL 安全字母表、
// 有无 padding、以及中间夹杂换行的情况。第二个返回值表示是否确实解出了内容。
func decodeBase64Loose(s string) (string, bool) {
	// 已经带 scheme 的明文列表不需要解码。
	if strings.Contains(s, "://") {
		return s, false
	}
	compact := strings.Map(func(r rune) rune {
		switch r {
		case '\n', '\r', '\t', ' ':
			return -1
		}
		return r
	}, s)
	if compact == "" {
		return s, false
	}

	encodings := []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	}
	for _, enc := range encodings {
		candidate := compact
		// 对需要 padding 的字母表补齐长度。
		if enc == base64.StdEncoding || enc == base64.URLEncoding {
			if pad := len(candidate) % 4; pad != 0 {
				candidate += strings.Repeat("=", 4-pad)
			}
		}
		out, err := enc.DecodeString(candidate)
		if err != nil {
			continue
		}
		decoded := string(out)
		if strings.Contains(decoded, "://") {
			return decoded, true
		}
	}
	return s, false
}

func splitLines(s string) []string {
	raw := strings.FieldsFunc(s, func(r rune) bool {
		return r == '\n' || r == '\r'
	})
	lines := make([]string, 0, len(raw))
	for _, line := range raw {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
			continue
		}
		lines = append(lines, line)
	}
	return lines
}

// ---------- 通用小工具 ----------

func parsePort(s string) (int, error) {
	port, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("非法端口 %q", s)
	}
	return port, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func truthy(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func splitCSV(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
