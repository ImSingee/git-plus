package app

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"

	"connectrpc.com/connect"
	appdb "github.com/ImSingee/git-plus/db"
	appconfig "github.com/ImSingee/git-plus/pkg/config"
	cronv1 "github.com/ImSingee/git-plus/pkg/rpc/gitplus/cron/v1"
	"github.com/ImSingee/git-plus/pkg/rpc/gitplus/cron/v1/cronv1connect"
	"github.com/ImSingee/git-plus/pkg/server"
	"github.com/spf13/cobra"
)

const (
	defaultListenAddr = ":8080"
)

func NewRootCommand(version string, frontendHandlerFactory server.FrontendHandlerFactory) *cobra.Command {
	var listenAddr string
	var dataDir string
	var autoMigrate bool

	cmd := &cobra.Command{
		Use:     "git-plus",
		Short:   "Run the git-plus server",
		Version: version,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := newServerConfig(listenAddr, cmd.Flags().Changed("listen"), dataDir, autoMigrate)
			if err != nil {
				return err
			}
			if err := validateStartupEnvironment(cmd.ErrOrStderr()); err != nil {
				return err
			}

			return server.Run(cmd.Context(), cfg, frontendHandlerFactory)
		},
		SilenceUsage: true,
	}

	cmd.Flags().StringVarP(&listenAddr, "listen", "l", defaultListenAddr, "listen address")
	cmd.Flags().BoolVar(&autoMigrate, "auto-migrate", true, "run embedded database migrations before startup")
	cmd.PersistentFlags().StringVar(&dataDir, "data-dir", "", "directory for runtime data")
	cmd.AddCommand(newDBCommand(&dataDir))
	cmd.AddCommand(newConfigCommand())
	cmd.AddCommand(newCronCommand())
	cmd.AddCommand(newIssuesCommand(&dataDir))

	return cmd
}

func newServerConfig(listenAddr string, flagChanged bool, dataDir string, autoMigrate bool) (server.Config, error) {
	normalizedDataDir, err := normalizeDataDir(dataDir)
	if err != nil {
		return server.Config{}, err
	}

	return server.Config{
		ListenAddr:  resolveListenAddr(listenAddr, flagChanged),
		DataDir:     normalizedDataDir,
		AutoMigrate: autoMigrate,
	}, nil
}

func newDBCommand(dataDir *string) *cobra.Command {
	dbCommand := &cobra.Command{
		Use:   "db",
		Short: "Database utilities",
	}

	dbCommand.AddCommand(&cobra.Command{
		Use:          "migrate",
		Short:        "Run embedded database migrations",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			normalizedDataDir, err := normalizeDataDir(*dataDir)
			if err != nil {
				return err
			}

			return appdb.Migrate(cmd.Context(), normalizedDataDir)
		},
	})

	return dbCommand
}

func newConfigCommand() *cobra.Command {
	configCommand := &cobra.Command{
		Use:   "config",
		Short: "Configuration utilities",
	}

	configCommand.AddCommand(&cobra.Command{
		Use:          "encrypt-token",
		Short:        "Encrypt a token for use in config.yaml",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			passphrase := os.Getenv(appconfig.TokenPassphraseEnvVar)
			if passphrase == "" {
				return fmt.Errorf("%s is required", appconfig.TokenPassphraseEnvVar)
			}

			input, err := io.ReadAll(cmd.InOrStdin())
			if err != nil {
				return fmt.Errorf("read token from stdin: %w", err)
			}

			token := strings.TrimRight(string(input), "\r\n")
			if token == "" {
				return errors.New("token is required on stdin")
			}

			encryptedToken, err := appconfig.EncryptToken(token, passphrase)
			if err != nil {
				return fmt.Errorf("encrypt token: %w", err)
			}

			if _, err := fmt.Fprintln(cmd.OutOrStdout(), encryptedToken); err != nil {
				return fmt.Errorf("write encrypted token: %w", err)
			}

			return nil
		},
	})

	return configCommand
}

func newCronCommand() *cobra.Command {
	cronCommand := &cobra.Command{
		Use:   "cron",
		Short: "Cron utilities",
	}

	var serverURL string
	var listenAddr string
	reloadCommand := &cobra.Command{
		Use:          "reload",
		Short:        "Reload cron configuration from config.yaml",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			targetServerURL := resolveServerURL(serverURL, listenAddr, cmd.Flags().Changed("listen"))
			httpClient, err := newAuthenticatedServerAPIClient()
			if err != nil {
				return err
			}
			client := cronv1connect.NewCronServiceClient(httpClient, strings.TrimRight(targetServerURL, "/")+"/api")
			response, err := client.ReloadCron(cmd.Context(), connect.NewRequest(&cronv1.ReloadCronRequest{}))
			if err != nil {
				return fmt.Errorf("reload cron: %w", err)
			}

			runtime := response.Msg.GetRuntime()
			if runtime == nil {
				_, err = fmt.Fprintln(cmd.OutOrStdout(), "cron reloaded")
				return err
			}

			if runtime.GetEnabled() {
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "cron reloaded: enabled (%s)\n", runtime.GetCron())
			} else {
				_, err = fmt.Fprintln(cmd.OutOrStdout(), "cron reloaded: disabled")
			}

			return err
		},
	}
	reloadCommand.Flags().StringVar(&serverURL, "server", "", "git-plus server base URL")
	reloadCommand.Flags().StringVarP(&listenAddr, "listen", "l", defaultListenAddr, "git-plus server listen address")
	cronCommand.AddCommand(reloadCommand)

	return cronCommand
}

func resolveServerURL(serverURL string, listenAddr string, flagChanged bool) string {
	trimmedServerURL := strings.TrimSpace(serverURL)
	if trimmedServerURL != "" {
		return trimmedServerURL
	}

	return serverURLForListenAddr(resolveListenAddr(listenAddr, flagChanged))
}

func serverURLForListenAddr(listenAddr string) string {
	normalizedListenAddr := normalizeListenAddr(listenAddr)
	if strings.HasPrefix(normalizedListenAddr, ":") {
		return "http://127.0.0.1" + normalizedListenAddr
	}

	host, port, err := net.SplitHostPort(normalizedListenAddr)
	if err != nil {
		return "http://127.0.0.1" + defaultListenAddr
	}

	normalizedHost := host
	switch host {
	case "", "0.0.0.0", "::":
		normalizedHost = "127.0.0.1"
	}

	return "http://" + net.JoinHostPort(normalizedHost, port)
}

func resolveListenAddr(flagValue string, flagChanged bool) string {
	if flagChanged {
		return normalizeListenAddr(flagValue)
	}

	if value := os.Getenv("PORT"); value != "" {
		return normalizeListenAddr(value)
	}

	return defaultListenAddr
}

func normalizeListenAddr(value string) string {
	if !strings.Contains(value, ":") {
		return ":" + value
	}

	return value
}

func normalizeDataDir(value string) (string, error) {
	normalizedValue := strings.TrimSpace(value)
	if normalizedValue == "" {
		return "", errors.New("--data-dir is required")
	}

	return normalizedValue, nil
}

func validateStartupEnvironment(stderr io.Writer) error {
	if os.Getenv(appconfig.TokenPassphraseEnvVar) == "" {
		return fmt.Errorf("%s is required", appconfig.TokenPassphraseEnvVar)
	}
	password := strings.TrimSpace(os.Getenv(server.PasswordEnvVar))
	if password == "" {
		return fmt.Errorf("%s is required", server.PasswordEnvVar)
	}
	if password == server.InsecureNoAuthPassword && stderr != nil {
		_, _ = fmt.Fprintf(stderr, "warning: %s=%q disables API authentication\n", server.PasswordEnvVar, server.InsecureNoAuthPassword)
	}

	return nil
}

func newAuthenticatedServerAPIClient() (*http.Client, error) {
	password, err := resolveAPIPasswordFromEnv()
	if err != nil {
		return nil, err
	}
	if password == server.InsecureNoAuthPassword {
		return http.DefaultClient, nil
	}

	return &http.Client{
		Transport: authRoundTripper{
			base:  http.DefaultTransport,
			token: password,
		},
	}, nil
}

func resolveAPIPasswordFromEnv() (string, error) {
	password := strings.TrimSpace(os.Getenv(server.PasswordEnvVar))
	if password == "" {
		return "", fmt.Errorf("%s is required", server.PasswordEnvVar)
	}

	return password, nil
}

type authRoundTripper struct {
	base  http.RoundTripper
	token string
}

func (transport authRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	base := transport.base
	if base == nil {
		base = http.DefaultTransport
	}

	clonedRequest := request.Clone(request.Context())
	clonedRequest.Header = request.Header.Clone()
	clonedRequest.Header.Set("Authorization", "Bearer "+transport.token)

	return base.RoundTrip(clonedRequest)
}
