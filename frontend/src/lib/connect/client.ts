import { createClient } from '@connectrpc/connect';
import { apiTransport } from './transport';
import { ConfigService } from '~rpc/gitplus/config/v1/config_pb';
import { CronService } from '~rpc/gitplus/cron/v1/cron_pb';
import { EventService } from '~rpc/gitplus/event/v1/event_pb';
import { IssueService } from '~rpc/gitplus/issue/v1/issue_pb';
import { RepoService } from '~rpc/gitplus/repo/v1/repo_pb';
import { TaskService } from '~rpc/gitplus/task/v1/task_pb';

export const configClient = createClient(ConfigService, apiTransport);
export const cronClient = createClient(CronService, apiTransport);
export const repoClient = createClient(RepoService, apiTransport);
export const taskClient = createClient(TaskService, apiTransport);
export const eventClient = createClient(EventService, apiTransport);
export const issueClient = createClient(IssueService, apiTransport);
