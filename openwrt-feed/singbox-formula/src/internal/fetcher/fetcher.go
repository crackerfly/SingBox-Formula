package fetcher

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/haierkeys/singbox-subscribe-convert/global"
)

const (
	NodeResponseLimit     int64 = 32 << 20
	TemplateResponseLimit int64 = 8 << 20
)

var ErrResponseTooLarge = errors.New("response exceeds configured limit")

// Client fetches complete, bounded response bodies without writing formal
// cache files. Callers own validation and atomic persistence.
type Client struct {
	http   *http.Client
	logger *zap.Logger
}

func NewClient(c *global.Config, l *zap.Logger) *Client {
	if l == nil {
		l = zap.NewNop()
	}
	timeout := 30 * time.Second
	if c != nil {
		timeout = c.GetRequestTimeout()
	}
	return &Client{
		logger: l,
		http: &http.Client{
			Timeout: timeout,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{
					InsecureSkipVerify: false,
				},
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 10,
				IdleConnTimeout:     90 * time.Second,
			},
		},
	}
}

// FetchBytes downloads a non-empty body up to limit bytes. The returned URL is
// the exact cache-busted value used for the request and intentionally retains
// the full original query string for compatibility with existing diagnostics.
func (c *Client) FetchBytes(ctx context.Context, rawURL string, limit int64) ([]byte, string, error) {
	if c == nil || c.http == nil {
		return nil, rawURL, errors.New("fetch client is not initialized")
	}
	if limit <= 0 {
		return nil, rawURL, fmt.Errorf("invalid response limit: %d", limit)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	actualURL := addCacheBusterParam(rawURL)
	c.logger.Debug("🚀 [DOWNLOAD] Starting fetch from URL: " + actualURL)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, actualURL, nil)
	if err != nil {
		return nil, actualURL, fmt.Errorf("create request error: %w", err)
	}

	// Use browser User-Agent to prevent subscription conversion servers from
	// returning degraded nodes.
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Cache-Control", "no-cache, no-store, must-revalidate")
	req.Header.Set("Pragma", "no-cache")
	req.Header.Set("Expires", "0")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, actualURL, fmt.Errorf("fetch error: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, actualURL, fmt.Errorf("fetch failed with status: %d", resp.StatusCode)
	}
	if resp.ContentLength > limit {
		return nil, actualURL, fmt.Errorf("%w: content length %d > %d", ErrResponseTooLarge, resp.ContentLength, limit)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, actualURL, fmt.Errorf("read response error: %w", err)
	}
	if int64(len(data)) > limit {
		return nil, actualURL, fmt.Errorf("%w: body length > %d", ErrResponseTooLarge, limit)
	}
	if len(data) == 0 {
		return nil, actualURL, errors.New("received empty file")
	}
	return data, actualURL, nil
}

func IsFileExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !info.IsDir() && info.Size() > 0
}

// GetFileModTime 获取文件修改时间
func GetFileModTime(path string) time.Time {
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}

// addCacheBusterParam 给 URL 添加随机数参数以绕过 CDN 缓存
// Add cache buster parameter to URL to bypass CDN cache.
func addCacheBusterParam(url string) string {
	if strings.Contains(url, "_t=") || strings.Contains(url, "_r=") {
		return url
	}
	separator := "?"
	if strings.Contains(url, "?") {
		separator = "&"
	}

	// 生成 4 字节的随机十六进制字符串 (8个字符)
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	randomStr := hex.EncodeToString(b)

	// 使用时间戳 and 随机字符串
	timestamp := time.Now().UnixNano()
	return fmt.Sprintf("%s%s_t=%d&_r=%s", url, separator, timestamp, randomStr)
}
