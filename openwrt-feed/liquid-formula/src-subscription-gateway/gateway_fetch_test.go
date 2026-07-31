package main

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestHTTPSourceFetcherRequiresExact200AndUserAgent(t *testing.T) {
	const userAgent = "Liquid-Formula-Slice2/1.8.3"
	statuses := []int{200, 201, 204, 206, 299, 300, 404, 500}
	for _, status := range statuses {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(
				func(writer http.ResponseWriter, request *http.Request) {
					if got := request.Header.Get("User-Agent"); got != userAgent {
						t.Errorf("User-Agent = %q, want %q", got, userAgent)
					}
					writer.WriteHeader(status)
					_, _ = writer.Write([]byte("body"))
				},
			))
			defer server.Close()

			result := newHTTPSourceFetcher(http.DefaultTransport).Fetch(
				context.Background(), server.URL, userAgent,
			)
			if status == http.StatusOK {
				if result.Code != fetchCodeOK || string(result.Body) != "body" {
					t.Fatalf("200 result = %#v", result)
				}
			} else if result.Code != fetchCodeHTTPStatus ||
				len(result.Body) != 0 {
				t.Fatalf("status %d result = %#v", status, result)
			}
		})
	}
}

func TestHTTPSourceFetcherUsesEffectiveUserAgentForInitialAndRedirect(t *testing.T) {
	const defaultUserAgent = "sing-box 1.11.0"
	for _, testCase := range []struct {
		name string
		raw  string
		want string
	}{
		{name: "empty", raw: "", want: defaultUserAgent},
		{name: "whitespace", raw: " \t  ", want: defaultUserAgent},
		{
			name: "trimmed",
			raw:  "  Liquid-Formula-Custom/1.8.3 \t",
			want: "Liquid-Formula-Custom/1.8.3",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			var userAgents []string
			transport := roundTripFunc(func(request *http.Request) (
				*http.Response, error,
			) {
				userAgents = append(
					userAgents, request.Header.Get("User-Agent"),
				)
				response := &http.Response{
					StatusCode: http.StatusOK,
					Status:     http.StatusText(http.StatusOK),
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader("ok")),
					Request:    request,
				}
				if len(userAgents) == 1 {
					response.StatusCode = http.StatusFound
					response.Status = http.StatusText(http.StatusFound)
					response.Header.Set(
						"Location",
						"http://effective-user-agent.invalid/final",
					)
					response.Body = io.NopCloser(strings.NewReader(""))
				}
				return response, nil
			})
			result := newHTTPSourceFetcher(transport).Fetch(
				context.Background(),
				"http://effective-user-agent.invalid/start",
				testCase.raw,
			)
			if result.Code != fetchCodeOK || string(result.Body) != "ok" {
				t.Fatalf("fetch result = %#v", result)
			}
			if fmt.Sprint(userAgents) !=
				fmt.Sprintf("[%s %s]", testCase.want, testCase.want) {
				t.Fatalf("User-Agents = %q, want [%q %q]",
					userAgents, testCase.want, testCase.want)
			}
		})
	}
}

func TestHTTPSourceFetcherRedirectPolicyAndHeaderReset(t *testing.T) {
	const (
		userAgent = "Liquid-Formula-Redirect/1.8.3"
		canary    = "REDIRECT_QUERY_SECRET_CANARY"
	)
	var mutex sync.Mutex
	requests := make(map[string]int)
	var headerFailures []string
	server := httptest.NewServer(http.HandlerFunc(
		func(writer http.ResponseWriter, request *http.Request) {
			mutex.Lock()
			requests[request.URL.Path]++
			if got := request.Header.Get("User-Agent"); got != userAgent {
				headerFailures = append(headerFailures,
					fmt.Sprintf("ua=%q path=%s", got, request.URL.Path))
			}
			if referer := request.Header.Get("Referer"); referer != "" {
				headerFailures = append(headerFailures,
					fmt.Sprintf("referer=%q path=%s", referer, request.URL.Path))
			}
			mutex.Unlock()

			parts := strings.Split(strings.TrimPrefix(request.URL.Path, "/"), "/")
			if len(parts) != 2 {
				http.NotFound(writer, request)
				return
			}
			step, err := strconv.Atoi(parts[1])
			if err != nil {
				http.NotFound(writer, request)
				return
			}
			switch parts[0] {
			case "five":
				if step < 5 {
					http.Redirect(writer, request,
						fmt.Sprintf("/five/%d", step+1),
						http.StatusFound)
					return
				}
				_, _ = writer.Write([]byte("five-ok"))
			case "six":
				if step < 6 {
					http.Redirect(writer, request,
						fmt.Sprintf("/six/%d", step+1),
						http.StatusFound)
					return
				}
				_, _ = writer.Write([]byte("must-not-open"))
			case "unsafe":
				writer.Header().Set("Location",
					"ftp://redirect.invalid/"+canary)
				writer.WriteHeader(http.StatusFound)
			default:
				http.NotFound(writer, request)
			}
		},
	))
	defer server.Close()
	fetcher := newHTTPSourceFetcher(http.DefaultTransport)

	five := fetcher.Fetch(
		context.Background(),
		server.URL+"/five/0?token="+canary,
		userAgent,
	)
	if five.Code != fetchCodeOK || string(five.Body) != "five-ok" {
		t.Fatalf("five redirects = %#v", five)
	}
	six := fetcher.Fetch(
		context.Background(),
		server.URL+"/six/0?token="+canary,
		userAgent,
	)
	if six.Code != fetchCodeRedirectLimit || len(six.Body) != 0 {
		t.Fatalf("six redirects = %#v", six)
	}
	unsafe := fetcher.Fetch(
		context.Background(),
		server.URL+"/unsafe/0?token="+canary,
		userAgent,
	)
	if unsafe.Code != fetchCodeRedirectLimit || len(unsafe.Body) != 0 {
		t.Fatalf("unsafe redirect = %#v", unsafe)
	}

	mutex.Lock()
	defer mutex.Unlock()
	if len(headerFailures) != 0 {
		t.Fatalf("redirect headers were not reset: %v", headerFailures)
	}
	if requests["/six/6"] != 0 {
		t.Fatalf("sixth redirect target was opened %d times", requests["/six/6"])
	}
}

func TestHTTPSourceFetcherLimitsDecodedBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(
		func(writer http.ResponseWriter, request *http.Request) {
			writer.Header().Set("Content-Encoding", "gzip")
			gzipWriter := gzip.NewWriter(writer)
			size := MaxInputBytes
			if request.URL.Path == "/too-large" {
				size++
			}
			block := make([]byte, 32*1024)
			for remaining := size; remaining > 0; {
				writeSize := len(block)
				if writeSize > remaining {
					writeSize = remaining
				}
				if _, err := gzipWriter.Write(block[:writeSize]); err != nil {
					return
				}
				remaining -= writeSize
			}
			_ = gzipWriter.Close()
		},
	))
	defer server.Close()
	fetcher := newHTTPSourceFetcher(http.DefaultTransport)

	exact := fetcher.Fetch(
		context.Background(), server.URL+"/exact", "decoded-limit",
	)
	if exact.Code != fetchCodeOK || len(exact.Body) != MaxInputBytes {
		t.Fatalf("exact decoded boundary = code=%q bytes=%d",
			exact.Code, len(exact.Body))
	}
	exact.Body = nil
	tooLarge := fetcher.Fetch(
		context.Background(), server.URL+"/too-large", "decoded-limit",
	)
	if tooLarge.Code != fetchCodeBodyTooLarge || len(tooLarge.Body) != 0 {
		t.Fatalf("decoded boundary + 1 = %#v", tooLarge)
	}
}

func TestHTTPSourceFetcherUsesOneDeadlineAcrossRedirects(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(
		func(writer http.ResponseWriter, request *http.Request) {
			time.Sleep(80 * time.Millisecond)
			step, err := strconv.Atoi(strings.TrimPrefix(request.URL.Path, "/"))
			if err != nil {
				http.NotFound(writer, request)
				return
			}
			if step < 3 {
				http.Redirect(writer, request,
					fmt.Sprintf("/%d", step+1), http.StatusFound)
				return
			}
			_, _ = writer.Write([]byte("late"))
		},
	))
	defer server.Close()
	ctx, cancel := context.WithTimeout(
		context.Background(), 180*time.Millisecond,
	)
	defer cancel()
	result := newHTTPSourceFetcher(http.DefaultTransport).Fetch(
		ctx, server.URL+"/0", "single-deadline",
	)
	if result.Code != fetchCodeTimeout || len(result.Body) != 0 {
		t.Fatalf("redirect deadline result = %#v", result)
	}
}

func TestHTTPSourceFetcherClosesEveryResponseBody(t *testing.T) {
	cases := []struct {
		name     string
		status   int
		reader   io.Reader
		wantCode sourceFetchCode
	}{
		{
			name: "success", status: http.StatusOK,
			reader: strings.NewReader("ok"), wantCode: fetchCodeOK,
		},
		{
			name: "http status", status: http.StatusNoContent,
			reader:   strings.NewReader("ignored"),
			wantCode: fetchCodeHTTPStatus,
		},
		{
			name: "read error", status: http.StatusOK,
			reader:   errorReader{err: errors.New("READ_SECRET_CANARY")},
			wantCode: fetchCodeTransport,
		},
		{
			name: "too large", status: http.StatusOK,
			reader:   &boundedZeroReader{remaining: MaxInputBytes + 1},
			wantCode: fetchCodeBodyTooLarge,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			body := &trackingReadCloser{Reader: testCase.reader}
			transport := roundTripFunc(func(request *http.Request) (
				*http.Response, error,
			) {
				return &http.Response{
					StatusCode: testCase.status,
					Status:     http.StatusText(testCase.status),
					Header:     make(http.Header),
					Body:       body,
					Request:    request,
				}, nil
			})
			result := newHTTPSourceFetcher(transport).Fetch(
				context.Background(),
				"http://close-body.invalid/list",
				"close-body",
			)
			if result.Code != testCase.wantCode {
				t.Fatalf("result = %#v, want code %q",
					result, testCase.wantCode)
			}
			if !body.closed {
				t.Fatal("response body was not closed")
			}
		})
	}
}

func TestHTTPSourceFetcherDrainsUnknownLengthBoundaryForConnectionReuse(
	t *testing.T,
) {
	var (
		connections atomic.Int32
		requests    atomic.Int32
	)
	server := httptest.NewUnstartedServer(http.HandlerFunc(
		func(writer http.ResponseWriter, request *http.Request) {
			if requests.Add(1) == 1 {
				writer.WriteHeader(http.StatusInternalServerError)
				writer.(http.Flusher).Flush()
				_, _ = writer.Write(make([]byte, 2048))
				return
			}
			_, _ = writer.Write([]byte("reused"))
		},
	))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connections.Add(1)
		}
	}
	server.Start()
	defer server.Close()

	fetcher := newHTTPSourceFetcher(server.Client().Transport)
	first := fetcher.Fetch(
		context.Background(), server.URL, "drain-small",
	)
	second := fetcher.Fetch(
		context.Background(), server.URL, "drain-small",
	)
	if first.Code != fetchCodeHTTPStatus || len(first.Body) != 0 {
		t.Fatalf("first result = %#v", first)
	}
	if second.Code != fetchCodeOK || string(second.Body) != "reused" {
		t.Fatalf("second result = %#v", second)
	}
	if got := connections.Load(); got != 1 {
		t.Fatalf("new connections = %d, want exactly 1", got)
	}
}

func TestHTTPSourceFetcherBoundsHTTPErrorDrain(t *testing.T) {
	body := &countingZeroReadCloser{remaining: 4096}
	transport := roundTripFunc(func(request *http.Request) (
		*http.Response, error,
	) {
		return &http.Response{
			StatusCode: http.StatusInternalServerError,
			Status:     http.StatusText(http.StatusInternalServerError),
			Header:     make(http.Header),
			Body:       body,
			Request:    request,
		}, nil
	})
	result := newHTTPSourceFetcher(transport).Fetch(
		context.Background(),
		"http://bounded-error-drain.invalid/list",
		"drain-large",
	)
	if result.Code != fetchCodeHTTPStatus || len(result.Body) != 0 {
		t.Fatalf("result = %#v", result)
	}
	if body.read != 2049 || body.remaining != 2047 || !body.closed {
		t.Fatalf("body read=%d remaining=%d closed=%v",
			body.read, body.remaining, body.closed)
	}
}

func TestHTTPSourceFetcherContainsTransportErrors(t *testing.T) {
	transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New(
			"TRANSPORT_SECRET_CANARY https://user:pass@example.invalid/token",
		)
	})
	result := newHTTPSourceFetcher(transport).Fetch(
		context.Background(),
		"http://transport.invalid/list?token=URL_SECRET_CANARY",
		"transport",
	)
	if result.Code != fetchCodeTransport ||
		len(result.Body) != 0 {
		t.Fatalf("transport error result = %#v", result)
	}
}

func TestSubscriptionEngineFetchesUniqueURLsSequentially(t *testing.T) {
	const (
		configDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		userAgent    = "Liquid-Formula-Unique/1.8.3"
	)
	var mutex sync.Mutex
	var order []string
	var aCalls, bCalls int
	newServer := func(name, body string, count *int) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(
			func(writer http.ResponseWriter, request *http.Request) {
				mutex.Lock()
				order = append(order, name)
				(*count)++
				mutex.Unlock()
				if request.Header.Get("User-Agent") != userAgent {
					t.Errorf("%s User-Agent = %q",
						name, request.Header.Get("User-Agent"))
				}
				_, _ = writer.Write([]byte(body))
			},
		))
	}
	serverA := newServer("A", "body-A", &aCalls)
	defer serverA.Close()
	serverB := newServer("B", "body-B", &bCalls)
	defer serverB.Close()

	store := &scriptedGenerationStore{
		observation: currentObservation{Kind: currentAbsent},
		selection:   currentSelection{Kind: currentAbsent},
		commitResult: generationCommitResult{
			Committed: true,
			Selection: currentSelection{
				Kind: currentPresent,
				Generation: validatedGeneration{
					GenerationID: "1111111111111111111111111111111111111111111111111111111111111111",
					ConfigDigest: configDigest,
					Aggregate:    []byte(`{"unique":true}`),
				},
			},
		},
	}
	normalizer := &capturingSourceNormalizer{}
	engine := newSubscriptionAggregateEngine(
		gatewayConfig{
			ConfigDigest:         configDigest,
			SourceTimeoutSeconds: 5,
			UserAgent:            userAgent,
			Sources: []gatewaySource{
				{URL: serverA.URL, URLDigest: testSHA256(serverA.URL)},
				{URL: serverA.URL, URLDigest: testSHA256(serverA.URL)},
				{URL: serverB.URL, URLDigest: testSHA256(serverB.URL)},
			},
		},
		subscriptionEngineDependencies{
			Locker:     &recordingSubscriptionLocker{},
			Store:      store,
			Fetcher:    newHTTPSourceFetcher(http.DefaultTransport),
			Normalizer: normalizer,
		},
	)
	if outcome := engine.Aggregate(context.Background()); outcome.Code != "" {
		t.Fatalf("aggregate outcome = %#v", outcome)
	}
	mutex.Lock()
	gotOrder := append([]string(nil), order...)
	mutex.Unlock()
	if fmt.Sprint(gotOrder) != "[A B]" || aCalls != 1 || bCalls != 1 {
		t.Fatalf("fetch order/calls = %v A=%d B=%d",
			gotOrder, aCalls, bCalls)
	}
	if fmt.Sprint(normalizer.inputs) != "[body-A body-B]" {
		t.Fatalf("normalize inputs = %q", normalizer.inputs)
	}
	if len(store.lastCandidate.Sources) != 3 {
		t.Fatalf("candidate occurrences = %d, want 3",
			len(store.lastCandidate.Sources))
	}
	if len(store.lastCandidate.Objects) != 2 {
		t.Fatalf("candidate objects = %d, want 2",
			len(store.lastCandidate.Objects))
	}
	for index, source := range store.lastCandidate.Sources {
		if source.Index != index+1 {
			t.Fatalf("candidate source %d index = %d", index, source.Index)
		}
		wantDigest := testSHA256(serverA.URL)
		if index == 2 {
			wantDigest = testSHA256(serverB.URL)
		}
		if source.URLDigest != wantDigest {
			t.Fatalf("candidate source %d digest = %q, want %q",
				index, source.URLDigest, wantDigest)
		}
		wantObjectIndex := 1
		if index == 2 {
			wantObjectIndex = 2
		}
		if source.ObjectIndex != wantObjectIndex {
			t.Fatalf("candidate source %d object index = %d, want %d",
				index, source.ObjectIndex, wantObjectIndex)
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(
	request *http.Request,
) (*http.Response, error) {
	return function(request)
}

type trackingReadCloser struct {
	io.Reader
	closed bool
}

func (reader *trackingReadCloser) Close() error {
	reader.closed = true
	return nil
}

type errorReader struct {
	err error
}

func (reader errorReader) Read([]byte) (int, error) {
	return 0, reader.err
}

type boundedZeroReader struct {
	remaining int
}

func (reader *boundedZeroReader) Read(buffer []byte) (int, error) {
	if reader.remaining == 0 {
		return 0, io.EOF
	}
	if len(buffer) > reader.remaining {
		buffer = buffer[:reader.remaining]
	}
	for index := range buffer {
		buffer[index] = 0
	}
	reader.remaining -= len(buffer)
	return len(buffer), nil
}

type countingZeroReadCloser struct {
	remaining int
	read      int
	closed    bool
}

func (reader *countingZeroReadCloser) Read(buffer []byte) (int, error) {
	if reader.remaining == 0 {
		return 0, io.EOF
	}
	if len(buffer) > reader.remaining {
		buffer = buffer[:reader.remaining]
	}
	for index := range buffer {
		buffer[index] = 0
	}
	reader.remaining -= len(buffer)
	reader.read += len(buffer)
	return len(buffer), nil
}

func (reader *countingZeroReadCloser) Close() error {
	reader.closed = true
	return nil
}

type capturingSourceNormalizer struct {
	inputs []string
}

func (normalizer *capturingSourceNormalizer) Normalize(
	raw []byte,
) ([]byte, NormalizeInfo, error) {
	normalizer.inputs = append(normalizer.inputs, string(raw))
	return []byte(fmt.Sprintf(
			`{"outbounds":[{"type":"socks","tag":%q}]}`,
			string(raw),
		)), NormalizeInfo{
			Format: FormatSingBoxJSON, Accepted: 1,
		}, nil
}
