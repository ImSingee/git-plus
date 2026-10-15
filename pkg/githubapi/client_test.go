package githubapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testClient(server *httptest.Server) *Client {
	client := New(server.URL, server.Client())
	client.Sleep = func(ctx context.Context, _ time.Duration) error { return ctx.Err() }
	return client
}

func TestPagesFollowEveryLinkAndPreserveRawObjects(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer secret" || r.Header.Get("X-GitHub-Api-Version") != Version || r.Header.Get("Accept") != "application/vnd.github+json" {
			t.Error("missing API headers")
		}
		page := r.URL.Query().Get("page")
		switch page {
		case "":
			w.Header().Add("Link", fmt.Sprintf(`<http://%s/issues?page=3>; rel="last", <http://%s/issues?page=2>; rel="next"`, r.Host, r.Host))
			io.WriteString(w, `[{"id":9007199254740993,"number":1,"unknown":{"a":true}}]`)
		case "2":
			w.Header().Add("Link", fmt.Sprintf(`<http://%s/issues?page=3>; rel="next"`, r.Host))
			io.WriteString(w, `[{"id":2,"number":2,"pull_request":{}}]`)
		case "3":
			io.WriteString(w, `[]`)
		default:
			t.Errorf("unexpected page %q", page)
			w.WriteHeader(500)
		}
	}))
	defer server.Close()
	pages, err := testClient(server).Pages(context.Background(), "secret", "/issues")
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 2 || calls != 3 || !strings.Contains(string(pages[0]), "9007199254740993") || !strings.Contains(string(pages[1]), "pull_request") {
		t.Fatalf("pagination must preserve all raw API objects for higher-level PR filtering: %s calls=%d", pages, calls)
	}
}

func TestPaginationRejectsCrossOriginAndCycles(t *testing.T) {
	var externalCalls atomic.Int32
	external := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { externalCalls.Add(1); io.WriteString(w, `[]`) }))
	defer external.Close()
	for _, mode := range []string{"cross-origin", "cycle", "invalid-link"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				switch mode {
				case "cross-origin":
					w.Header().Set("Link", fmt.Sprintf(`<%s/stolen>; rel="next"`, external.URL))
				case "cycle":
					w.Header().Set("Link", fmt.Sprintf(`<http://%s/issues>; rel="next"`, r.Host))
				case "invalid-link":
					w.Header().Set("Link", `missing-brackets; rel="next"`)
				}
				io.WriteString(w, `[{"id":1}]`)
			}))
			defer server.Close()
			if _, err := testClient(server).Pages(context.Background(), "secret", "/issues"); err == nil {
				t.Fatal("unsafe pagination was accepted")
			}
			if calls > 2 {
				t.Fatalf("pagination cycle made %d calls", calls)
			}
		})
	}
	if externalCalls.Load() != 0 {
		t.Fatal("sent a request or token to another origin")
	}
}

func TestConditionalPagesCacheIsolatedByCredential(t *testing.T) {
	var mu sync.Mutex
	requests := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		key := token + "|" + r.URL.String()
		mu.Lock()
		requests[key]++
		count := requests[key]
		mu.Unlock()
		etag := `"` + token + r.URL.Path + `"`
		if count > 1 {
			if r.Header.Get("If-None-Match") != etag {
				t.Errorf("missing or cross-credential conditional ETag for %s: %q", key, r.Header.Get("If-None-Match"))
			}
			w.WriteHeader(304)
			return
		}
		if r.Header.Get("If-None-Match") != "" {
			t.Errorf("new credential got another token's ETag: %s", key)
		}
		w.Header().Set("ETag", etag)
		if r.URL.Path == "/first" {
			w.Header().Set("Link", fmt.Sprintf(`<http://%s/second>; rel="next"`, r.Host))
		}
		fmt.Fprintf(w, `[{"token_owner":%q,"page":%q}]`, token, r.URL.Path)
	}))
	defer server.Close()
	client := testClient(server)
	for _, token := range []string{"alpha", "alpha", "beta", "beta", ""} {
		result, err := client.Pages(context.Background(), token, "/first")
		if err != nil {
			t.Fatal(err)
		}
		if len(result) != 2 {
			t.Fatalf("304 lost cached pagination next link: %s", result)
		}
		for _, raw := range result {
			var item map[string]string
			if err := json.Unmarshal(raw, &item); err != nil {
				t.Fatal(err)
			}
			if item["token_owner"] != token {
				t.Fatalf("credential cache leak: got %s for token %q", raw, token)
			}
		}
	}
}

func TestUncached304IsNotSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(304) }))
	defer server.Close()
	if _, err := testClient(server).Object(context.Background(), "new-token", "/object"); err == nil {
		t.Fatal("accepted 304 without a cached response")
	}
}

func TestRetryPolicyAndRateLimitDelays(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		status  int
		headers map[string]string
		body    string
		calls   int
		sleeps  []time.Duration
		retryAt time.Time
	}{
		{name: "server-errors", status: 503, calls: 3, sleeps: []time.Duration{time.Second, 2 * time.Second}},
		{name: "permission-denied", status: 403, calls: 1},
		{name: "short-429", status: 429, headers: map[string]string{"Retry-After": "2"}, calls: 3, sleeps: []time.Duration{2 * time.Second, 2 * time.Second}, retryAt: now.Add(2 * time.Second)},
		{name: "long-429", status: 429, headers: map[string]string{"Retry-After": "120"}, calls: 1, retryAt: now.Add(120 * time.Second)},
		{name: "reset-403", status: 403, headers: map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": strconv.FormatInt(now.Add(time.Hour).Unix(), 10)}, calls: 1, retryAt: now.Add(time.Hour)},
		{name: "secondary-403", status: 403, body: `{"message":"You have exceeded a secondary rate limit"}`, calls: 1, retryAt: now.Add(time.Minute)},
		{name: "404", status: 404, calls: 1},
		{name: "410", status: 410, calls: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				for key, value := range test.headers {
					w.Header().Set(key, value)
				}
				w.WriteHeader(test.status)
				io.WriteString(w, test.body)
			}))
			defer server.Close()
			client := testClient(server)
			client.Now = func() time.Time { return now }
			var sleeps []time.Duration
			client.Sleep = func(ctx context.Context, delay time.Duration) error { sleeps = append(sleeps, delay); return ctx.Err() }
			_, err := client.Object(context.Background(), "token", "/object")
			var apiError *Error
			if !errors.As(err, &apiError) || apiError.Status != test.status || !apiError.RetryAt.Equal(test.retryAt) {
				t.Fatalf("unexpected error: %+v (%v)", apiError, err)
			}
			if calls != test.calls || fmt.Sprint(sleeps) != fmt.Sprint(test.sleeps) {
				t.Fatalf("retry policy: calls=%d sleeps=%v, wanted calls=%d sleeps=%v", calls, sleeps, test.calls, test.sleeps)
			}
		})
	}
}

func TestTransientServerErrorEventuallySucceeds(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 3 {
			w.WriteHeader(502)
			return
		}
		io.WriteString(w, `{"id":1}`)
	}))
	defer server.Close()
	if _, err := testClient(server).Object(context.Background(), "token", "/object"); err != nil || calls != 3 {
		t.Fatalf("retry result calls=%d err=%v", calls, err)
	}
}

func TestCancellationDuringRequestAndRetry(t *testing.T) {
	t.Run("request", func(t *testing.T) {
		started := make(chan struct{})
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done() }))
		defer server.Close()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() { _, err := testClient(server).Object(ctx, "token", "/object"); done <- err }()
		<-started
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation not propagated: %v", err)
		}
	})
	t.Run("retry", func(t *testing.T) {
		calls := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(503) }))
		defer server.Close()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		client := testClient(server)
		client.Sleep = func(ctx context.Context, _ time.Duration) error { cancel(); return ctx.Err() }
		if _, err := client.Object(ctx, "token", "/object"); !errors.Is(err, context.Canceled) || calls != 1 {
			t.Fatalf("retry cancellation calls=%d err=%v", calls, err)
		}
	})
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }

func TestErrorsNeverExposeTokensBodiesOrURLs(t *testing.T) {
	secret := "ghp_DO_NOT_DISCLOSE"
	for _, status := range []int{401, 403, 404, 410, 429, 500} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				fmt.Fprintf(w, `{"message":%q}`, secret)
			}))
			defer server.Close()
			_, err := testClient(server).Object(context.Background(), secret, "/object?secret="+secret)
			if err == nil || strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), server.URL) {
				t.Fatalf("unsafe API error: %v", err)
			}
		})
	}
	client := New("https://api.github.test", &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("transport failed at %s using %s", r.URL, r.Header.Get("Authorization"))
	})})
	client.Sleep = func(context.Context, time.Duration) error { return nil }
	if _, err := client.Object(context.Background(), secret, "/object?secret="+secret); err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("unsafe network error: %v", err)
	}
	for _, path := range []string{"https://user:" + secret + "@evil.test/path", "https://evil.test/" + secret, "/x#" + secret, "%" + secret} {
		if _, err := client.Object(context.Background(), secret, path); err == nil || strings.Contains(err.Error(), secret) {
			t.Fatalf("unsafe validation error: %v", err)
		}
	}
}

func TestRedirectCannotForwardCredentialsAcrossOrigins(t *testing.T) {
	var externalCalls atomic.Int32
	external := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { externalCalls.Add(1); io.WriteString(w, `{}`) }))
	defer external.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, external.URL+"/capture", http.StatusFound)
	}))
	defer server.Close()
	if _, err := testClient(server).Object(context.Background(), "secret", "/object"); err == nil {
		t.Fatal("cross-origin redirect succeeded")
	}
	if externalCalls.Load() != 0 {
		t.Fatal("credential-bearing redirect reached another origin")
	}
}

func TestHTTPDateRetryAfter(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", now.Add(2*time.Hour).Format(http.TimeFormat))
		w.WriteHeader(429)
	}))
	defer server.Close()
	client := testClient(server)
	client.Now = func() time.Time { return now }
	_, err := client.Object(context.Background(), "token", "/object")
	var apiError *Error
	if !errors.As(err, &apiError) || !apiError.RetryAt.Equal(now.Add(2*time.Hour)) {
		t.Fatalf("HTTP-date Retry-After was not respected: %+v, %v", apiError, err)
	}
}

func TestVeryLargeRetryAfterDoesNotOverflow(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Retry-After", "9223372037")
		w.WriteHeader(429)
	}))
	defer server.Close()
	client := testClient(server)
	client.Now = func() time.Time { return now }
	client.Sleep = func(context.Context, time.Duration) error {
		t.Error("must not retry a huge Retry-After delay")
		return nil
	}
	_, err := client.Object(context.Background(), "token", "/object")
	var apiError *Error
	if !errors.As(err, &apiError) || !apiError.RetryAt.After(now) || calls != 1 {
		t.Fatalf("overflowed Retry-After: calls=%d error=%+v", calls, apiError)
	}
}

func TestRelativeNextLinkUsesCurrentPageURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/owner/repo/issues" {
			w.WriteHeader(404)
			return
		}
		if r.URL.Query().Get("page") == "2" {
			io.WriteString(w, `[{"id":2}]`)
			return
		}
		w.Header().Set("Link", `<?page=2>; rel="next"`)
		io.WriteString(w, `[{"id":1}]`)
	}))
	defer server.Close()
	items, err := testClient(server).Pages(context.Background(), "token", "/repos/owner/repo/issues")
	if err != nil || len(items) != 2 {
		t.Fatalf("relative Link lost current page path: items=%s err=%v", items, err)
	}
}

func TestLongRateLimitStopsOtherClientsForSameCredential(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("Authorization") == "Bearer limited" {
			w.Header().Set("Retry-After", "120")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{"id":1}`))
	}))
	defer server.Close()
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	first := New(server.URL, server.Client())
	first.Now = func() time.Time { return now }
	if _, err := first.Object(context.Background(), "limited", "/repo"); err == nil {
		t.Fatal("expected rate limit")
	}
	second := New(server.URL, server.Client())
	second.Now = first.Now
	if _, err := second.Object(context.Background(), "limited", "/another-repo"); err == nil {
		t.Fatal("expected shared rate limit")
	}
	if requests.Load() != 1 {
		t.Fatalf("requested during known reset: %d requests", requests.Load())
	}
	if _, err := second.Object(context.Background(), "other", "/repo"); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 2 {
		t.Fatalf("unrelated credential was blocked: %d", requests.Load())
	}
	now = now.Add(121 * time.Second)
	if _, err := second.Object(context.Background(), "limited", "/repo"); err == nil {
		t.Fatal("expected new server rate response")
	}
	if requests.Load() != 3 {
		t.Fatalf("did not retry after reset: %d", requests.Load())
	}
}
