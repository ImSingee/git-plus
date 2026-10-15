import { queryOptions } from '@tanstack/react-query';
import { issueClient } from './connect/client';

export function issueStatusQueryOptions(repoId: string) {
  return queryOptions({
    queryKey: ['issues', repoId, 'status'],
    queryFn: ({ signal }) =>
      issueClient.getStatus({ repoId: BigInt(repoId) }, { signal }),
    refetchInterval: 5_000,
  });
}

export function issueListQueryOptions(
  repoId: string,
  state: string,
  search: string,
  pageToken: string,
  latestCommit: string,
) {
  return queryOptions({
    queryKey: [
      'issues',
      repoId,
      'list',
      { state, search, pageToken, latestCommit },
    ],
    queryFn: ({ signal }) =>
      issueClient.listIssues(
        { repoId: BigInt(repoId), pageSize: 30, pageToken, state, search },
        { signal },
      ),
  });
}

export function issueDetailQueryOptions(
  repoId: string,
  issueId: string,
  snapshot: string,
  latestCommit: string,
) {
  return queryOptions({
    queryKey: ['issues', repoId, 'detail', { issueId, snapshot, latestCommit }],
    queryFn: ({ signal }) =>
      issueClient.getIssue(
        { repoId: BigInt(repoId), issueId, snapshot },
        { signal },
      ),
  });
}

export function issueSnapshotsQueryOptions(
  repoId: string,
  latestCommit: string,
) {
  return queryOptions({
    queryKey: ['issues', repoId, 'snapshots', latestCommit],
    queryFn: ({ signal }) =>
      issueClient.listSnapshots({ repoId: BigInt(repoId) }, { signal }),
  });
}
