package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
	"github.com/nicrepository/nchat/services/chat-service/internal/ws"
)

var _ ws.CallHandler = (*callHandlerAdapter)(nil)

type callHandlerAdapter struct{ service *service.CallService }

func (a *callHandlerAdapter) StartCall(ctx context.Context, command ws.StartCallCommand) (domain.Call, string, error) {
	return a.service.Start(ctx, service.StartCallInput{
		WorkspaceID: command.WorkspaceID, RequestID: command.RequestID,
		CallerID: command.CallerID, CalleeID: command.CalleeID, Type: command.Type,
		TargetType: domain.CallTargetType(command.TargetType), TargetID: command.TargetID,
	})
}

func (a *callHandlerAdapter) RenewCallPresence(ctx context.Context, workspaceID, actorID, callID, participationID string) error {
	return a.service.Presence(ctx, workspaceID, actorID, callID, participationID)
}

func (a *callHandlerAdapter) TransitionCall(ctx context.Context, workspaceID, actorID, callID string, action ws.ClientMessageType) (domain.Call, error) {
	switch action {
	case ws.ClientMessageTypeCallAccept:
		return a.service.Accept(ctx, workspaceID, actorID, callID)
	case ws.ClientMessageTypeCallDecline:
		return a.service.Decline(ctx, workspaceID, actorID, callID)
	case ws.ClientMessageTypeCallCancel:
		return a.service.Cancel(ctx, workspaceID, actorID, callID)
	case ws.ClientMessageTypeCallEnd:
		return a.service.End(ctx, workspaceID, actorID, callID)
	default:
		return domain.Call{}, domain.ErrInvalidInput
	}
}

func (a *callHandlerAdapter) CurrentCall(ctx context.Context, workspaceID, actorID, callID string) (domain.Call, error) {
	return a.service.Current(ctx, workspaceID, actorID, callID)
}

func (a *callHandlerAdapter) LeaveCall(ctx context.Context, workspaceID, actorID, callID, participationID string) (domain.Call, bool, error) {
	return a.service.Leave(ctx, workspaceID, actorID, callID, participationID)
}

func (a *callHandlerAdapter) ResourceSync(ctx context.Context, workspaceID, actorID string, targetType ws.TargetType, targetID string) (domain.Call, bool, time.Time, error) {
	return a.service.ResourceSync(ctx, workspaceID, actorID, domain.CallTargetType(targetType), targetID)
}

func (a *callHandlerAdapter) JoinCall(ctx context.Context, workspaceID, actorID, callID string, targetType ws.TargetType, targetID string) (domain.Call, string, error) {
	return a.service.Join(ctx, service.JoinCallInput{
		WorkspaceID: workspaceID, ActorID: actorID, CallID: callID,
		TargetType: domain.CallTargetType(targetType), TargetID: targetID,
	})
}

func runCallExpiryWorker(ctx context.Context, calls *service.CallService, logger *slog.Logger) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if err := calls.ExpireDue(ctx); err != nil && ctx.Err() == nil {
				logger.WarnContext(ctx, "call expiry scan failed", "error_code", "call_expiry_failed")
			}
		case <-ctx.Done():
			return
		}
	}
}
