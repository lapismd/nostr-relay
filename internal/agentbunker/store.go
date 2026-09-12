package agentbunker

import (
	"context"
	"errors"
	"time"
)

var (
	ErrNotFound = errors.New("managed agent record not found")
	ErrConflict = errors.New("managed agent conflict")
	ErrReplay   = errors.New("managed agent request replay")
)

type Store interface {
	CreateAgent(context.Context, Agent, Client) error
	Agent(context.Context, string) (Agent, error)
	Client(context.Context, string, string) (Client, error)
	ListAgents(context.Context, string) ([]AgentView, error)
	OwnerCounts(context.Context, string, time.Time) (active, pending, recent int, err error)
	Activate(context.Context, string, []byte, []byte, time.Time) error
	PairClient(context.Context, string, string, time.Time) error
	RevokeCurrent(context.Context, string, time.Time) error
	DeleteAgent(context.Context, string) error
	SaveReplacement(context.Context, Client) error
	CachedRequest(context.Context, string, string, string) (CachedRequest, error)
	SaveRequest(context.Context, string, string, string, CachedRequest, time.Time) error
	UseAuthEvent(context.Context, string, time.Time) error
	HTTPResult(context.Context, string, string) (requestHash string, response []byte, err error)
	SaveHTTPResult(context.Context, string, string, string, []byte, time.Time) error
	Audit(context.Context, Audit) error
}
