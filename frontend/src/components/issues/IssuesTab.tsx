import { useState } from 'react';
import {
  Alert,
  Badge,
  Box,
  Button,
  Group,
  Loader,
  Paper,
  Select,
  SimpleGrid,
  Stack,
  Table,
  Text,
  TextInput,
  UnstyledButton,
} from '@mantine/core';
import { useDebouncedValue } from '@mantine/hooks';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import {
  IconAlertTriangle,
  IconDownload,
  IconRefresh,
  IconSearch,
} from '@tabler/icons-react';
import { toast } from 'sonner';
import { IssueDetail } from './IssueDetail';
import { issueClient } from '~lib/connect/client';
import { issueStateLabel, issueTime } from '~lib/issue-display';
import { downloadIssueSnapshot } from '~lib/issue-download';
import {
  issueListQueryOptions,
  issueSnapshotsQueryOptions,
  issueStatusQueryOptions,
} from '~lib/issue-queries';
import { TaskEnqueueResult } from '~rpc/gitplus/task/v1/task_pb';

export function IssuesTab({ repoId }: { repoId: string }) {
  const queryClient = useQueryClient();
  const statusQuery = useQuery(issueStatusQueryOptions(repoId));
  const status = statusQuery.data?.status;
  const latestCommit = status?.latestCommit ?? '';
  const snapshotsQuery = useQuery(
    issueSnapshotsQueryOptions(repoId, latestCommit),
  );
  const [snapshot, setSnapshot] = useState('');
  const [selectedIssueId, setSelectedIssueId] = useState<string | null>(null);
  const [search, setSearch] = useState('');
  const [debouncedSearch] = useDebouncedValue(search.trim(), 250);
  const [state, setState] = useState('');

  const syncMutation = useMutation({
    mutationFn: (full: boolean) =>
      issueClient.enqueueSync({ repoId: BigInt(repoId), full }),
    onSuccess: (response) => {
      toast.success(
        response.result === TaskEnqueueResult.DEDUPED
          ? 'An issue backup is already queued or running'
          : 'Issue backup queued',
      );
      void queryClient.invalidateQueries({ queryKey: ['issues', repoId] });
      void queryClient.invalidateQueries({ queryKey: ['task'] });
    },
    onError: (error) => toast.error(error.message),
  });
  const exportMutation = useMutation({
    mutationFn: () => downloadIssueSnapshot(repoId, snapshot),
    onSuccess: () => toast.success('Issue backup download started'),
    onError: (error) => toast.error(error.message),
  });

  const snapshots = snapshotsQuery.data?.snapshots ?? [];
  const snapshotOptions = [
    { value: '', label: 'Latest snapshot' },
    ...snapshots.map((item) => ({
      value: item.commit,
      label: `${issueTime(item.createdAt)} · ${item.commit.slice(0, 8)} · ${item.issueCount} issues`,
    })),
  ];
  // Keep the selected historical version reachable even if the history query fails.
  if (snapshot && !snapshots.some((item) => item.commit === snapshot)) {
    snapshotOptions.push({
      value: snapshot,
      label: `Snapshot ${snapshot.slice(0, 12)}`,
    });
  }
  const busy =
    syncMutation.isPending ||
    status?.state === 'running' ||
    status?.state === 'syncing';

  return (
    <Stack gap="lg">
      <Group justify="space-between" align="flex-start">
        <Box style={{ flex: 1, minWidth: 220 }}>
          <Group gap="xs">
            <Text fw={600}>GitHub Issues backup</Text>
            {status && (
              <Badge
                variant="light"
                color={
                  !status.enabled
                    ? 'gray'
                    : status.lastError
                      ? 'yellow'
                      : 'teal'
                }
              >
                {!status.enabled ? 'Disabled' : issueStateLabel(status.state)}
              </Badge>
            )}
          </Group>
          <Text size="sm" c="dimmed" mt={4}>
            Read saved issues without contacting GitHub. Sync actions fetch
            updates from GitHub.
          </Text>
        </Box>
        <Group gap="xs">
          <Button
            type="button"
            variant="default"
            size="sm"
            disabled={!status?.enabled || busy}
            onClick={() => syncMutation.mutate(true)}
          >
            Full reconcile
          </Button>
          <Button
            type="button"
            size="sm"
            leftSection={<IconRefresh size={14} />}
            disabled={!status?.enabled || busy}
            loading={syncMutation.isPending}
            onClick={() => syncMutation.mutate(false)}
          >
            Sync issues
          </Button>
        </Group>
      </Group>

      {statusQuery.isPending && (
        <Group role="status">
          <Loader size="sm" />
          <Text size="sm">Loading backup status…</Text>
        </Group>
      )}
      {statusQuery.isError && (
        <QueryError
          title="Could not load backup status"
          message={statusQuery.error.message}
          retry={() => void statusQuery.refetch()}
        />
      )}
      {status && !status.enabled && (
        <Alert color="gray" title="Issue backups are turned off">
          Enable “Back up GitHub Issues” in this repository's source settings to
          start backing up. Previously saved snapshots remain readable.
        </Alert>
      )}
      {status?.lastError && (
        <Alert
          color="yellow"
          title="Last backup did not complete"
          icon={<IconAlertTriangle size={18} />}
        >
          <Text size="sm" style={{ overflowWrap: 'anywhere' }}>
            {status.lastError}
          </Text>
          <Text size="sm" mt="xs">
            The last successful snapshot remains available. Check source access
            and retry the sync.
          </Text>
        </Alert>
      )}
      {status && (
        <Paper withBorder p="md" radius="sm">
          <SimpleGrid cols={{ base: 1, sm: 3 }} spacing="md">
            <StatusValue
              label="Last successful backup"
              value={issueTime(status.lastSuccessfulAt)}
            />
            <StatusValue
              label="Last full reconcile"
              value={issueTime(status.lastFullReconcileAt)}
            />
            <StatusValue
              label="Latest saved coverage"
              value={`${status.issueCount.toLocaleString()} issues · ${status.commentCount.toLocaleString()} comments`}
            />
          </SimpleGrid>
        </Paper>
      )}
      <Text size="xs" c="dimmed">
        Coverage: issues, comments, and available timeline events. Pull requests
        are excluded. Attachments are links only; files are not downloaded and
        may be unavailable offline. Full reconcile rechecks all issues,
        including older changes.
      </Text>

      <Group align="flex-end" gap="sm">
        <Select
          label="Snapshot for issue details and export"
          data={snapshotOptions}
          value={snapshot}
          onChange={(value) => setSnapshot(value ?? '')}
          allowDeselect={false}
          searchable
          disabled={snapshotsQuery.isPending}
          style={{ flex: 1, minWidth: 220 }}
        />
        <Button
          type="button"
          variant="default"
          leftSection={<IconDownload size={14} />}
          loading={exportMutation.isPending}
          disabled={!latestCommit && snapshots.length === 0 && !snapshot}
          onClick={() => exportMutation.mutate()}
        >
          Export ZIP
        </Button>
      </Group>
      {snapshotsQuery.isError && (
        <QueryError
          title="Could not load snapshot history"
          message={snapshotsQuery.error.message}
          retry={() => void snapshotsQuery.refetch()}
        />
      )}
      {snapshot && (
        <Alert color="blue">
          Details and ZIP export use snapshot {snapshot.slice(0, 12)}. The issue
          index and counts still show the latest backup.
        </Alert>
      )}

      {selectedIssueId ? (
        <IssueDetail
          repoId={repoId}
          issueId={selectedIssueId}
          snapshot={snapshot}
          latestCommit={latestCommit}
          onBack={() => setSelectedIssueId(null)}
        />
      ) : (
        <Stack gap="md">
          <Group align="flex-end" gap="sm">
            <TextInput
              label="Search latest issue index"
              placeholder="Search titles or issue numbers"
              leftSection={<IconSearch size={16} />}
              value={search}
              onChange={(event) => setSearch(event.currentTarget.value)}
              style={{ flex: 1, minWidth: 220 }}
            />
            <Select
              label="Issue state"
              value={state}
              onChange={(value) => setState(value ?? '')}
              allowDeselect={false}
              data={[
                { value: '', label: 'All states' },
                { value: 'open', label: 'Open' },
                { value: 'closed', label: 'Closed' },
              ]}
              w={160}
            />
          </Group>
          <IssueList
            key={`${state}:${debouncedSearch}:${latestCommit}`}
            repoId={repoId}
            state={state}
            search={debouncedSearch}
            latestCommit={latestCommit}
            onSelect={setSelectedIssueId}
          />
        </Stack>
      )}
    </Stack>
  );
}

function StatusValue({ label, value }: { label: string; value: string }) {
  return (
    <Box>
      <Text size="xs" c="dimmed">
        {label}
      </Text>
      <Text size="sm" fw={500} mt={4}>
        {value}
      </Text>
    </Box>
  );
}

function QueryError({
  title,
  message,
  retry,
}: {
  title: string;
  message: string;
  retry: () => void;
}) {
  return (
    <Alert color="red" title={title} icon={<IconAlertTriangle size={18} />}>
      <Text size="sm" style={{ overflowWrap: 'anywhere' }}>
        {message}
      </Text>
      <Button
        type="button"
        size="xs"
        variant="light"
        color="red"
        mt="sm"
        onClick={retry}
      >
        Retry
      </Button>
    </Alert>
  );
}

function IssueList({
  repoId,
  state,
  search,
  latestCommit,
  onSelect,
}: {
  repoId: string;
  state: string;
  search: string;
  latestCommit: string;
  onSelect: (issueId: string) => void;
}) {
  const [tokens, setTokens] = useState<Array<string>>(['']);
  const list = useQuery(
    issueListQueryOptions(
      repoId,
      state,
      search,
      tokens.at(-1) ?? '',
      latestCommit,
    ),
  );

  if (list.isPending)
    return (
      <Group role="status" py="lg">
        <Loader size="sm" />
        <Text size="sm">Loading archived issues…</Text>
      </Group>
    );
  if (list.isError)
    return (
      <QueryError
        title="Could not load archived issues"
        message={list.error.message}
        retry={() => void list.refetch()}
      />
    );

  return (
    <Stack gap="sm">
      {list.data.issues.length === 0 ? (
        <Paper withBorder p="xl" radius="sm">
          <Text fw={500}>
            {search || state
              ? 'No matching issues'
              : latestCommit
                ? 'No issues in the latest backup'
                : 'No issues backed up yet'}
          </Text>
          <Text size="sm" c="dimmed" mt={4}>
            {search || state
              ? 'Try another search or choose all states.'
              : latestCommit
                ? 'This snapshot contains no issues. Pull requests are not included in issue backups.'
                : 'Enable issue backups for this source, then sync issues. Repositories with no issues will remain empty.'}
          </Text>
        </Paper>
      ) : (
        <Table.ScrollContainer minWidth={600}>
          <Table verticalSpacing="sm" highlightOnHover>
            <Table.Thead>
              <Table.Tr>
                <Table.Th>Issue</Table.Th>
                <Table.Th>State</Table.Th>
                <Table.Th>Updated</Table.Th>
              </Table.Tr>
            </Table.Thead>
            <Table.Tbody>
              {list.data.issues.map((issue) => (
                <Table.Tr key={issue.id}>
                  <Table.Td>
                    <UnstyledButton
                      type="button"
                      onClick={() => onSelect(issue.id)}
                      style={{ display: 'block', maxWidth: '100%' }}
                    >
                      <Text
                        size="sm"
                        fw={500}
                        style={{ overflowWrap: 'anywhere' }}
                      >
                        {issue.title}
                      </Text>
                      <Text size="xs" c="dimmed" mt={2}>
                        #{String(issue.number)} · @
                        {issue.authorLogin || 'unknown'}
                      </Text>
                    </UnstyledButton>
                    {issue.labels.length > 0 && (
                      <Group gap={4} mt={6}>
                        {issue.labels.slice(0, 4).map((label) => (
                          <Badge
                            key={label}
                            size="xs"
                            color="gray"
                            variant="outline"
                            tt="none"
                          >
                            {label}
                          </Badge>
                        ))}
                        {issue.labels.length > 4 && (
                          <Text size="xs" c="dimmed">
                            +{issue.labels.length - 4}
                          </Text>
                        )}
                      </Group>
                    )}
                  </Table.Td>
                  <Table.Td>
                    <Stack gap={4} align="flex-start">
                      <Badge
                        variant="light"
                        color={issue.state === 'open' ? 'green' : 'gray'}
                      >
                        {issueStateLabel(issue.state)}
                      </Badge>
                      {issue.availability &&
                        issue.availability !== 'available' && (
                          <Badge variant="light" color="yellow">
                            {issueStateLabel(issue.availability)}
                          </Badge>
                        )}
                    </Stack>
                  </Table.Td>
                  <Table.Td>
                    <Text size="xs" c="dimmed" style={{ whiteSpace: 'nowrap' }}>
                      {issueTime(issue.updatedAt)}
                    </Text>
                  </Table.Td>
                </Table.Tr>
              ))}
            </Table.Tbody>
          </Table>
        </Table.ScrollContainer>
      )}
      <Group justify="space-between">
        <Text size="xs" c="dimmed" aria-live="polite">
          {list.data.totalCount.toLocaleString()} issues · Page {tokens.length}
        </Text>
        <Group gap="xs">
          <Button
            type="button"
            variant="default"
            size="xs"
            disabled={tokens.length === 1}
            onClick={() => setTokens((current) => current.slice(0, -1))}
          >
            Previous
          </Button>
          <Button
            type="button"
            variant="default"
            size="xs"
            disabled={!list.data.nextPageToken}
            onClick={() =>
              setTokens((current) => [...current, list.data.nextPageToken])
            }
          >
            Next
          </Button>
        </Group>
      </Group>
    </Stack>
  );
}
