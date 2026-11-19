package agenttransport

import (
	"context"
	"net/http"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
)

// RescueSubprotocol is the only subprotocol of the rescue reception route.
const RescueSubprotocol = "rescue-agent-reception.v1"

// RescueRegistry holds the receptions; *agentgateway.Gateway implements it.
type RescueRegistry interface {
	AdmitRescue() error
	OpenRescue(agentgateway.RescueRequest) (agentgateway.RescueReception, error)
	EndRescue(id []byte)
}

// RescueHandler is the gateway end of the rescue reception route
// (docs/rescue-connection-start.md, 受付の登録).
type RescueHandler struct{}

func NewRescueHandler(reg RescueRegistry) (*RescueHandler, error) { return &RescueHandler{}, nil }

func (h *RescueHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
}

func (h *RescueHandler) Close(ctx context.Context) error { return nil }
