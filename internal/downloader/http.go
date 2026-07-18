package downloader

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"
)

const (
	defaultHTTPTimeout = 30 * time.Second
	maxJSONResponse    = int64(64 << 20)
	maxTextResponse    = int64(1 << 20)
)

func parseBaseURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, fmt.Errorf("%w: base URL is malformed", ErrInvalidConfig)
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, fmt.Errorf("%w: base URL must be absolute HTTP(S)", ErrInvalidConfig)
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("%w: base URL cannot contain credentials, query, or fragment", ErrInvalidConfig)
	}
	return parsed, nil
}

func cloneHTTPClient(source *http.Client, withCookies bool) (*http.Client, error) {
	client := &http.Client{}
	if source != nil {
		copy := *source
		client = &copy
	}
	if client.Timeout == 0 {
		client.Timeout = defaultHTTPTimeout
	}
	if withCookies && client.Jar == nil {
		jar, err := cookiejar.New(nil)
		if err != nil {
			return nil, fmt.Errorf("create downloader cookie jar: %w", err)
		}
		client.Jar = jar
	}
	return client, nil
}

func appendURLPath(base *url.URL, suffix string) string {
	copy := *base
	copy.Path = strings.TrimRight(copy.Path, "/") + "/" + strings.TrimLeft(suffix, "/")
	copy.RawPath = ""
	return copy.String()
}

func originFor(base *url.URL) string {
	return base.Scheme + "://" + base.Host
}

func closeResponse(response *http.Response) {
	if response == nil || response.Body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
	_ = response.Body.Close()
}

func decodeJSONResponse(response *http.Response, destination any, operation string) error {
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxJSONResponse))
	decoder.UseNumber()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("%s: invalid JSON response: %w", operation, err)
	}
	return nil
}

func unexpectedStatus(operation string, status int) error {
	return fmt.Errorf("%s: unexpected HTTP status %d", operation, status)
}
