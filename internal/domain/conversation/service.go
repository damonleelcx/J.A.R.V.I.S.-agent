package conversation

import (
	"context"
	"strings"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/id"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// Service records and returns what was said at the workbench.
type Service struct {
	pool  *db.Pool
	repo  *Repository
	clock clock.Clock
	log   *logx.Logger
}

// NewService wires the record.
func NewService(pool *db.Pool, clk clock.Clock, log *logx.Logger) *Service {
	return &Service{pool: pool, repo: NewRepository(), clock: clk, log: log}
}

// Said is one turn to record.
type Said struct {
	// ConversationID is empty for the first turn of a conversation, and this
	// service mints one. See Resolve for why a client may not.
	ConversationID string
	OwnerID        string
	ProjectID      string
	Role           Role
	Text           string
	Detail         string
	Images         int
}

// Resolve returns the conversation this turn belongs to, minting one when the
// caller has none.
//
// # Why a client cannot name a conversation that does not exist yet
//
// If it could, it would choose its own ids — and an id chosen by a client is one
// that can be chosen twice, guessed, or aimed at somebody else's record. So the
// rule is narrow and has no exceptions: an id that already exists must belong to
// the caller, and an id that does not exist is refused rather than created.
//
// A refusal here does not distinguish "somebody else's" from "never existed".
// Both are NOT_FOUND, because the difference is exactly the fact a stranger
// would be probing for.
func (s *Service) Resolve(ctx context.Context, conversationID, ownerID string) (string, error) {
	const op = "conversation.Service.Resolve"

	if strings.TrimSpace(ownerID) == "" {
		return "", errs.New(op, errs.CodeValidationFailed).
			WithDetail("a conversation belongs to somebody; there is no unattributed record")
	}
	if strings.TrimSpace(conversationID) == "" {
		return id.New(id.PrefixConversation), nil
	}
	owner, err := s.repo.OwnerOf(ctx, s.pool, conversationID)
	if err != nil {
		return "", err
	}
	if owner != ownerID {
		return "", errs.New(op, errs.CodeNotFound).
			WithDetail("no conversation %s", conversationID)
	}
	return conversationID, nil
}

// Record appends a turn.
//
// Returns the stored turn so a caller can report what was actually kept rather
// than what it hoped would be.
func (s *Service) Record(ctx context.Context, said Said) (*Turn, error) {
	t := &Turn{
		ID: id.New(id.PrefixTurn), ConversationID: said.ConversationID,
		OwnerID: said.OwnerID, ProjectID: said.ProjectID,
		Role: said.Role, Text: said.Text, Detail: said.Detail,
		Images: said.Images, SaidAt: s.clock.Now(),
	}
	if err := s.repo.Append(ctx, s.pool, t); err != nil {
		return nil, err
	}
	return t, nil
}

// History returns a conversation's turns in order, for this owner only.
func (s *Service) History(ctx context.Context, conversationID, ownerID string) ([]Turn, error) {
	const op = "conversation.Service.History"

	if strings.TrimSpace(conversationID) == "" || strings.TrimSpace(ownerID) == "" {
		return nil, errs.New(op, errs.CodeValidationFailed).
			WithDetail("reading a conversation needs both the conversation and whose it is")
	}
	turns, err := s.repo.List(ctx, s.pool, conversationID, ownerID)
	if err != nil {
		return nil, err
	}
	if len(turns) == 0 {
		// Empty and absent are the same thing here — a conversation exists
		// exactly when it has a turn — so this is NOT_FOUND rather than an empty
		// list, which a client would otherwise render as a conversation somebody
		// had and lost.
		return nil, errs.New(op, errs.CodeNotFound).
			WithDetail("no conversation %s", conversationID)
	}
	return turns, nil
}

// Recent returns the tail of a conversation and how long the whole of it is.
//
// Empty and no error for a conversation with no turns — unlike History, which
// treats absence as NOT_FOUND. The difference is what each is for: History
// answers "show me this conversation", where nothing is a wrong answer, and this
// answers "what came before this turn", where nothing is the right one on the
// first turn of every conversation there has ever been.
func (s *Service) Recent(ctx context.Context, conversationID, ownerID string, limit int) ([]Turn, int, error) {
	const op = "conversation.Service.Recent"

	if strings.TrimSpace(conversationID) == "" || strings.TrimSpace(ownerID) == "" {
		return nil, 0, errs.New(op, errs.CodeValidationFailed).
			WithDetail("reading a conversation needs both the conversation and whose it is")
	}
	return s.repo.Recent(ctx, s.pool, conversationID, ownerID, limit)
}

// Forget deletes a conversation and reports how many turns went with it.
//
// PRD AUD-07 asks for delete-session to be reachable at all times, and MEM-01
// asks each layer to have its own retention. This layer's retention is "until
// the person says otherwise", which is only true if saying otherwise works.
func (s *Service) Forget(ctx context.Context, conversationID, ownerID string) (int64, error) {
	const op = "conversation.Service.Forget"

	if strings.TrimSpace(conversationID) == "" || strings.TrimSpace(ownerID) == "" {
		return 0, errs.New(op, errs.CodeValidationFailed).
			WithDetail("deleting a conversation needs both the conversation and whose it is")
	}
	n, err := s.repo.Delete(ctx, s.pool, conversationID, ownerID)
	if err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, errs.New(op, errs.CodeNotFound).
			WithDetail("no conversation %s", conversationID)
	}
	s.log.Info(ctx, logx.EventConversationForgotten,
		"conversation_id", conversationID, "owner_id", ownerID, "turns", n)
	return n, nil
}
