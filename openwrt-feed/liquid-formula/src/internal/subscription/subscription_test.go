package subscription

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func mustNormalize(t *testing.T, raw string) ([]map[string]any, Info) {
	t.Helper()
	out, info, err := Normalize([]byte(raw))
	if err != nil {
		t.Fatalf("Normalize 失败: %v", err)
	}
	var parsed struct {
		Outbounds []map[string]any `json:"outbounds"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("输出不是合法 JSON: %v\n%s", err, out)
	}
	return parsed.Outbounds, info
}

func TestBase64URIList(t *testing.T) {
	list := strings.Join([]string{
		"ss://YWVzLTI1Ni1nY206cGFzc3dvcmQxMjM@1.2.3.4:8388#%E9%A6%99%E6%B8%AF01",
		"trojan://trojanpass@tj.example.com:443?sni=tj.example.com&type=ws&path=/ws#%E6%97%A5%E6%9C%ACA",
		"hysteria2://hy2pass@hy.example.com:8443?sni=hy.example.com&insecure=1&obfs=salamander&obfs-password=ob#US-01",
	}, "\n")
	encoded := base64.StdEncoding.EncodeToString([]byte(list))

	nodes, info := mustNormalize(t, encoded)
	if info.Format != FormatBase64URI {
		t.Fatalf("格式探测错误: %s", info.Format)
	}
	if len(nodes) != 3 {
		t.Fatalf("节点数 = %d, 期望 3", len(nodes))
	}
	if nodes[0]["type"] != "shadowsocks" || nodes[0]["method"] != "aes-256-gcm" || nodes[0]["password"] != "password123" {
		t.Fatalf("ss 节点解析错误: %#v", nodes[0])
	}
	if nodes[0]["tag"] != "香港01" {
		t.Fatalf("中文 tag 解码错误: %v", nodes[0]["tag"])
	}
	if nodes[1]["type"] != "trojan" || nodes[1]["password"] != "trojanpass" {
		t.Fatalf("trojan 节点解析错误: %#v", nodes[1])
	}
	transport, _ := nodes[1]["transport"].(map[string]any)
	if transport["type"] != "ws" || transport["path"] != "/ws" {
		t.Fatalf("trojan ws transport 错误: %#v", transport)
	}
	obfs, _ := nodes[2]["obfs"].(map[string]any)
	if obfs["type"] != "salamander" || obfs["password"] != "ob" {
		t.Fatalf("hysteria2 obfs 错误: %#v", nodes[2])
	}
	tls, _ := nodes[2]["tls"].(map[string]any)
	if tls["insecure"] != true || tls["server_name"] != "hy.example.com" {
		t.Fatalf("hysteria2 tls 错误: %#v", tls)
	}
}

func TestRawURLBase64NoPadding(t *testing.T) {
	list := "vless://11111111-2222-3333-4444-555555555555@v.example.com:443?encryption=none&security=reality&sni=www.microsoft.com&fp=chrome&pbk=PUBKEY&sid=ab12&type=grpc&serviceName=grpcsvc#REALITY"
	encoded := base64.RawURLEncoding.EncodeToString([]byte(list))

	nodes, info := mustNormalize(t, encoded)
	if info.Format != FormatBase64URI {
		t.Fatalf("无 padding base64 未识别: %s", info.Format)
	}
	node := nodes[0]
	if node["type"] != "vless" || node["uuid"] != "11111111-2222-3333-4444-555555555555" {
		t.Fatalf("vless 解析错误: %#v", node)
	}
	tls, _ := node["tls"].(map[string]any)
	reality, _ := tls["reality"].(map[string]any)
	if reality["public_key"] != "PUBKEY" || reality["short_id"] != "ab12" {
		t.Fatalf("reality 解析错误: %#v", tls)
	}
	utls, _ := tls["utls"].(map[string]any)
	if utls["fingerprint"] != "chrome" {
		t.Fatalf("utls 指纹错误: %#v", tls)
	}
	transport, _ := node["transport"].(map[string]any)
	if transport["type"] != "grpc" || transport["service_name"] != "grpcsvc" {
		t.Fatalf("grpc transport 错误: %#v", transport)
	}
}

func TestVMessBase64Payload(t *testing.T) {
	payload := `{"v":"2","ps":"东京 01","add":"jp.example.com","port":"443","id":"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee","aid":"0","scy":"auto","net":"ws","type":"none","host":"jp.example.com","path":"/vm?ed=2048","tls":"tls","sni":"jp.example.com","fp":"chrome","alpn":"h2,http/1.1"}`
	uri := "vmess://" + base64.StdEncoding.EncodeToString([]byte(payload))

	nodes, _ := mustNormalize(t, uri)
	node := nodes[0]
	if node["type"] != "vmess" || node["tag"] != "东京 01" {
		t.Fatalf("vmess 基本字段错误: %#v", node)
	}
	if node["server_port"].(float64) != 443 || node["alter_id"].(float64) != 0 {
		t.Fatalf("vmess 端口/alter_id 错误: %#v", node)
	}
	transport, _ := node["transport"].(map[string]any)
	if transport["path"] != "/vm" {
		t.Fatalf("early data 路径未剥离: %#v", transport)
	}
	if transport["max_early_data"].(float64) != 2048 {
		t.Fatalf("max_early_data 错误: %#v", transport)
	}
	headers, _ := transport["headers"].(map[string]any)
	if headers["Host"] != "jp.example.com" {
		t.Fatalf("ws Host 头错误: %#v", transport)
	}
	tls, _ := node["tls"].(map[string]any)
	alpn, _ := tls["alpn"].([]any)
	if len(alpn) != 2 || alpn[0] != "h2" {
		t.Fatalf("alpn 错误: %#v", tls)
	}
}

func TestLegacyShadowsocksWholeBase64(t *testing.T) {
	inner := "chacha20-ietf-poly1305:mypass@ss.example.com:8443"
	uri := "ss://" + base64.RawStdEncoding.EncodeToString([]byte(inner)) + "#Legacy"

	nodes, _ := mustNormalize(t, uri)
	node := nodes[0]
	if node["method"] != "chacha20-ietf-poly1305" || node["password"] != "mypass" {
		t.Fatalf("旧版 ss 解析错误: %#v", node)
	}
	if node["server"] != "ss.example.com" || node["server_port"].(float64) != 8443 {
		t.Fatalf("旧版 ss 地址错误: %#v", node)
	}
	if node["tag"] != "Legacy" {
		t.Fatalf("旧版 ss tag 错误: %#v", node)
	}
}

func TestPlainURIListAndSkips(t *testing.T) {
	body := strings.Join([]string{
		"ss://YWVzLTI1Ni1nY206cHc@1.1.1.1:80#ok",
		"ssr://c29tZXRoaW5n",
		"这是一行说明文字",
		"tuic://uuid-1:tuicpass@t.example.com:443?alpn=h3&sni=t.example.com#TUIC",
	}, "\n")

	nodes, info := mustNormalize(t, body)
	if info.Format != FormatPlainURI {
		t.Fatalf("明文列表未识别: %s", info.Format)
	}
	if len(nodes) != 2 {
		t.Fatalf("节点数 = %d, 期望 2", len(nodes))
	}
	if info.Skipped != 2 {
		t.Fatalf("跳过数 = %d, 期望 2", info.Skipped)
	}
	tuic := nodes[1]
	if tuic["type"] != "tuic" || tuic["uuid"] != "uuid-1" || tuic["password"] != "tuicpass" {
		t.Fatalf("tuic 解析错误: %#v", tuic)
	}
	if tuic["congestion_control"] != "bbr" {
		t.Fatalf("tuic 默认拥塞控制错误: %#v", tuic)
	}
}

func TestSingBoxJSONPassthrough(t *testing.T) {
	raw := `{"outbounds":[{"type":"shadowsocks","tag":"a","server":"1.1.1.1","server_port":80,"method":"aes-128-gcm","password":"p"}]}`
	out, info, err := Normalize([]byte(raw))
	if err != nil {
		t.Fatalf("透传失败: %v", err)
	}
	if info.Format != FormatSingBox || info.NodeCount != 1 {
		t.Fatalf("透传信息错误: %#v", info)
	}
	if string(out) != raw {
		t.Fatalf("透传应保持字节不变")
	}
}

func TestClashYAMLDetected(t *testing.T) {
	raw := "port: 7890\nmixed-port: 7890\nproxies:\n  - name: a\n    type: ss\n"
	_, info, err := Normalize([]byte(raw))
	if err == nil {
		t.Fatal("Clash YAML 应当报错")
	}
	if info.Format != FormatClashYAML {
		t.Fatalf("Clash 格式未识别: %s", info.Format)
	}
}

func TestTagDeduplication(t *testing.T) {
	body := strings.Join([]string{
		"ss://YWVzLTI1Ni1nY206cHc@1.1.1.1:80#dup",
		"ss://YWVzLTI1Ni1nY206cHc@1.1.1.2:80#dup",
		"ss://YWVzLTI1Ni1nY206cHc@1.1.1.3:80#dup",
	}, "\n")
	nodes, _ := mustNormalize(t, body)
	if len(nodes) != 3 {
		t.Fatalf("重名节点被丢弃, 只剩 %d", len(nodes))
	}
	tags := map[string]bool{}
	for _, n := range nodes {
		tag := n["tag"].(string)
		if tags[tag] {
			t.Fatalf("tag 仍然重复: %s", tag)
		}
		tags[tag] = true
	}
	if !tags["dup"] || !tags["dup #1"] || !tags["dup #2"] {
		t.Fatalf("去重命名不符合预期: %#v", tags)
	}
}

func TestEmptyAndGarbage(t *testing.T) {
	if _, _, err := Normalize([]byte("   ")); err == nil {
		t.Fatal("空内容应报错")
	}
	if _, _, err := Normalize([]byte("客户端版本过低，请升级")); err == nil {
		t.Fatal("面板文字提示应报错而不是当成节点")
	}
}
