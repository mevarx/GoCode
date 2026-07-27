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
}

func NewApprovalBridge() *ApprovalBridge {
	return &ApprovalBridge{
		RequestCh: make(chan ApprovalRequest, 1),
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
