import dayjs from 'dayjs';

export function issueTime(value: string) {
  return value && dayjs(value).isValid()
    ? dayjs(value).format('YYYY-MM-DD HH:mm')
    : 'Not recorded';
}

export function issueStateLabel(value: string) {
  const words = value.replace(/[_-]/g, ' ').trim();
  return words ? words.charAt(0).toUpperCase() + words.slice(1) : 'Not synced';
}

export function jsonObject(
  value: unknown,
): Record<string, unknown> | undefined {
  return value !== null && typeof value === 'object' && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : undefined;
}

export function jsonString(value: unknown): string {
  return typeof value === 'string' ? value : '';
}

export function parseIssueObject(raw: string) {
  try {
    return jsonObject(JSON.parse(raw));
  } catch {
    return undefined;
  }
}

export function parseIssueRecords(
  raw: string,
): Array<Record<string, unknown>> | null {
  try {
    const value: unknown = JSON.parse(raw);
    if (!Array.isArray(value)) return null;
    return value.map(jsonObject).filter((item) => item !== undefined);
  } catch {
    return null;
  }
}

export function recordAuthor(record: Record<string, unknown>) {
  const user = jsonObject(record.user) ?? jsonObject(record.actor);
  return jsonString(user?.login) || 'Unknown author';
}
