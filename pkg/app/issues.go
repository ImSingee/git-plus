package app

import (
	"fmt"
	"os"
	"strings"

	"connectrpc.com/connect"
	appdb "github.com/ImSingee/git-plus/db"
	dbsqlc "github.com/ImSingee/git-plus/db/sqlc"
	"github.com/ImSingee/git-plus/pkg/issuearchive"
	"github.com/ImSingee/git-plus/pkg/issuesync"
	issuev1 "github.com/ImSingee/git-plus/pkg/rpc/gitplus/issue/v1"
	"github.com/ImSingee/git-plus/pkg/rpc/gitplus/issue/v1/issuev1connect"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/proto"
)

func newIssuesCommand(dataDir *string) *cobra.Command {
	parent := &cobra.Command{Use: "issues", Short: "Back up, verify, and export GitHub issue metadata"}
	var syncRepoID int64
	var full bool
	var serverURL string
	syncCmd := &cobra.Command{Use: "sync", Short: "Queue an issue backup on the running server", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		if syncRepoID <= 0 {
			return fmt.Errorf("--repo must be a positive local repository ID")
		}
		client, err := newAuthenticatedServerAPIClient()
		if err != nil {
			return err
		}
		rpc := issuev1connect.NewIssueServiceClient(client, strings.TrimRight(resolveServerURL(serverURL, defaultListenAddr, false), "/")+"/api")
		response, err := rpc.EnqueueSync(cmd.Context(), connect.NewRequest(&issuev1.EnqueueSyncRequest{RepoId: proto.Int64(syncRepoID), Full: proto.Bool(full)}))
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Issue backup task: %s (%s)\n", response.Msg.GetTask().GetTaskId(), response.Msg.GetResult())
		return err
	}}
	syncCmd.Flags().Int64Var(&syncRepoID, "repo", 0, "local repository ID shown in the Web URL")
	syncCmd.Flags().BoolVar(&full, "full", false, "force a full reconciliation")
	syncCmd.Flags().StringVar(&serverURL, "server", "", "git-plus server base URL (uses PASSWORD for authentication)")
	parent.AddCommand(syncCmd)
	for _, mode := range []string{"export", "verify", "reindex"} {
		mode := mode
		var repoID int64
		var revision, output string
		command := &cobra.Command{Use: mode, Short: map[string]string{"export": "Export a verified local snapshot ZIP (attachments remain links)", "verify": "Verify a local snapshot and its checksums", "reindex": "Rebuild the disposable SQLite index from the latest local archive"}[mode], Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
			if repoID <= 0 {
				return fmt.Errorf("--repo must be a positive local repository ID")
			}
			directory, err := normalizeDataDir(*dataDir)
			if err != nil {
				return err
			}
			db, err := appdb.Open(cmd.Context(), directory)
			if err != nil {
				return err
			}
			defer db.Close()
			repo, err := dbsqlc.New(db).GetRepoById(cmd.Context(), repoID)
			if err != nil {
				return fmt.Errorf("find local repository: %w", err)
			}
			if mode == "reindex" {
				if err := issuesync.New(directory, db).Reindex(cmd.Context(), repo, ""); err != nil {
					return err
				}
				_, err = fmt.Fprintln(cmd.OutOrStdout(), "Issue index rebuilt")
				return err
			}
			path, err := issuearchive.Path(directory, repo.SourceID, repo.RefID)
			if err != nil {
				return err
			}
			store, err := issuearchive.NewStore(path)
			if err != nil {
				return err
			}
			if mode == "verify" {
				if err := store.Verify(cmd.Context(), revision); err != nil {
					return err
				}
				_, err = fmt.Fprintln(cmd.OutOrStdout(), "Issue snapshot verified")
				return err
			}
			if output == "" {
				return fmt.Errorf("--output is required")
			}
			file, err := os.OpenFile(output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err != nil {
				return err
			}
			success := false
			defer func() {
				file.Close()
				if !success {
					os.Remove(output)
				}
			}()
			if err := store.ExportZIP(cmd.Context(), file, revision); err != nil {
				return err
			}
			if err := file.Sync(); err != nil {
				return err
			}
			if err := file.Close(); err != nil {
				return err
			}
			success = true
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "Issue snapshot exported:", output)
			return err
		}}
		command.Flags().Int64Var(&repoID, "repo", 0, "local repository ID shown in the Web URL")
		if mode != "reindex" {
			command.Flags().StringVar(&revision, "snapshot", "", "snapshot commit (default: latest)")
		}
		if mode == "export" {
			command.Flags().StringVar(&output, "output", "", "new ZIP output path; never overwrites an existing file")
		}
		parent.AddCommand(command)
	}
	return parent
}
