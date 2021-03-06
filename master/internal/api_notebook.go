package internal

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/pkg/errors"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/determined-ai/determined/master/internal/api"
	"github.com/determined-ai/determined/master/internal/command"
	"github.com/determined-ai/determined/master/internal/grpc"
	"github.com/determined-ai/determined/master/internal/resourcemanagers"
	"github.com/determined-ai/determined/master/pkg/actor"
	"github.com/determined-ai/determined/master/pkg/logger"
	"github.com/determined-ai/determined/proto/pkg/apiv1"
	"github.com/determined-ai/determined/proto/pkg/notebookv1"
)

var notebooksAddr = actor.Addr("notebooks")

func (a *apiServer) GetNotebooks(
	_ context.Context, req *apiv1.GetNotebooksRequest,
) (resp *apiv1.GetNotebooksResponse, err error) {
	err = a.actorRequest("/notebooks", req, &resp)
	if err != nil {
		return nil, err
	}
	a.sort(resp.Notebooks, req.OrderBy, req.SortBy, apiv1.GetNotebooksRequest_SORT_BY_ID)
	return resp, a.paginate(&resp.Pagination, &resp.Notebooks, req.Offset, req.Limit)
}

func (a *apiServer) GetNotebook(
	_ context.Context, req *apiv1.GetNotebookRequest) (resp *apiv1.GetNotebookResponse, err error) {
	return resp, a.actorRequest(fmt.Sprintf("/notebooks/%s", req.NotebookId), req, &resp)
}

func (a *apiServer) KillNotebook(
	_ context.Context, req *apiv1.KillNotebookRequest) (resp *apiv1.KillNotebookResponse, err error) {
	return resp, a.actorRequest(fmt.Sprintf("/notebooks/%s", req.NotebookId), req, &resp)
}

func (a *apiServer) NotebookLogs(
	req *apiv1.NotebookLogsRequest, resp apiv1.Determined_NotebookLogsServer) error {
	if err := grpc.ValidateRequest(
		grpc.ValidateLimit(req.Limit),
	); err != nil {
		return err
	}

	cmdManagerAddr := actor.Addr("notebooks", req.NotebookId)
	eventManager := a.m.system.Get(cmdManagerAddr.Child("events"))

	logRequest := api.LogsRequest{
		Offset: int(req.Offset),
		Limit:  int(req.Limit),
		Follow: req.Follow,
	}

	onLogEntry := func(log *logger.Entry) error {
		return resp.Send(&apiv1.NotebookLogsResponse{LogEntry: api.LogEntryToProtoLogEntry(log)})
	}

	streamID, err := uuid.NewUUID()
	if err != nil {
		return status.Errorf(codes.Internal, "failed to generate the stream uuid")
	}
	logStreamActorAddr := cmdManagerAddr.Child("logStream-" + streamID.String())
	logStreamActor, created := a.m.system.ActorOf(
		logStreamActorAddr,
		api.NewLogStreamActor(
			resp.Context(),
			eventManager,
			logRequest,
			onLogEntry,
		),
	)

	if !created {
		return errors.New("failed to create actor")
	}

	// Keep the request context open until the actor stops.
	return logStreamActor.AwaitTermination()
}

func (a *apiServer) LaunchNotebook(
	ctx context.Context, req *apiv1.LaunchNotebookRequest,
) (*apiv1.LaunchNotebookResponse, error) {
	cmdParams, user, err := a.prepareLaunchParams(ctx, &protoCommandParams{
		TemplateName: req.TemplateName,
		Config:       req.Config,
		Files:        req.Files,
	})
	if err != nil {
		return nil, err
	}

	notebookLaunchReq := command.NotebookLaunchRequest{
		CommandParams: cmdParams,
		User:          user,
	}
	notebookIDFut := a.m.system.AskAt(notebooksAddr, notebookLaunchReq)
	if err = api.ProcessActorResponseError(&notebookIDFut); err != nil {
		return nil, err
	}

	notebookID := notebookIDFut.Get().(resourcemanagers.TaskID)
	notebook := a.m.system.AskAt(notebooksAddr.Child(notebookID), &notebookv1.Notebook{})
	if err = api.ProcessActorResponseError(&notebook); err != nil {
		return nil, err
	}

	return &apiv1.LaunchNotebookResponse{
		Notebook: notebook.Get().(*notebookv1.Notebook),
	}, nil
}
