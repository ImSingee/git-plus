import { useEffect, useState } from 'react';
import {
  ActionIcon,
  Anchor,
  Box,
  Button,
  Collapse,
  Drawer,
  Group,
  PasswordInput,
  Stack,
  Switch,
  Text,
  TextInput,
  UnstyledButton,
} from '@mantine/core';
import { useForm } from '@mantine/form';
import {
  IconBrandGithub,
  IconChevronDown,
  IconChevronRight,
  IconInfoCircle,
  IconPlus,
  IconX,
} from '@tabler/icons-react';
import type { Source } from '~rpc/gitplus/config/v1/config_pb';
import { Platform } from '~rpc/gitplus/config/v1/config_pb';
import { sourceSecondaryLabel } from '~lib/source-display';

interface CreateData {
  source: {
    name: string;
    platform: Platform;
    username: string;
    tokenPlaintext: string;
    onlyIncludeRepos: Array<string>;
    excludeRepos: Array<string>;
    includeDefaults: boolean;
    includeStarred: boolean;
    includeWatching: boolean;
    issuesEnabled: boolean;
  };
}

interface UpdateData {
  sourceId: string;
  patch: {
    name: string;
    platform: Platform;
    username: string;
    onlyIncludeRepos: { values: Array<string> };
    excludeRepos: { values: Array<string> };
    includeDefaults: boolean;
    includeStarred: boolean;
    includeWatching: boolean;
    issuesEnabled: boolean;
  };
}

interface SourceFormDrawerProps {
  mode: 'create' | 'edit';
  source?: Source | null;
  opened: boolean;
  onClose: () => void;
  onSubmit: (data: CreateData | UpdateData) => void;
  loading?: boolean;
}

interface FormValues {
  name: string;
  username: string;
  tokenPlaintext: string;
  onlyIncludeRepos: Array<string>;
  excludeRepos: Array<string>;
  includeDefaults: boolean;
  includeStarred: boolean;
  includeWatching: boolean;
  issuesEnabled: boolean;
}

export function SourceFormDrawer({
  mode,
  source,
  opened,
  onClose,
  onSubmit,
  loading,
}: SourceFormDrawerProps) {
  const [platform, setPlatform] = useState<Platform | null>(null);
  const [advancedOpen, setAdvancedOpen] = useState(false);

  const form = useForm<FormValues>({
    initialValues: {
      name: '',
      username: '',
      tokenPlaintext: '',
      onlyIncludeRepos: [],
      excludeRepos: [],
      includeDefaults: true,
      includeStarred: false,
      includeWatching: false,
      issuesEnabled: false,
    },
    validate: {
      username: (value) => (value.trim() ? null : 'Username is required'),
      tokenPlaintext: (value) =>
        mode === 'create' && !value.trim() ? 'Token is required' : null,
    },
  });

  useEffect(() => {
    if (!opened) return;
    if (mode === 'edit' && source) {
      setPlatform(source.platform);
      form.setValues({
        name: sourceSecondaryLabel(source) == null ? '' : source.name,
        username: source.username,
        tokenPlaintext: '',
        onlyIncludeRepos: [...source.onlyIncludeRepos],
        excludeRepos: [...source.excludeRepos],
        includeDefaults: source.includeDefaults,
        includeStarred: source.includeStarred,
        includeWatching: source.includeWatching,
        issuesEnabled: source.issuesEnabled,
      });
      form.resetDirty();
      setAdvancedOpen(
        source.onlyIncludeRepos.length > 0 ||
          source.excludeRepos.length > 0 ||
          !source.includeDefaults ||
          source.includeStarred ||
          source.includeWatching,
      );
    } else {
      setPlatform(null);
      setAdvancedOpen(false);
      form.setInitialValues({
        name: '',
        username: '',
        tokenPlaintext: '',
        onlyIncludeRepos: [],
        excludeRepos: [],
        includeDefaults: true,
        includeStarred: false,
        includeWatching: false,
        issuesEnabled: false,
      });
      form.reset();
    }
  }, [opened, mode, source]);

  const handlePlatformSelect = (p: Platform) => {
    setPlatform(p);
  };

  const handleSubmit = (values: FormValues) => {
    if (platform == null) return;
    if (mode === 'create') {
      onSubmit({
        source: {
          name: values.name.trim(),
          platform,
          username: values.username.trim(),
          tokenPlaintext: values.tokenPlaintext.trim(),
          onlyIncludeRepos: values.onlyIncludeRepos,
          excludeRepos: values.excludeRepos,
          includeDefaults: values.includeDefaults,
          includeStarred: values.includeStarred,
          includeWatching: values.includeWatching,
          issuesEnabled: values.issuesEnabled,
        },
      });
    } else {
      onSubmit({
        sourceId: source!.id,
        patch: {
          name: values.name.trim(),
          platform,
          username: values.username.trim(),
          onlyIncludeRepos: { values: values.onlyIncludeRepos },
          excludeRepos: { values: values.excludeRepos },
          includeDefaults: values.includeDefaults,
          includeStarred: values.includeStarred,
          includeWatching: values.includeWatching,
          issuesEnabled: values.issuesEnabled,
        },
      });
    }
  };

  const platformSelected = platform != null;

  return (
    <Drawer
      opened={opened}
      onClose={onClose}
      position="right"
      size={720}
      title={mode === 'create' ? 'Add Source' : 'Edit Source'}
    >
      <form onSubmit={form.onSubmit(handleSubmit)}>
        <Stack gap="lg">
          {/* Tier 1: Platform */}
          <Box>
            <Text size="xs" fw={500} c="dimmed" tt="uppercase" mb="xs">
              Platform
            </Text>
            <Group gap="sm">
              <UnstyledButton
                onClick={() => handlePlatformSelect(Platform.GITHUB)}
                disabled={mode === 'edit'}
                style={{
                  border: `1.5px solid var(--mantine-color-${platform === Platform.GITHUB ? 'blue-5' : 'default-border'})`,
                  borderRadius: 'var(--mantine-radius-md)',
                  padding: '10px 16px',
                  backgroundColor:
                    platform === Platform.GITHUB
                      ? 'var(--mantine-color-blue-light)'
                      : 'transparent',
                  cursor: mode === 'edit' ? 'default' : undefined,
                }}
              >
                <Group gap="xs">
                  <IconBrandGithub size={18} />
                  <Text size="sm" fw={500}>
                    GitHub
                  </Text>
                </Group>
              </UnstyledButton>
            </Group>
          </Box>

          {/* Tier 2: Required fields */}
          {platformSelected && (
            <Box>
              <Stack gap="sm">
                <TextInput
                  label="Name"
                  description="(Optional) display name or remark for this source"
                  placeholder="My GitHub"
                  {...form.getInputProps('name')}
                />
                <TextInput
                  label="Username"
                  description="Your username on this platform"
                  placeholder="octocat"
                  required
                  {...form.getInputProps('username')}
                />
                {mode === 'create' ? (
                  <PasswordInput
                    label="Personal Access Token (classic)"
                    description={
                      <>
                        Create one at{' '}
                        <Anchor
                          href="https://github.com/settings/tokens"
                          target="_blank"
                          style={{ fontSize: 'inherit', lineHeight: 'inherit' }}
                        >
                          https://github.com/settings/tokens
                        </Anchor>
                        .
                      </>
                    }
                    placeholder="ghp_xxxxxxxxxxxx"
                    required
                    {...form.getInputProps('tokenPlaintext')}
                  />
                ) : (
                  <Group gap="xs" c="dimmed">
                    <IconInfoCircle size={14} />
                    <Text size="sm">
                      Use &quot;Replace Token&quot; from the source menu to
                      update the token.
                    </Text>
                  </Group>
                )}
              </Stack>
            </Box>
          )}

          {platformSelected && (
            <Switch
              label="Back up GitHub Issues"
              description="Opt in to read-only backups of issues, comments, and timeline events for this source. Pull requests are excluded. Attachments are saved as links only."
              {...form.getInputProps('issuesEnabled', { type: 'checkbox' })}
            />
          )}

          {/* Tier 3: Advanced Options */}
          {platformSelected && (
            <Box>
              <UnstyledButton
                onClick={() => setAdvancedOpen((o) => !o)}
                style={{ borderRadius: 'var(--mantine-radius-sm)' }}
              >
                <Group gap={4}>
                  {advancedOpen ? (
                    <IconChevronDown
                      size={14}
                      color="var(--mantine-color-dimmed)"
                    />
                  ) : (
                    <IconChevronRight
                      size={14}
                      color="var(--mantine-color-dimmed)"
                    />
                  )}
                  <Text size="xs" fw={500} c="dimmed" tt="uppercase">
                    Advanced Options
                  </Text>
                </Group>
              </UnstyledButton>
              <Collapse expanded={advancedOpen}>
                <Stack gap="sm" mt="sm">
                  <Stack gap="xs">
                    <Switch
                      label="Include default accessible repositories"
                      description="Repositories you can access by default, including your own and organization repositories."
                      {...form.getInputProps('includeDefaults', {
                        type: 'checkbox',
                      })}
                    />
                    <Switch
                      label="Include starred repositories"
                      description="Repositories you have starred on GitHub."
                      {...form.getInputProps('includeStarred', {
                        type: 'checkbox',
                      })}
                    />
                    <Switch
                      label="Include watching repositories"
                      description="Repositories you are watching on GitHub."
                      {...form.getInputProps('includeWatching', {
                        type: 'checkbox',
                      })}
                    />
                  </Stack>
                  <StringListInput
                    label="Only Include Repos"
                    description="Supports wildcards, e.g. my-org/*, *-backup"
                    placeholder="repo-name or org/*"
                    value={form.values.onlyIncludeRepos}
                    onChange={(v) => form.setFieldValue('onlyIncludeRepos', v)}
                  />
                  <StringListInput
                    label="Exclude Repos"
                    description="Supports wildcards, e.g. my-org/*, *-backup"
                    placeholder="repo-name or org/*"
                    value={form.values.excludeRepos}
                    onChange={(v) => form.setFieldValue('excludeRepos', v)}
                  />
                </Stack>
              </Collapse>
            </Box>
          )}

          {/* Actions */}
          {platformSelected && (
            <Group justify="flex-end" mt="md">
              <Button variant="default" onClick={onClose}>
                Cancel
              </Button>
              <Button type="submit" loading={loading}>
                {mode === 'create' ? 'Create Source' : 'Save Changes'}
              </Button>
            </Group>
          )}
        </Stack>
      </form>
    </Drawer>
  );
}

/* ---- String list input with add/remove ---- */

function StringListInput({
  label,
  description,
  placeholder,
  value,
  onChange,
}: {
  label: string;
  description?: string;
  placeholder?: string;
  value: Array<string>;
  onChange: (value: Array<string>) => void;
}) {
  const [inputValue, setInputValue] = useState('');

  const addItem = () => {
    const trimmed = inputValue.trim();
    if (trimmed && !value.includes(trimmed)) {
      onChange([...value, trimmed]);
      setInputValue('');
    }
  };

  const removeItem = (index: number) => {
    onChange(value.filter((_, i) => i !== index));
  };

  return (
    <Box>
      <Text size="sm" fw={500} mb={2}>
        {label}
      </Text>
      {description && (
        <Text size="xs" c="dimmed" mb={4}>
          {description}
        </Text>
      )}
      <Group gap="xs" wrap="nowrap">
        <TextInput
          style={{ flex: 1 }}
          size="sm"
          value={inputValue}
          onChange={(e) => setInputValue(e.currentTarget.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter') {
              e.preventDefault();
              addItem();
            }
          }}
          placeholder={placeholder}
        />
        <ActionIcon
          variant="default"
          size="lg"
          onClick={addItem}
          disabled={!inputValue.trim()}
        >
          <IconPlus size={14} />
        </ActionIcon>
      </Group>
      {value.length > 0 && (
        <Stack gap={4} mt="xs">
          {value.map((item, i) => (
            <Group key={i} gap="xs" wrap="nowrap">
              <Text
                size="sm"
                ff="monospace"
                style={{ flex: 1, wordBreak: 'break-all' }}
              >
                {item}
              </Text>
              <ActionIcon
                variant="subtle"
                color="gray"
                size="sm"
                onClick={() => removeItem(i)}
              >
                <IconX size={12} />
              </ActionIcon>
            </Group>
          ))}
        </Stack>
      )}
    </Box>
  );
}
