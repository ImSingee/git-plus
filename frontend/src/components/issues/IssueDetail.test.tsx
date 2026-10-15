import { create } from '@bufbuild/protobuf';
import { MantineProvider } from '@mantine/core';
import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import { ArchivedText, IssueDetailContent } from './IssueDetail';
import { GetIssueResponseSchema } from '~rpc/gitplus/issue/v1/issue_pb';

function renderDetail(
  rawJson: string,
  commentsJson = '[]',
  timelineJson = '[]',
) {
  const data = create(GetIssueResponseSchema, {
    issue: {
      id: '123',
      number: 7n,
      title: 'Offline issue',
      state: 'open',
      authorLogin: 'octocat',
      availability: 'unavailable',
    },
    rawJson,
    commentsJson,
    timelineJson,
    markdown: '# Derived archive document',
  });
  return renderToStaticMarkup(
    <MantineProvider>
      <IssueDetailContent data={data} />
    </MantineProvider>,
  );
}

describe('archived issue HTML safety', () => {
  it('preserves raw large integer IDs without parsing or rounding them', () => {
    const raw = '{"id":900000000000000001,"body":"saved"}';
    const html = renderToStaticMarkup(
      <MantineProvider>
        <ArchivedText>{raw}</ArchivedText>
      </MantineProvider>,
    );
    expect(html).toContain('900000000000000001');
    expect(html).not.toContain('900000000000000000');
  });
  it('escapes hostile bodies, comments, and timeline text without active images or links', () => {
    const html = renderDetail(
      JSON.stringify({
        body: '<script>alert(1)</script> ![remote](https://example.test/image.png)',
      }),
      JSON.stringify([
        {
          body: '<img src="https://example.test/tracker.png" onerror="alert(1)">',
        },
      ]),
      JSON.stringify([
        {
          event: 'commented',
          body: '<iframe src="https://example.test/embed"></iframe>',
        },
      ]),
    );
    expect(html).toContain('&lt;script&gt;alert(1)&lt;/script&gt;');
    expect(html).toContain('&lt;img');
    expect(html).toContain('&lt;iframe');
    expect(html).not.toMatch(/<(script|img|iframe)\b/i);
    expect(html).not.toContain('href="https://example.test');
    expect(html).toContain('Last known copy');
    expect(html).toContain('does not prove the issue was deleted');
  });

  it('keeps a missing description empty instead of substituting the entire derived document', () => {
    const html = renderDetail('{"body":null}');
    expect(html).toContain('No description.');
    expect(html).not.toContain('# Derived archive document');
    expect(html).toContain('No comments recorded');
    expect(html).toContain('No timeline events recorded');
  });

  it('exposes unreadable archived collections rather than silently showing empty coverage', () => {
    const html = renderDetail(
      '{"body":"Readable description"}',
      'broken',
      'null',
    );
    expect(html).toContain('Comments could not be read');
    expect(html).toContain('Timeline could not be read');
    expect(html).toContain('Original JSON');
  });
});
