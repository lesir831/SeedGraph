// Package iyuu fetches the public IYUU site metadata catalog.
package iyuu

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	// DefaultBaseURL is the public IYUU API origin. The upstream client still
	// spells this as HTTP, but the endpoint supports HTTPS and the catalog must
	// not be fetched over plaintext by default.
	DefaultBaseURL = "https://2025.iyuu.cn"
	// SitesPath is the path of the public site metadata catalog.
	SitesPath = "/reseed/sites/index"
	// DefaultSitesEndpoint is the complete default catalog endpoint.
	DefaultSitesEndpoint = DefaultBaseURL + SitesPath
	// MaxResponseBytes bounds an IYUU catalog response before JSON decoding.
	MaxResponseBytes int64 = 2 << 20
	// DefaultTimeout is used when Config.HTTPClient is omitted.
	DefaultTimeout = 10 * time.Second
)

var (
	// ErrInvalidConfig reports a client configuration that is unsafe or cannot
	// identify an HTTP(S) endpoint.
	ErrInvalidConfig = errors.New("invalid IYUU client configuration")
	// ErrInvalidResponse reports a malformed or internally inconsistent remote
	// response. Response contents are deliberately not included in the error.
	ErrInvalidResponse = errors.New("invalid IYUU response")
	// ErrResponseTooLarge reports a response exceeding MaxResponseBytes.
	ErrResponseTooLarge = errors.New("IYUU response exceeds size limit")
)

var (
	siteSlugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
	secretPattern   = regexp.MustCompile(`(?i)((?:passkey|token|authkey|torrent_pass|secret)\s*[=:]\s*)[^&\s]+`)
)

// Config configures an IYUU catalog client. BaseURL is an origin or path
// prefix; SitesPath is appended unless BaseURL already ends with it. Token is
// optional and, when present, is sent in IYUU's raw "token" header.
type Config struct {
	BaseURL    string
	Token      string
	HTTPClient *http.Client
}

// Client fetches and validates IYUU site metadata.
type Client struct {
	endpoint   string
	token      string
	httpClient *http.Client
}

// Site is one entry in the IYUU site catalog. DownloadPage and DetailsPage are
// public templates and must never be expanded with user credentials by this
// package.
type Site struct {
	ID             int64  `json:"id"`
	Site           string `json:"site"`
	Nickname       string `json:"nickname"`
	BaseURL        string `json:"base_url"`
	DownloadPage   string `json:"download_page"`
	DetailsPage    string `json:"details_page"`
	IsHTTPS        int    `json:"is_https"`
	CookieRequired int    `json:"cookie_required"`
}

// RetryHint carries optional server-provided throttling metadata. After is a
// relative delay in seconds; ResetAt is an absolute HTTP date or Unix reset
// time. Limit preserves IYUU's X-RateLimit-Limit value without interpreting it.
type RetryHint struct {
	After   time.Duration
	ResetAt time.Time
	Limit   int64
}

func (hint RetryHint) available() bool {
	return hint.After > 0 || !hint.ResetAt.IsZero() || hint.Limit > 0
}

// HTTPError reports a non-200 response without exposing its body.
type HTTPError struct {
	StatusCode int
	hint       RetryHint
}

func (err *HTTPError) Error() string {
	return fmt.Sprintf("IYUU sites request returned HTTP %d", err.StatusCode)
}

// RetryHint returns any throttling metadata supplied in HTTP headers.
func (err *HTTPError) RetryHint() (RetryHint, bool) {
	return err.hint, err.hint.available()
}

// APIError reports a nonzero IYUU application response code. Message is
// control-character stripped, length bounded, and secret redacted.
type APIError struct {
	Code    int
	Message string
	hint    RetryHint
}

func (err *APIError) Error() string {
	if err.Message == "" {
		return fmt.Sprintf("IYUU API returned code %d", err.Code)
	}
	return fmt.Sprintf("IYUU API returned code %d: %s", err.Code, err.Message)
}

// RetryHint returns any throttling metadata supplied in the JSON error data.
func (err *APIError) RetryHint() (RetryHint, bool) {
	return err.hint, err.hint.available()
}

type retryHinter interface {
	RetryHint() (RetryHint, bool)
}

// RetryHintFrom extracts a retry hint from an HTTPError or APIError, including
// when the error has been wrapped.
func RetryHintFrom(err error) (RetryHint, bool) {
	var source retryHinter
	if !errors.As(err, &source) {
		return RetryHint{}, false
	}
	return source.RetryHint()
}

// New constructs a catalog client without making a network request.
func New(config Config) (*Client, error) {
	baseURL := strings.TrimSpace(config.BaseURL)
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	endpoint, err := buildEndpoint(baseURL)
	if err != nil {
		return nil, err
	}
	if err := validateToken(config.Token); err != nil {
		return nil, err
	}

	httpClient := config.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: DefaultTimeout}
	}
	return &Client{
		endpoint:   endpoint,
		token:      config.Token,
		httpClient: httpClient,
	}, nil
}

// Sites fetches a complete catalog. A nil result is returned on every error;
// callers can therefore apply the returned slice transactionally without ever
// persisting a partially validated response.
func (client *Client) Sites(ctx context.Context) ([]Site, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, client.endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("create IYUU sites request: %w", ErrInvalidConfig)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "SeedGraph/iyuu-catalog")
	if client.token != "" {
		request.Header.Set("token", client.token)
	}

	response, err := client.httpClient.Do(request)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("request IYUU sites: %w", ctxErr)
		}
		return nil, fmt.Errorf("request IYUU sites: %s", safeRemoteText(err.Error(), client.token))
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return nil, &HTTPError{
			StatusCode: response.StatusCode,
			hint:       retryHintFromHeader(response.Header),
		}
	}
	if err := validateJSONContentType(response.Header.Get("Content-Type")); err != nil {
		return nil, err
	}

	body, err := readBounded(response.Body, client.token)
	if err != nil {
		return nil, err
	}
	var envelope responseEnvelope
	if err := decodeOneJSON(body, &envelope); err != nil {
		return nil, fmt.Errorf("%w: malformed JSON", ErrInvalidResponse)
	}
	if envelope.Code == nil {
		return nil, fmt.Errorf("%w: missing application code", ErrInvalidResponse)
	}
	if *envelope.Code != 0 {
		hint := retryHintFromHeader(response.Header)
		hint = mergeRetryHints(hint, retryHintFromJSON(envelope.Data))
		return nil, &APIError{
			Code:    *envelope.Code,
			Message: safeRemoteText(envelope.Message, client.token),
			hint:    hint,
		}
	}

	var data sitesData
	if len(envelope.Data) == 0 || bytes.Equal(bytes.TrimSpace(envelope.Data), []byte("null")) {
		return nil, fmt.Errorf("%w: missing catalog data", ErrInvalidResponse)
	}
	if err := decodeOneJSON(envelope.Data, &data); err != nil {
		return nil, fmt.Errorf("%w: malformed catalog data", ErrInvalidResponse)
	}
	if data.Count == nil || data.Sites == nil {
		return nil, fmt.Errorf("%w: missing catalog fields", ErrInvalidResponse)
	}
	if *data.Count < 0 || *data.Count != len(*data.Sites) {
		return nil, fmt.Errorf("%w: inconsistent catalog count", ErrInvalidResponse)
	}
	if err := validateSites(*data.Sites); err != nil {
		return nil, err
	}
	return *data.Sites, nil
}

type responseEnvelope struct {
	Code    *int            `json:"code"`
	Message string          `json:"msg"`
	Data    json.RawMessage `json:"data"`
}

type sitesData struct {
	Count *int    `json:"count"`
	Sites *[]Site `json:"sites"`
}

func buildEndpoint(baseURL string) (string, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("%w: base URL must be absolute", ErrInvalidConfig)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("%w: base URL must use HTTP or HTTPS", ErrInvalidConfig)
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("%w: base URL cannot contain credentials, query, or fragment", ErrInvalidConfig)
	}
	path := strings.TrimRight(parsed.Path, "/")
	if !strings.HasSuffix(path, SitesPath) {
		path += SitesPath
	}
	parsed.Path = path
	parsed.RawPath = ""
	return parsed.String(), nil
}

func validateToken(token string) error {
	if len(token) > 4096 {
		return fmt.Errorf("%w: token is too long", ErrInvalidConfig)
	}
	for _, char := range token {
		if unicode.IsControl(char) || unicode.IsSpace(char) {
			return fmt.Errorf("%w: token contains whitespace or control characters", ErrInvalidConfig)
		}
	}
	return nil
}

func validateJSONContentType(value string) error {
	mediaType, _, err := mime.ParseMediaType(value)
	if err != nil || mediaType != "application/json" {
		return fmt.Errorf("%w: expected application/json", ErrInvalidResponse)
	}
	return nil
}

func readBounded(reader io.Reader, secret string) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(reader, MaxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read IYUU response: %s", safeRemoteText(err.Error(), secret))
	}
	if int64(len(body)) > MaxResponseBytes {
		return nil, ErrResponseTooLarge
	}
	return body, nil
}

func decodeOneJSON(body []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func validateSites(sites []Site) error {
	ids := make(map[int64]struct{}, len(sites))
	slugs := make(map[string]struct{}, len(sites))
	for index, site := range sites {
		if site.ID <= 0 {
			return invalidSite(index, "id")
		}
		if !siteSlugPattern.MatchString(site.Site) {
			return invalidSite(index, "slug")
		}
		if !isHostnameOnly(site.BaseURL) {
			return invalidSite(index, "base_url")
		}
		if site.IsHTTPS < 0 || site.IsHTTPS > 2 {
			return invalidSite(index, "is_https")
		}
		if site.CookieRequired < 0 || site.CookieRequired > 1 {
			return invalidSite(index, "cookie_required")
		}
		if _, exists := ids[site.ID]; exists {
			return invalidSite(index, "duplicate id")
		}
		if _, exists := slugs[site.Site]; exists {
			return invalidSite(index, "duplicate slug")
		}
		ids[site.ID] = struct{}{}
		slugs[site.Site] = struct{}{}
	}
	return nil
}

func invalidSite(index int, field string) error {
	return fmt.Errorf("%w: site %d has invalid %s", ErrInvalidResponse, index, field)
}

func isHostnameOnly(value string) bool {
	if value == "" || value != strings.TrimSpace(value) || len(value) > 253 {
		return false
	}
	if strings.ContainsAny(value, "/\\:?#@") || !utf8.ValidString(value) {
		return false
	}
	labels := strings.Split(value, ".")
	if len(labels) < 2 {
		return false
	}
	for _, label := range labels {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, char := range label {
			if !((char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '-') {
				return false
			}
		}
	}
	return true
}

type flexibleInt64 struct {
	value int64
	set   bool
}

func (number *flexibleInt64) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		return nil
	}
	if len(trimmed) >= 2 && trimmed[0] == '"' && trimmed[len(trimmed)-1] == '"' {
		unquoted, err := strconv.Unquote(trimmed)
		if err != nil {
			return err
		}
		trimmed = unquoted
	}
	value, err := strconv.ParseInt(trimmed, 10, 64)
	if err != nil {
		return err
	}
	number.value = value
	number.set = true
	return nil
}

type retryData struct {
	After flexibleInt64 `json:"Retry-After"`
	Limit flexibleInt64 `json:"X-RateLimit-Limit"`
	Reset flexibleInt64 `json:"X-RateLimit-Reset"`
}

func retryHintFromJSON(raw json.RawMessage) RetryHint {
	var data retryData
	if len(raw) == 0 || json.Unmarshal(raw, &data) != nil {
		return RetryHint{}
	}
	var hint RetryHint
	if data.After.set && data.After.value > 0 && data.After.value <= int64(^uint64(0)>>1)/int64(time.Second) {
		hint.After = time.Duration(data.After.value) * time.Second
	}
	if data.Limit.set && data.Limit.value > 0 {
		hint.Limit = data.Limit.value
	}
	if data.Reset.set && data.Reset.value > 0 {
		hint.ResetAt = time.Unix(data.Reset.value, 0).UTC()
	}
	return hint
}

func retryHintFromHeader(header http.Header) RetryHint {
	var hint RetryHint
	if after := strings.TrimSpace(header.Get("Retry-After")); after != "" {
		if seconds, err := strconv.ParseInt(after, 10, 64); err == nil && seconds > 0 && seconds <= int64(^uint64(0)>>1)/int64(time.Second) {
			hint.After = time.Duration(seconds) * time.Second
		} else if when, err := http.ParseTime(after); err == nil {
			hint.ResetAt = when.UTC()
		}
	}
	if limit, err := strconv.ParseInt(strings.TrimSpace(header.Get("X-RateLimit-Limit")), 10, 64); err == nil && limit > 0 {
		hint.Limit = limit
	}
	if reset, err := strconv.ParseInt(strings.TrimSpace(header.Get("X-RateLimit-Reset")), 10, 64); err == nil && reset > 0 {
		hint.ResetAt = time.Unix(reset, 0).UTC()
	}
	return hint
}

// mergeRetryHints fills values from override when IYUU provides the same
// metadata in its application-level data object and standard HTTP headers.
func mergeRetryHints(base, override RetryHint) RetryHint {
	if override.After > 0 {
		base.After = override.After
	}
	if !override.ResetAt.IsZero() {
		base.ResetAt = override.ResetAt
	}
	if override.Limit > 0 {
		base.Limit = override.Limit
	}
	return base
}

func safeRemoteText(value, secret string) string {
	if secret != "" {
		value = strings.ReplaceAll(value, secret, "[redacted]")
	}
	value = secretPattern.ReplaceAllString(value, "$1[redacted]")
	value = strings.Map(func(char rune) rune {
		if unicode.IsControl(char) {
			return ' '
		}
		return char
	}, value)
	value = strings.Join(strings.Fields(value), " ")
	const maxRunes = 256
	if utf8.RuneCountInString(value) > maxRunes {
		runes := []rune(value)
		value = string(runes[:maxRunes]) + "…"
	}
	if value == "" {
		return "remote request failed"
	}
	return value
}
