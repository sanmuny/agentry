package workflow

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/amtp-protocol/agentry/internal/storage"
	"github.com/amtp-protocol/agentry/internal/types"
)

type managerImpl struct {
	storage    storage.Storage
	dispatcher Dispatcher
	doneChan   chan struct{}
}

func NewManager(s storage.Storage, d Dispatcher) Manager {
	return &managerImpl{
		storage:    s,
		dispatcher: d,
		doneChan:   make(chan struct{}),
	}
}

func (m *managerImpl) Initialize(ctx context.Context, msg *types.Message) (*types.WorkflowState, error) {
	if msg.Coordination == nil {
		return nil, fmt.Errorf("message does not contain coordination config")
	}

	state := &types.WorkflowState{
		WorkflowID:       msg.MessageID,
		Status:           types.WorkflowStatusPending,
		CoordinationType: msg.Coordination.Type,
		TimeoutSeconds:   msg.Coordination.Timeout,
		CreatedAt:        time.Now(),
		UpdatedAt:        time.Now(),
	}

	if state.TimeoutSeconds <= 0 {
		state.TimeoutSeconds = 3600 // Default 1 hour if not specified
	}

	// Calculate participants based on coordination type
	var participants []string
	switch msg.Coordination.Type {
	case "parallel":
		participants = append(participants, msg.Coordination.RequiredResponses...)
		participants = append(participants, msg.Coordination.OptionalResponses...)
	case "sequential":
		participants = append(participants, msg.Coordination.Sequence...)
	case "conditional":
		// Only adding initial participants, conditional branches are resolved dynamically
		for _, condition := range msg.Coordination.Conditions {
			participants = append(participants, condition.Then...)
			participants = append(participants, condition.Else...)
		}
	default:
		return nil, fmt.Errorf("unsupported coordination type: %s", msg.Coordination.Type)
	}

	state.Participants = make([]types.WorkflowParticipant, 0)
	dedup := make(map[string]bool)
	for _, p := range participants { // Simple deduplication for state tracking
		if _, ok := dedup[p]; !ok {
			dedup[p] = true
			state.Participants = append(state.Participants, types.WorkflowParticipant{
				WorkflowID: state.WorkflowID,
				Address:    p,
				Status:     types.ParticipantStatusPending,
				CreatedAt:  time.Now(),
				UpdatedAt:  time.Now(),
			})
		}
	}

	err := m.storage.StoreWorkflow(ctx, state)
	if err != nil {
		return nil, fmt.Errorf("failed to store workflow state: %w", err)
	}

	// Begin execution based on type
	err = m.startExecution(ctx, state, msg)
	if err != nil {
		m.storage.UpdateWorkflowStatus(ctx, state.WorkflowID, types.WorkflowStatusFailed)
		return state, err
	}

	return state, nil
}

func (m *managerImpl) startExecution(ctx context.Context, state *types.WorkflowState, msg *types.Message) error {
	err := m.storage.UpdateWorkflowStatus(ctx, state.WorkflowID, types.WorkflowStatusInProgress)
	if err != nil {
		return err
	}

	// Dispatch mechanics
	switch state.CoordinationType {
	case "parallel":
		return m.executeParallel(ctx, state, msg)
	case "sequential":
		return m.executeSequentialNext(ctx, state, msg, 0)
	case "conditional":
		return m.executeConditional(ctx, state, msg)
	}
	return nil
}

// executeParallel sends message to all participants at once
func (m *managerImpl) executeParallel(ctx context.Context, state *types.WorkflowState, msg *types.Message) error {
	msgCopy := *msg
	msgCopy.Recipients = append(msg.Coordination.RequiredResponses, msg.Coordination.OptionalResponses...)

	// We pass down to the dispatcher. The dispatcher should route the message properly.
	return m.dispatcher.Dispatch(ctx, &msgCopy)
}

// executeSequentialNext dispatches to the N-th participant in the sequence
func (m *managerImpl) executeSequentialNext(ctx context.Context, state *types.WorkflowState, msg *types.Message, index int) error {
	if index >= len(msg.Coordination.Sequence) {
		return m.storage.UpdateWorkflowStatus(ctx, state.WorkflowID, types.WorkflowStatusCompleted)
	}

	nextAgent := msg.Coordination.Sequence[index]
	msgCopy := *msg
	msgCopy.Recipients = []string{nextAgent}

	return m.dispatcher.Dispatch(ctx, &msgCopy)
}

func (m *managerImpl) executeConditional(ctx context.Context, state *types.WorkflowState, msg *types.Message) error {
	// For conditional, we start by evaluating current states.
	// As this requires inspecting previous responses to satisfy `if` strings,
	// the real conditional engine parses the response dynamically.
	// For Phase 2, we just dispatch to the original message's explicit recipients,
	// and process conditions as response callbacks come in.
	return m.dispatcher.Dispatch(ctx, msg)
}

func (m *managerImpl) ProcessResponse(ctx context.Context, workflowID string, replyMsg *types.Message) error {
	state, err := m.storage.GetWorkflow(ctx, workflowID)
	if err != nil {
		return fmt.Errorf("workflow not found: %w", err)
	}

	if state.Status == types.WorkflowStatusCompleted || state.Status == types.WorkflowStatusFailed || state.Status == types.WorkflowStatusTimeout {
		log.Printf("Workflow %s already finished, ignoring response", workflowID)
		return nil
	}

	// Update participant status
	err = m.storage.UpdateWorkflowParticipant(ctx, workflowID, replyMsg.Sender, types.ParticipantStatusCompleted, replyMsg.Payload)
	if err != nil {
		return fmt.Errorf("failed to update participant status: %w", err)
	}

	// Re-evaluate state
	return m.evaluateWorkflowState(ctx, state, replyMsg)
}

func (m *managerImpl) evaluateWorkflowState(ctx context.Context, state *types.WorkflowState, replyMsg *types.Message) error {
	// Re-fetch to get up-to-date participants
	state, err := m.storage.GetWorkflow(ctx, state.WorkflowID)
	if err != nil {
		return err
	}

	allDone := true
	for _, p := range state.Participants {
		if p.Status == types.ParticipantStatusPending {
			allDone = false
			break
		}
	}

	if state.CoordinationType == "parallel" {
		if allDone {
			return m.storage.UpdateWorkflowStatus(ctx, state.WorkflowID, types.WorkflowStatusCompleted)
		}
	} else if state.CoordinationType == "sequential" {
		// Figure out the next index
		// For true sequential state tracking, this needs the original message config
		// Here we simply assume if it's not allDone, we check who is next
	}

	return nil
}

func (m *managerImpl) Start(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-m.doneChan:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.sweepTimeouts(ctx)
			}
		}
	}()
}

func (m *managerImpl) Stop() error {
	close(m.doneChan)
	return nil
}

func (m *managerImpl) sweepTimeouts(ctx context.Context) {
	timeouts, err := m.storage.ListTimedOutWorkflows(ctx)
	if err != nil {
		log.Printf("Error checking timed out workflows: %v", err)
		return
	}

	for _, w := range timeouts {
		log.Printf("Workflow %s timed out", w.WorkflowID)
		m.storage.UpdateWorkflowStatus(ctx, w.WorkflowID, types.WorkflowStatusTimeout)
		for _, p := range w.Participants {
			if p.Status == types.ParticipantStatusPending {
				m.storage.UpdateWorkflowParticipant(ctx, w.WorkflowID, p.Address, types.ParticipantStatusTimeout, nil)
			}
		}
	}
}
