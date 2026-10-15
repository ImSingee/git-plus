import { describe, expect, it } from 'vitest';
import {
  issueStateLabel,
  issueTime,
  jsonString,
  parseIssueObject,
  parseIssueRecords,
  recordAuthor,
} from './issue-display';

describe('archived issue display', () => {
  it('handles missing and malformed API records without throwing', () => {
    expect(parseIssueObject('broken')).toBeUndefined();
    expect(parseIssueObject('[]')).toBeUndefined();
    expect(parseIssueRecords('null')).toBeNull();
    expect(parseIssueRecords('broken')).toBeNull();
    expect(parseIssueRecords('[null,1,{"body":"hello"}]')).toEqual([
      { body: 'hello' },
    ]);
    expect(jsonString({ body: 'not a string' })).toBe('');
  });

  it('preserves original text and JSON including unsafe-looking content', () => {
    const body =
      '<script>alert(1)</script> ![image](https://example.test/image.png)';
    expect(parseIssueObject(JSON.stringify({ body }))?.body).toBe(body);
  });

  it('formats archive states, dates, and author fallbacks', () => {
    expect(issueTime('')).toBe('Not recorded');
    expect(issueTime('invalid')).toBe('Not recorded');
    expect(issueStateLabel('not_available')).toBe('Not available');
    expect(issueStateLabel('')).toBe('Not synced');
    expect(recordAuthor({ user: { login: 'octocat' } })).toBe('octocat');
    expect(recordAuthor({ actor: { login: 'maintainer' } })).toBe('maintainer');
    expect(recordAuthor({ user: null })).toBe('Unknown author');
  });
});
