package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
)

var errSourceRedirectPolicy = errors.New("source redirect rejected")

type httpSourceFetcher struct {
	transport http.RoundTripper
}

func newHTTPSourceFetcher(
	transport http.RoundTripper,
) *httpSourceFetcher {
	if transport == nil {
		transport = http.DefaultTransport
	}
	return &httpSourceFetcher{transport: transport}
}

func (fetcher *httpSourceFetcher) Fetch(
	ctx context.Context,
	rawURL string,
	userAgent string,
) sourceFetchResult {
	if fetcher == nil || fetcher.transport == nil {
		return sourceFetchResult{Code: fetchCodeTransport}
	}
	effectiveUserAgent := strings.TrimSpace(userAgent)
	if effectiveUserAgent == "" {
		effectiveUserAgent = "sing-box 1.11.0"
	}
	request, err := http.NewRequestWithContext(
		ctx, http.MethodGet, rawURL, nil,
	)
	if err != nil {
		return sourceFetchResult{Code: fetchCodeTransport}
	}
	request.Header.Set("User-Agent", effectiveUserAgent)
	client := &http.Client{
		Transport: fetcher.transport,
		CheckRedirect: func(
			next *http.Request,
			via []*http.Request,
		) error {
			if len(via) > 5 ||
				next.URL == nil ||
				next.URL.Host == "" ||
				next.URL.Hostname() == "" {
				return errSourceRedirectPolicy
			}
			scheme := strings.ToLower(next.URL.Scheme)
			if scheme != "http" && scheme != "https" {
				return errSourceRedirectPolicy
			}
			next.Header.Del("Referer")
			next.Header.Set("User-Agent", effectiveUserAgent)
			return nil
		},
	}
	response, err := client.Do(request)
	if err != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		switch {
		case ctx.Err() != nil:
			return sourceFetchResult{Code: fetchCodeTimeout}
		case errors.Is(err, errSourceRedirectPolicy):
			return sourceFetchResult{Code: fetchCodeRedirectLimit}
		default:
			return sourceFetchResult{Code: fetchCodeTransport}
		}
	}
	if response == nil || response.Body == nil {
		return sourceFetchResult{Code: fetchCodeTransport}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(
			io.Discard,
			io.LimitReader(response.Body, 2048+1),
		)
		return sourceFetchResult{Code: fetchCodeHTTPStatus}
	}
	body, err := io.ReadAll(io.LimitReader(
		response.Body, int64(MaxInputBytes)+1,
	))
	if err != nil {
		if ctx.Err() != nil {
			return sourceFetchResult{Code: fetchCodeTimeout}
		}
		return sourceFetchResult{Code: fetchCodeTransport}
	}
	if len(body) > MaxInputBytes {
		return sourceFetchResult{Code: fetchCodeBodyTooLarge}
	}
	if ctx.Err() != nil {
		return sourceFetchResult{Code: fetchCodeTimeout}
	}
	return sourceFetchResult{
		Body: body,
		Code: fetchCodeOK,
	}
}
