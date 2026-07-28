package tui

import (
	"encoding/json"
)

type ApprovalRequest struct {
	ToolName string
	Args     json.RawMessage
	Preview  string
	ReplyCh  chan bool
}

type ApprovalBridge struct {
	RequestCh chan ApprovalRequest
	done      chan struct{}
}

func NewApprovalBridge() *ApprovalBridge {
	return &ApprovalBridge{
		RequestCh: make(chan ApprovalRequest, 1),
		done:      make(chan struct{}),
	}
}

func (b *ApprovalBridge) Done() <-chan struct{} {
	return b.done
}

func (b *ApprovalBridge) Respond() {
	select {
	case <-b.done:
		// already closed
	default:
		close(b.done)
	}
}

func (b *ApprovalBridge) RequestApproval(toolName string, args json.RawMessage, preview string) (bool, error) {
	replyCh := make(chan bool, 1)
	b.RequestCh <- ApprovalRequest{
		ToolName: toolName,
		Args:     args,
		Preview:  preview,
		ReplyCh:  replyCh,
	}
	return <-replyCh, nil
}
