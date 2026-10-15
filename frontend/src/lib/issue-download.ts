import { apiFetch } from './connect/transport';

export function issueExportUrl(repoId: string, snapshot: string) {
  const query = snapshot ? `?${new URLSearchParams({ snapshot })}` : '';
  return `/api/repos/${encodeURIComponent(repoId)}/issues/export${query}`;
}

export async function downloadIssueSnapshot(repoId: string, snapshot: string) {
  const response = await apiFetch(issueExportUrl(repoId, snapshot));
  if (!response.ok) {
    const message = await response.text();
    throw new Error(message.trim() || 'Failed to export issue backup');
  }

  const blob = await response.blob();
  const objectUrl = URL.createObjectURL(blob);
  const anchor = document.createElement('a');
  anchor.href = objectUrl;
  anchor.download = `issues-${repoId}-${snapshot ? snapshot.slice(0, 12) : 'latest'}.zip`;
  anchor.style.display = 'none';
  document.body.append(anchor);
  try {
    anchor.click();
  } finally {
    anchor.remove();
    URL.revokeObjectURL(objectUrl);
  }
}
