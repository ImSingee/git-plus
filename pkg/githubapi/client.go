// Package githubapi provides read-only, bounded GitHub API access for archives.
package githubapi

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const Version = "2022-11-28"
const maxBody = 32 << 20

// Error deliberately excludes server response bodies and credential-bearing URLs.
type Error struct {
	Status  int
	RetryAt time.Time
}

func (e *Error) Error() string {
	if !e.RetryAt.IsZero() {
		return fmt.Sprintf("GitHub API rate limited; retry after %s", e.RetryAt.UTC().Format(time.RFC3339))
	}
	switch e.Status {
	case 401:
		return "GitHub API authentication failed"
	case 403:
		return "GitHub API access forbidden; check Issues read permission"
	case 404:
		return "GitHub resource unavailable or inaccessible; previous backup retained"
	case 410:
		return "GitHub Issues unavailable or disabled; previous backup retained"
	default:
		return fmt.Sprintf("GitHub API returned HTTP %d", e.Status)
	}
}

type cached struct {
	etag string
	body []byte
	next string
}
type Client struct {
	BaseURL    string
	HTTP       *http.Client
	Sleep      func(context.Context, time.Duration) error
	Now        func() time.Time
	mu         sync.Mutex
	cache      map[string]cached
	cacheBytes int
}

type credentialGate struct {
	gate         chan struct{}
	limitedUntil time.Time
	status       int
}

var credentialLocks sync.Map

func New(baseURL string, client *http.Client) *Client {
	if baseURL == "" {
		baseURL = "https://api.github.com"
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), HTTP: client, Now: time.Now, Sleep: sleep, cache: map[string]cached{}}
}
func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (c *Client) Object(ctx context.Context, token, path string) (json.RawMessage, error) {
	body, _, err := c.get(ctx, token, path)
	if err != nil {
		return nil, err
	}
	var object map[string]json.RawMessage
	if err = json.Unmarshal(body, &object); err != nil || object == nil {
		return nil, fmt.Errorf("invalid GitHub object response")
	}
	return json.RawMessage(body), nil
}
func (c *Client) Pages(ctx context.Context, token, path string) ([]json.RawMessage, error) {
	result := make([]json.RawMessage, 0)
	seen := map[string]bool{}
	for path != "" {
		if seen[path] || len(seen) >= 100000 {
			return nil, fmt.Errorf("invalid GitHub pagination cycle or excessive pages")
		}
		seen[path] = true
		body, next, err := c.get(ctx, token, path)
		if err != nil {
			return nil, err
		}
		var page []json.RawMessage
		if err = json.Unmarshal(body, &page); err != nil || page == nil {
			return nil, fmt.Errorf("invalid GitHub paginated response")
		}
		result = append(result, page...)
		path = next
	}
	return result, nil
}
func (c *Client) validateURL(path string) (*url.URL, error) {
	base, err := url.Parse(c.BaseURL)
	if err != nil || base.Host == "" || base.User != nil || (base.Scheme != "https" && base.Scheme != "http") {
		return nil, fmt.Errorf("invalid GitHub API base URL")
	}
	u, err := url.Parse(path)
	if err != nil {
		return nil, fmt.Errorf("invalid GitHub API URL")
	}
	u = base.ResolveReference(u)
	if u.Scheme != base.Scheme || !strings.EqualFold(u.Host, base.Host) || u.User != nil || u.Fragment != "" {
		return nil, fmt.Errorf("refusing GitHub API URL outside configured origin")
	}
	return u, nil
}
func (c *Client) get(ctx context.Context, token, path string) ([]byte, string, error) {
	u, err := c.validateURL(path)
	if err != nil {
		return nil, "", err
	}
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(c.BaseURL+"\x00"+token)))
	value, _ := credentialLocks.LoadOrStore(key, &credentialGate{gate: make(chan struct{}, 1)})
	credential := value.(*credentialGate)
	gate := credential.gate
	select {
	case gate <- struct{}{}:
	case <-ctx.Done():
		return nil, "", ctx.Err()
	}
	defer func() { <-gate }()
	// A long reset must also stop later queued repositories using this credential.
	// Keep this state across Client instances; never hammer GitHub until reset.
	if credential.limitedUntil.After(c.Now()) {
		return nil, "", &Error{Status: credential.status, RetryAt: credential.limitedUntil}
	}
	credential.limitedUntil = time.Time{}
	cacheKey := key + "|" + u.String()
	c.mu.Lock()
	prev := c.cache[cacheKey]
	c.mu.Unlock()
	client := *c.HTTP
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("too many GitHub API redirects")
		}
		_, err := c.validateURL(req.URL.String())
		return err
	}
	for attempt := 0; attempt < 3; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return nil, "", fmt.Errorf("create GitHub request")
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", Version)
		req.Header.Set("User-Agent", "git-plus-issue-backup")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if prev.etag != "" {
			req.Header.Set("If-None-Match", prev.etag)
		}
		resp, err := client.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, "", ctx.Err()
			}
			if attempt == 2 {
				return nil, "", fmt.Errorf("GitHub API request failed (network or redirect)")
			}
			if err = c.Sleep(ctx, time.Duration(1<<attempt)*time.Second); err != nil {
				return nil, "", err
			}
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
		resp.Body.Close()
		if readErr != nil {
			return nil, "", fmt.Errorf("read GitHub API response")
		}
		if len(body) > maxBody {
			return nil, "", fmt.Errorf("GitHub API response exceeds size limit")
		}
		if resp.StatusCode == http.StatusNotModified && prev.body != nil {
			credential.limitedUntil = time.Time{}
			return append([]byte(nil), prev.body...), prev.next, nil
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			credential.limitedUntil = time.Time{}
			next, err := nextLink(resp.Header.Values("Link"))
			if err != nil {
				return nil, "", err
			}
			if next != "" {
				relative, parseErr := url.Parse(next)
				if parseErr != nil {
					return nil, "", fmt.Errorf("invalid GitHub pagination URL")
				}
				next = resp.Request.URL.ResolveReference(relative).String()
				if _, err = c.validateURL(next); err != nil {
					return nil, "", err
				}
			}
			// Conditional response bodies are bounded in memory, and never persisted.
			if etag := resp.Header.Get("ETag"); etag != "" && len(body) <= 4<<20 {
				entry := cached{etag: etag, body: append([]byte(nil), body...), next: next}
				c.mu.Lock()
				if len(c.cache) >= 2000 || c.cacheBytes+len(body) > 32<<20 {
					c.cache = map[string]cached{}
					c.cacheBytes = 0
				}
				c.cacheBytes -= len(c.cache[cacheKey].body)
				c.cache[cacheKey] = entry
				c.cacheBytes += len(body)
				c.mu.Unlock()
			}
			return body, next, nil
		}
		apiErr := &Error{Status: resp.StatusCode}
		delay := time.Duration(1<<attempt) * time.Second
		// Ordinary forbidden errors are not retried. Rate-limit responses are explicit.
		limited := resp.StatusCode == 429 || (resp.StatusCode == 403 && (resp.Header.Get("Retry-After") != "" || resp.Header.Get("X-RateLimit-Remaining") == "0" || strings.Contains(strings.ToLower(string(body)), "rate limit")))
		if limited {
			delay = time.Minute
			retryAfter := resp.Header.Get("Retry-After")
			if seconds, parseErr := strconv.ParseInt(retryAfter, 10, 64); parseErr == nil && seconds >= 0 {
				if seconds > math.MaxInt64/int64(time.Second) {
					delay = time.Duration(math.MaxInt64)
				} else {
					delay = time.Duration(seconds) * time.Second
				}
			} else if when, parseErr := http.ParseTime(retryAfter); parseErr == nil {
				delay = when.Sub(c.Now())
			} else if reset, parseErr := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64); parseErr == nil {
				delay = time.Unix(reset, 0).Sub(c.Now())
			}
			if delay < time.Second {
				delay = time.Second
			}
			apiErr.RetryAt = c.Now().Add(delay)
			credential.limitedUntil = apiErr.RetryAt
			credential.status = apiErr.Status
		}
		if (!limited && resp.StatusCode < 500) || attempt == 2 || delay > 30*time.Second {
			return nil, "", apiErr
		}
		if err = c.Sleep(ctx, delay); err != nil {
			return nil, "", err
		}
	}
	return nil, "", fmt.Errorf("GitHub request retries exhausted")
}
func nextLink(headers []string) (string, error) {
	for _, header := range headers {
		for _, part := range strings.Split(header, ",") {
			sections := strings.Split(part, ";")
			if len(sections) < 2 {
				continue
			}
			next := false
			for _, param := range sections[1:] {
				p := strings.TrimSpace(param)
				if strings.HasPrefix(p, "rel=") {
					for _, rel := range strings.Fields(strings.Trim(strings.TrimPrefix(p, "rel="), "\"")) {
						if rel == "next" {
							next = true
						}
					}
				}
			}
			if next {
				target := strings.TrimSpace(sections[0])
				if !strings.HasPrefix(target, "<") || !strings.HasSuffix(target, ">") {
					return "", fmt.Errorf("invalid GitHub pagination Link")
				}
				return target[1 : len(target)-1], nil
			}
		}
	}
	return "", nil
}
