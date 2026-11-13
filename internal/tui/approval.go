package tui

import (
	"encoding/json"

	"github.com/mevarx/GoCode/internal/tools"
)

type ApprovalRequest struct {
	ToolName string
	Args     json.RawMessage
	Preview  string
	// ReplyCh carries the legacy yes/no answer. It is nil when the requester
	// made a choice-aware request.
	ReplyCh chan bool
	// ChoiceCh carries the full decision for choice-aware requesters.
	ChoiceCh chan tools.ApprovalChoice
}

type ApprovalBridge struct {
	RequestCh chan ApprovalRequest
}

func NewApprovalBridge() *ApprovalBridge {
	return &ApprovalBridge{
		RequestCh: make(chan ApprovalRequest, 1),
	}
}

// RequestApproval is the legacy yes/no request. Grant and read-only choices
// still approve the pending call; only the additional scope is dropped.
func (b *ApprovalBridge) RequestApproval(toolName string, args json.RawMessage, preview string) (bool, error) {
	choice, err := b.RequestApprovalChoice(toolName, args, preview)
	if err != nil {
		return false, err
	}
	return choice.Approved(), nil
}

// RequestApprovalChoice asks the TUI for a full decision. The gate installs
// this as OnPresentChoice so grants and the per-turn read-only flag can be
// expressed from the modal.
//
// Limitation: the TUI has no argument editor, so ApprovalChoice.ApprovalEdit
// is never produced here. The plain loop wires tools.ApprovalGate.EditArgs;
// the TUI deliberately offers only y/a/r/n to avoid claiming parity.
func (b *ApprovalBridge) RequestApprovalChoice(toolName string, args json.RawMessage, preview string) (tools.ApprovalChoice, error) {
	choiceCh := make(chan tools.ApprovalChoice, 1)
	b.RequestCh <- ApprovalRequest{
		ToolName: toolName,
		Args:     args,
		Preview:  preview,
		ChoiceCh: choiceCh,
	}
	return <-choiceCh, nil
}
