package issuearchive

import (
	"encoding/json"
	"fmt"
	"html"
	"sort"
	"strings"
)

// RenderMarkdown produces a readable, inert representation of an issue. User
// text is never interpreted as HTML or active Markdown: bodies are fenced text,
// and titles/metadata are escaped. Raw JSON remains authoritative.
func RenderMarkdown(issue Issue) (string, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(issue.Raw, &fields); err != nil {
		return "", fmt.Errorf("render issue: %w", err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# #%d %s\n\n", issue.Number, markdownText(stringField(fields, "title")))
	fmt.Fprintf(&b, "- Stable ID: %s\n- State: %s\n- Availability: %s\n", markdownText(issue.ID), markdownText(stringField(fields, "state")), markdownText(issue.Availability))
	if issue.MissingSince != "" {
		fmt.Fprintf(&b, "- Unavailable since: %s\n", markdownText(issue.MissingSince))
	}
	if url := stringField(fields, "html_url"); url != "" {
		fmt.Fprintf(&b, "- Source: %s\n", markdownText(url))
	}
	b.WriteString("\n## Description\n\n")
	b.WriteString(fencedText(stringField(fields, "body")))
	b.WriteString("\n## Comments\n\n")
	if len(issue.Comments) == 0 {
		b.WriteString("No comments captured.\n")
	}
	for _, comment := range issue.Comments {
		var c map[string]json.RawMessage
		if err := json.Unmarshal(comment, &c); err != nil {
			return "", fmt.Errorf("render comment: %w", err)
		}
		var author map[string]json.RawMessage
		_ = json.Unmarshal(c["user"], &author)
		fmt.Fprintf(&b, "### Comment %s\n\nAuthor: %s\n\n", markdownText(rawID(comment)), markdownText(stringField(author, "login")))
		b.WriteString(fencedText(stringField(c, "body")))
		b.WriteByte('\n')
	}
	b.WriteString("\n## Timeline\n\n")
	if len(issue.Timeline) == 0 {
		b.WriteString("No timeline events captured.\n")
	}
	for _, event := range issue.Timeline {
		var e map[string]json.RawMessage
		if err := json.Unmarshal(event, &e); err != nil {
			return "", fmt.Errorf("render timeline: %w", err)
		}
		fmt.Fprintf(&b, "- %s: %s\n", markdownText(stringField(e, "created_at")), markdownText(stringField(e, "event")))
	}
	b.WriteString("\nAttachments are links only. Original JSON files contain the complete captured fields.\n")
	return b.String(), nil
}

func stringField(fields map[string]json.RawMessage, key string) string {
	var value string
	_ = json.Unmarshal(fields[key], &value)
	return value
}

func markdownText(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	value = html.EscapeString(value)
	return strings.NewReplacer("\\", "\\\\", "`", "\\`", "*", "\\*", "_", "\\_", "[", "\\[", "]", "\\]", "(", "\\(", ")", "\\)", "#", "\\#", "!", "\\!", "|", "\\|").Replace(value)
}

func fencedText(text string) string {
	// A body can itself contain arbitrary Markdown fences. Choose a delimiter
	// longer than every backtick run so the body cannot escape its text block.
	maxRun, run := 0, 0
	for _, r := range text {
		if r == '`' {
			run++
			if run > maxRun {
				maxRun = run
			}
		} else {
			run = 0
		}
	}
	length := maxRun + 1
	if length < 3 {
		length = 3
	}
	fence := strings.Repeat("`", length)
	return fence + "text\n" + text + "\n" + fence + "\n"
}

func archiveReadme(snapshot Snapshot) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# GitHub Issues archive: %s\n\n", markdownText(snapshot.Repository.FullName))
	b.WriteString("Raw JSON is authoritative. Markdown is a derived, plain-text view.\n\nThis snapshot covers non-pull-request issues, issue comments, repository labels and milestones, and REST-supported timeline events visible to the backup account. Attachments are links only; attachment binaries are not downloaded. Unavailable issues retain previously observed content. Missing content is not proof of deletion. Content removed before it was observed cannot be recovered.\n\nThe ZIP export contains this snapshot only; copy the local bare Git archive to preserve all snapshot history. manifest.json lists SHA-256 checksums for every other file.\n\n## Issues\n\n")
	ids := make([]string, 0, len(snapshot.Issues))
	for id := range snapshot.Issues {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		a, b := snapshot.Issues[ids[i]], snapshot.Issues[ids[j]]
		if a.Number != b.Number {
			return a.Number < b.Number
		}
		return ids[i] < ids[j]
	})
	for _, id := range ids {
		issue := snapshot.Issues[id]
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(issue.Raw, &fields)
		fmt.Fprintf(&b, "- [#%d](issues/%s/README.md) %s (%s)\n", issue.Number, id, markdownText(stringField(fields, "title")), issue.Availability)
	}
	return b.String()
}
