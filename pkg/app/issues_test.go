package app

import (
	"bytes"
	"connectrpc.com/connect"
	"context"
	issuev1 "github.com/ImSingee/git-plus/pkg/rpc/gitplus/issue/v1"
	"github.com/ImSingee/git-plus/pkg/rpc/gitplus/issue/v1/issuev1connect"
	taskv1 "github.com/ImSingee/git-plus/pkg/rpc/gitplus/task/v1"
	"github.com/ImSingee/git-plus/pkg/server"
	"google.golang.org/protobuf/proto"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type syncServer struct {
	issuev1connect.UnimplementedIssueServiceHandler
	received bool
}

func (s *syncServer) EnqueueSync(_ context.Context, req *connect.Request[issuev1.EnqueueSyncRequest]) (*connect.Response[issuev1.EnqueueSyncResponse], error) {
	s.received = req.Msg.GetRepoId() == 7 && req.Msg.GetFull() && req.Header().Get("Authorization") == "Bearer test-password"
	return connect.NewResponse(&issuev1.EnqueueSyncResponse{Task: &taskv1.Task{TaskId: proto.String("task-7")}}), nil
}
func TestIssuesSyncCommandUsesAuthenticatedRPC(t *testing.T) {
	t.Setenv(server.PasswordEnvVar, "test-password")
	handler := &syncServer{}
	path, rpc := issuev1connect.NewIssueServiceHandler(handler)
	mux := http.NewServeMux()
	mux.Handle("/api"+path, http.StripPrefix("/api", rpc))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	root := NewRootCommand("test", nil)
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"issues", "sync", "--repo", "7", "--full", "--server", srv.URL})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !handler.received || !strings.Contains(out.String(), "task-7") {
		t.Fatalf("command did not queue expected request: %s", out.String())
	}
}
func TestIssuesCommandsRejectMissingRepository(t *testing.T) {
	for _, name := range []string{"sync", "export", "verify", "reindex"} {
		root := NewRootCommand("test", nil)
		root.SetArgs([]string{"issues", name})
		if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "--repo") {
			t.Fatalf("%s accepted missing repo: %v", name, err)
		}
	}
}
