import { create } from '@bufbuild/protobuf';
import { MantineProvider } from '@mantine/core';
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { SourceFormDrawer } from './SourceFormDrawer';
import { Platform, SourceSchema } from '~rpc/gitplus/config/v1/config_pb';

afterEach(cleanup);

describe('source issue backup opt-in', () => {
  it('starts disabled on new sources and submits explicit opt-in', async () => {
    const onSubmit = vi.fn();
    render(
      <MantineProvider>
        <SourceFormDrawer
          mode="create"
          opened
          onClose={() => {}}
          onSubmit={onSubmit}
        />
      </MantineProvider>,
    );
    fireEvent.click(screen.getByRole('button', { name: 'GitHub' }));
    const toggle = screen.getByRole('switch', {
      name: /^Back up GitHub Issues/,
    });
    expect(toggle).not.toBeChecked();
    fireEvent.change(screen.getByLabelText('Username', { exact: false }), {
      target: { value: 'octocat' },
    });
    fireEvent.change(screen.getByLabelText('Token', { exact: false }), {
      target: { value: 'test-token' },
    });
    fireEvent.click(toggle);
    fireEvent.click(screen.getByRole('button', { name: 'Create Source' }));
    await waitFor(() =>
      expect(onSubmit).toHaveBeenCalledWith(
        expect.objectContaining({
          source: expect.objectContaining({ issuesEnabled: true }),
        }),
      ),
    );
  });

  it.each([true, false])(
    'preserves existing opt-in %s when editing unrelated fields',
    async (enabled) => {
      const onSubmit = vi.fn();
      const source = create(SourceSchema, {
        id: 'source-1',
        platform: Platform.GITHUB,
        name: 'My source',
        username: 'octocat',
        includeDefaults: true,
        issuesEnabled: enabled,
      });
      render(
        <MantineProvider>
          <SourceFormDrawer
            mode="edit"
            source={source}
            opened
            onClose={() => {}}
            onSubmit={onSubmit}
          />
        </MantineProvider>,
      );
      const toggle = await screen.findByRole('switch', {
        name: /^Back up GitHub Issues/,
      });
      expect((toggle as HTMLInputElement).checked).toBe(enabled);
      fireEvent.change(screen.getByLabelText('Name'), {
        target: { value: 'Renamed source' },
      });
      fireEvent.click(screen.getByRole('button', { name: 'Save Changes' }));
      await waitFor(() =>
        expect(onSubmit).toHaveBeenCalledWith(
          expect.objectContaining({
            sourceId: source.id,
            patch: expect.objectContaining({
              name: 'Renamed source',
              issuesEnabled: enabled,
            }),
          }),
        ),
      );
    },
  );

  it('can explicitly disable an enabled backup', async () => {
    const onSubmit = vi.fn();
    const source = create(SourceSchema, {
      id: 'source-1',
      platform: Platform.GITHUB,
      username: 'octocat',
      issuesEnabled: true,
    });
    render(
      <MantineProvider>
        <SourceFormDrawer
          mode="edit"
          source={source}
          opened
          onClose={() => {}}
          onSubmit={onSubmit}
        />
      </MantineProvider>,
    );
    fireEvent.click(
      await screen.findByRole('switch', { name: /^Back up GitHub Issues/ }),
    );
    fireEvent.click(screen.getByRole('button', { name: 'Save Changes' }));
    await waitFor(() =>
      expect(onSubmit).toHaveBeenCalledWith(
        expect.objectContaining({
          patch: expect.objectContaining({ issuesEnabled: false }),
        }),
      ),
    );
  });
});
