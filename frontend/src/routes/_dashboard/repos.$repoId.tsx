import { Suspense, useEffect, useRef, useState } from 'react';
import { Link, createFileRoute } from '@tanstack/react-router';
import {
  ActionIcon,
  Alert,
  Anchor,
  Avatar,
  Badge,
  Box,
  Breadcrumbs,
  Button,
  Center,
  Checkbox,
  Code,
  Container,
  Drawer,
  Group,
  Loader,
  Paper,
  Progress,
  SimpleGrid,
  Stack,
  Stepper,
  Table,
  Tabs,
  Text,
  Title,
} from '@mantine/core';
import {
  useQuery,
  useSuspenseInfiniteQuery,
  useSuspenseQuery,
} from '@tanstack/react-query';
import { useIntersection } from '@mantine/hooks';
import { timestampDate } from '@bufbuild/protobuf/wkt';
import dayjs from 'dayjs';
import {
  IconAlertTriangle,
  IconCheck,
  IconCircleX,
  IconDownload,
  IconExternalLink,
  IconFileZip,
  IconGitBranch,
  IconHistory,
  IconMessageCircle,
  IconPackage,
  IconTag,
  IconX,
} from '@tabler/icons-react';
import { toast } from 'sonner';
import type {
  RepoRef,
  RepoRefChange,
  StreamRepositoryDownloadResponse,
} from '~rpc/gitplus/repo/v1/repo_pb';
import { DownloadStage, DownloadState } from '~rpc/gitplus/repo/v1/repo_pb';
import { repoClient } from '~lib/connect/client';
import { apiFetch } from '~lib/connect/transport';
import { configQueryOptions } from '~lib/config-queries';
import {
  downloadStageLabel,
  estimateProcessingTime,
  estimatedDownloadSize,
  formatEstimatedBytes,
} from '~lib/repo-download';
import {
  repoDetailQueryOptions,
  repoRefChangesQueryOptions,
  repoRefsQueryOptions,
} from '~lib/repo-queries';
import { sourcePrimaryLabel, sourceSecondaryLabel } from '~lib/source-display';
import { IssuesTab } from '~components/issues/IssuesTab';

export const Route = createFileRoute('/_dashboard/repos/$repoId')({
  loader: ({ context: { queryClient }, params: { repoId } }) =>
    queryClient.ensureQueryData(repoDetailQueryOptions(repoId)),
  component: RepoDetailPage,
});

function formatTime(ts: Parameters<typeof timestampDate>[0] | undefined) {
  if (!ts) return '—';
  return dayjs(timestampDate(ts)).format('YYYY-MM-DD HH:mm');
}

function formatTimeAgo(ts: Parameters<typeof timestampDate>[0] | undefined) {
  if (!ts) return '—';
  return dayjs(timestampDate(ts)).fromNow();
}

function actionBadgeColor(action: string) {
  switch (action) {
    case 'create':
      return 'green';
    case 'update':
      return 'blue';
    case 'delete':
      return 'red';
    default:
      return 'gray';
  }
}

function shortHash(hash: string) {
  return hash ? hash.slice(0, 8) : '';
}

function stripRefPrefix(name: string) {
  return name.replace(/^refs\/(heads|tags)\//, '');
}

function truncateMessage(msg: string, max = 72) {
  const firstLine = msg.split('\n')[0];
  if (firstLine.length <= max) return firstLine;
  return firstLine.slice(0, max) + '...';
}

async function triggerRepositoryDownload(
  repoId: string,
  downloadId: string,
  filename: string,
) {
  const response = await apiFetch(
    `/api/repos/${repoId}/downloads/${downloadId}/archive`,
  );
  if (!response.ok) {
    const message = await response.text();
    throw new Error(message.trim() || 'Failed to download repository archive');
  }

  const blob = await response.blob();
  const objectUrl = URL.createObjectURL(blob);
  const anchor = document.createElement('a');
  anchor.href = objectUrl;
  anchor.download = filename;
  anchor.style.display = 'none';
  document.body.append(anchor);
  anchor.click();
  anchor.remove();
  URL.revokeObjectURL(objectUrl);
}

function renderHashDisplay(change: RepoRefChange) {
  switch (change.action) {
    case 'create':
      return <Code>{shortHash(change.newHash)}</Code>;
    case 'delete':
      return (
        <Text size="xs" c="dimmed" span>
          <Code>{shortHash(change.oldHash)}</Code> (deleted)
        </Text>
      );
    case 'update':
      return (
        <Group gap={4}>
          <Code>{shortHash(change.oldHash)}</Code>
          <Text size="xs" c="dimmed" span>
            &rarr;
          </Text>
          <Code>{shortHash(change.newHash)}</Code>
        </Group>
      );
    default:
      return '—';
  }
}

function ChangeCommitCell({
  change,
  maxWidth,
}: {
  change: RepoRefChange;
  maxWidth?: number;
}) {
  const commit = change.newCommit;

  return (
    <Table.Td style={maxWidth ? { maxWidth } : undefined}>
      <Box>
        <Box fz="xs">{renderHashDisplay(change)}</Box>
        {commit?.message ? (
          <Text size="xs" c="dimmed" lineClamp={1}>
            {truncateMessage(commit.message)}
          </Text>
        ) : (
          <Text size="xs" c="dimmed">
            —
          </Text>
        )}
      </Box>
    </Table.Td>
  );
}

function TabFallback() {
  return (
    <Center py="xl">
      <Loader size="sm" />
    </Center>
  );
}

function RepoDetailPage() {
  const { repoId } = Route.useParams();
  const { data } = useSuspenseQuery(repoDetailQueryOptions(repoId));
  const { data: configData } = useQuery(configQueryOptions);

  const [activeTab, setActiveTab] = useState<string | null>('branches');

  const { data: branchesData } = useQuery({
    ...repoRefsQueryOptions(repoId, 'head'),
    enabled: false,
  });
  const { data: tagsData } = useQuery({
    ...repoRefsQueryOptions(repoId, 'tag'),
    enabled: false,
  });

  const repo = data.repository!;
  const source = (configData?.config?.sources ?? []).find(
    (item) => item.id === repo.sourceId,
  );
  const sourceValue = source ? sourcePrimaryLabel(source) : repo.sourceId;
  const sourceDescription = source ? sourceSecondaryLabel(source) : null;

  const meta = repo.meta as Record<string, unknown> | undefined;
  const ownerMeta = meta?.['owner'] as Record<string, unknown> | undefined;
  const avatarUrl = (ownerMeta?.['avatar_url'] as string) ?? '';
  const language = (meta?.['language'] as string) ?? '';
  const stars = (meta?.['stargazers_count'] as number) ?? 0;
  const forks = (meta?.['forks_count'] as number) ?? 0;
  const [owner, repoName] = repo.fullName.split('/');

  return (
    <Container fluid py="xl" px="xl">
      <Breadcrumbs mb="lg">
        <Link to="/repos" style={{ textDecoration: 'none', color: 'inherit' }}>
          <Text size="sm" c="dimmed">
            Repositories
          </Text>
        </Link>
        <Text size="sm">{repo.fullName}</Text>
      </Breadcrumbs>

      <Group align="flex-start" gap="lg" mb="xl">
        <Avatar src={avatarUrl} alt={owner} size="xl" radius="md" />
        <div style={{ flex: 1 }}>
          <Group gap="sm" align="center">
            <Title order={2}>{repoName}</Title>
            {repo.isArchived && (
              <Badge variant="light" color="yellow" size="sm">
                Archived
              </Badge>
            )}
            {repo.isPrivate && (
              <Badge variant="light" color="gray" size="sm">
                Private
              </Badge>
            )}
            {repo.isFork && (
              <Badge variant="light" color="grape" size="sm">
                Fork
              </Badge>
            )}
          </Group>
          <Text c="dimmed" size="sm">
            {owner}
          </Text>
          {repo.description && (
            <Text mt="xs" size="sm">
              {repo.description}
            </Text>
          )}
          <Group gap="lg" mt="sm">
            {language && (
              <Text size="xs" c="dimmed">
                {language}
              </Text>
            )}
            {stars > 0 && (
              <Text size="xs" c="dimmed">
                {stars.toLocaleString()} stars
              </Text>
            )}
            {forks > 0 && (
              <Text size="xs" c="dimmed">
                {forks.toLocaleString()} forks
              </Text>
            )}
            {repo.htmlUrl && (
              <Anchor
                href={repo.htmlUrl}
                target="_blank"
                rel="noopener noreferrer"
                size="xs"
                style={{ display: 'flex', alignItems: 'center', gap: 4 }}
              >
                GitHub <IconExternalLink size={12} />
              </Anchor>
            )}
          </Group>
        </div>
      </Group>

      <SimpleGrid cols={{ base: 1, sm: 2, lg: 4 }} mb="xl">
        <InfoCard
          label="Source"
          value={sourceValue}
          description={sourceDescription ?? undefined}
        />
        <InfoCard label="Default Branch" value={repo.defaultBranch || '—'} />
        <InfoCard label="First Seen" value={formatTimeAgo(repo.createdAt)} />
        <InfoCard label="Last Seen" value={formatTimeAgo(repo.lastSeenAt)} />
      </SimpleGrid>

      <Tabs value={activeTab} onChange={setActiveTab}>
        <Tabs.List>
          <Tabs.Tab value="branches" leftSection={<IconGitBranch size={14} />}>
            Branches
            {branchesData ? ` (${branchesData.refs.length})` : ''}
          </Tabs.Tab>
          <Tabs.Tab value="tags" leftSection={<IconTag size={14} />}>
            Tags
            {tagsData ? ` (${tagsData.refs.length})` : ''}
          </Tabs.Tab>
          <Tabs.Tab value="changes" leftSection={<IconHistory size={14} />}>
            Changes
          </Tabs.Tab>
          <Tabs.Tab
            value="issues"
            leftSection={<IconMessageCircle size={14} />}
          >
            Issues
          </Tabs.Tab>
          <Tabs.Tab value="download" leftSection={<IconDownload size={14} />}>
            Download
          </Tabs.Tab>
        </Tabs.List>

        <Tabs.Panel value="branches" pt="md">
          {activeTab === 'branches' && (
            <Suspense fallback={<TabFallback />}>
              <RefsTab
                repoId={repoId}
                refKind="head"
                emptyLabel="No branches tracked yet."
              />
            </Suspense>
          )}
        </Tabs.Panel>

        <Tabs.Panel value="tags" pt="md">
          {activeTab === 'tags' && (
            <Suspense fallback={<TabFallback />}>
              <RefsTab
                repoId={repoId}
                refKind="tag"
                emptyLabel="No tags tracked yet."
              />
            </Suspense>
          )}
        </Tabs.Panel>

        <Tabs.Panel value="changes" pt="md">
          {activeTab === 'changes' && (
            <Suspense fallback={<TabFallback />}>
              <ChangesTab repoId={repoId} />
            </Suspense>
          )}
        </Tabs.Panel>

        <Tabs.Panel value="download" pt="md">
          {activeTab === 'download' && (
            <DownloadTab
              repoId={repoId}
              archiveRepoSizeBytes={repo.archiveRepoSizeBytes}
            />
          )}
        </Tabs.Panel>
        <Tabs.Panel value="issues" pt="md">
          {activeTab === 'issues' && <IssuesTab key={repoId} repoId={repoId} />}
        </Tabs.Panel>
      </Tabs>
    </Container>
  );
}

function stageToStep(stage: DownloadStage): number {
  switch (stage) {
    case DownloadStage.COPY_BARE:
      return 0;
    case DownloadStage.MATERIALIZE_REFS:
      return 1;
    case DownloadStage.PACKAGE_ZIP:
      return 2;
    case DownloadStage.READY:
      return 3;
    default:
      return 0;
  }
}

function DownloadTab({
  repoId,
  archiveRepoSizeBytes,
}: {
  repoId: string;
  archiveRepoSizeBytes: bigint | undefined;
}) {
  const [downloadEvent, setDownloadEvent] =
    useState<StreamRepositoryDownloadResponse | null>(null);
  const [isPreparing, setIsPreparing] = useState(false);
  const [lastCompletedAt, setLastCompletedAt] = useState<Date | null>(null);
  const abortRef = useRef<AbortController | null>(null);

  useEffect(() => {
    return () => {
      abortRef.current?.abort();
      abortRef.current = null;
    };
  }, []);

  const handleCancel = () => {
    abortRef.current?.abort();
    abortRef.current = null;
    setIsPreparing(false);
    setDownloadEvent(null);
  };

  const handlePrepareDownload = async () => {
    abortRef.current?.abort();

    const controller = new AbortController();
    abortRef.current = controller;
    setIsPreparing(true);
    setDownloadEvent(null);

    try {
      for await (const event of repoClient.streamRepositoryDownload(
        { repoId: BigInt(repoId) },
        { signal: controller.signal },
      )) {
        setDownloadEvent(event);

        if (event.state === DownloadState.READY) {
          await triggerRepositoryDownload(
            repoId,
            event.downloadId,
            event.downloadFilename,
          );
          setLastCompletedAt(new Date());
          toast.success('Repository download started');
          break;
        }

        if (event.state === DownloadState.FAILED) {
          toast.error(event.errorMessage || event.summary || 'Download failed');
          break;
        }
      }
    } catch (error) {
      if (controller.signal.aborted) {
        return;
      }

      const message =
        error instanceof Error
          ? error.message
          : 'Failed to prepare repository download';
      setDownloadEvent((current) => ({
        $typeName: 'gitplus.repo.v1.StreamRepositoryDownloadResponse',
        repoId: BigInt(repoId),
        state: DownloadState.FAILED,
        stage: current?.stage ?? DownloadStage.UNSPECIFIED,
        summary: current?.summary || 'Failed to prepare download',
        progressPercent: current?.progressPercent ?? 0,
        estimatedProcessingLabel: current?.estimatedProcessingLabel || '',
        estimatedProcessingBytes:
          current?.estimatedProcessingBytes ?? archiveRepoSizeBytes ?? 0n,
        estimatedDownloadBytes:
          current?.estimatedDownloadBytes ??
          estimatedDownloadSize(archiveRepoSizeBytes) ??
          0n,
        archiveSizeBytes:
          current?.archiveSizeBytes ?? archiveRepoSizeBytes ?? 0n,
        downloadId: current?.downloadId || '',
        downloadFilename: current?.downloadFilename || '',
        errorMessage: message,
      }));
      toast.error(message);
    } finally {
      if (abortRef.current === controller) {
        abortRef.current = null;
      }
      setIsPreparing(false);
    }
  };

  const currentState = downloadEvent?.state ?? DownloadState.UNSPECIFIED;
  const currentStage = downloadEvent?.stage ?? DownloadStage.UNSPECIFIED;
  const progressPercent = downloadEvent?.progressPercent ?? 0;
  const isRunning = isPreparing || currentState === DownloadState.RUNNING;
  const isFailed = currentState === DownloadState.FAILED;
  const isReady = currentState === DownloadState.READY;
  const processingSize =
    downloadEvent?.estimatedProcessingBytes || archiveRepoSizeBytes;
  const downloadSize =
    downloadEvent?.estimatedDownloadBytes ??
    estimatedDownloadSize(archiveRepoSizeBytes);
  const processingTime =
    downloadEvent?.estimatedProcessingLabel ||
    estimateProcessingTime(archiveRepoSizeBytes);

  return (
    <Stack gap="lg">
      <Text size="sm" c="dimmed">
        Download a zipped repository snapshot with the current active branches
        and tags materialized into a normal Git repository.
      </Text>

      <SimpleGrid cols={{ base: 1, sm: 3 }}>
        <Paper withBorder p="md" radius="sm">
          <Text size="xs" c="dimmed" tt="uppercase" fw={600}>
            Repo Size
          </Text>
          <Text size="sm" fw={500} mt={4}>
            {formatEstimatedBytes(processingSize)}
          </Text>
        </Paper>
        <Paper withBorder p="md" radius="sm">
          <Text size="xs" c="dimmed" tt="uppercase" fw={600}>
            Download Size (est.)
          </Text>
          <Text size="sm" fw={500} mt={4}>
            {formatEstimatedBytes(downloadSize)}
          </Text>
        </Paper>
        <Paper withBorder p="md" radius="sm">
          <Text size="xs" c="dimmed" tt="uppercase" fw={600}>
            Processing Time (est.)
          </Text>
          <Text size="sm" fw={500} mt={4}>
            {processingTime}
          </Text>
        </Paper>
      </SimpleGrid>

      {isRunning && (
        <Paper withBorder p="lg" radius="sm">
          <Stepper active={stageToStep(currentStage)} size="sm" iconSize={28}>
            <Stepper.Step
              label="Copy"
              description="Clone bare repo"
              icon={<IconPackage size={14} />}
            />
            <Stepper.Step
              label="Restore"
              description="Refs & branches"
              icon={<IconGitBranch size={14} />}
            />
            <Stepper.Step
              label="Package"
              description="Zip archive"
              icon={<IconFileZip size={14} />}
            />
            <Stepper.Step
              label="Ready"
              description="Download file"
              icon={<IconCheck size={14} />}
            />
          </Stepper>

          <Progress
            value={progressPercent}
            radius="xl"
            size="sm"
            mt="lg"
            animated
          />

          <Group justify="space-between" mt="xs">
            <Text size="xs" c="dimmed">
              {downloadEvent?.summary || downloadStageLabel(currentStage)}
            </Text>
            <Text size="xs" c="dimmed">
              {progressPercent}%
            </Text>
          </Group>

          <Group justify="center" mt="md">
            <Button
              variant="subtle"
              color="gray"
              size="xs"
              leftSection={<IconX size={14} />}
              onClick={handleCancel}
            >
              Cancel
            </Button>
          </Group>
        </Paper>
      )}

      {isFailed && (
        <Alert
          variant="light"
          color="red"
          title="Download failed"
          icon={<IconAlertTriangle size={18} />}
        >
          <Text size="sm">
            {downloadEvent?.errorMessage || 'An unexpected error occurred.'}
          </Text>
          <Button
            size="xs"
            variant="light"
            color="red"
            mt="sm"
            leftSection={<IconCircleX size={14} />}
            onClick={handlePrepareDownload}
          >
            Retry
          </Button>
        </Alert>
      )}

      {isReady && lastCompletedAt && (
        <Alert variant="light" color="teal" icon={<IconCheck size={18} />}>
          <Group justify="space-between" align="center">
            <div>
              <Text size="sm" fw={500}>
                Download started
              </Text>
              <Text size="xs" c="dimmed">
                {downloadEvent?.downloadFilename} &middot;{' '}
                {dayjs(lastCompletedAt).fromNow()}
              </Text>
            </div>
            <Button
              variant="light"
              color="teal"
              size="xs"
              leftSection={<IconDownload size={14} />}
              onClick={handlePrepareDownload}
            >
              Download again
            </Button>
          </Group>
        </Alert>
      )}

      {!isRunning && (
        <div>
          <Button
            leftSection={<IconDownload size={16} />}
            onClick={handlePrepareDownload}
          >
            {isFailed ? 'Retry download' : 'Prepare & download'}
          </Button>
          {lastCompletedAt && !isReady && (
            <Text size="xs" c="dimmed" mt="xs">
              Last download {dayjs(lastCompletedAt).fromNow()}
            </Text>
          )}
        </div>
      )}
    </Stack>
  );
}

function RefsTab({
  repoId,
  refKind,
  emptyLabel,
}: {
  repoId: string;
  refKind: 'head' | 'tag';
  emptyLabel: string;
}) {
  const [showDeleted, setShowDeleted] = useState(false);
  const [historyRefName, setHistoryRefName] = useState<string | null>(null);
  const { data } = useSuspenseQuery(
    repoRefsQueryOptions(repoId, refKind, showDeleted),
  );
  const refs = data.refs;

  if (refs.length === 0 && !showDeleted) {
    return (
      <Text size="sm" c="dimmed">
        {emptyLabel}
      </Text>
    );
  }

  return (
    <>
      <Table striped highlightOnHover>
        <Table.Thead>
          <Table.Tr>
            <Table.Th>Name</Table.Th>
            <Table.Th>Hash</Table.Th>
            <Table.Th>Last Commit</Table.Th>
            {showDeleted && <Table.Th>Status</Table.Th>}
            <Table.Th>Updated</Table.Th>
            <Table.Th style={{ width: 1 }} />
          </Table.Tr>
        </Table.Thead>
        <Table.Tbody>
          {refs.map((r) => (
            <RefRow
              key={r.id.toString()}
              ref_={r}
              showStatus={showDeleted}
              onHistory={() => setHistoryRefName(r.refName)}
            />
          ))}
        </Table.Tbody>
      </Table>
      <Checkbox
        mt="md"
        label="Show deleted"
        checked={showDeleted}
        onChange={(e) => setShowDeleted(e.currentTarget.checked)}
      />
      <Drawer
        opened={historyRefName !== null}
        onClose={() => setHistoryRefName(null)}
        title={
          <Group gap="xs">
            <IconHistory size={16} />
            <Text fw={600}>
              {historyRefName && stripRefPrefix(historyRefName)}
            </Text>
          </Group>
        }
        position="right"
        size="lg"
      >
        {historyRefName && (
          <Suspense fallback={<TabFallback />}>
            <RefHistoryDrawerContent repoId={repoId} refName={historyRefName} />
          </Suspense>
        )}
      </Drawer>
    </>
  );
}

function RefHistoryDrawerContent({
  repoId,
  refName,
}: {
  repoId: string;
  refName: string;
}) {
  const { data, hasNextPage, fetchNextPage, isFetchingNextPage } =
    useSuspenseInfiniteQuery(repoRefChangesQueryOptions(repoId, refName));

  const { ref: sentinelRef, entry } = useIntersection({ threshold: 0 });

  useEffect(() => {
    if (entry?.isIntersecting && hasNextPage && !isFetchingNextPage) {
      fetchNextPage();
    }
  }, [entry?.isIntersecting, hasNextPage, isFetchingNextPage, fetchNextPage]);

  const changes = data.pages.flatMap((p) => p.changes);

  if (changes.length === 0) {
    return (
      <Text size="sm" c="dimmed">
        No changes recorded for this ref.
      </Text>
    );
  }

  return (
    <>
      <Table striped highlightOnHover>
        <Table.Thead>
          <Table.Tr>
            <Table.Th>Time</Table.Th>
            <Table.Th>Action</Table.Th>
            <Table.Th>Commit</Table.Th>
          </Table.Tr>
        </Table.Thead>
        <Table.Tbody>
          {changes.map((c) => (
            <RefChangeRow key={c.id.toString()} change={c} />
          ))}
        </Table.Tbody>
      </Table>
      <div ref={sentinelRef} style={{ height: 1 }} />
      {isFetchingNextPage && (
        <Center mt="md">
          <Loader size="sm" />
        </Center>
      )}
    </>
  );
}

function RefChangeRow({ change }: { change: RepoRefChange }) {
  return (
    <Table.Tr>
      <Table.Td>
        <Text size="xs">{formatTime(change.createdAt)}</Text>
      </Table.Td>
      <Table.Td>
        <Badge
          variant="light"
          color={actionBadgeColor(change.action)}
          size="sm"
        >
          {change.action}
        </Badge>
      </Table.Td>
      <ChangeCommitCell change={change} />
    </Table.Tr>
  );
}

function ChangesTab({ repoId }: { repoId: string }) {
  const { data, hasNextPage, fetchNextPage, isFetchingNextPage } =
    useSuspenseInfiniteQuery(repoRefChangesQueryOptions(repoId));

  const { ref: sentinelRef, entry } = useIntersection({ threshold: 0 });

  useEffect(() => {
    if (entry?.isIntersecting && hasNextPage && !isFetchingNextPage) {
      fetchNextPage();
    }
  }, [entry?.isIntersecting, hasNextPage, isFetchingNextPage, fetchNextPage]);

  const changes = data.pages.flatMap((p) => p.changes);

  if (changes.length === 0) {
    return (
      <Text size="sm" c="dimmed">
        No ref changes recorded yet.
      </Text>
    );
  }

  return (
    <>
      <Table striped highlightOnHover>
        <Table.Thead>
          <Table.Tr>
            <Table.Th>Time</Table.Th>
            <Table.Th>Ref</Table.Th>
            <Table.Th>Action</Table.Th>
            <Table.Th>Commit</Table.Th>
          </Table.Tr>
        </Table.Thead>
        <Table.Tbody>
          {changes.map((c) => (
            <ChangeRow key={c.id.toString()} change={c} />
          ))}
        </Table.Tbody>
      </Table>
      <div ref={sentinelRef} style={{ height: 1 }} />
      {isFetchingNextPage && (
        <Center mt="md">
          <Loader size="sm" />
        </Center>
      )}
    </>
  );
}

function InfoCard({
  label,
  value,
  description,
}: {
  label: string;
  value: string;
  description?: string;
}) {
  return (
    <Box>
      <Text size="xs" c="dimmed" tt="uppercase" fw={600}>
        {label}
      </Text>
      <Text size="sm" fw={500} mt={2}>
        {value}
      </Text>
      {description && (
        <Text size="xs" c="dimmed" mt={2}>
          {description}
        </Text>
      )}
    </Box>
  );
}

function RefRow({
  ref_,
  showStatus,
  onHistory,
}: {
  ref_: RepoRef;
  showStatus: boolean;
  onHistory: () => void;
}) {
  const commit = ref_.currentCommit;

  return (
    <Table.Tr>
      <Table.Td>
        <Code>{stripRefPrefix(ref_.refName)}</Code>
      </Table.Td>
      <Table.Td>
        <Code>{shortHash(ref_.currentHash)}</Code>
      </Table.Td>
      <Table.Td style={{ maxWidth: 360 }}>
        {commit?.message ? (
          <>
            <Text size="xs" lineClamp={1}>
              {truncateMessage(commit.message)}
            </Text>
            {(commit.authorName || commit.committedAt) && (
              <Text size="xs" c="dimmed">
                {[
                  commit.authorName,
                  commit.committedAt && formatTimeAgo(commit.committedAt),
                ]
                  .filter(Boolean)
                  .join(', ')}
              </Text>
            )}
          </>
        ) : (
          <Text size="xs" c="dimmed">
            —
          </Text>
        )}
      </Table.Td>
      {showStatus && (
        <Table.Td>
          <Badge
            variant="light"
            color={ref_.status === 'active' ? 'green' : 'red'}
            size="sm"
          >
            {ref_.status}
          </Badge>
        </Table.Td>
      )}
      <Table.Td>
        <Text size="xs" c="dimmed">
          {formatTimeAgo(ref_.lastHashUpdatedAt ?? ref_.lastSeenAt)}
        </Text>
      </Table.Td>
      <Table.Td>
        <ActionIcon variant="subtle" size="sm" onClick={onHistory}>
          <IconHistory size={14} />
        </ActionIcon>
      </Table.Td>
    </Table.Tr>
  );
}

function ChangeRow({ change }: { change: RepoRefChange }) {
  return (
    <Table.Tr>
      <Table.Td>
        <Text size="xs">{formatTime(change.createdAt)}</Text>
      </Table.Td>
      <Table.Td>
        <Group gap={6}>
          {change.refKind === 'tag' ? (
            <IconTag size={14} />
          ) : (
            <IconGitBranch size={14} />
          )}
          <Code>{stripRefPrefix(change.refName)}</Code>
        </Group>
      </Table.Td>
      <Table.Td>
        <Badge
          variant="light"
          color={actionBadgeColor(change.action)}
          size="sm"
        >
          {change.action}
        </Badge>
      </Table.Td>
      <ChangeCommitCell change={change} maxWidth={320} />
    </Table.Tr>
  );
}
