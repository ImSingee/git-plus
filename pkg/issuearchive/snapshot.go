package issuearchive

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
)

// canonicalJSON keeps all unknown fields and json.Number values. In particular,
// it never round-trips GitHub IDs through float64.
func canonicalJSON(raw []byte) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("expected exactly one JSON value")
	}
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func encodeJSON(value any) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return canonicalJSON(data)
}

func canonicalObject(raw json.RawMessage) (json.RawMessage, error) {
	result, err := canonicalJSON(raw)
	if err != nil {
		return nil, err
	}
	if len(result) == 0 || result[0] != '{' {
		return nil, errors.New("expected a JSON object")
	}
	return result, nil
}

func canonicalCollection(values []json.RawMessage) ([]json.RawMessage, error) {
	result := make([]json.RawMessage, len(values))
	for i, raw := range values {
		value, err := canonicalObject(raw)
		if err != nil {
			return nil, fmt.Errorf("item %d: %w", i, err)
		}
		result[i] = value
	}
	// API order and page boundaries are not content. Numeric IDs sort correctly
	// without conversion to a bounded integer. Events without IDs sort by JSON.
	sort.SliceStable(result, func(i, j int) bool {
		left, right := rawID(result[i]), rawID(result[j])
		if left != "" && right != "" && len(left) != len(right) {
			return len(left) < len(right)
		}
		if left != right {
			return left < right
		}
		return bytes.Compare(result[i], result[j]) < 0
	})
	return result, nil
}

func rawID(raw json.RawMessage) string {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return ""
	}
	return strings.Trim(string(fields["id"]), `"`)
}

func normalizeSnapshot(input, previous Snapshot) (Snapshot, error) {
	if err := validateIdentity(input.Repository.SourceID, input.Repository.ID); err != nil {
		return Snapshot{}, err
	}
	if previous.Repository.ID != "" && (input.Repository.ID != previous.Repository.ID || input.Repository.SourceID != previous.Repository.SourceID) {
		return Snapshot{}, errors.New("cannot change the stable repository identity of an archive")
	}
	input.CompletedAt = effectiveTime(input.CompletedAt)
	if input.StartedAt.IsZero() {
		input.StartedAt = input.CompletedAt
	} else {
		input.StartedAt = input.StartedAt.UTC()
	}
	if input.CompletedAt.Before(input.StartedAt) {
		return Snapshot{}, errors.New("snapshot completed before it started")
	}
	output := input
	output.Issues = make(map[string]Issue, len(input.Issues)+len(previous.Issues))
	for id, issue := range previous.Issues {
		if _, present := input.Issues[id]; !present && input.Full {
			issue.Availability = AvailabilityUnavailable
			if issue.MissingSince == "" {
				issue.MissingSince = input.CompletedAt.Format(time.RFC3339Nano)
			}
		}
		output.Issues[id] = issue
	}
	for id, issue := range input.Issues {
		if !numericIDPattern.MatchString(id) || issue.ID != id {
			return Snapshot{}, fmt.Errorf("invalid or mismatched issue ID %q", id)
		}
		prev, existed := previous.Issues[id]
		if issue.Availability == "" {
			issue.Availability = AvailabilityAvailable
		}
		switch issue.Availability {
		case AvailabilityUnavailable:
			if existed {
				issue.Raw, issue.Comments, issue.Timeline, issue.Number = prev.Raw, prev.Comments, prev.Timeline, prev.Number
				if prev.MissingSince != "" {
					issue.MissingSince = prev.MissingSince
				}
			}
			if issue.MissingSince == "" {
				issue.MissingSince = input.CompletedAt.Format(time.RFC3339Nano)
			}
		case AvailabilityAvailable:
			issue.MissingSince = ""
			if issue.Comments == nil && existed {
				issue.Comments = prev.Comments
			}
			if issue.Timeline == nil && existed {
				issue.Timeline = prev.Timeline
			}
		default:
			return Snapshot{}, fmt.Errorf("invalid availability for issue %s", id)
		}
		normalized, err := normalizeIssue(issue)
		if err != nil {
			return Snapshot{}, fmt.Errorf("issue %s: %w", id, err)
		}
		output.Issues[id] = normalized
	}
	if output.Labels == nil {
		output.Labels = previous.Labels
	}
	if output.Milestones == nil {
		output.Milestones = previous.Milestones
	}
	var err error
	output.Labels, err = canonicalCollection(output.Labels)
	if err != nil {
		return Snapshot{}, fmt.Errorf("labels: %w", err)
	}
	output.Milestones, err = canonicalCollection(output.Milestones)
	if err != nil {
		return Snapshot{}, fmt.Errorf("milestones: %w", err)
	}
	return output, nil
}

func normalizeIssue(issue Issue) (Issue, error) {
	if !numericIDPattern.MatchString(issue.ID) || issue.Number <= 0 {
		return Issue{}, errors.New("valid stable ID and positive issue number are required")
	}
	raw, err := canonicalObject(issue.Raw)
	if err != nil {
		return Issue{}, fmt.Errorf("raw issue: %w", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return Issue{}, err
	}
	if rawID(raw) != issue.ID {
		return Issue{}, errors.New("raw issue ID does not match archive ID")
	}
	if number, err := strconv.ParseInt(string(fields["number"]), 10, 64); err != nil || number != issue.Number {
		return Issue{}, errors.New("raw issue number does not match archive number")
	}
	if _, isPR := fields["pull_request"]; isPR {
		return Issue{}, errors.New("pull requests are outside issue archive coverage")
	}
	if issue.Availability != AvailabilityAvailable && issue.Availability != AvailabilityUnavailable {
		return Issue{}, errors.New("invalid issue availability")
	}
	if issue.MissingSince != "" {
		if _, err := time.Parse(time.RFC3339Nano, issue.MissingSince); err != nil {
			return Issue{}, errors.New("invalid missing_since timestamp")
		}
	}
	if (issue.Availability == AvailabilityUnavailable) != (issue.MissingSince != "") {
		return Issue{}, errors.New("availability and missing_since disagree")
	}
	issue.Raw = raw
	issue.Comments, err = canonicalCollection(issue.Comments)
	if err != nil {
		return Issue{}, fmt.Errorf("comments: %w", err)
	}
	issue.Timeline, err = canonicalCollection(issue.Timeline)
	if err != nil {
		return Issue{}, fmt.Errorf("timeline: %w", err)
	}
	return issue, nil
}

type issueState struct {
	ID           string `json:"id"`
	Number       int64  `json:"number"`
	Availability string `json:"availability"`
	MissingSince string `json:"missing_since,omitempty"`
}

func snapshotFiles(ctx context.Context, snapshot Snapshot) (map[string][]byte, Manifest, error) {
	files := make(map[string][]byte, len(snapshot.Issues)*5+5)
	addJSON := func(path string, value any) error {
		data, err := encodeJSON(value)
		if err != nil {
			return err
		}
		files[path] = data
		return nil
	}
	if err := addJSON("repository.json", snapshot.Repository); err != nil {
		return nil, Manifest{}, err
	}
	if err := addJSON("labels.json", snapshot.Labels); err != nil {
		return nil, Manifest{}, err
	}
	if err := addJSON("milestones.json", snapshot.Milestones); err != nil {
		return nil, Manifest{}, err
	}
	var comments int
	for id, issue := range snapshot.Issues {
		if err := ctx.Err(); err != nil {
			return nil, Manifest{}, err
		}
		prefix := "issues/" + id + "/"
		files[prefix+"issue.json"] = issue.Raw
		if err := addJSON(prefix+"comments.json", issue.Comments); err != nil {
			return nil, Manifest{}, err
		}
		if err := addJSON(prefix+"timeline.json", issue.Timeline); err != nil {
			return nil, Manifest{}, err
		}
		state := issueState{ID: id, Number: issue.Number, Availability: issue.Availability, MissingSince: issue.MissingSince}
		if err := addJSON(prefix+"state.json", state); err != nil {
			return nil, Manifest{}, err
		}
		markdown, err := RenderMarkdown(issue)
		if err != nil {
			return nil, Manifest{}, err
		}
		files[prefix+"README.md"] = []byte(markdown)
		comments += len(issue.Comments)
	}
	files["README.md"] = []byte(archiveReadme(snapshot))
	m := Manifest{
		SchemaVersion: SchemaVersion, APIVersion: snapshot.APIVersion, StartedAt: snapshot.StartedAt, CompletedAt: snapshot.CompletedAt,
		IssuesSince: snapshot.IssuesSince, CommentsSince: snapshot.CommentsSince, LastFullReconcileAt: snapshot.LastFullReconcileAt,
		Full: snapshot.Full, IssueCount: len(snapshot.Issues), CommentCount: comments, Coverage: snapshotCoverage(), Files: make(map[string]string, len(files)),
	}
	for path, data := range files {
		m.Files[path] = sha256Hex(data)
	}
	m.ContentHash = filesHash(m.Files)
	if err := addJSON("manifest.json", m); err != nil {
		return nil, Manifest{}, err
	}
	return files, m, nil
}

func sha256Hex(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

func filesHash(files map[string]string) string {
	// json.Marshal sorts string map keys; all values are hexadecimal strings.
	data, _ := json.Marshal(files)
	return sha256Hex(data)
}
