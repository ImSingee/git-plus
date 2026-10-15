import { afterEach, describe, expect, it, vi } from 'vitest';
import { downloadIssueSnapshot, issueExportUrl } from './issue-download';
import { apiFetch } from './connect/transport';

vi.mock('./connect/transport', () => ({ apiFetch: vi.fn() }));

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  vi.mocked(apiFetch).mockReset();
});

describe('issue snapshot export', () => {
  it('encodes the snapshot without putting credentials in the URL', () => {
    expect(issueExportUrl('42', '')).toBe('/api/repos/42/issues/export');
    expect(issueExportUrl('42', 'a/b')).toBe(
      '/api/repos/42/issues/export?snapshot=a%2Fb',
    );
  });

  it('uses the shared authenticated fetch and downloads the returned ZIP', async () => {
    vi.mocked(apiFetch).mockResolvedValue(new Response('zip contents'));
    const anchor = {
      href: '',
      download: '',
      style: { display: '' },
      click: vi.fn(),
      remove: vi.fn(),
    };
    const append = vi.fn();
    vi.stubGlobal('document', {
      createElement: vi.fn(() => anchor),
      body: { append },
    });
    const createUrl = vi
      .spyOn(URL, 'createObjectURL')
      .mockReturnValue('blob:test');
    const revokeUrl = vi
      .spyOn(URL, 'revokeObjectURL')
      .mockImplementation(() => {});

    await downloadIssueSnapshot('42', 'abc123');

    expect(apiFetch).toHaveBeenCalledWith(
      '/api/repos/42/issues/export?snapshot=abc123',
    );
    expect(createUrl).toHaveBeenCalledWith(expect.any(Blob));
    expect(anchor.download).toBe('issues-42-abc123.zip');
    expect(anchor.click).toHaveBeenCalledOnce();
    expect(anchor.remove).toHaveBeenCalledOnce();
    expect(revokeUrl).toHaveBeenCalledWith('blob:test');
  });

  it('surfaces HTTP errors and does not try to download an error response', async () => {
    vi.mocked(apiFetch).mockResolvedValue(
      new Response('Authentication required', { status: 401 }),
    );
    await expect(downloadIssueSnapshot('42', '')).rejects.toThrow(
      'Authentication required',
    );
  });
});
