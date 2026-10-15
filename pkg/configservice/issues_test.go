package configservice

import (
	"connectrpc.com/connect"
	"context"
	appconfig "github.com/ImSingee/git-plus/pkg/config"
	configv1 "github.com/ImSingee/git-plus/pkg/rpc/gitplus/config/v1"
	"google.golang.org/protobuf/proto"
	"testing"
)

func TestIssuesEnabledPatchPreservesAndClears(t *testing.T) {
	dir := t.TempDir()
	source := appconfig.SourceConfig{ID: "source", Platform: "github", Username: "owner", Token: "opaque-existing-ciphertext", IssuesEnabled: true}
	if err := appconfig.Save(appconfig.PathForDataDir(dir), appconfig.Config{Sources: []appconfig.SourceConfig{source}}); err != nil {
		t.Fatal(err)
	}
	s := &serviceServer{dataDir: dir}
	_, err := s.UpdateSource(context.Background(), connect.NewRequest(&configv1.UpdateSourceRequest{SourceId: proto.String("source"), Patch: &configv1.UpdateSourcePatch{Name: proto.String("renamed")}}))
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := appconfig.LoadFromDataDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.Data.Sources[0].IssuesEnabled || loaded.Data.Sources[0].Token != source.Token {
		t.Fatal("unrelated edit changed issue setting or ciphertext")
	}
	_, err = s.UpdateSource(context.Background(), connect.NewRequest(&configv1.UpdateSourceRequest{SourceId: proto.String("source"), Patch: &configv1.UpdateSourcePatch{IssuesEnabled: proto.Bool(false)}}))
	if err != nil {
		t.Fatal(err)
	}
	loaded, err = appconfig.LoadFromDataDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Data.Sources[0].IssuesEnabled {
		t.Fatal("explicit false was ignored")
	}
	if isEmptySourcePatch(&configv1.UpdateSourcePatch{IssuesEnabled: proto.Bool(false)}) {
		t.Fatal("false patch considered empty")
	}
	if toProtoSource(source).GetIssuesEnabled() != true {
		t.Fatal("source RPC omitted issue setting")
	}
}
