package config

import (
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-co-op/gocron/v2"
	"gopkg.in/yaml.v3"
)

type Severity string

const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
	SeverityInfo    Severity = "info"
)

type ValidationIssue struct {
	Severity Severity `json:"severity"`
	Code     string   `json:"code"`
	Message  string   `json:"message"`
	Path     string   `json:"path,omitempty"`
	SourceID string   `json:"source_id,omitempty"`
	Line     int      `json:"line,omitempty"`
}

type CheckResult struct {
	Path   string            `json:"path"`
	Exists bool              `json:"exists"`
	Issues []ValidationIssue `json:"issues"`
}

var linePattern = regexp.MustCompile(`line ([0-9]+)`)

func CheckFile(path string, opts ...SecretOptions) CheckResult {
	result := CheckResult{
		Path: path,
	}

	loaded, err := Load(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return result
		}

		result.Exists = !errors.Is(err, fs.ErrNotExist)
		result.Issues = []ValidationIssue{loadErrorIssue(err)}
		return result
	}

	result.Exists = true
	result.Issues = ValidateConfig(loaded, opts...)
	return result
}

func CheckSource(path string, sourceID string, opts ...SecretOptions) CheckResult {
	result := CheckResult{
		Path: path,
	}

	loaded, err := Load(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return result
		}

		result.Exists = !errors.Is(err, fs.ErrNotExist)
		result.Issues = []ValidationIssue{loadErrorIssue(err)}
		return result
	}

	result.Exists = true
	result.Issues = ValidateSource(loaded, sourceID, opts...)
	return result
}

func ValidateConfig(loaded LoadedConfig, opts ...SecretOptions) []ValidationIssue {
	root := documentRoot(loaded.Node)
	issues := make([]ValidationIssue, 0)
	secretOptions := firstSecretOptions(opts)

	issues = append(issues, validateUnknownFields(root, "", topLevelFields)...)
	issues = append(issues, validateSourceCollection(loaded.Data.Sources, root, secretOptions)...)
	issues = append(issues, validateConcurrency(loaded.Data, root)...)
	issues = append(issues, validateMaxRetryTimes(loaded.Data, root)...)
	issues = append(issues, validateCron(loaded.Data, root)...)

	return issues
}

func ValidateSource(loaded LoadedConfig, sourceID string, opts ...SecretOptions) []ValidationIssue {
	root := documentRoot(loaded.Node)
	sourcesNode, _, _ := mappingValue(root, "sources")
	sourceNodes := sequenceItems(sourcesNode)
	issues := make([]ValidationIssue, 0)
	matchCount := 0
	secretOptions := firstSecretOptions(opts)

	for index, source := range loaded.Data.Sources {
		if source.ID != sourceID {
			continue
		}

		matchCount++
		issues = append(issues, validateUnknownFields(sourceNodeAt(sourceNodes, index), fmt.Sprintf("sources[%d].", index), sourceFields)...)
		issues = append(issues, validateSourceFields(source, sourceNodeAt(sourceNodes, index), index, secretOptions)...)
	}

	issues = append(issues, validateDuplicateSourceIDs(loaded.Data.Sources, sourceNodes, sourceID)...)

	if matchCount == 0 {
		issues = append(issues, ValidationIssue{
			Severity: SeverityError,
			Code:     "source_not_found",
			Message:  fmt.Sprintf("source %q was not found", sourceID),
			Path:     "sources",
			SourceID: sourceID,
		})
	}

	return issues
}

var topLevelFields = []string{
	"sources",
	"concurrency",
	"max_retry_times",
	"cron",
}

var sourceFields = []string{
	"id",
	"name",
	"platform",
	"username",
	"token",
	"only_include_repos",
	"exclude_repos",
	"include_defaults",
	"include_starred",
	"include_watching",
	"issues_enabled",
}

func validateSourceCollection(sources []SourceConfig, root *yaml.Node, opts SecretOptions) []ValidationIssue {
	issues := make([]ValidationIssue, 0)
	sourcesNode, sourcesKeyNode, sourcesExists := mappingValue(root, "sources")
	sourceNodes := sequenceItems(sourcesNode)

	if len(sources) == 0 {
		line := 0
		if sourcesExists && sourcesKeyNode != nil {
			line = sourcesKeyNode.Line
		} else if root != nil {
			line = root.Line
		}

		issues = append(issues, ValidationIssue{
			Severity: SeverityWarning,
			Code:     "empty_sources",
			Message:  "no source configured",
			Path:     "sources",
			Line:     line,
		})
	}

	for index, source := range sources {
		node := sourceNodeAt(sourceNodes, index)
		issues = append(issues, validateUnknownFields(node, fmt.Sprintf("sources[%d].", index), sourceFields)...)
		issues = append(issues, validateSourceFields(source, node, index, opts)...)
	}

	issues = append(issues, validateDuplicateSourceIDs(sources, sourceNodes, "")...)

	return issues
}

func validateCron(cfg Config, root *yaml.Node) []ValidationIssue {
	cronNode, _, exists := mappingValue(root, "cron")
	if !exists {
		return nil
	}
	line := 0
	if cronNode != nil {
		line = cronNode.Line
	}

	cronValue := strings.TrimSpace(cfg.Cron)
	if cronValue == "" {
		return nil
	}

	if err := ValidateCronExpression(cronValue); err != nil {
		return []ValidationIssue{
			{
				Severity: SeverityError,
				Code:     "invalid_cron",
				Message:  "cron must be a valid 5-field cron expression",
				Path:     "cron",
				Line:     line,
			},
		}
	}

	return nil
}

func ValidateCronExpression(value string) error {
	cronValue := strings.TrimSpace(value)
	if cronValue == "" {
		return nil
	}

	return gocron.NewDefaultCron(false).IsValid(cronValue, time.Local, time.Now())
}

func validateSourceFields(source SourceConfig, node *yaml.Node, index int, opts SecretOptions) []ValidationIssue {
	issues := make([]ValidationIssue, 0)

	requiredFields := []struct {
		name  string
		value string
	}{
		{name: "id", value: source.ID},
		{name: "platform", value: source.Platform},
		{name: "username", value: source.Username},
		{name: "token", value: source.Token},
	}

	for _, field := range requiredFields {
		if strings.TrimSpace(field.value) != "" {
			continue
		}

		line := node.Line
		if valueNode, _, ok := mappingValue(node, field.name); ok && valueNode != nil {
			line = valueNode.Line
		}

		issues = append(issues, ValidationIssue{
			Severity: SeverityError,
			Code:     "missing_required_field",
			Message:  fmt.Sprintf("%s is required", field.name),
			Path:     fmt.Sprintf("sources[%d].%s", index, field.name),
			SourceID: source.ID,
			Line:     line,
		})
	}

	if platform := strings.TrimSpace(source.Platform); platform != "" && platform != "github" {
		line := node.Line
		if valueNode, _, ok := mappingValue(node, "platform"); ok && valueNode != nil {
			line = valueNode.Line
		}

		issues = append(issues, ValidationIssue{
			Severity: SeverityError,
			Code:     "unsupported_platform",
			Message:  fmt.Sprintf("platform %q is not supported", source.Platform),
			Path:     fmt.Sprintf("sources[%d].platform", index),
			SourceID: source.ID,
			Line:     line,
		})
	}

	issues = append(issues, validateToken(source, node, index, opts)...)

	return issues
}

func validateToken(source SourceConfig, node *yaml.Node, index int, opts SecretOptions) []ValidationIssue {
	tokenValue := strings.TrimSpace(source.Token)
	if tokenValue == "" {
		return nil
	}

	line := nodeLineForField(node, "token")
	path := fmt.Sprintf("sources[%d].token", index)

	if !IsEncryptedToken(tokenValue) {
		return []ValidationIssue{
			{
				Severity: SeverityError,
				Code:     "unencrypted_token",
				Message:  fmt.Sprintf("token must use %s format", encryptedTokenPrefix+"..."),
				Path:     path,
				SourceID: source.ID,
				Line:     line,
			},
		}
	}

	if _, err := parseEncryptedTokenPayload(tokenValue); err != nil {
		return []ValidationIssue{
			{
				Severity: SeverityError,
				Code:     "invalid_encrypted_token",
				Message:  "token has an invalid encrypted payload",
				Path:     path,
				SourceID: source.ID,
				Line:     line,
			},
		}
	}

	if _, err := DecryptToken(tokenValue, opts.Passphrase); err != nil {
		code := "token_decryption_failed"
		message := "token could not be decrypted with the configured passphrase"
		if errors.Is(err, errInvalidEncryptedToken) {
			code = "invalid_encrypted_token"
			message = "token has an invalid encrypted payload"
		}

		return []ValidationIssue{
			{
				Severity: SeverityError,
				Code:     code,
				Message:  message,
				Path:     path,
				SourceID: source.ID,
				Line:     line,
			},
		}
	}

	return nil
}

func validateDuplicateSourceIDs(sources []SourceConfig, sourceNodes []*yaml.Node, sourceID string) []ValidationIssue {
	occurrences := make(map[string][]int)
	issues := make([]ValidationIssue, 0)

	for index, source := range sources {
		normalizedID := strings.TrimSpace(source.ID)
		if normalizedID == "" {
			continue
		}

		occurrences[normalizedID] = append(occurrences[normalizedID], index)
	}

	for duplicateID, indexes := range occurrences {
		if len(indexes) < 2 {
			continue
		}
		if sourceID != "" && duplicateID != sourceID {
			continue
		}

		for _, index := range indexes {
			node := sourceNodeAt(sourceNodes, index)
			line := node.Line
			if valueNode, _, ok := mappingValue(node, "id"); ok && valueNode != nil {
				line = valueNode.Line
			}

			issues = append(issues, ValidationIssue{
				Severity: SeverityError,
				Code:     "duplicate_source_id",
				Message:  fmt.Sprintf("source id %q is duplicated", duplicateID),
				Path:     fmt.Sprintf("sources[%d].id", index),
				SourceID: duplicateID,
				Line:     line,
			})
		}
	}

	return issues
}

func validateConcurrency(cfg Config, root *yaml.Node) []ValidationIssue {
	if !mappingHasKey(root, "concurrency") {
		return nil
	}
	if cfg.Concurrency > 0 {
		return nil
	}

	valueNode, _, _ := mappingValue(root, "concurrency")
	line := 0
	if valueNode != nil {
		line = valueNode.Line
	}

	return []ValidationIssue{
		{
			Severity: SeverityError,
			Code:     "invalid_concurrency",
			Message:  "concurrency must be greater than 0",
			Path:     "concurrency",
			Line:     line,
		},
	}
}

func validateMaxRetryTimes(cfg Config, root *yaml.Node) []ValidationIssue {
	if !mappingHasKey(root, "max_retry_times") {
		return nil
	}
	if cfg.MaxRetryTimes >= 0 {
		return nil
	}

	valueNode, _, _ := mappingValue(root, "max_retry_times")
	line := 0
	if valueNode != nil {
		line = valueNode.Line
	}

	return []ValidationIssue{
		{
			Severity: SeverityError,
			Code:     "invalid_max_retry_times",
			Message:  "max_retry_times must be greater than or equal to 0",
			Path:     "max_retry_times",
			Line:     line,
		},
	}
}

func validateUnknownFields(node *yaml.Node, pathPrefix string, allowed []string) []ValidationIssue {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}

	issues := make([]ValidationIssue, 0)
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, field := range allowed {
		allowedSet[field] = struct{}{}
	}

	for index := 0; index+1 < len(node.Content); index += 2 {
		keyNode := node.Content[index]
		if _, ok := allowedSet[keyNode.Value]; ok {
			continue
		}

		issues = append(issues, ValidationIssue{
			Severity: SeverityWarning,
			Code:     "unknown_field",
			Message:  fmt.Sprintf("field %q is not recognized", keyNode.Value),
			Path:     pathPrefix + keyNode.Value,
			Line:     keyNode.Line,
		})
	}

	return issues
}

func loadErrorIssue(err error) ValidationIssue {
	code := "config_read_failed"
	message := err.Error()

	if strings.Contains(err.Error(), "parse config file") || strings.Contains(err.Error(), "decode config file") {
		code = "invalid_yaml"
	}

	return ValidationIssue{
		Severity: SeverityError,
		Code:     code,
		Message:  message,
		Line:     parseLineFromError(err),
	}
}

func parseLineFromError(err error) int {
	match := linePattern.FindStringSubmatch(err.Error())
	if len(match) != 2 {
		return 0
	}

	line, parseErr := strconv.Atoi(match[1])
	if parseErr != nil {
		return 0
	}

	return line
}

func documentRoot(node *yaml.Node) *yaml.Node {
	if node == nil {
		return nil
	}
	if node.Kind == yaml.DocumentNode && len(node.Content) > 0 {
		return node.Content[0]
	}

	return node
}

func mappingValue(node *yaml.Node, key string) (valueNode *yaml.Node, keyNode *yaml.Node, ok bool) {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil, nil, false
	}

	for index := 0; index+1 < len(node.Content); index += 2 {
		currentKeyNode := node.Content[index]
		if currentKeyNode.Value != key {
			continue
		}

		return node.Content[index+1], currentKeyNode, true
	}

	return nil, nil, false
}

func mappingHasKey(node *yaml.Node, key string) bool {
	_, _, ok := mappingValue(node, key)
	return ok
}

func sequenceItems(node *yaml.Node) []*yaml.Node {
	if node == nil || node.Kind != yaml.SequenceNode {
		return nil
	}

	return node.Content
}

func sourceNodeAt(nodes []*yaml.Node, index int) *yaml.Node {
	if index < 0 || index >= len(nodes) {
		return nil
	}

	return nodes[index]
}

func nodeLineForField(node *yaml.Node, field string) int {
	line := 0
	if node != nil {
		line = node.Line
	}
	if valueNode, _, ok := mappingValue(node, field); ok && valueNode != nil {
		line = valueNode.Line
	}

	return line
}

func firstSecretOptions(opts []SecretOptions) SecretOptions {
	if len(opts) == 0 {
		return SecretOptions{}
	}

	return opts[0]
}

func Summary(issues []ValidationIssue) map[Severity]int {
	summary := map[Severity]int{
		SeverityError:   0,
		SeverityWarning: 0,
		SeverityInfo:    0,
	}

	for _, issue := range issues {
		summary[issue.Severity]++
	}

	return summary
}

func SortIssues(issues []ValidationIssue) {
	slices.SortStableFunc(issues, func(a ValidationIssue, b ValidationIssue) int {
		switch {
		case a.Line != b.Line:
			return a.Line - b.Line
		case a.Path != b.Path:
			return strings.Compare(a.Path, b.Path)
		default:
			return strings.Compare(a.Code, b.Code)
		}
	})
}
