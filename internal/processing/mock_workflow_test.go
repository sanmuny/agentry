package processing

import (
	"context"

	"github.com/amtp-protocol/agentry/internal/types"
	"github.com/amtp-protocol/agentry/internal/workflow"
)

// MockWorkflowManager is a mock implementation of workflow.Manager
type MockWorkflowManager struct {
	InitializeFunc      func(ctx context.Context, msg *types.Message) (*types.WorkflowState, error)
	ProcessResponseFunc func(ctx context.Context, workflowID string, replyMsg *types.Message) error
	StartFunc           func(ctx context.Context)
	StopFunc            func() error
}

func (m *MockWorkflowManager) Initialize(ctx context.Context, msg *types.Message) (*types.WorkflowState, error) {
	if m.InitializeFunc != nil {
		return m.InitializeFunc(ctx, msg)
	}
	return &types.WorkflowState{}, nil
}

func (m *MockWorkflowManager) ProcessResponse(ctx context.Context, workflowID string, replyMsg *types.Message) error {
	if m.ProcessResponseFunc != nil {
		return m.ProcessResponseFunc(ctx, workflowID, replyMsg)
	}
	return nil
}

func (m *MockWorkflowManager) Start(ctx context.Context) {
	if m.StartFunc != nil {
		m.StartFunc(ctx)
	}
}

func (m *MockWorkflowManager) Stop() error {
	if m.StopFunc != nil {
		return m.StopFunc()
	}
	return nil
}

var _ workflow.Manager = (*MockWorkflowManager)(nil)
