package configservice

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"

	"connectrpc.com/connect"
	connectvalidate "connectrpc.com/validate"
	appconfig "github.com/ImSingee/git-plus/pkg/config"
	configv1 "github.com/ImSingee/git-plus/pkg/rpc/gitplus/config/v1"
	"github.com/ImSingee/git-plus/pkg/rpc/gitplus/config/v1/configv1connect"
)

type serviceServer struct {
	dataDir string
}

func NewHandler(dataDir string) http.Handler {
	rpcMux := http.NewServeMux()
	RegisterHandlers(rpcMux, dataDir)
	return http.StripPrefix("/api", rpcMux)
}

func RegisterHandlers(mux *http.ServeMux, dataDir string) {
	path, handler := configv1connect.NewConfigServiceHandler(
		&serviceServer{dataDir: dataDir},
		connect.WithInterceptors(mustValidateInterceptor()),
	)
	mux.Handle(path, handler)
}

func (s *serviceServer) Ping(
	_ context.Context,
	_ *connect.Request[configv1.PingRequest],
) (*connect.Response[configv1.PingResponse], error) {
	return connect.NewResponse(&configv1.PingResponse{}), nil
}

func (s *serviceServer) CheckConfig(
	_ context.Context,
	_ *connect.Request[configv1.CheckConfigRequest],
) (*connect.Response[configv1.CheckConfigResponse], error) {
	result := appconfig.CheckFile(appconfig.PathForDataDir(s.dataDir), secretOptionsFromEnv())
	if !result.Exists && len(result.Issues) == 0 {
		result.Issues = append(result.Issues, appconfig.ValidationIssue{
			Severity: appconfig.SeverityError,
			Code:     "config_not_found",
			Message:  "config file does not exist",
		})
	}
	appconfig.SortIssues(result.Issues)

	return connect.NewResponse(&configv1.CheckConfigResponse{
		Issues:  toProtoIssues(result.Issues),
		Summary: summarizeProtoIssues(result.Issues),
	}), nil
}

func (s *serviceServer) CheckSourceConfig(
	_ context.Context,
	req *connect.Request[configv1.CheckSourceConfigRequest],
) (*connect.Response[configv1.CheckSourceConfigResponse], error) {
	sourceID := strings.TrimSpace(req.Msg.GetSourceId())
	result := appconfig.CheckSource(appconfig.PathForDataDir(s.dataDir), sourceID, secretOptionsFromEnv())
	appconfig.SortIssues(result.Issues)

	return connect.NewResponse(&configv1.CheckSourceConfigResponse{
		Exists:   boolPtr(result.Exists),
		SourceId: stringPtr(sourceID),
		Issues:   toProtoIssues(result.Issues),
		Summary:  summarizeProtoIssues(result.Issues),
	}), nil
}

func (s *serviceServer) GetConfig(
	_ context.Context,
	_ *connect.Request[configv1.GetConfigRequest],
) (*connect.Response[configv1.GetConfigResponse], error) {
	path := appconfig.PathForDataDir(s.dataDir)
	loaded, exists, err := appconfig.LoadOrDefault(path)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("load config: %w", err))
	}

	return connect.NewResponse(&configv1.GetConfigResponse{
		Exists: boolPtr(exists),
		Config: toProtoConfigSnapshot(loaded.Data),
	}), nil
}

func (s *serviceServer) UpdateConfig(
	_ context.Context,
	req *connect.Request[configv1.UpdateConfigRequest],
) (*connect.Response[configv1.UpdateConfigResponse], error) {
	loaded, err := s.loadConfigForMutation()
	if err != nil {
		return nil, err
	}

	updatedConfig, err := applyConfigPatch(req.Msg, loaded.Data)
	if err != nil {
		return nil, err
	}

	if err := appconfig.Save(loaded.Path, updatedConfig); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("save config: %w", err))
	}

	return connect.NewResponse(&configv1.UpdateConfigResponse{
		Config: toProtoConfigSnapshot(updatedConfig),
	}), nil
}

func (s *serviceServer) CreateSource(
	_ context.Context,
	req *connect.Request[configv1.CreateSourceRequest],
) (*connect.Response[configv1.CreateSourceResponse], error) {
	loaded, err := s.loadConfigForMutation()
	if err != nil {
		return nil, err
	}

	source, err := createSourceFromProto(req.Msg.GetSource())
	if err != nil {
		return nil, err
	}
	source.ID, err = generateUniqueSourceID(loaded.Data.Sources)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("generate source id: %w", err))
	}
	if findSourceIndex(loaded.Data.Sources, source.ID) >= 0 {
		return nil, connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("source %q already exists", source.ID))
	}

	loaded.Data.Sources = append(loaded.Data.Sources, source)
	s.sortSources(&loaded.Data)

	if err := appconfig.Save(loaded.Path, loaded.Data); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("save config: %w", err))
	}

	return connect.NewResponse(&configv1.CreateSourceResponse{
		Config: toProtoConfigSnapshot(loaded.Data),
	}), nil
}

func (s *serviceServer) UpdateSource(
	_ context.Context,
	req *connect.Request[configv1.UpdateSourceRequest],
) (*connect.Response[configv1.UpdateSourceResponse], error) {
	loaded, err := s.loadConfigForMutation()
	if err != nil {
		return nil, err
	}

	currentID := strings.TrimSpace(req.Msg.GetSourceId())
	index := findSourceIndex(loaded.Data.Sources, currentID)
	if index < 0 {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("source %q was not found", currentID))
	}

	updatedSource, err := applySourcePatch(req.Msg.Patch, loaded.Data.Sources[index])
	if err != nil {
		return nil, err
	}

	loaded.Data.Sources[index] = updatedSource
	s.sortSources(&loaded.Data)

	if err := appconfig.Save(loaded.Path, loaded.Data); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("save config: %w", err))
	}

	return connect.NewResponse(&configv1.UpdateSourceResponse{
		Config: toProtoConfigSnapshot(loaded.Data),
	}), nil
}

func (s *serviceServer) ReplaceSourceToken(
	_ context.Context,
	req *connect.Request[configv1.ReplaceSourceTokenRequest],
) (*connect.Response[configv1.ReplaceSourceTokenResponse], error) {
	loaded, err := s.loadConfigForMutation()
	if err != nil {
		return nil, err
	}

	sourceID := strings.TrimSpace(req.Msg.GetSourceId())
	index := findSourceIndex(loaded.Data.Sources, sourceID)
	if index < 0 {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("source %q was not found", sourceID))
	}

	encryptedToken, err := encryptPlaintextToken(req.Msg.GetTokenPlaintext())
	if err != nil {
		return nil, err
	}

	loaded.Data.Sources[index].Token = encryptedToken

	if err := appconfig.Save(loaded.Path, loaded.Data); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("save config: %w", err))
	}

	return connect.NewResponse(&configv1.ReplaceSourceTokenResponse{
		Source: toProtoSource(loaded.Data.Sources[index]),
	}), nil
}

func (s *serviceServer) DeleteSource(
	_ context.Context,
	req *connect.Request[configv1.DeleteSourceRequest],
) (*connect.Response[configv1.DeleteSourceResponse], error) {
	loaded, err := s.loadConfigForMutation()
	if err != nil {
		return nil, err
	}

	sourceID := strings.TrimSpace(req.Msg.GetSourceId())
	index := findSourceIndex(loaded.Data.Sources, sourceID)
	if index < 0 {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("source %q was not found", sourceID))
	}

	loaded.Data.Sources = slices.Delete(loaded.Data.Sources, index, index+1)
	s.sortSources(&loaded.Data)

	if err := appconfig.Save(loaded.Path, loaded.Data); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("save config: %w", err))
	}

	return connect.NewResponse(&configv1.DeleteSourceResponse{
		Config: toProtoConfigSnapshot(loaded.Data),
	}), nil
}

func LogIssuesOnStartup(dataDir string, logger *log.Logger) {
	result := appconfig.CheckFile(appconfig.PathForDataDir(dataDir), secretOptionsFromEnv())
	if !result.Exists {
		return
	}

	appconfig.SortIssues(result.Issues)
	for _, issue := range result.Issues {
		message := "config check " + string(issue.Severity) + " [" + issue.Code + "] " + issue.Message
		if issue.Path != "" {
			message += " (path: " + issue.Path + ")"
		}
		if issue.Line > 0 {
			message += " (line: " + strconv.Itoa(issue.Line) + ")"
		}

		logger.Print(message)
	}
}

func (s *serviceServer) loadConfigForMutation() (appconfig.LoadedConfig, error) {
	loaded, _, err := appconfig.LoadOrDefault(appconfig.PathForDataDir(s.dataDir))
	if err != nil {
		return appconfig.LoadedConfig{}, connect.NewError(connect.CodeInternal, fmt.Errorf("load config: %w", err))
	}

	return loaded, nil
}

func (s *serviceServer) sortSources(cfg *appconfig.Config) {
	slices.SortFunc(cfg.Sources, func(a appconfig.SourceConfig, b appconfig.SourceConfig) int {
		if nameCompare := strings.Compare(appconfig.EffectiveSourceName(a), appconfig.EffectiveSourceName(b)); nameCompare != 0 {
			return nameCompare
		}

		return strings.Compare(a.ID, b.ID)
	})
}

func summarizeProtoIssues(issues []appconfig.ValidationIssue) *configv1.IssueSummary {
	summary := appconfig.Summary(issues)

	return &configv1.IssueSummary{
		Error:   int32Ptr(int32(summary[appconfig.SeverityError])),
		Warning: int32Ptr(int32(summary[appconfig.SeverityWarning])),
		Info:    int32Ptr(int32(summary[appconfig.SeverityInfo])),
	}
}

func toProtoIssues(issues []appconfig.ValidationIssue) []*configv1.ValidationIssue {
	protoIssues := make([]*configv1.ValidationIssue, 0, len(issues))

	for _, issue := range issues {
		protoIssues = append(protoIssues, &configv1.ValidationIssue{
			Severity: severityPtr(toProtoSeverity(issue.Severity)),
			Code:     stringPtr(issue.Code),
			Message:  stringPtr(issue.Message),
			Path:     stringPtr(issue.Path),
			SourceId: stringPtr(issue.SourceID),
			Line:     int32Ptr(int32(issue.Line)),
		})
	}

	return protoIssues
}

func toProtoSeverity(severity appconfig.Severity) configv1.ValidationIssue_Severity {
	switch severity {
	case appconfig.SeverityError:
		return configv1.ValidationIssue_SEVERITY_ERROR
	case appconfig.SeverityWarning:
		return configv1.ValidationIssue_SEVERITY_WARNING
	case appconfig.SeverityInfo:
		return configv1.ValidationIssue_SEVERITY_INFO
	default:
		return configv1.ValidationIssue_SEVERITY_UNSPECIFIED
	}
}

func toProtoConfigSnapshot(cfg appconfig.Config) *configv1.ConfigSnapshot {
	sources := make([]*configv1.Source, 0, len(cfg.Sources))
	for _, source := range cfg.Sources {
		sources = append(sources, toProtoSource(source))
	}

	return &configv1.ConfigSnapshot{
		Concurrency:   int32Ptr(int32(cfg.Concurrency)),
		Sources:       sources,
		MaxRetryTimes: int32Ptr(int32(cfg.MaxRetryTimes)),
		Cron:          stringPtr(cfg.Cron),
	}
}

func toProtoSource(source appconfig.SourceConfig) *configv1.Source {
	return &configv1.Source{
		Id:               stringPtr(source.ID),
		Platform:         platformPtr(toProtoPlatform(source.Platform)),
		Username:         stringPtr(source.Username),
		Token:            stringPtr(sanitizeTokenForRead(source.Token)),
		OnlyIncludeRepos: append([]string{}, source.OnlyIncludeRepos...),
		ExcludeRepos:     append([]string{}, source.ExcludeRepos...),
		IncludeDefaults:  boolPtr(source.IncludeDefaults),
		IncludeStarred:   boolPtr(source.IncludeStarred),
		IncludeWatching:  boolPtr(source.IncludeWatching),
		IssuesEnabled:    boolPtr(source.IssuesEnabled),
		Name:             stringPtr(appconfig.EffectiveSourceName(source)),
	}
}

func toProtoPlatform(platform string) configv1.Platform {
	switch strings.TrimSpace(platform) {
	case "github":
		return configv1.Platform_PLATFORM_GITHUB
	default:
		return configv1.Platform_PLATFORM_UNSPECIFIED
	}
}

func fromProtoPlatform(platform configv1.Platform) (string, error) {
	switch platform {
	case configv1.Platform_PLATFORM_GITHUB:
		return "github", nil
	default:
		return "", connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unsupported platform %q", platform.String()))
	}
}

func createSourceFromProto(input *configv1.CreateSourceInput) (appconfig.SourceConfig, error) {
	if input == nil {
		return appconfig.SourceConfig{}, connect.NewError(connect.CodeInvalidArgument, errors.New("source is required"))
	}

	platform, err := fromProtoPlatform(input.GetPlatform())
	if err != nil {
		return appconfig.SourceConfig{}, err
	}
	encryptedToken, err := encryptPlaintextToken(input.GetTokenPlaintext())
	if err != nil {
		return appconfig.SourceConfig{}, err
	}

	name, err := normalizeOptionalString(input.GetName())
	if err != nil {
		return appconfig.SourceConfig{}, err
	}
	username, err := normalizeRequiredString("source.username", input.GetUsername())
	if err != nil {
		return appconfig.SourceConfig{}, err
	}
	onlyIncludeRepos, err := normalizeStringList("source.only_include_repos", input.GetOnlyIncludeRepos())
	if err != nil {
		return appconfig.SourceConfig{}, err
	}
	excludeRepos, err := normalizeStringList("source.exclude_repos", input.GetExcludeRepos())
	if err != nil {
		return appconfig.SourceConfig{}, err
	}

	return appconfig.SourceConfig{
		Name:             name,
		Platform:         platform,
		Username:         username,
		Token:            encryptedToken,
		OnlyIncludeRepos: onlyIncludeRepos,
		ExcludeRepos:     excludeRepos,
		IncludeDefaults:  boolValueOrDefault(input.IncludeDefaults, appconfig.DefaultIncludeDefaults),
		IncludeStarred:   boolValueOrDefault(input.IncludeStarred, false),
		IncludeWatching:  boolValueOrDefault(input.IncludeWatching, false),
		IssuesEnabled:    boolValueOrDefault(input.IssuesEnabled, false),
	}, nil
}

func applySourcePatch(input *configv1.UpdateSourcePatch, existingSource appconfig.SourceConfig) (appconfig.SourceConfig, error) {
	if input == nil {
		return appconfig.SourceConfig{}, connect.NewError(connect.CodeInvalidArgument, errors.New("patch is required"))
	}
	if isEmptySourcePatch(input) {
		return appconfig.SourceConfig{}, connect.NewError(connect.CodeInvalidArgument, errors.New("patch must include at least one field"))
	}

	updatedSource := existingSource

	if input.Platform != nil {
		platform, err := fromProtoPlatform(input.GetPlatform())
		if err != nil {
			return appconfig.SourceConfig{}, err
		}
		updatedSource.Platform = platform
	}

	if input.Username != nil {
		username, err := normalizeRequiredString("patch.username", input.GetUsername())
		if err != nil {
			return appconfig.SourceConfig{}, err
		}
		updatedSource.Username = username
	}

	if input.OnlyIncludeRepos != nil {
		onlyIncludeRepos, err := normalizeStringList("patch.only_include_repos", input.OnlyIncludeRepos.GetValues())
		if err != nil {
			return appconfig.SourceConfig{}, err
		}
		updatedSource.OnlyIncludeRepos = onlyIncludeRepos
	}

	if input.ExcludeRepos != nil {
		excludeRepos, err := normalizeStringList("patch.exclude_repos", input.ExcludeRepos.GetValues())
		if err != nil {
			return appconfig.SourceConfig{}, err
		}
		updatedSource.ExcludeRepos = excludeRepos
	}

	if input.IncludeDefaults != nil {
		updatedSource.IncludeDefaults = input.GetIncludeDefaults()
	}

	if input.IncludeStarred != nil {
		updatedSource.IncludeStarred = input.GetIncludeStarred()
	}

	if input.IssuesEnabled != nil {
		updatedSource.IssuesEnabled = input.GetIssuesEnabled()
	}

	if input.IncludeWatching != nil {
		updatedSource.IncludeWatching = input.GetIncludeWatching()
	}

	if input.Name != nil {
		name, err := normalizeOptionalString(input.GetName())
		if err != nil {
			return appconfig.SourceConfig{}, err
		}
		updatedSource.Name = name
	}

	return updatedSource, nil
}

func applyConfigPatch(input *configv1.UpdateConfigRequest, existingConfig appconfig.Config) (appconfig.Config, error) {
	if input == nil {
		return appconfig.Config{}, connect.NewError(connect.CodeInvalidArgument, errors.New("patch is required"))
	}
	if isEmptyConfigPatch(input) {
		return appconfig.Config{}, connect.NewError(connect.CodeInvalidArgument, errors.New("patch must include at least one field"))
	}

	updatedConfig := existingConfig

	if input.Concurrency != nil {
		updatedConfig.Concurrency = int(input.GetConcurrency())
	}

	if input.MaxRetryTimes != nil {
		updatedConfig.MaxRetryTimes = int(input.GetMaxRetryTimes())
	}

	return updatedConfig, nil
}

func encryptPlaintextToken(plaintext string) (string, error) {
	token, err := normalizeRequiredString("token_plaintext", plaintext)
	if err != nil {
		return "", err
	}

	encryptedToken, err := appconfig.EncryptToken(token, os.Getenv(appconfig.TokenPassphraseEnvVar))
	if err != nil {
		return "", connect.NewError(connect.CodeInternal, fmt.Errorf("encrypt token: %w", err))
	}

	return encryptedToken, nil
}

func sanitizeTokenForRead(token string) string {
	if appconfig.IsEncryptedToken(token) {
		return token
	}

	return ""
}

func normalizeRequiredString(field string, value string) (string, error) {
	trimmedValue := strings.TrimSpace(value)
	if trimmedValue == "" {
		return "", connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%s is required", field))
	}

	return trimmedValue, nil
}

func normalizeOptionalString(value string) (string, error) {
	return strings.TrimSpace(value), nil
}

func normalizeStringList(field string, values []string) ([]string, error) {
	if len(values) == 0 {
		return []string{}, nil
	}

	normalizedValues := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))

	for index, value := range values {
		trimmedValue := strings.TrimSpace(value)
		if trimmedValue == "" {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%s[%d] is required", field, index))
		}
		if _, exists := seen[trimmedValue]; exists {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%s contains duplicate value %q", field, trimmedValue))
		}

		seen[trimmedValue] = struct{}{}
		normalizedValues = append(normalizedValues, trimmedValue)
	}

	return normalizedValues, nil
}

func findSourceIndex(sources []appconfig.SourceConfig, sourceID string) int {
	for index, source := range sources {
		if source.ID == sourceID {
			return index
		}
	}

	return -1
}

func isEmptySourcePatch(input *configv1.UpdateSourcePatch) bool {
	return input.Platform == nil &&
		input.Username == nil &&
		input.OnlyIncludeRepos == nil &&
		input.ExcludeRepos == nil &&
		input.IncludeDefaults == nil &&
		input.IncludeStarred == nil &&
		input.IncludeWatching == nil &&
		input.IssuesEnabled == nil &&
		input.Name == nil
}

func isEmptyConfigPatch(input *configv1.UpdateConfigRequest) bool {
	return input.Concurrency == nil &&
		input.MaxRetryTimes == nil
}

func mustValidateInterceptor() connect.Interceptor {
	interceptor, err := connectvalidate.NewInterceptor()
	if err != nil {
		panic(fmt.Sprintf("create connect validate interceptor: %v", err))
	}

	return interceptor
}

func secretOptionsFromEnv() appconfig.SecretOptions {
	return appconfig.SecretOptions{
		Passphrase: os.Getenv(appconfig.TokenPassphraseEnvVar),
	}
}

func stringPtr(value string) *string {
	return &value
}

func boolPtr(value bool) *bool {
	return &value
}

func int32Ptr(value int32) *int32 {
	return &value
}

func severityPtr(value configv1.ValidationIssue_Severity) *configv1.ValidationIssue_Severity {
	return &value
}

func platformPtr(value configv1.Platform) *configv1.Platform {
	return &value
}

func boolValueOrDefault(value *bool, defaultValue bool) bool {
	if value == nil {
		return defaultValue
	}

	return *value
}

func generateUniqueSourceID(sources []appconfig.SourceConfig) (string, error) {
	for attempt := 0; attempt < 32; attempt++ {
		id, err := generateSourceID()
		if err != nil {
			return "", err
		}
		if findSourceIndex(sources, id) < 0 {
			return id, nil
		}
	}

	return "", errors.New("could not generate a unique source id")
}

func generateSourceID() (string, error) {
	randomBytes := make([]byte, 12)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", err
	}

	return "src_" + hex.EncodeToString(randomBytes), nil
}
