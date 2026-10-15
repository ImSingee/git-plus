package taskservice

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/rand/v2"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	connectvalidate "connectrpc.com/validate"
	appdb "github.com/ImSingee/git-plus/db"
	dbsqlc "github.com/ImSingee/git-plus/db/sqlc"
	appconfig "github.com/ImSingee/git-plus/pkg/config"
	"github.com/ImSingee/git-plus/pkg/issuetask"
	taskv1 "github.com/ImSingee/git-plus/pkg/rpc/gitplus/task/v1"
	"github.com/ImSingee/git-plus/pkg/rpc/gitplus/task/v1/taskv1connect"
	"github.com/ImSingee/git-plus/pkg/syncsource"
	"github.com/ImSingee/git-plus/pkg/task"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const defaultStubStepDelay = 75 * time.Millisecond
const defaultListTaskRunsPageSize = 20

type serviceServer struct {
	dataDir            string
	db                 *sql.DB
	manager            *task.Manager
	stepDelay          time.Duration
	progressTick       time.Duration
	sourceSyncDuration func() time.Duration
	sourceSyncExecutor SourceSyncExecutor
}

type Option func(*serviceServer)

type SourceSyncReporter = syncsource.ProgressReporter

type SourceSyncExecutor interface {
	Sync(ctx context.Context, request syncsource.SyncRequest, reporter SourceSyncReporter) error
}

func NewHandler(dataDir string, manager *task.Manager, options ...Option) http.Handler {
	rpcMux := http.NewServeMux()
	RegisterHandlers(rpcMux, dataDir, manager, options...)
	return http.StripPrefix("/api", rpcMux)
}

func RegisterHandlers(mux *http.ServeMux, dataDir string, manager *task.Manager, options ...Option) {
	path, handler := taskv1connect.NewTaskServiceHandler(
		newServiceServer(dataDir, manager, options...),
		connect.WithInterceptors(mustValidateInterceptor()),
	)
	mux.Handle(path, handler)
}

func newServiceServer(dataDir string, manager *task.Manager, options ...Option) *serviceServer {
	server := &serviceServer{
		dataDir:            dataDir,
		manager:            manager,
		stepDelay:          defaultStubStepDelay,
		progressTick:       time.Second,
		sourceSyncDuration: randomSourceSyncDuration,
	}
	for _, option := range options {
		option(server)
	}

	if server.sourceSyncExecutor == nil {
		executorOptions := make([]syncsource.Option, 0, 1)
		if server.db != nil {
			executorOptions = append(executorOptions, syncsource.WithDatabase(server.db))
		}
		server.sourceSyncExecutor = syncsource.NewExecutor(dataDir, executorOptions...)
	}

	return server
}

func WithDatabase(db *sql.DB) Option {
	return func(server *serviceServer) {
		if db != nil {
			server.db = db
		}
	}
}

func WithSourceSyncDuration(fn func() time.Duration) Option {
	return func(server *serviceServer) {
		if fn != nil {
			server.sourceSyncDuration = fn
		}
	}
}

func WithProgressTick(interval time.Duration) Option {
	return func(server *serviceServer) {
		if interval > 0 {
			server.progressTick = interval
		}
	}
}

func WithSourceSyncExecutor(executor SourceSyncExecutor) Option {
	return func(server *serviceServer) {
		if executor != nil {
			server.sourceSyncExecutor = executor
		}
	}
}

func (s *serviceServer) ListTaskRuns(
	ctx context.Context,
	req *connect.Request[taskv1.ListTaskRunsRequest],
) (*connect.Response[taskv1.ListTaskRunsResponse], error) {
	pageSize := int(req.Msg.GetPageSize())
	if pageSize <= 0 {
		pageSize = defaultListTaskRunsPageSize
	}

	offset, err := decodePageToken(req.Msg.GetPageToken())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid page_token: %w", err))
	}

	jobType := optionalFilter(req.Msg.GetJobType())
	parentTaskID := optionalFilter(req.Msg.GetParentTaskId())

	queries, cleanup, err := s.openTaskQueries(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	defer cleanup()

	totalCount, err := queries.CountTaskRuns(ctx, dbsqlc.CountTaskRunsParams{
		Column1: jobType,
		Column2: parentTaskID,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("count task runs: %w", err))
	}

	taskRuns, err := queries.ListTaskRunsPaginated(ctx, dbsqlc.ListTaskRunsPaginatedParams{
		Column1: jobType,
		Column2: parentTaskID,
		Limit:   int64(pageSize),
		Offset:  int64(offset),
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("list task runs: %w", err))
	}

	protoTaskRuns := make([]*taskv1.Task, 0, len(taskRuns))
	for _, taskRun := range taskRuns {
		protoTaskRuns = append(protoTaskRuns, toProtoTaskRun(taskRun))
	}

	response := &taskv1.ListTaskRunsResponse{
		TaskRuns:   protoTaskRuns,
		TotalCount: int32Ptr(int32(totalCount)),
	}
	if int64(offset)+int64(len(taskRuns)) < totalCount {
		response.NextPageToken = stringPtr(encodePageToken(offset + len(taskRuns)))
	}

	return connect.NewResponse(response), nil
}

func (s *serviceServer) GetTaskRun(
	ctx context.Context,
	req *connect.Request[taskv1.GetTaskRunRequest],
) (*connect.Response[taskv1.GetTaskRunResponse], error) {
	queries, cleanup, err := s.openTaskQueries(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	defer cleanup()

	taskRun, err := queries.GetTaskRun(ctx, strings.TrimSpace(req.Msg.GetTaskId()))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("task run %q was not found", req.Msg.GetTaskId()))
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("get task run: %w", err))
	}

	return connect.NewResponse(&taskv1.GetTaskRunResponse{
		TaskRun: toProtoTaskRun(taskRun),
	}), nil
}

func (s *serviceServer) ListTaskRunLogs(
	ctx context.Context,
	req *connect.Request[taskv1.ListTaskRunLogsRequest],
) (*connect.Response[taskv1.ListTaskRunLogsResponse], error) {
	taskID := strings.TrimSpace(req.Msg.GetTaskId())

	queries, cleanup, err := s.openTaskQueries(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	defer cleanup()

	if _, err := queries.GetTaskRun(ctx, taskID); errors.Is(err, sql.ErrNoRows) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("task run %q was not found", taskID))
	} else if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("get task run: %w", err))
	}

	logs, err := queries.ListTaskRunLogs(ctx, taskID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("list task run logs: %w", err))
	}

	protoLogs := make([]*taskv1.TaskRunLog, 0, len(logs))
	for _, taskRunLog := range logs {
		protoLogs = append(protoLogs, toProtoTaskRunLog(taskRunLog))
	}

	return connect.NewResponse(&taskv1.ListTaskRunLogsResponse{
		Logs: protoLogs,
	}), nil
}

func (s *serviceServer) GetTaskRuntime(
	_ context.Context,
	_ *connect.Request[taskv1.GetTaskRuntimeRequest],
) (*connect.Response[taskv1.GetTaskRuntimeResponse], error) {
	runtime := s.manager.Runtime()
	queuedTasks := make([]*taskv1.Task, 0, len(runtime.Queued))
	for _, queuedTask := range runtime.Queued {
		queuedTasks = append(queuedTasks, toProtoTask(queuedTask))
	}

	var runningTask *taskv1.Task
	if runtime.Running != nil {
		runningTask = toProtoTask(*runtime.Running)
	}

	return connect.NewResponse(&taskv1.GetTaskRuntimeResponse{
		RunningTask: runningTask,
		QueuedTasks: queuedTasks,
	}), nil
}

func (s *serviceServer) EnqueueFullSync(
	_ context.Context,
	_ *connect.Request[taskv1.EnqueueFullSyncRequest],
) (*connect.Response[taskv1.EnqueueFullSyncResponse], error) {
	result, snapshot, err := s.manager.Enqueue(task.Spec{
		JobID:   task.JobIDSyncAll,
		JobType: task.JobTypeSyncAll,
		Name:    "Sync all sources",
		Args:    nil,
		Run:     s.syncAllRunner(),
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("enqueue full sync: %w", err))
	}

	return connect.NewResponse(&taskv1.EnqueueFullSyncResponse{
		Result: enqueueResultPtr(toProtoEnqueueResult(result)),
		Task:   toProtoTask(snapshot),
	}), nil
}

func (s *serviceServer) EnqueueSourceSync(
	_ context.Context,
	req *connect.Request[taskv1.EnqueueSourceSyncRequest],
) (*connect.Response[taskv1.EnqueueSourceSyncResponse], error) {
	sourceID := strings.TrimSpace(req.Msg.GetSourceId())
	source, err := s.loadSourceByID(sourceID)
	if err != nil {
		return nil, err
	}

	result, snapshot, err := s.manager.Enqueue(s.sourceSyncSpec(source, ""))
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("enqueue source sync: %w", err))
	}

	return connect.NewResponse(&taskv1.EnqueueSourceSyncResponse{
		Result: enqueueResultPtr(toProtoEnqueueResult(result)),
		Task:   toProtoTask(snapshot),
	}), nil
}

func NewSyncAllRun(dataDir string, db *sql.DB, manager *task.Manager, logger *log.Logger) func(*task.ExecutionContext) error {
	server := newServiceServer(dataDir, manager, WithDatabase(db))

	return server.syncAllRunnerWithLogger(logger)
}

func (s *serviceServer) CancelQueuedTask(
	_ context.Context,
	req *connect.Request[taskv1.CancelQueuedTaskRequest],
) (*connect.Response[taskv1.CancelQueuedTaskResponse], error) {
	taskID := strings.TrimSpace(req.Msg.GetTaskId())

	snapshot, err := s.manager.CancelQueuedTask(taskID)
	switch {
	case errors.Is(err, task.ErrTaskNotFound):
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("task %q was not found", taskID))
	case errors.Is(err, task.ErrTaskNotQueued):
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("task %q is not queued", taskID))
	case err != nil:
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("cancel queued task: %w", err))
	default:
		return connect.NewResponse(&taskv1.CancelQueuedTaskResponse{
			Task: toProtoTask(snapshot),
		}), nil
	}
}

func (s *serviceServer) loadSourceByID(sourceID string) (appconfig.SourceConfig, error) {
	loaded, _, err := appconfig.LoadOrDefault(appconfig.PathForDataDir(s.dataDir))
	if err != nil {
		return appconfig.SourceConfig{}, connect.NewError(connect.CodeInternal, fmt.Errorf("load config: %w", err))
	}

	for _, source := range loaded.Data.Sources {
		if source.ID == sourceID {
			return source, nil
		}
	}

	return appconfig.SourceConfig{}, connect.NewError(connect.CodeNotFound, fmt.Errorf("source %q was not found", sourceID))
}

func (s *serviceServer) sourceSyncSpec(source appconfig.SourceConfig, parentTaskID string) task.Spec {
	return task.Spec{
		ParentTaskID: parentTaskID,
		JobID:        task.BuildSourceSyncJobID(source.ID),
		JobType:      task.JobTypeSyncSource,
		Name:         fmt.Sprintf("Sync source %s", appconfig.EffectiveSourceName(source)),
		Args: map[string]any{
			"source_id": source.ID,
		},
		Run: s.sourceSyncRunner(source.ID),
	}
}

func (s *serviceServer) syncAllRunner() func(*task.ExecutionContext) error {
	return s.syncAllRunnerWithLogger(log.Default())
}

func (s *serviceServer) syncAllRunnerWithLogger(logger *log.Logger) func(*task.ExecutionContext) error {
	if logger == nil {
		logger = log.Default()
	}

	return func(ctx *task.ExecutionContext) error {
		if err := ctx.SetProgress("Loading sources", map[string]any{
			"phase": "load_sources",
		}); err != nil {
			return err
		}

		loaded, _, err := appconfig.LoadOrDefault(appconfig.PathForDataDir(s.dataDir))
		if err != nil {
			if progressErr := ctx.SetProgress("Failed to load sources", map[string]any{
				"phase": "load_sources",
				"error": err.Error(),
			}); progressErr != nil {
				return progressErr
			}
			logger.Printf("sync-all load config failed: %v", err)
			return nil
		}

		total := len(loaded.Data.Sources)
		if total == 0 {
			if err := ctx.SetProgress("No source configured", map[string]any{
				"phase": "enqueue_sources",
				"total": 0,
			}); err != nil {
				return err
			}
			return nil
		}

		startedCount := 0
		queuedCount := 0
		dedupedCount := 0
		failedCount := 0

		for index, source := range loaded.Data.Sources {
			if err := ctx.SetProgress(fmt.Sprintf("Queueing source %s (%d/%d)", appconfig.EffectiveSourceName(source), index+1, total), map[string]any{
				"phase": "enqueue_sources",
				"index": index + 1,
				"total": total,
			}); err != nil {
				return err
			}

			result, _, enqueueErr := s.manager.Enqueue(s.sourceSyncSpec(source, ctx.TaskID()))
			if enqueueErr != nil {
				failedCount++
				logger.Printf("sync-all enqueue source %q failed: %v", source.ID, enqueueErr)
				continue
			}

			switch result {
			case task.EnqueueResultStarted:
				startedCount++
			case task.EnqueueResultQueued:
				queuedCount++
			case task.EnqueueResultDeduped:
				dedupedCount++
			}
		}

		if err := ctx.SetProgress("Queued source sync tasks", map[string]any{
			"phase":   "done",
			"total":   total,
			"started": startedCount,
			"queued":  queuedCount,
			"deduped": dedupedCount,
			"failed":  failedCount,
		}); err != nil {
			return err
		}

		return nil
	}
}

func (s *serviceServer) sourceSyncRunner(sourceID string) func(*task.ExecutionContext) error {
	return func(ctx *task.ExecutionContext) error {
		request, err := s.loadSourceSyncRequest(sourceID, ctx.TaskID())
		if err != nil {
			return err
		}

		if s.sourceSyncExecutor == nil {
			return fmt.Errorf("source sync executor is required")
		}

		syncErr := s.sourceSyncExecutor.Sync(context.Background(), request, ctx)
		if !request.Source.IssuesEnabled || s.db == nil {
			return syncErr
		}
		repos, err := dbsqlc.New(s.db).ListActiveReposForSource(context.Background(), sourceID)
		if err != nil {
			return errors.Join(syncErr, err)
		}
		runner := issuetask.New(s.dataDir, s.db, s.manager)
		for _, repo := range repos {
			if _, _, err := runner.Enqueue(context.Background(), repo.ID, false, ctx.TaskID()); err != nil {
				syncErr = errors.Join(syncErr, err)
			}
		}
		return syncErr
	}
}

func (s *serviceServer) loadSourceSyncRequest(sourceID string, runID string) (syncsource.SyncRequest, error) {
	loaded, err := appconfig.Load(appconfig.PathForDataDir(s.dataDir))
	if err != nil {
		return syncsource.SyncRequest{}, fmt.Errorf("load config: %w", err)
	}

	request := syncsource.SyncRequest{
		RunID:         runID,
		Concurrency:   loaded.Data.Concurrency,
		MaxRetryTimes: loaded.Data.MaxRetryTimes,
	}

	for _, source := range loaded.Data.Sources {
		if source.ID != sourceID {
			continue
		}

		plaintextToken, err := appconfig.DecryptToken(source.Token, os.Getenv(appconfig.TokenPassphraseEnvVar))
		if err != nil {
			return syncsource.SyncRequest{}, fmt.Errorf("decrypt source %q token: %w", sourceID, err)
		}

		resolvedSource := source
		resolvedSource.Token = plaintextToken
		request.Source = resolvedSource
		return request, nil
	}

	return syncsource.SyncRequest{}, fmt.Errorf("source %q was not found", sourceID)
}

func (s *serviceServer) openTaskQueries(ctx context.Context) (*dbsqlc.Queries, func(), error) {
	if s.db != nil {
		return dbsqlc.New(s.db), func() {}, nil
	}

	db, err := appdb.Open(ctx, s.dataDir)
	if err != nil {
		return nil, nil, fmt.Errorf("open sqlite database: %w", err)
	}

	return dbsqlc.New(db), func() {
		_ = db.Close()
	}, nil
}

func randomSourceSyncDuration() time.Duration {
	return time.Duration(rand.IntN(9)+2) * time.Second
}

func toProtoTask(snapshot task.Snapshot) *taskv1.Task {
	protoTask := &taskv1.Task{
		TaskId:    stringPtr(snapshot.TaskID),
		JobId:     stringPtr(snapshot.JobID),
		JobType:   stringPtr(snapshot.JobType),
		Name:      stringPtr(snapshot.Name),
		State:     taskStatePtr(toProtoTaskState(snapshot.State)),
		CreatedAt: timestamppb.New(snapshot.CreatedAt),
	}
	if snapshot.ParentTaskID != "" {
		protoTask.ParentTaskId = stringPtr(snapshot.ParentTaskID)
	}
	if args := toProtoStruct(snapshot.Args); args != nil {
		protoTask.Args = args
	}
	if snapshot.StartedAt != nil {
		protoTask.StartedAt = timestamppb.New(*snapshot.StartedAt)
	}
	if snapshot.Progress != nil {
		protoTask.Progress = toProtoProgress(*snapshot.Progress)
	}
	if snapshot.FinishedAt != nil {
		protoTask.FinishedAt = timestamppb.New(*snapshot.FinishedAt)
	}
	if snapshot.ErrorMessage != "" {
		protoTask.ErrorMessage = stringPtr(snapshot.ErrorMessage)
	}

	return protoTask
}

func toProtoProgress(progress task.Progress) *taskv1.TaskProgress {
	return &taskv1.TaskProgress{
		Summary:   stringPtr(progress.Summary),
		Meta:      toProtoStruct(progress.Meta),
		UpdatedAt: timestamppb.New(progress.UpdatedAt),
	}
}

func toProtoStruct(meta map[string]any) *structpb.Struct {
	if len(meta) == 0 {
		return nil
	}

	value, err := structpb.NewStruct(meta)
	if err != nil {
		log.Printf("task metadata conversion failed: %v", err)
		return nil
	}

	return value
}

func toProtoTaskState(state task.State) taskv1.TaskState {
	switch state {
	case task.StateQueued:
		return taskv1.TaskState_TASK_STATE_QUEUED
	case task.StateRunning:
		return taskv1.TaskState_TASK_STATE_RUNNING
	case task.StateFinished:
		return taskv1.TaskState_TASK_STATE_FINISHED
	case task.StateFailed:
		return taskv1.TaskState_TASK_STATE_FAILED
	default:
		return taskv1.TaskState_TASK_STATE_UNSPECIFIED
	}
}

func toProtoTaskRun(taskRun dbsqlc.TaskRun) *taskv1.Task {
	protoTask := &taskv1.Task{
		TaskId:    stringPtr(taskRun.TaskID),
		JobId:     stringPtr(taskRun.JobID),
		JobType:   stringPtr(taskRun.JobType),
		Name:      stringPtr(taskRun.Name),
		State:     taskStatePtr(toProtoTaskState(task.State(taskRun.Status))),
		CreatedAt: toProtoTimestamp(taskRun.CreatedAt),
		StartedAt: toProtoTimestamp(taskRun.StartedAt),
	}
	if taskRun.ParentTaskID.Valid {
		protoTask.ParentTaskId = stringPtr(taskRun.ParentTaskID.String)
	}
	if args := toProtoJSONStruct(taskRun.ArgsJson); args != nil {
		protoTask.Args = args
	}
	if progress := toProtoPersistedProgress(taskRun.LastProgressSummary, taskRun.LastProgressMetaJson, taskRun.UpdatedAt); progress != nil {
		protoTask.Progress = progress
	}
	if taskRun.FinishedAt.Valid {
		protoTask.FinishedAt = toProtoTimestamp(taskRun.FinishedAt.String)
	}
	if taskRun.ErrorMessage.Valid {
		protoTask.ErrorMessage = stringPtr(taskRun.ErrorMessage.String)
	}

	return protoTask
}

func toProtoTaskRunLog(taskRunLog dbsqlc.TaskRunLog) *taskv1.TaskRunLog {
	protoLog := &taskv1.TaskRunLog{
		Id:        int64Ptr(taskRunLog.ID),
		TaskId:    stringPtr(taskRunLog.TaskID),
		EventType: stringPtr(taskRunLog.EventType),
		CreatedAt: toProtoTimestamp(taskRunLog.CreatedAt),
	}
	if taskRunLog.Summary.Valid {
		protoLog.Summary = stringPtr(taskRunLog.Summary.String)
	}
	if meta := toProtoJSONStruct(taskRunLog.MetaJson); meta != nil {
		protoLog.Meta = meta
	}
	if taskRunLog.ErrorMessage.Valid {
		protoLog.ErrorMessage = stringPtr(taskRunLog.ErrorMessage.String)
	}

	return protoLog
}

func toProtoPersistedProgress(summary sql.NullString, meta sql.NullString, updatedAt string) *taskv1.TaskProgress {
	if !summary.Valid && !meta.Valid {
		return nil
	}

	progress := &taskv1.TaskProgress{
		UpdatedAt: toProtoTimestamp(updatedAt),
	}
	if summary.Valid {
		progress.Summary = stringPtr(summary.String)
	}
	if value := toProtoJSONStruct(meta); value != nil {
		progress.Meta = value
	}

	return progress
}

func toProtoJSONStruct(value sql.NullString) *structpb.Struct {
	if !value.Valid || strings.TrimSpace(value.String) == "" {
		return nil
	}

	var decoded map[string]any
	if err := json.Unmarshal([]byte(value.String), &decoded); err != nil {
		log.Printf("task metadata conversion failed: %v", err)
		return nil
	}

	return toProtoStruct(decoded)
}

func toProtoTimestamp(value string) *timestamppb.Timestamp {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		log.Printf("task timestamp conversion failed: %v", err)
		return nil
	}

	return timestamppb.New(parsed)
}

func toProtoEnqueueResult(result task.EnqueueResult) taskv1.TaskEnqueueResult {
	switch result {
	case task.EnqueueResultStarted:
		return taskv1.TaskEnqueueResult_TASK_ENQUEUE_RESULT_STARTED
	case task.EnqueueResultQueued:
		return taskv1.TaskEnqueueResult_TASK_ENQUEUE_RESULT_QUEUED
	case task.EnqueueResultDeduped:
		return taskv1.TaskEnqueueResult_TASK_ENQUEUE_RESULT_DEDUPED
	default:
		return taskv1.TaskEnqueueResult_TASK_ENQUEUE_RESULT_UNSPECIFIED
	}
}

func mustValidateInterceptor() connect.Interceptor {
	interceptor, err := connectvalidate.NewInterceptor()
	if err != nil {
		panic(fmt.Sprintf("create connect validate interceptor: %v", err))
	}

	return interceptor
}

func stringPtr(value string) *string {
	return &value
}

func int32Ptr(value int32) *int32 {
	return &value
}

func int64Ptr(value int64) *int64 {
	return &value
}

func taskStatePtr(value taskv1.TaskState) *taskv1.TaskState {
	return &value
}

func enqueueResultPtr(value taskv1.TaskEnqueueResult) *taskv1.TaskEnqueueResult {
	return &value
}

func optionalFilter(value string) any {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}

	return trimmed
}

func decodePageToken(token string) (int, error) {
	trimmed := strings.TrimSpace(token)
	if trimmed == "" {
		return 0, nil
	}

	decoded, err := base64.RawURLEncoding.DecodeString(trimmed)
	if err != nil {
		return 0, err
	}

	offset, err := strconv.Atoi(string(decoded))
	if err != nil {
		return 0, err
	}
	if offset < 0 {
		return 0, fmt.Errorf("offset must be non-negative")
	}

	return offset, nil
}

func encodePageToken(offset int) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(offset)))
}
