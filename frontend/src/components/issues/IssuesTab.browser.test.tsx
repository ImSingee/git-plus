import { create } from '@bufbuild/protobuf';
import { MantineProvider } from '@mantine/core';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { IssuesTab } from './IssuesTab';
import { issueClient } from '~lib/connect/client';
import { downloadIssueSnapshot } from '~lib/issue-download';
import {
  EnqueueSyncResponseSchema,
  GetIssueResponseSchema,
  GetStatusResponseSchema,
  ListIssuesResponseSchema,
  ListSnapshotsResponseSchema,
} from '~rpc/gitplus/issue/v1/issue_pb';
import { TaskEnqueueResult } from '~rpc/gitplus/task/v1/task_pb';

vi.mock('~lib/connect/client', () => ({
  issueClient: {
    getStatus: vi.fn(),
    listIssues: vi.fn(),
    getIssue: vi.fn(),
    listSnapshots: vi.fn(),
    enqueueSync: vi.fn(),
  },
}));
vi.mock('~lib/issue-download', () => ({ downloadIssueSnapshot: vi.fn() }));

const issue = {
  id: '123',
  number: 7n,
  title: 'Preserve offline discussions',
  state: 'open',
  authorLogin: 'octocat',
  updatedAt: '2026-10-15T10:00:00Z',
  availability: 'available',
  labels: ['backup'],
  assignees: ['maintainer'],
};
const clients: Array<QueryClient> = [];

function renderTab() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  clients.push(client);
  return render(
    <MantineProvider>
      <QueryClientProvider client={client}>
        <IssuesTab repoId="42" />
      </QueryClientProvider>
    </MantineProvider>,
  );
}

async function waitForLoadedIndex() {
  // The first status response changes latestCommit and remounts the issue list.
  // Wait for that response before finding the current, attached row button.
  await screen.findByText('Ready');
  await screen.findByRole('button', { name: /Preserve offline discussions/ });
}

beforeEach(() => {
  vi.mocked(issueClient.getStatus).mockResolvedValue(
    create(GetStatusResponseSchema, {
      status: {
        enabled: true,
        state: 'ready',
        latestCommit: 'abcdef1234',
        issueCount: 1,
        commentCount: 1,
        lastSuccessfulAt: '2026-10-15T10:00:00Z',
      },
    }),
  );
  vi.mocked(issueClient.listIssues).mockResolvedValue(
    create(ListIssuesResponseSchema, { issues: [issue], totalCount: 1 }),
  );
  vi.mocked(issueClient.listSnapshots).mockResolvedValue(
    create(ListSnapshotsResponseSchema, {
      snapshots: [
        {
          commit: 'abcdef1234',
          createdAt: '2026-10-15T10:00:00Z',
          issueCount: 1,
          commentCount: 1,
        },
      ],
    }),
  );
  vi.mocked(issueClient.getIssue).mockResolvedValue(
    create(GetIssueResponseSchema, {
      issue,
      rawJson: JSON.stringify({ body: 'Archived issue body' }),
      commentsJson: JSON.stringify([
        {
          id: 1,
          body: 'An offline comment',
          user: { login: 'reader' },
          created_at: '2026-10-15T09:00:00Z',
        },
      ]),
      timelineJson: JSON.stringify([
        {
          id: 2,
          event: 'labeled',
          actor: { login: 'maintainer' },
          label: { name: 'backup' },
          created_at: '2026-10-15T09:00:00Z',
        },
      ]),
      snapshot: 'abcdef1234',
    }),
  );
  vi.mocked(issueClient.enqueueSync).mockResolvedValue(
    create(EnqueueSyncResponseSchema, { result: TaskEnqueueResult.QUEUED }),
  );
  vi.mocked(downloadIssueSnapshot).mockResolvedValue(undefined);
});

afterEach(() => {
  cleanup();
  clients.splice(0).forEach((client) => client.clear());
  vi.resetAllMocks();
});

describe('offline issue browser', () => {
  it('shows loading status and list states', () => {
    vi.mocked(issueClient.getStatus).mockReturnValue(new Promise(() => {}));
    vi.mocked(issueClient.listIssues).mockReturnValue(new Promise(() => {}));
    renderTab();
    expect(screen.getByText('Loading backup status…')).toBeVisible();
    expect(screen.getByText('Loading archived issues…')).toBeVisible();
  });

  it('explains empty backups and disabled source settings', async () => {
    vi.mocked(issueClient.getStatus).mockResolvedValue(
      create(GetStatusResponseSchema, { status: { enabled: false } }),
    );
    vi.mocked(issueClient.listIssues).mockResolvedValue(
      create(ListIssuesResponseSchema),
    );
    renderTab();
    expect(await screen.findByText('No issues backed up yet')).toBeVisible();
    expect(
      await screen.findByText('Issue backups are turned off'),
    ).toBeVisible();
    expect(screen.getByRole('button', { name: 'Sync issues' })).toBeDisabled();
  });

  it('shows list failures with retry instead of claiming the archive is empty', async () => {
    vi.mocked(issueClient.listIssues).mockRejectedValue(
      new Error('Archive temporarily unavailable'),
    );
    renderTab();
    await screen.findByText('Ready');
    expect(
      await screen.findByText('Could not load archived issues'),
    ).toBeVisible();
    expect(screen.getByText('Archive temporarily unavailable')).toBeVisible();
    expect(screen.queryByText('No issues backed up yet')).toBeNull();
    vi.mocked(issueClient.listIssues).mockResolvedValue(
      create(ListIssuesResponseSchema, { issues: [issue], totalCount: 1 }),
    );
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }));
    expect(
      await screen.findByRole('button', {
        name: /Preserve offline discussions/,
      }),
    ).toBeVisible();
  });

  it('opens the saved body, comments, timeline, and original JSON', async () => {
    renderTab();
    await waitForLoadedIndex();
    fireEvent.click(
      screen.getByRole('button', {
        name: /Preserve offline discussions/,
      }),
    );
    expect(await screen.findByText('Archived issue body')).toBeVisible();
    expect(screen.getByText('An offline comment')).toBeVisible();
    expect(screen.getByText('Labeled')).toBeVisible();
    expect(screen.getByText('Label: backup')).toBeVisible();
    expect(screen.getByText(/Original Markdown shown as text/)).toBeVisible();
    fireEvent.click(screen.getByRole('button', { name: 'Original JSON' }));
    await waitFor(() =>
      expect(screen.getByText(/Preserved API records/)).toBeVisible(),
    );
    expect(issueClient.getIssue).toHaveBeenCalledWith(
      { repoId: 42n, issueId: '123', snapshot: '' },
      expect.anything(),
    );
  });

  it('renders hostile Markdown, HTML, and remote images only as escaped text', async () => {
    const hostile =
      '<img src="https://example.test/tracker.png" onerror="alert(1)"><script>alert(1)</script> ![remote](https://example.test/image.png) [bad](javascript:alert(1))';
    vi.mocked(issueClient.getIssue).mockResolvedValue(
      create(GetIssueResponseSchema, {
        issue,
        rawJson: JSON.stringify({ body: hostile }),
        commentsJson: JSON.stringify([
          { body: '<iframe src="https://example.test/embed"></iframe>' },
        ]),
        timelineJson: '[]',
      }),
    );
    const { container } = renderTab();
    await waitForLoadedIndex();
    fireEvent.click(
      screen.getByRole('button', {
        name: /Preserve offline discussions/,
      }),
    );
    expect(await screen.findByText(hostile)).toBeVisible();
    expect(
      screen.getByText('<iframe src="https://example.test/embed"></iframe>'),
    ).toBeVisible();
    expect(
      container.querySelector('img, iframe, script, a[href^="javascript:"]'),
    ).toBeNull();
  });

  it('handles missing historical details without losing the index', async () => {
    vi.mocked(issueClient.getIssue).mockRejectedValue(
      new Error('Issue not present in this snapshot'),
    );
    renderTab();
    await waitForLoadedIndex();
    fireEvent.click(
      screen.getByRole('button', {
        name: /Preserve offline discussions/,
      }),
    );
    expect(
      await screen.findByText('Could not load archived issue'),
    ).toBeVisible();
    fireEvent.click(
      screen.getByRole('button', { name: 'Back to latest issue index' }),
    );
    expect(
      await screen.findByRole('button', {
        name: /Preserve offline discussions/,
      }),
    ).toBeVisible();
  });

  it('uses opaque page tokens and resets pagination for new searches', async () => {
    vi.mocked(issueClient.listIssues).mockResolvedValue(
      create(ListIssuesResponseSchema, {
        issues: [issue],
        totalCount: 31,
        nextPageToken: 'opaque-next',
      }),
    );
    renderTab();
    await waitForLoadedIndex();
    fireEvent.click(screen.getByRole('button', { name: 'Next' }));
    await waitFor(() =>
      expect(issueClient.listIssues).toHaveBeenLastCalledWith(
        expect.objectContaining({ pageToken: 'opaque-next' }),
        expect.anything(),
      ),
    );
    fireEvent.change(screen.getByLabelText('Search latest issue index'), {
      target: { value: 'offline' },
    });
    await waitFor(() =>
      expect(issueClient.listIssues).toHaveBeenLastCalledWith(
        expect.objectContaining({ search: 'offline', pageToken: '' }),
        expect.anything(),
      ),
    );
  });

  it('queues incremental and full sync and exports the selected snapshot', async () => {
    renderTab();
    await waitForLoadedIndex();
    fireEvent.click(screen.getByRole('button', { name: 'Sync issues' }));
    await waitFor(() =>
      expect(issueClient.enqueueSync).toHaveBeenCalledWith({
        repoId: 42n,
        full: false,
      }),
    );
    await waitFor(() =>
      expect(
        screen.getByRole('button', { name: 'Full reconcile' }),
      ).not.toBeDisabled(),
    );
    fireEvent.click(screen.getByRole('button', { name: 'Full reconcile' }));
    await waitFor(() =>
      expect(issueClient.enqueueSync).toHaveBeenCalledWith({
        repoId: 42n,
        full: true,
      }),
    );
    fireEvent.click(
      screen.getByRole('combobox', {
        name: 'Snapshot for issue details and export',
      }),
    );
    fireEvent.click(await screen.findByRole('option', { name: /abcdef12/ }));
    expect(
      screen.getByText(
        /The issue index and counts still show the latest backup/,
      ),
    ).toBeVisible();
    fireEvent.click(screen.getByRole('button', { name: 'Export ZIP' }));
    await waitFor(() =>
      expect(downloadIssueSnapshot).toHaveBeenCalledWith('42', 'abcdef1234'),
    );
  });
});
