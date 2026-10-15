import {
  ActionIcon,
  Badge,
  Card,
  Group,
  Menu,
  Stack,
  Text,
} from '@mantine/core';
import {
  IconBrandGithub,
  IconDotsVertical,
  IconKey,
  IconPencil,
  IconTrash,
} from '@tabler/icons-react';
import type { Source } from '~rpc/gitplus/config/v1/config_pb';
import { sourcePrimaryLabel, sourceSecondaryLabel } from '~lib/source-display';

interface SourceCardProps {
  source: Source;
  onEdit: () => void;
  onReplaceToken: () => void;
  onDelete: () => void;
}

export function SourceCard({
  source,
  onEdit,
  onReplaceToken,
  onDelete,
}: SourceCardProps) {
  const scopeBadges = getScopeBadges(source);
  const repoSummary = getRepoSummary(source);
  const secondaryLabel = sourceSecondaryLabel(source);

  return (
    <Card withBorder radius="md" padding="md">
      <Group justify="space-between" wrap="nowrap" mb="sm">
        <Group gap="xs" wrap="nowrap" style={{ minWidth: 0 }}>
          <IconBrandGithub size={18} style={{ flexShrink: 0 }} />
          <Stack gap={0} style={{ minWidth: 0 }}>
            <Text fw={600} size="sm" truncate="end">
              {sourcePrimaryLabel(source)}
            </Text>
            {secondaryLabel && (
              <Text size="xs" c="dimmed" truncate="end">
                {secondaryLabel}
              </Text>
            )}
          </Stack>
        </Group>

        <Menu position="bottom-end" withArrow shadow="md">
          <Menu.Target>
            <ActionIcon variant="subtle" color="gray" size="sm">
              <IconDotsVertical size={16} />
            </ActionIcon>
          </Menu.Target>
          <Menu.Dropdown>
            <Menu.Item leftSection={<IconPencil size={14} />} onClick={onEdit}>
              Edit
            </Menu.Item>
            <Menu.Item
              leftSection={<IconKey size={14} />}
              onClick={onReplaceToken}
            >
              Replace Token
            </Menu.Item>
            <Menu.Divider />
            <Menu.Item
              leftSection={<IconTrash size={14} />}
              color="red"
              onClick={onDelete}
            >
              Delete
            </Menu.Item>
          </Menu.Dropdown>
        </Menu>
      </Group>

      <Stack gap={6}>
        <Text size="sm" c="dimmed">
          @{source.username}
        </Text>

        <Group gap={6} wrap="wrap">
          <Text size="xs" c="dimmed">
            {repoSummary.label}
          </Text>
          {scopeBadges.map((badge) => (
            <Badge
              key={badge.label}
              size="xs"
              variant="light"
              color={badge.color}
              fw={500}
            >
              {badge.label}
            </Badge>
          ))}
          <Badge
            size="xs"
            variant="light"
            color={source.issuesEnabled ? 'teal' : 'gray'}
            fw={500}
          >
            Issues {source.issuesEnabled ? 'enabled' : 'off'}
          </Badge>
          {source.onlyIncludeRepos.slice(0, 2).map((repo) => (
            <Badge
              key={`include-${repo}`}
              size="xs"
              variant="outline"
              color="gray"
              fw={400}
            >
              + {repo}
            </Badge>
          ))}
          {source.excludeRepos.slice(0, 2).map((repo) => (
            <Badge
              key={`exclude-${repo}`}
              size="xs"
              variant="outline"
              color="gray"
              fw={400}
            >
              - {repo}
            </Badge>
          ))}
          {repoSummary.extra > 0 && (
            <Text size="xs" c="dimmed">
              +{repoSummary.extra}
            </Text>
          )}
        </Group>
      </Stack>
    </Card>
  );
}

function getRepoSummary(source: Source) {
  const scopeCount =
    (source.includeDefaults ? 1 : 0) +
    (source.includeStarred ? 1 : 0) +
    (source.includeWatching ? 1 : 0);
  const filterCount =
    source.onlyIncludeRepos.length + source.excludeRepos.length;

  if (scopeCount == 0 && filterCount == 0) {
    return { label: 'No repository groups enabled', extra: 0 };
  }

  return {
    label: `${scopeCount} group${scopeCount !== 1 ? 's' : ''} · ${filterCount} filter${filterCount !== 1 ? 's' : ''}`,
    extra:
      Math.max(0, source.onlyIncludeRepos.length - 2) +
      Math.max(0, source.excludeRepos.length - 2),
  };
}

function getScopeBadges(source: Source) {
  const badges: Array<{ label: string; color: string }> = [];

  if (source.includeDefaults) {
    badges.push({ label: 'Default access', color: 'blue' });
  }
  if (source.includeStarred) {
    badges.push({ label: 'Starred', color: 'yellow' });
  }
  if (source.includeWatching) {
    badges.push({ label: 'Watching', color: 'grape' });
  }

  return badges;
}
