package server

import (
	"net/http"
	"strings"
	"testing"
)

func TestIssueRoutesRequireExistingAPIAuthentication(t *testing.T) {
	server := newTestServerWithPassword(t, t.TempDir(), "test-dashboard-password")
	for _, test := range []struct{ method, path string }{
		{http.MethodPost, "/api/gitplus.issue.v1.IssueService/GetStatus"},
		{http.MethodPost, "/api/gitplus.issue.v1.IssueService/ListIssues"},
		{http.MethodPost, "/api/gitplus.issue.v1.IssueService/GetIssue"},
		{http.MethodPost, "/api/gitplus.issue.v1.IssueService/ListSnapshots"},
		{http.MethodPost, "/api/gitplus.issue.v1.IssueService/EnqueueSync"},
		{http.MethodGet, "/api/repos/1/issues/export?password=test-dashboard-password"},
	} {
		request, err := http.NewRequest(test.method, server.URL+test.path, strings.NewReader(`{"repoId":"1"}`))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", "application/json")
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s: got %d", test.path, response.StatusCode)
		}
	}
}
