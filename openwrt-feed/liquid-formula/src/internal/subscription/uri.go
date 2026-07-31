package subscription

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// ParseURI 把单条节点分享链接转成一个 sing-box outbound 对象。
func ParseURI(line string) (map[string]any, error) {
	line = strings.TrimSpace(line)
	idx := strings.Index(line, "://")
	if idx <= 0 {
		return nil, fmt.Errorf("不是节点链接: %s", preview(line))
	}
	scheme := strings.ToLower(line[:idx])

	switch scheme {
	case "ss":
		return parseShadowsocks(line)
	case "vmess":
		return parseVMess(line)
	case "vless":
		return parseVLESS(line)
	case "trojan":
		return parseTrojan(line)
	case "hysteria2", "hy2":
		return parseHysteria2(line)
	case "tuic":
		return parseTUIC(line)
	case "anytls":
		return parseAnyTLS(line)
	case "socks", "socks5":
		return parseSOCKS(line)
	case "ssr":
		return nil, fmt.Errorf("sing-box 不支持 ShadowsocksR，已跳过: %s", preview(line))
	default:
		return nil, fmt.Errorf("不支持的协议 %q: %s", scheme, preview(line))
	}
}

func preview(s string) string {
	if len(s) > 48 {
		return s[:48] + "..."
	}
	return s
}

// ---------- shadowsocks ----------

// 支持三种常见写法：
//
//	ss://base64(method:password)@host:port#tag        (SIP002)
//	ss://base64(method:password@host:port)#tag        (旧版整体编码)
//	ss://method:password@host:port#tag                (明文)
func parseShadowsocks(line string) (map[string]any, error) {
	body := line[len("ss://"):]
	tag := ""
	if hash := strings.Index(body, "#"); hash >= 0 {
		tag = decodeFragment(body[hash+1:])
		body = body[:hash]
	}
	query := url.Values{}
	if q := strings.Index(body, "?"); q >= 0 {
		query, _ = url.ParseQuery(body[q+1:])
		body = body[:q]
	}

	// 旧版：整段（含 host:port）被 base64 编码，没有裸 '@'。
	if !strings.Contains(body, "@") {
		if decoded, ok := decodeSegment(body); ok {
			body = decoded
			if hash := strings.Index(body, "#"); hash >= 0 {
				if tag == "" {
					tag = decodeFragment(body[hash+1:])
				}
				body = body[:hash]
			}
		}
	}

	at := strings.LastIndex(body, "@")
	if at < 0 {
		return nil, fmt.Errorf("ss 链接缺少 host 部分: %s", preview(line))
	}
	userinfo, hostport := body[:at], body[at+1:]

	// userinfo 可能是 base64(method:password)，也可能是明文。
	if !strings.Contains(userinfo, ":") {
		if decoded, ok := decodeSegment(userinfo); ok {
			userinfo = decoded
		}
	} else if decoded, ok := decodeSegment(userinfo); ok && strings.Contains(decoded, ":") {
		// 少数面板会把含 ':' 的 base64 也塞进来，解出来能用就用解出来的。
		userinfo = decoded
	}
	colon := strings.Index(userinfo, ":")
	if colon < 0 {
		return nil, fmt.Errorf("ss 链接缺少加密方式或密码: %s", preview(line))
	}
	method, password := userinfo[:colon], userinfo[colon+1:]

	host, port, err := splitHostPort(hostport)
	if err != nil {
		return nil, fmt.Errorf("ss 链接 %s: %w", preview(line), err)
	}
	if method == "" || host == "" {
		return nil, fmt.Errorf("ss 链接字段不完整: %s", preview(line))
	}

	node := map[string]any{
		"type":        "shadowsocks",
		"tag":         fallbackTag(tag, host, port),
		"server":      host,
		"server_port": port,
		"method":      method,
		"password":    password,
	}
	if plugin := query.Get("plugin"); plugin != "" {
		name, opts := splitPlugin(plugin)
		node["plugin"] = name
		if opts != "" {
			node["plugin_opts"] = opts
		}
	}
	return node, nil
}

// splitPlugin 把 "obfs-local;obfs=http;obfs-host=a.com" 拆成插件名与选项串。
func splitPlugin(raw string) (string, string) {
	parts := strings.SplitN(raw, ";", 2)
	name := strings.TrimSpace(parts[0])
	// sing-box 用 obfs-local 的通用名 obfs-local / v2ray-plugin。
	if len(parts) == 1 {
		return name, ""
	}
	return name, strings.TrimSpace(parts[1])
}

// ---------- vmess ----------

func parseVMess(line string) (map[string]any, error) {
	payload := line[len("vmess://"):]
	if hash := strings.Index(payload, "#"); hash >= 0 {
		payload = payload[:hash]
	}
	decoded, ok := decodeSegment(payload)
	if !ok {
		return nil, fmt.Errorf("vmess 链接 base64 解码失败: %s", preview(line))
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(decoded), &raw); err != nil {
		return nil, fmt.Errorf("vmess 链接 JSON 解析失败: %s", preview(line))
	}

	host := str(raw["add"])
	port, err := parsePort(str(raw["port"]))
	if err != nil {
		return nil, fmt.Errorf("vmess 链接 %s: %w", preview(line), err)
	}
	uuid := str(raw["id"])
	if host == "" || uuid == "" {
		return nil, fmt.Errorf("vmess 链接缺少 add/id: %s", preview(line))
	}

	alterID := 0
	if v := str(raw["aid"]); v != "" {
		alterID, _ = strconv.Atoi(v)
	}
	security := firstNonEmpty(str(raw["scy"]), str(raw["security"]), "auto")

	node := map[string]any{
		"type":        "vmess",
		"tag":         fallbackTag(str(raw["ps"]), host, port),
		"server":      host,
		"server_port": port,
		"uuid":        uuid,
		"alter_id":    alterID,
		"security":    security,
	}

	network := strings.ToLower(firstNonEmpty(str(raw["net"]), "tcp"))
	headerType := strings.ToLower(str(raw["type"]))
	wsHost := firstNonEmpty(str(raw["host"]), "")
	path := firstNonEmpty(str(raw["path"]), "/")
	if transport := buildTransport(network, headerType, path, wsHost, str(raw["serviceName"])); transport != nil {
		node["transport"] = transport
	}

	tlsMode := strings.ToLower(str(raw["tls"]))
	if tlsMode == "tls" || tlsMode == "reality" {
		sni := firstNonEmpty(str(raw["sni"]), wsHost, host)
		node["tls"] = buildTLS(tlsOptions{
			serverName:  sni,
			insecure:    truthy(str(raw["allowInsecure"])) || truthy(str(raw["skip-cert-verify"])),
			alpn:        splitCSV(str(raw["alpn"])),
			fingerprint: str(raw["fp"]),
		})
	}
	return node, nil
}

// ---------- vless ----------

func parseVLESS(line string) (map[string]any, error) {
	u, err := url.Parse(line)
	if err != nil {
		return nil, fmt.Errorf("vless 链接解析失败: %s", preview(line))
	}
	host, port, err := splitHostPort(u.Host)
	if err != nil {
		return nil, fmt.Errorf("vless 链接 %s: %w", preview(line), err)
	}
	uuid := u.User.Username()
	if uuid == "" || host == "" {
		return nil, fmt.Errorf("vless 链接缺少 uuid/host: %s", preview(line))
	}
	q := u.Query()

	node := map[string]any{
		"type":        "vless",
		"tag":         fallbackTag(decodeFragment(u.Fragment), host, port),
		"server":      host,
		"server_port": port,
		"uuid":        uuid,
	}
	if flow := q.Get("flow"); flow != "" {
		node["flow"] = flow
	}
	if pe := q.Get("packetEncoding"); pe != "" {
		node["packet_encoding"] = pe
	}

	network := strings.ToLower(firstNonEmpty(q.Get("type"), "tcp"))
	if transport := buildTransport(network, strings.ToLower(q.Get("headerType")),
		firstNonEmpty(q.Get("path"), "/"), q.Get("host"), q.Get("serviceName")); transport != nil {
		node["transport"] = transport
	}

	security := strings.ToLower(q.Get("security"))
	if security == "tls" || security == "reality" || security == "xtls" {
		options := tlsOptions{
			serverName:  firstNonEmpty(q.Get("sni"), q.Get("peer"), q.Get("host"), host),
			insecure:    truthy(q.Get("allowInsecure")) || truthy(q.Get("insecure")),
			alpn:        splitCSV(q.Get("alpn")),
			fingerprint: q.Get("fp"),
		}
		if security == "reality" {
			options.realityPublicKey = q.Get("pbk")
			options.realityShortID = q.Get("sid")
		}
		node["tls"] = buildTLS(options)
	}
	return node, nil
}

// ---------- trojan ----------

func parseTrojan(line string) (map[string]any, error) {
	u, err := url.Parse(line)
	if err != nil {
		return nil, fmt.Errorf("trojan 链接解析失败: %s", preview(line))
	}
	host, port, err := splitHostPort(u.Host)
	if err != nil {
		return nil, fmt.Errorf("trojan 链接 %s: %w", preview(line), err)
	}
	password := u.User.Username()
	if password == "" || host == "" {
		return nil, fmt.Errorf("trojan 链接缺少密码/host: %s", preview(line))
	}
	q := u.Query()

	node := map[string]any{
		"type":        "trojan",
		"tag":         fallbackTag(decodeFragment(u.Fragment), host, port),
		"server":      host,
		"server_port": port,
		"password":    password,
	}
	network := strings.ToLower(firstNonEmpty(q.Get("type"), "tcp"))
	if transport := buildTransport(network, strings.ToLower(q.Get("headerType")),
		firstNonEmpty(q.Get("path"), "/"), q.Get("host"), q.Get("serviceName")); transport != nil {
		node["transport"] = transport
	}
	// trojan 默认走 TLS，除非明确 security=none。
	if strings.ToLower(q.Get("security")) != "none" {
		node["tls"] = buildTLS(tlsOptions{
			serverName:  firstNonEmpty(q.Get("sni"), q.Get("peer"), q.Get("host"), host),
			insecure:    truthy(q.Get("allowInsecure")) || truthy(q.Get("insecure")),
			alpn:        splitCSV(q.Get("alpn")),
			fingerprint: q.Get("fp"),
		})
	}
	return node, nil
}

// ---------- hysteria2 ----------

func parseHysteria2(line string) (map[string]any, error) {
	u, err := url.Parse(line)
	if err != nil {
		return nil, fmt.Errorf("hysteria2 链接解析失败: %s", preview(line))
	}
	host, port, err := splitHostPort(u.Host)
	if err != nil {
		return nil, fmt.Errorf("hysteria2 链接 %s: %w", preview(line), err)
	}
	if host == "" {
		return nil, fmt.Errorf("hysteria2 链接缺少 host: %s", preview(line))
	}
	q := u.Query()

	password := u.User.Username()
	if pw, ok := u.User.Password(); ok && pw != "" {
		// hy2://user:pass@host 形式，sing-box 用完整的 user:pass 作为密码。
		password = password + ":" + pw
	}
	if password == "" {
		password = q.Get("password")
	}

	node := map[string]any{
		"type":        "hysteria2",
		"tag":         fallbackTag(decodeFragment(u.Fragment), host, port),
		"server":      host,
		"server_port": port,
		"password":    password,
	}
	if obfs := q.Get("obfs"); obfs != "" {
		node["obfs"] = map[string]any{
			"type":     obfs,
			"password": firstNonEmpty(q.Get("obfs-password"), q.Get("obfsParam")),
		}
	}
	node["tls"] = buildTLS(tlsOptions{
		serverName: firstNonEmpty(q.Get("sni"), q.Get("peer"), host),
		insecure:   truthy(q.Get("insecure")) || truthy(q.Get("allowInsecure")),
		alpn:       splitCSV(q.Get("alpn")),
	})
	return node, nil
}

// ---------- tuic ----------

func parseTUIC(line string) (map[string]any, error) {
	u, err := url.Parse(line)
	if err != nil {
		return nil, fmt.Errorf("tuic 链接解析失败: %s", preview(line))
	}
	host, port, err := splitHostPort(u.Host)
	if err != nil {
		return nil, fmt.Errorf("tuic 链接 %s: %w", preview(line), err)
	}
	uuid := u.User.Username()
	password, _ := u.User.Password()
	if uuid == "" || host == "" {
		return nil, fmt.Errorf("tuic 链接缺少 uuid/host: %s", preview(line))
	}
	q := u.Query()

	node := map[string]any{
		"type":               "tuic",
		"tag":                fallbackTag(decodeFragment(u.Fragment), host, port),
		"server":             host,
		"server_port":        port,
		"uuid":               uuid,
		"password":           password,
		"congestion_control": firstNonEmpty(q.Get("congestion_control"), "bbr"),
		"udp_relay_mode":     firstNonEmpty(q.Get("udp_relay_mode"), "native"),
	}
	alpn := splitCSV(q.Get("alpn"))
	if alpn == nil {
		alpn = []string{"h3"}
	}
	node["tls"] = buildTLS(tlsOptions{
		serverName: firstNonEmpty(q.Get("sni"), q.Get("peer"), host),
		insecure:   truthy(q.Get("insecure")) || truthy(q.Get("allow_insecure")),
		alpn:       alpn,
	})
	return node, nil
}

// ---------- anytls ----------

func parseAnyTLS(line string) (map[string]any, error) {
	u, err := url.Parse(line)
	if err != nil {
		return nil, fmt.Errorf("anytls 链接解析失败: %s", preview(line))
	}
	host, port, err := splitHostPort(u.Host)
	if err != nil {
		return nil, fmt.Errorf("anytls 链接 %s: %w", preview(line), err)
	}
	password := u.User.Username()
	if pw, ok := u.User.Password(); ok && pw != "" {
		password = pw
	}
	if host == "" || password == "" {
		return nil, fmt.Errorf("anytls 链接缺少 host/密码: %s", preview(line))
	}
	q := u.Query()
	return map[string]any{
		"type":        "anytls",
		"tag":         fallbackTag(decodeFragment(u.Fragment), host, port),
		"server":      host,
		"server_port": port,
		"password":    password,
		"tls": buildTLS(tlsOptions{
			serverName: firstNonEmpty(q.Get("sni"), q.Get("peer"), host),
			insecure:   truthy(q.Get("insecure")) || truthy(q.Get("allowInsecure")),
			alpn:       splitCSV(q.Get("alpn")),
		}),
	}, nil
}

// ---------- socks ----------

func parseSOCKS(line string) (map[string]any, error) {
	u, err := url.Parse(line)
	if err != nil {
		return nil, fmt.Errorf("socks 链接解析失败: %s", preview(line))
	}
	host, port, err := splitHostPort(u.Host)
	if err != nil {
		return nil, fmt.Errorf("socks 链接 %s: %w", preview(line), err)
	}
	node := map[string]any{
		"type":        "socks",
		"tag":         fallbackTag(decodeFragment(u.Fragment), host, port),
		"server":      host,
		"server_port": port,
		"version":     "5",
	}
	if user := u.User.Username(); user != "" {
		node["username"] = user
		if pw, ok := u.User.Password(); ok {
			node["password"] = pw
		}
	}
	return node, nil
}

// ---------- 共用构造 ----------

type tlsOptions struct {
	serverName       string
	insecure         bool
	alpn             []string
	fingerprint      string
	realityPublicKey string
	realityShortID   string
}

func buildTLS(o tlsOptions) map[string]any {
	tls := map[string]any{"enabled": true}
	if o.serverName != "" {
		tls["server_name"] = o.serverName
	}
	if o.insecure {
		tls["insecure"] = true
	}
	if len(o.alpn) > 0 {
		tls["alpn"] = o.alpn
	}
	if o.fingerprint != "" {
		tls["utls"] = map[string]any{"enabled": true, "fingerprint": o.fingerprint}
	}
	if o.realityPublicKey != "" {
		reality := map[string]any{"enabled": true, "public_key": o.realityPublicKey}
		if o.realityShortID != "" {
			reality["short_id"] = o.realityShortID
		}
		tls["reality"] = reality
		// REALITY 必须配合 uTLS，未指定指纹时给一个安全默认值。
		if o.fingerprint == "" {
			tls["utls"] = map[string]any{"enabled": true, "fingerprint": "chrome"}
		}
	}
	return tls
}

// buildTransport 把 v2ray 系的 network/headerType 映射到 sing-box transport。
// 返回 nil 表示裸 TCP，sing-box 下不需要 transport 字段。
func buildTransport(network, headerType, path, host, serviceName string) map[string]any {
	switch network {
	case "", "tcp":
		if headerType == "http" {
			transport := map[string]any{"type": "http", "path": path}
			if host != "" {
				transport["host"] = splitCSV(host)
			}
			return transport
		}
		return nil
	case "ws", "websocket":
		transport := map[string]any{"type": "ws"}
		cleanPath, earlyData := splitEarlyData(path)
		transport["path"] = cleanPath
		if host != "" {
			transport["headers"] = map[string]any{"Host": host}
		}
		if earlyData > 0 {
			transport["max_early_data"] = earlyData
			transport["early_data_header_name"] = "Sec-WebSocket-Protocol"
		}
		return transport
	case "grpc":
		name := firstNonEmpty(serviceName, strings.TrimPrefix(path, "/"))
		return map[string]any{"type": "grpc", "service_name": name}
	case "h2", "http":
		transport := map[string]any{"type": "http", "path": path}
		if host != "" {
			transport["host"] = splitCSV(host)
		}
		return transport
	case "httpupgrade":
		transport := map[string]any{"type": "httpupgrade", "path": path}
		if host != "" {
			transport["host"] = host
		}
		return transport
	case "quic":
		return map[string]any{"type": "quic"}
	default:
		return nil
	}
}

// splitEarlyData 处理 "/path?ed=2048" 这种 v2ray 早期数据写法。
func splitEarlyData(path string) (string, int) {
	q := strings.Index(path, "?")
	if q < 0 {
		return path, 0
	}
	values, err := url.ParseQuery(path[q+1:])
	if err != nil {
		return path, 0
	}
	size := 0
	if ed := values.Get("ed"); ed != "" {
		size, _ = strconv.Atoi(ed)
	}
	clean := path[:q]
	if clean == "" {
		clean = "/"
	}
	return clean, size
}

func splitHostPort(hostport string) (string, int, error) {
	hostport = strings.TrimSpace(hostport)
	if hostport == "" {
		return "", 0, fmt.Errorf("缺少 host:port")
	}
	host, portText, err := net.SplitHostPort(hostport)
	if err != nil {
		return "", 0, fmt.Errorf("无法拆分 host:port %q", hostport)
	}
	port, err := parsePort(portText)
	if err != nil {
		return "", 0, err
	}
	return strings.Trim(host, "[]"), port, nil
}

// decodeSegment 对单个 base64 片段做宽松解码。
func decodeSegment(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", false
	}
	for _, enc := range []*base64.Encoding{
		base64.RawURLEncoding, base64.RawStdEncoding,
		base64.URLEncoding, base64.StdEncoding,
	} {
		candidate := s
		if enc == base64.StdEncoding || enc == base64.URLEncoding {
			if pad := len(candidate) % 4; pad != 0 {
				candidate += strings.Repeat("=", 4-pad)
			}
		}
		if out, err := enc.DecodeString(candidate); err == nil && isMostlyPrintable(out) {
			return string(out), true
		}
	}
	return "", false
}

func isMostlyPrintable(b []byte) bool {
	if len(b) == 0 {
		return false
	}
	bad := 0
	for _, c := range b {
		if c < 0x09 || (c > 0x0d && c < 0x20) {
			bad++
		}
	}
	return bad*10 < len(b)
}

func decodeFragment(s string) string {
	if s == "" {
		return ""
	}
	if decoded, err := url.QueryUnescape(s); err == nil {
		return strings.TrimSpace(decoded)
	}
	if decoded, err := url.PathUnescape(s); err == nil {
		return strings.TrimSpace(decoded)
	}
	return strings.TrimSpace(s)
}

func fallbackTag(tag, host string, port int) string {
	if tag = strings.TrimSpace(tag); tag != "" {
		return tag
	}
	return fmt.Sprintf("%s:%d", host, port)
}

// str 把 JSON 里可能是字符串也可能是数字的字段统一取成字符串。
func str(v any) string {
	switch value := v.(type) {
	case string:
		return strings.TrimSpace(value)
	case float64:
		if value == float64(int64(value)) {
			return strconv.FormatInt(int64(value), 10)
		}
		return strconv.FormatFloat(value, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(value)
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", value)
	}
}
