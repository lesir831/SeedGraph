package iyuu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestNewUsesSecureDefaultsAndInjectedDependencies(t *testing.T) {
	t.Parallel()

	client, err := New(Config{})
	if err != nil {
		t.Fatalf("New defaults: %v", err)
	}
	if client.endpoint != DefaultSitesEndpoint {
		t.Fatalf("endpoint = %q, want %q", client.endpoint, DefaultSitesEndpoint)
	}
	if client.httpClient.Timeout != DefaultTimeout {
		t.Fatalf("timeout = %s, want %s", client.httpClient.Timeout, DefaultTimeout)
	}

	injected := &http.Client{Timeout: 3 * time.Second}
	client, err = New(Config{
		BaseURL:    "https://catalog.example/proxy/",
		HTTPClient: injected,
	})
	if err != nil {
		t.Fatalf("New injected: %v", err)
	}
	if client.httpClient != injected {
		t.Fatal("New did not retain the injected HTTP client")
	}
	if client.endpoint != "https://catalog.example/proxy/reseed/sites/index" {
		t.Fatalf("endpoint = %q", client.endpoint)
	}

	client, err = New(Config{BaseURL: "https://catalog.example/reseed/sites/index/"})
	if err != nil {
		t.Fatalf("New full endpoint: %v", err)
	}
	if client.endpoint != "https://catalog.example/reseed/sites/index" {
		t.Fatalf("full endpoint = %q", client.endpoint)
	}
}

func TestNewRejectsUnsafeConfiguration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		config Config
	}{
		{name: "relative URL", config: Config{BaseURL: "iyuu.local"}},
		{name: "unsupported scheme", config: Config{BaseURL: "ftp://iyuu.local"}},
		{name: "credentials", config: Config{BaseURL: "https://user:password@iyuu.local"}},
		{name: "query", config: Config{BaseURL: "https://iyuu.local?token=secret"}},
		{name: "fragment", config: Config{BaseURL: "https://iyuu.local/#fragment"}},
		{name: "newline token", config: Config{BaseURL: "https://iyuu.local", Token: "secret\nheader"}},
		{name: "spaced token", config: Config{BaseURL: "https://iyuu.local", Token: "secret token"}},
		{name: "oversized token", config: Config{BaseURL: "https://iyuu.local", Token: strings.Repeat("x", 4097)}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client, err := New(test.config)
			if client != nil || !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("New() = (%v, %v), want ErrInvalidConfig", client, err)
			}
			if strings.Contains(err.Error(), "password") || strings.Contains(err.Error(), "secret") {
				t.Fatalf("configuration error leaked a credential: %v", err)
			}
		})
	}
}

func TestSitesSuccessUsesContractAndReturnsCompleteCatalog(t *testing.T) {
	t.Parallel()
	const token = "IYUU123456Tcatalog-token"
	want := []Site{
		{
			ID: 1, Site: "keepfrds", Nickname: "朋友", BaseURL: "pt.keepfrds.com",
			DownloadPage: "download.php?id={}&passkey={passkey}",
			DetailsPage:  "details.php?id={}", IsHTTPS: 2, CookieRequired: 0,
		},
		{
			ID: 3, Site: "m-team", Nickname: "馒头", BaseURL: "api.m-team.cc",
			DownloadPage: "api/torrent/genDlToken", DetailsPage: "detail/{}",
			IsHTTPS: 1, CookieRequired: 1,
		},
	}

	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", request.Method)
		}
		if request.URL.String() != "https://iyuu.test/prefix/reseed/sites/index" {
			t.Errorf("URL = %q", request.URL.String())
		}
		if request.URL.RawQuery != "" || request.Body != nil {
			t.Errorf("catalog request unexpectedly has query or body")
		}
		if got := request.Header.Get("token"); got != token {
			t.Errorf("token header = %q, want exact token", got)
		}
		if got := request.Header.Get("Accept"); got != "application/json" {
			t.Errorf("Accept = %q", got)
		}
		if got := request.Header.Get("User-Agent"); got != "SeedGraph/iyuu-catalog" {
			t.Errorf("User-Agent = %q", got)
		}
		return jsonResponse(http.StatusOK, catalogJSON(t, want, len(want)), nil), nil
	})
	client := mustClient(t, Config{
		BaseURL:    "https://iyuu.test/prefix/",
		Token:      token,
		HTTPClient: &http.Client{Transport: transport},
	})

	got, err := client.Sites(context.Background())
	if err != nil {
		t.Fatalf("Sites: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("site count = %d, want %d", len(got), len(want))
	}
	for index := range want {
		if got[index] != want[index] {
			t.Errorf("site %d = %#v, want %#v", index, got[index], want[index])
		}
	}
}

func TestSitesReturnsGenericCloseFailureWithoutCatalog(t *testing.T) {
	t.Parallel()
	const payload = `{"code":0,"data":{"count":0,"sites":[]},"msg":"ok"}`
	transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
		response := jsonResponse(http.StatusOK, payload, nil)
		response.Body = closeErrorBody{Reader: strings.NewReader(payload)}
		return response, nil
	})
	client := mustClient(t, Config{
		BaseURL:    "https://iyuu.test",
		HTTPClient: &http.Client{Transport: transport},
	})

	sites, err := client.Sites(context.Background())
	if sites != nil || err == nil || err.Error() != "close IYUU response body" {
		t.Fatalf("Sites() = (%#v, %v), want nil catalog and generic close error", sites, err)
	}
}

func TestSitesOmitsEmptyTokenHeaderAndAcceptsEmptyCatalog(t *testing.T) {
	t.Parallel()
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if _, exists := request.Header["Token"]; exists {
			t.Error("empty token unexpectedly produced a Token header")
		}
		return jsonResponse(http.StatusOK, `{"code":0,"data":{"count":0,"sites":[]},"msg":"ok"}`, nil), nil
	})
	client := mustClient(t, Config{
		BaseURL:    "https://iyuu.test",
		HTTPClient: &http.Client{Transport: transport},
	})
	sites, err := client.Sites(context.Background())
	if err != nil {
		t.Fatalf("Sites: %v", err)
	}
	if sites == nil || len(sites) != 0 {
		t.Fatalf("sites = %#v, want a non-nil empty catalog", sites)
	}
}

func TestSitesRejectsMalformedEnvelopes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		contentType string
		body        string
	}{
		{name: "wrong content type", contentType: "text/plain", body: `{"code":0,"data":{"count":0,"sites":[]}}`},
		{name: "missing content type", contentType: "", body: `{"code":0,"data":{"count":0,"sites":[]}}`},
		{name: "malformed JSON", contentType: "application/json", body: `{"code":`},
		{name: "trailing JSON", contentType: "application/json", body: `{"code":0,"data":{"count":0,"sites":[]}} {}`},
		{name: "missing code", contentType: "application/json", body: `{"data":{"count":0,"sites":[]}}`},
		{name: "string code", contentType: "application/json", body: `{"code":"0","data":{"count":0,"sites":[]}}`},
		{name: "missing data", contentType: "application/json", body: `{"code":0}`},
		{name: "null data", contentType: "application/json", body: `{"code":0,"data":null}`},
		{name: "array data", contentType: "application/json", body: `{"code":0,"data":[]}`},
		{name: "missing count", contentType: "application/json", body: `{"code":0,"data":{"sites":[]}}`},
		{name: "missing sites", contentType: "application/json", body: `{"code":0,"data":{"count":0}}`},
		{name: "null sites", contentType: "application/json", body: `{"code":0,"data":{"count":0,"sites":null}}`},
		{name: "negative count", contentType: "application/json", body: `{"code":0,"data":{"count":-1,"sites":[]}}`},
		{name: "count mismatch", contentType: "application/json", body: `{"code":0,"data":{"count":1,"sites":[]}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			client := clientReturning(t, http.StatusOK, test.contentType, test.body, nil)
			sites, err := client.Sites(context.Background())
			if sites != nil || !errors.Is(err, ErrInvalidResponse) {
				t.Fatalf("Sites() = (%#v, %v), want ErrInvalidResponse", sites, err)
			}
		})
	}
}

func TestSitesValidatesEveryEntryAllOrNothing(t *testing.T) {
	t.Parallel()

	base := []Site{
		{ID: 1, Site: "valid-one", BaseURL: "one.example", IsHTTPS: 2, CookieRequired: 0},
		{ID: 2, Site: "valid-two", BaseURL: "two.example", IsHTTPS: 1, CookieRequired: 1},
	}
	tests := []struct {
		name   string
		mutate func([]Site)
	}{
		{name: "zero id", mutate: func(sites []Site) { sites[1].ID = 0 }},
		{name: "negative id", mutate: func(sites []Site) { sites[1].ID = -1 }},
		{name: "uppercase slug", mutate: func(sites []Site) { sites[1].Site = "Invalid" }},
		{name: "spaced slug", mutate: func(sites []Site) { sites[1].Site = "invalid slug" }},
		{name: "empty base URL", mutate: func(sites []Site) { sites[1].BaseURL = "" }},
		{name: "base URL scheme", mutate: func(sites []Site) { sites[1].BaseURL = "https://two.example" }},
		{name: "base URL path", mutate: func(sites []Site) { sites[1].BaseURL = "two.example/path" }},
		{name: "base URL port", mutate: func(sites []Site) { sites[1].BaseURL = "two.example:443" }},
		{name: "single label host", mutate: func(sites []Site) { sites[1].BaseURL = "localhost" }},
		{name: "empty host label", mutate: func(sites []Site) { sites[1].BaseURL = "two..example" }},
		{name: "negative HTTPS enum", mutate: func(sites []Site) { sites[1].IsHTTPS = -1 }},
		{name: "large HTTPS enum", mutate: func(sites []Site) { sites[1].IsHTTPS = 3 }},
		{name: "negative cookie flag", mutate: func(sites []Site) { sites[1].CookieRequired = -1 }},
		{name: "large cookie flag", mutate: func(sites []Site) { sites[1].CookieRequired = 2 }},
		{name: "duplicate id", mutate: func(sites []Site) { sites[1].ID = sites[0].ID }},
		{name: "duplicate slug", mutate: func(sites []Site) { sites[1].Site = sites[0].Site }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			sites := append([]Site(nil), base...)
			test.mutate(sites)
			client := clientReturning(t, http.StatusOK, "application/json", catalogJSON(t, sites, len(sites)), nil)
			got, err := client.Sites(context.Background())
			if got != nil || !errors.Is(err, ErrInvalidResponse) {
				t.Fatalf("Sites() = (%#v, %v), want nil ErrInvalidResponse", got, err)
			}
			if strings.Contains(err.Error(), sites[1].BaseURL) && sites[1].BaseURL != "" {
				t.Fatalf("validation error exposed remote field value: %v", err)
			}
		})
	}
}

func TestSitesHTTPErrorIsBodySafeAndExposesRetryHint(t *testing.T) {
	t.Parallel()
	const token = "IYUU-secret-token"
	reset := time.Unix(2_000_000_000, 0).UTC()
	header := make(http.Header)
	header.Set("Content-Type", "text/plain")
	header.Set("Retry-After", "30")
	header.Set("X-RateLimit-Limit", "50")
	header.Set("X-RateLimit-Reset", strconvUnix(reset))
	client := mustClient(t, Config{
		BaseURL: "https://iyuu.test", Token: token,
		HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return rawResponse(http.StatusTooManyRequests, header, "body echoes "+token+" passkey=do-not-log"), nil
		})},
	})

	sites, err := client.Sites(context.Background())
	if sites != nil {
		t.Fatalf("sites = %#v", sites)
	}
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("error = %T %v", err, err)
	}
	if strings.Contains(err.Error(), token) || strings.Contains(err.Error(), "do-not-log") {
		t.Fatalf("HTTP error leaked response content: %v", err)
	}
	hint, ok := RetryHintFrom(fmt.Errorf("wrapped: %w", err))
	if !ok || hint.After != 30*time.Second || hint.Limit != 50 || !hint.ResetAt.Equal(reset) {
		t.Fatalf("retry hint = %+v, %v", hint, ok)
	}
}

func TestSitesHTTPDateRetryHint(t *testing.T) {
	t.Parallel()
	want := time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)
	header := make(http.Header)
	header.Set("Retry-After", want.Format(http.TimeFormat))
	client := clientReturning(t, http.StatusServiceUnavailable, "text/plain", "unavailable", header)
	_, err := client.Sites(context.Background())
	hint, ok := RetryHintFrom(err)
	if !ok || !hint.ResetAt.Equal(want) || hint.After != 0 {
		t.Fatalf("retry hint = %+v, %v", hint, ok)
	}
}

func TestSitesApplicationErrorRedactsSecretsAndExposesRetryHint(t *testing.T) {
	t.Parallel()
	const token = "IYUU-secret-token"
	reset := time.Unix(2_000_000_100, 0).UTC()
	body, err := json.Marshal(map[string]any{
		"code": 429,
		"msg":  "slow down token=" + token + " passkey=announce-secret\nnext",
		"data": map[string]any{
			"Retry-After":       "45",
			"X-RateLimit-Limit": 10,
			"X-RateLimit-Reset": strconvUnix(reset),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	header := make(http.Header)
	header.Set("Retry-After", "5")
	header.Set("X-RateLimit-Limit", "2")
	client := mustClient(t, Config{
		BaseURL: "https://iyuu.test", Token: token,
		HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return jsonResponse(http.StatusOK, string(body), header), nil
		})},
	})

	sites, fetchErr := client.Sites(context.Background())
	if sites != nil {
		t.Fatalf("sites = %#v", sites)
	}
	var apiErr *APIError
	if !errors.As(fetchErr, &apiErr) || apiErr.Code != 429 {
		t.Fatalf("error = %T %v", fetchErr, fetchErr)
	}
	if strings.Contains(fetchErr.Error(), token) || strings.Contains(fetchErr.Error(), "announce-secret") || strings.Contains(fetchErr.Error(), "\n") {
		t.Fatalf("API error was not sanitized: %v", fetchErr)
	}
	if !strings.Contains(fetchErr.Error(), "[redacted]") {
		t.Fatalf("API error did not mark redaction: %v", fetchErr)
	}
	hint, ok := RetryHintFrom(fetchErr)
	if !ok || hint.After != 45*time.Second || hint.Limit != 10 || !hint.ResetAt.Equal(reset) {
		t.Fatalf("retry hint = %+v, %v", hint, ok)
	}
}

func TestSitesLiveStyleRateLimitErrorWithoutHint(t *testing.T) {
	t.Parallel()
	client := clientReturning(t, http.StatusOK, "application/json", `{"code":400,"data":[],"msg":"访问频率过快"}`, nil)
	_, err := client.Sites(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != 400 || apiErr.Message != "访问频率过快" {
		t.Fatalf("error = %T %v", err, err)
	}
	if _, ok := RetryHintFrom(err); ok {
		t.Fatal("unexpected retry metadata for live-style data: [] error")
	}
}

func TestSitesRejectsOversizedResponseBeforeDecoding(t *testing.T) {
	t.Parallel()
	client := clientReturning(t, http.StatusOK, "application/json", strings.Repeat("x", int(MaxResponseBytes)+1), nil)
	sites, err := client.Sites(context.Background())
	if sites != nil || !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("Sites() = (%#v, %v), want ErrResponseTooLarge", sites, err)
	}
}

func TestSitesTransportErrorsAreRedactedAndCancellationIsPreserved(t *testing.T) {
	t.Parallel()
	const token = "IYUU-secret-token"
	client := mustClient(t, Config{
		BaseURL: "https://iyuu.test", Token: token,
		HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, fmt.Errorf("dial failed token=%s passkey=announce-secret", token)
		})},
	})
	_, err := client.Sites(context.Background())
	if err == nil || strings.Contains(err.Error(), token) || strings.Contains(err.Error(), "announce-secret") {
		t.Fatalf("transport error was not safely redacted: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client = mustClient(t, Config{
		BaseURL: "https://iyuu.test",
		HTTPClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			return nil, request.Context().Err()
		})},
	})
	_, err = client.Sites(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

type closeErrorBody struct {
	io.Reader
}

func (closeErrorBody) Close() error {
	return errors.New("sensitive transport detail")
}

func mustClient(t *testing.T, config Config) *Client {
	t.Helper()
	client, err := New(config)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return client
}

func clientReturning(t *testing.T, status int, contentType, body string, header http.Header) *Client {
	t.Helper()
	return mustClient(t, Config{
		BaseURL: "https://iyuu.test",
		HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			if header == nil {
				header = make(http.Header)
			} else {
				header = header.Clone()
			}
			if contentType != "" {
				header.Set("Content-Type", contentType)
			}
			return rawResponse(status, header, body), nil
		})},
	})
}

func jsonResponse(status int, body string, header http.Header) *http.Response {
	if header == nil {
		header = make(http.Header)
	} else {
		header = header.Clone()
	}
	header.Set("Content-Type", "application/json; charset=utf-8")
	return rawResponse(status, header, body)
}

func rawResponse(status int, header http.Header, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header:     header,
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func catalogJSON(t *testing.T, sites []Site, count int) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"code": 0,
		"data": map[string]any{"count": count, "sites": sites},
		"msg":  "ok",
	})
	if err != nil {
		t.Fatalf("marshal catalog: %v", err)
	}
	return string(body)
}

func strconvUnix(value time.Time) string {
	return fmt.Sprintf("%d", value.Unix())
}
