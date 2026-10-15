import { useState } from 'react';
import { createFileRoute } from '@tanstack/react-router';
import {
  Button,
  Container,
  Group,
  SimpleGrid,
  Stack,
  Text,
  Title,
} from '@mantine/core';
import { modals } from '@mantine/modals';
import {
  useMutation,
  useQueryClient,
  useSuspenseQuery,
} from '@tanstack/react-query';
import { ConnectError } from '@connectrpc/connect';
import { toast } from 'sonner';
import { IconPlus } from '@tabler/icons-react';
import type { Source } from '~rpc/gitplus/config/v1/config_pb';
import { configClient } from '~lib/connect/client';
import { configQueryOptions } from '~lib/config-queries';
import { sourcePrimaryLabel, sourceSecondaryLabel } from '~lib/source-display';
import { SourceCard } from '~components/sources/SourceCard';
import { SourceFormDrawer } from '~components/sources/SourceFormDrawer';
import { ReplaceTokenModal } from '~components/sources/ReplaceTokenModal';

export const Route = createFileRoute('/_dashboard/config/sources')({
  loader: ({ context: { queryClient } }) =>
    queryClient.ensureQueryData(configQueryOptions),
  component: SourcesPage,
});

function getErrorMessage(error: unknown): string {
  if (error instanceof ConnectError) {
    return error.message;
  }
  return 'An unexpected error occurred';
}

function SourcesPage() {
  const { data } = useSuspenseQuery(configQueryOptions);
  const queryClient = useQueryClient();

  const [createOpened, setCreateOpened] = useState(false);
  const [editingSource, setEditingSource] = useState<Source | null>(null);
  const [tokenSource, setTokenSource] = useState<Source | null>(null);

  const sources = data.config?.sources ?? [];

  const invalidateConfig = () =>
    queryClient.invalidateQueries({ queryKey: ['config'] });

  const createMutation = useMutation({
    mutationFn: (input: {
      source: {
        name: string;
        platform: number;
        username: string;
        tokenPlaintext: string;
        onlyIncludeRepos: Array<string>;
        excludeRepos: Array<string>;
        includeDefaults: boolean;
        includeStarred: boolean;
        includeWatching: boolean;
        issuesEnabled: boolean;
      };
    }) => configClient.createSource(input),
    onSuccess: () => {
      invalidateConfig();
      setCreateOpened(false);
      toast.success('Source created');
    },
    onError: (error) => toast.error(getErrorMessage(error)),
  });

  const updateMutation = useMutation({
    mutationFn: (input: {
      sourceId: string;
      patch: {
        name: string;
        platform: number;
        username: string;
        onlyIncludeRepos: { values: Array<string> };
        excludeRepos: { values: Array<string> };
        includeDefaults: boolean;
        includeStarred: boolean;
        includeWatching: boolean;
        issuesEnabled: boolean;
      };
    }) => configClient.updateSource(input),
    onSuccess: () => {
      invalidateConfig();
      setEditingSource(null);
      toast.success('Source updated');
    },
    onError: (error) => toast.error(getErrorMessage(error)),
  });

  const replaceTokenMutation = useMutation({
    mutationFn: (input: { sourceId: string; tokenPlaintext: string }) =>
      configClient.replaceSourceToken(input),
    onSuccess: () => {
      invalidateConfig();
      setTokenSource(null);
      toast.success('Token replaced');
    },
    onError: (error) => toast.error(getErrorMessage(error)),
  });

  const deleteMutation = useMutation({
    mutationFn: (input: { sourceId: string }) =>
      configClient.deleteSource(input),
    onSuccess: () => {
      invalidateConfig();
      toast.success('Source deleted');
    },
    onError: (error) => toast.error(getErrorMessage(error)),
  });

  const handleDelete = (source: Source) => {
    const secondaryLabel = sourceSecondaryLabel(source);

    modals.openConfirmModal({
      title: 'Delete Source',
      children: (
        <Text size="sm">
          Are you sure you want to delete{' '}
          <Text component="span" fw={600}>
            {sourcePrimaryLabel(source)}
          </Text>
          {secondaryLabel ? ` (${secondaryLabel})` : ''}? This action cannot be
          undone.
        </Text>
      ),
      labels: { confirm: 'Delete', cancel: 'Cancel' },
      confirmProps: { color: 'red' },
      onConfirm: () => deleteMutation.mutate({ sourceId: source.id }),
    });
  };

  return (
    <Container fluid py="xl" px="xl">
      <Group justify="space-between" align="flex-start" mb="xl">
        <div>
          <Title order={2}>Sources</Title>
          <Text c="dimmed">Manage your Git platform connections</Text>
        </div>
        <Button
          leftSection={<IconPlus size={16} />}
          onClick={() => setCreateOpened(true)}
        >
          Add Source
        </Button>
      </Group>

      {sources.length === 0 ? (
        <EmptyState onAdd={() => setCreateOpened(true)} />
      ) : (
        <SimpleGrid cols={{ base: 1, md: 2 }}>
          {sources.map((source) => (
            <SourceCard
              key={source.id}
              source={source}
              onEdit={() => setEditingSource(source)}
              onReplaceToken={() => setTokenSource(source)}
              onDelete={() => handleDelete(source)}
            />
          ))}
        </SimpleGrid>
      )}

      <SourceFormDrawer
        mode="create"
        opened={createOpened}
        onClose={() => setCreateOpened(false)}
        onSubmit={(formData) =>
          createMutation.mutate(
            formData as Parameters<typeof createMutation.mutate>[0],
          )
        }
        loading={createMutation.isPending}
      />

      <SourceFormDrawer
        mode="edit"
        source={editingSource}
        opened={!!editingSource}
        onClose={() => setEditingSource(null)}
        onSubmit={(formData) =>
          updateMutation.mutate(
            formData as Parameters<typeof updateMutation.mutate>[0],
          )
        }
        loading={updateMutation.isPending}
      />

      <ReplaceTokenModal
        source={tokenSource}
        opened={!!tokenSource}
        onClose={() => setTokenSource(null)}
        onSubmit={(formData) => replaceTokenMutation.mutate(formData)}
        loading={replaceTokenMutation.isPending}
      />
    </Container>
  );
}

function EmptyState({ onAdd }: { onAdd: () => void }) {
  return (
    <Stack gap="md" maw={400} mt="xl">
      <div>
        <Text fw={600} size="lg">
          No sources yet
        </Text>
        <Text c="dimmed" size="sm" mt="xs" lh={1.6}>
          Sources connect Git Plus to platforms like GitHub. Each source uses a
          personal access token to sync your repositories.
        </Text>
      </div>
      <Group>
        <Button leftSection={<IconPlus size={16} />} onClick={onAdd}>
          Add Source
        </Button>
      </Group>
    </Stack>
  );
}
