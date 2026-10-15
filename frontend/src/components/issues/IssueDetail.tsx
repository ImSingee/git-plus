import { useState } from 'react';
import {
  Accordion,
  Alert,
  Badge,
  Box,
  Button,
  Divider,
  Group,
  Loader,
  Paper,
  Stack,
  Text,
  Title,
} from '@mantine/core';
import { useQuery } from '@tanstack/react-query';
import { IconAlertTriangle, IconArrowLeft } from '@tabler/icons-react';
import type { GetIssueResponse } from '~rpc/gitplus/issue/v1/issue_pb';
import { issueDetailQueryOptions } from '~lib/issue-queries';
import {
  issueStateLabel,
  issueTime,
  jsonObject,
  jsonString,
  parseIssueObject,
  parseIssueRecords,
  recordAuthor,
} from '~lib/issue-display';

// React escapes all content here. Never parse archived HTML or fetch embedded media.
export function ArchivedText({ children }: { children: string }) {
  return (
    <Text
      component="pre"
      size="sm"
      ff="inherit"
      m={0}
      style={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}
    >
      {children}
    </Text>
  );
}

export function IssueDetail({
  repoId,
  issueId,
  snapshot,
  latestCommit,
  onBack,
}: {
  repoId: string;
  issueId: string;
  snapshot: string;
  latestCommit: string;
  onBack: () => void;
}) {
  const detail = useQuery(
    issueDetailQueryOptions(repoId, issueId, snapshot, latestCommit),
  );

  return (
    <Stack gap="md">
      <Button
        type="button"
        variant="subtle"
        leftSection={<IconArrowLeft size={14} />}
        onClick={onBack}
        style={{ alignSelf: 'flex-start' }}
      >
        Back to latest issue index
      </Button>
      {detail.isPending ? (
        <Group role="status">
          <Loader size="sm" />
          <Text size="sm">Loading archived issue…</Text>
        </Group>
      ) : detail.isError ? (
        <Alert
          color="red"
          title="Could not load archived issue"
          icon={<IconAlertTriangle size={18} />}
        >
          <Text size="sm">{detail.error.message}</Text>
          <Text size="sm" mt="xs">
            An issue may not exist in an older snapshot. Try another snapshot or
            return to the latest version.
          </Text>
          <Button
            type="button"
            variant="light"
            color="red"
            size="xs"
            mt="sm"
            onClick={() => void detail.refetch()}
          >
            Retry
          </Button>
        </Alert>
      ) : (
        <IssueDetailContent key={`${issueId}:${snapshot}`} data={detail.data} />
      )}
    </Stack>
  );
}

export function IssueDetailContent({ data }: { data: GetIssueResponse }) {
  const raw = parseIssueObject(data.rawJson);
  const comments = parseIssueRecords(data.commentsJson);
  const timeline = parseIssueRecords(data.timelineJson);
  const issue = data.issue;
  const body = raw ? jsonString(raw.body) : data.markdown;
  const [commentLimit, setCommentLimit] = useState(30);
  const [eventLimit, setEventLimit] = useState(50);

  if (!issue) {
    return (
      <Alert color="yellow" title="Issue not found">
        No issue was recorded in this snapshot.
      </Alert>
    );
  }

  return (
    <Stack gap="lg">
      <Box>
        <Group gap="xs" mb="xs">
          <Badge
            color={issue.state === 'open' ? 'green' : 'gray'}
            variant="light"
          >
            {issueStateLabel(issue.state)}
          </Badge>
          {issue.availability && issue.availability !== 'available' && (
            <Badge color="yellow" variant="light">
              {issueStateLabel(issue.availability)}
            </Badge>
          )}
          <Text size="sm" c="dimmed">
            #{String(issue.number)}
          </Text>
        </Group>
        <Title order={3} style={{ overflowWrap: 'anywhere' }}>
          {issue.title}
        </Title>
        <Text size="sm" c="dimmed" mt={4}>
          @{issue.authorLogin || 'unknown'} · Updated{' '}
          {issueTime(issue.updatedAt)}
        </Text>
        {data.snapshot && (
          <Text size="xs" c="dimmed" mt={4}>
            Snapshot {data.snapshot.slice(0, 12)}
          </Text>
        )}
        {issue.labels.length > 0 && (
          <Group gap={6} mt="sm">
            {issue.labels.map((label) => (
              <Badge key={label} variant="outline" color="gray" tt="none">
                {label}
              </Badge>
            ))}
          </Group>
        )}
        {issue.assignees.length > 0 && (
          <Text size="sm" mt="sm">
            Assignees: {issue.assignees.map((login) => `@${login}`).join(', ')}
          </Text>
        )}
        {issue.availability && issue.availability !== 'available' && (
          <Alert color="yellow" mt="md" title="Last known copy">
            This issue was not available at the last check recorded in this
            snapshot. The archived content is preserved; this does not prove the
            issue was deleted.
          </Alert>
        )}
      </Box>

      <Box>
        <Title order={4} mb="xs">
          Description
        </Title>
        <Text size="xs" c="dimmed" mb="sm">
          Original Markdown shown as text. HTML, images, and attachments are not
          loaded.
        </Text>
        <Paper withBorder p="md" radius="sm">
          <ArchivedText>{body || 'No description.'}</ArchivedText>
        </Paper>
      </Box>

      <Box>
        <Title order={4} mb="md">
          Comments{comments ? ` (${comments.length})` : ''}
        </Title>
        {comments === null ? (
          <Alert color="yellow">
            Comments could not be read. The original data is available below.
          </Alert>
        ) : comments.length === 0 ? (
          <Text size="sm" c="dimmed">
            No comments recorded in this snapshot.
          </Text>
        ) : (
          <Stack gap="md">
            {comments.slice(0, commentLimit).map((comment, index) => (
              <Box key={`${String(comment.id ?? '')}:${index}`}>
                <Group gap="xs" mb="xs">
                  <Text size="sm" fw={600}>
                    @{recordAuthor(comment)}
                  </Text>
                  <Text size="xs" c="dimmed">
                    {issueTime(jsonString(comment.created_at))}
                  </Text>
                </Group>
                <ArchivedText>
                  {jsonString(comment.body) || 'No comment body recorded.'}
                </ArchivedText>
                <Divider mt="md" />
              </Box>
            ))}
            {comments.length > commentLimit && (
              <Button
                type="button"
                variant="default"
                onClick={() => setCommentLimit((limit) => limit + 30)}
              >
                Show more comments
              </Button>
            )}
          </Stack>
        )}
      </Box>

      <Box>
        <Title order={4} mb="md">
          Timeline{timeline ? ` (${timeline.length})` : ''}
        </Title>
        <Text size="xs" c="dimmed" mb="sm">
          Only events returned by GitHub and captured during backup are
          included.
        </Text>
        {timeline === null ? (
          <Alert color="yellow">
            Timeline could not be read. The original data is available below.
          </Alert>
        ) : timeline.length === 0 ? (
          <Text size="sm" c="dimmed">
            No timeline events recorded in this snapshot.
          </Text>
        ) : (
          <Stack gap="sm">
            {timeline.slice(0, eventLimit).map((event, index) => {
              const label = jsonString(jsonObject(event.label)?.name);
              const assignee = jsonString(jsonObject(event.assignee)?.login);
              const rename = jsonObject(event.rename);
              return (
                <Box key={`${String(event.id ?? index)}:${index}`}>
                  <Group gap="xs">
                    <Text size="sm" fw={500}>
                      {issueStateLabel(jsonString(event.event) || 'event')}
                    </Text>
                    <Text size="xs" c="dimmed">
                      @{recordAuthor(event)} ·{' '}
                      {issueTime(jsonString(event.created_at))}
                    </Text>
                  </Group>
                  {label && <Text size="sm">Label: {label}</Text>}
                  {assignee && <Text size="sm">Assignee: @{assignee}</Text>}
                  {rename && (
                    <Text size="sm">
                      {jsonString(rename.from)} → {jsonString(rename.to)}
                    </Text>
                  )}
                  {jsonString(event.body) && (
                    <ArchivedText>{jsonString(event.body)}</ArchivedText>
                  )}
                </Box>
              );
            })}
            {timeline.length > eventLimit && (
              <Button
                type="button"
                variant="default"
                onClick={() => setEventLimit((limit) => limit + 50)}
              >
                Show more events
              </Button>
            )}
          </Stack>
        )}
      </Box>

      <Accordion variant="separated">
        <Accordion.Item value="raw">
          <Accordion.Control>Original JSON</Accordion.Control>
          <Accordion.Panel>
            <Stack gap="md">
              <Text size="xs" c="dimmed">
                Preserved API records for inspection. Export the snapshot ZIP to
                keep these files.
              </Text>
              {[
                ['Issue', data.rawJson],
                ['Comments', data.commentsJson],
                ['Timeline', data.timelineJson],
              ].map(([label, value]) => (
                <Box key={label}>
                  <Text size="sm" fw={600} mb="xs">
                    {label}
                  </Text>
                  <Box mah={360} style={{ overflow: 'auto' }}>
                    {/* Preserve raw numeric IDs without JavaScript rounding. */}
                    <ArchivedText>{value}</ArchivedText>
                  </Box>
                </Box>
              ))}
            </Stack>
          </Accordion.Panel>
        </Accordion.Item>
      </Accordion>
    </Stack>
  );
}
