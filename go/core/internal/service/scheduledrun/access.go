package scheduledrun

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/kagent-dev/kagent/go/api/v1alpha3"
	dbpkg "github.com/kagent-dev/kagent/go/core/internal/database"
	"github.com/kagent-dev/kagent/go/core/internal/service/serviceerrors"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
)

// ResolveInstanceAccess binds a conversation to its current owning schedule.
// Kubernetes UID, rather than a reusable name, is the authorization boundary.
func (s *Service) ResolveInstanceAccess(ctx context.Context, instanceID string) (*auth.ShareContext, error) {
	if _, err := uuid.Parse(instanceID); err != nil {
		// Ordinary instance request validation reports malformed identifiers.
		return nil, nil
	}
	execution, err := s.store.GetScheduledRunExecutionByAgentInstanceID(ctx, instanceID)
	if errors.Is(err, dbpkg.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, serviceerrors.NewInternal("Failed to resolve scheduled conversation", err)
	}
	session, ok := auth.AuthSessionFrom(ctx)
	if !ok {
		return nil, serviceerrors.NewUnauthenticated("Authentication is required", nil)
	}
	key := types.NamespacedName{Namespace: execution.ScheduledRunNamespace, Name: execution.ScheduledRunName}
	if err := s.authorizer.Check(ctx, session.Principal(), auth.VerbGet, auth.Resource{Type: "ScheduledRun", Name: key.String()}); err != nil {
		return nil, serviceerrors.NewPermissionDenied("Not authorized to read ScheduledRun", err)
	}
	var run v1alpha3.ScheduledRun
	if err := s.kube.Get(ctx, key, &run); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, serviceerrors.NewNotFound("ScheduledRun no longer exists", err)
		}
		return nil, serviceerrors.NewInternal("Failed to load ScheduledRun", err)
	}
	if string(run.UID) != execution.ScheduledRunUID || !run.DeletionTimestamp.IsZero() {
		return nil, serviceerrors.NewNotFound("ScheduledRun no longer owns this conversation", nil)
	}
	userID := execution.UserID
	// Agent callbacks carry a delegated user ID, not a human caller identity.
	principal := session.Principal()
	return &auth.ShareContext{
		AgentInstanceID: instanceID,
		UserID:          userID,
		ReadOnly:        userID == auth.ScheduledRunUserID || principal.Agent.ID != "" || principal.User.ID != userID,
	}, nil
}
