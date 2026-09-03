package memory

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/claim"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/id"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// Service is the use-case layer over memory: what FORGE and its users may do
// with it, as opposed to how it is stored.
type Service struct {
	pool  *db.Pool
	repo  *Repository
	clock clock.Clock
	log   *logx.Logger
}

// NewService wires the service.
func NewService(pool *db.Pool, clk clock.Clock, log *logx.Logger) *Service {
	return &Service{pool: pool, repo: NewRepository(), clock: clk, log: log}
}

// Repo exposes the persistence port for callers already inside a transaction.
func (s *Service) Repo() *Repository { return s.repo }

// Write is a request to remember something.
//
// The epistemic label is required and has no default. A default would be a lie
// told by omission: whatever value it took, some caller would get it without
// having decided, and a month later nobody could tell that item from one whose
// label was chosen deliberately.
type Write struct {
	Scope Scope
	// Owner is the id of the thing this layer hangs off — a goal, a project or a
	// user, as the layer's Owner names. Ignored for organisation memory.
	Owner string
	Key   string
	Value any
	How   claim.Epistemic
	// Source is provenance: the file, tool, standard or person it came from.
	Source string
	// TTL overrides the layer's default. A negative TTL means "never expires",
	// stated explicitly so that "no expiry" is a decision rather than a zero
	// value nobody looked at.
	TTL time.Duration
}

// Remember writes an item, applying the layer's retention when the caller sets
// none, and refusing keys a user has forgotten.
func (s *Service) Remember(ctx context.Context, w Write) (*Item, error) {
	const op = "memory.Service.Remember"

	layer, err := LayerOf(w.Scope)
	if err != nil {
		return nil, err
	}
	value, err := json.Marshal(w.Value)
	if err != nil {
		return nil, errs.Wrap(op, errs.CodeSerializationFail, err).
			WithDetail("the value for memory key %q cannot be encoded as JSON", w.Key)
	}

	now := s.clock.Now()
	item := &Item{
		ID: id.New(id.PrefixMemory), Scope: w.Scope, Key: strings.TrimSpace(w.Key),
		Value: value, How: w.How, Source: w.Source,
		CreatedAt: now, UpdatedAt: now,
	}
	switch layer.Owner {
	case OwnerGoal:
		item.GoalID = &w.Owner
	case OwnerProject:
		item.ProjectID = &w.Owner
	case OwnerUser:
		item.UserID = &w.Owner
	}

	// Retention: the caller's TTL, else the layer's. This is the write-time half
	// of MEM-01's "distinct retention"; Item.Live is the read-time half.
	switch {
	case w.TTL < 0:
		item.ExpiresAt = nil
	case w.TTL > 0:
		t := now.Add(w.TTL)
		item.ExpiresAt = &t
	case layer.DefaultTTL > 0:
		t := now.Add(layer.DefaultTTL)
		item.ExpiresAt = &t
	}

	if err := s.repo.Upsert(ctx, s.pool, item); err != nil {
		return nil, err
	}
	s.log.Info(ctx, logx.EventMemoryWritten, "key", item.Key, "scope", string(item.Scope),
		"how", string(item.How), "expires_at", expiryString(item.ExpiresAt))
	return item, nil
}

// Recall is a request to remember what is relevant.
//
// Scopes defaults to every layer the caller has an owner for, narrowest first,
// so a caller that does not think about layering still gets turn context ahead
// of org knowledge rather than an arbitrary order.
type Recall struct {
	GoalID    string
	ProjectID string
	UserID    string
	// Keys are exact keys the caller wants. Each is looked up by name and, when
	// found, reported with ReasonExactKey.
	Keys []string
	// Prefix restricts the layer sweep to keys beginning with it.
	Prefix string
	// Scopes overrides which layers are read.
	Scopes []Scope
	Limit  int
}

// Recall returns live memory with, for every item, why this query returned it.
//
// # Why the reason is derived here rather than passed in
//
// PRD MEM-02 asks FORGE to show why an item was retrieved. That is only worth
// anything if the answer describes the query that actually ran. So each reason
// is attached at the point the corresponding predicate matched — an exact-key
// lookup can only produce ReasonExactKey, a layer sweep can only produce
// ReasonLayer or ReasonPinned — and no code path can attach a reason to an item
// it did not fetch that way.
//
// # Ordering
//
// Narrow beats broad, and within a layer pinned beats recent. A project-wide
// note and a turn-scoped correction of it must not come back in an order that
// depends on which row was written last: the correction is the point.
func (s *Service) Recall(ctx context.Context, rc Recall) ([]Recalled, error) {
	const op = "memory.Service.Recall"

	owners := map[Owner]string{}
	if rc.GoalID != "" {
		owners[OwnerGoal] = rc.GoalID
	}
	if rc.ProjectID != "" {
		owners[OwnerProject] = rc.ProjectID
	}
	if rc.UserID != "" {
		owners[OwnerUser] = rc.UserID
	}

	scopes := rc.Scopes
	if len(scopes) == 0 {
		scopes = readableScopes(owners)
	}
	for _, sc := range scopes {
		layer, err := LayerOf(sc)
		if err != nil {
			return nil, err
		}
		if layer.Owner != OwnerNone {
			if _, ok := owners[layer.Owner]; !ok {
				return nil, errs.New(op, errs.CodeValidationFailed).
					WithDetail("recalling %s memory needs a %s id; without one this would read somebody else's",
						sc, layer.Owner)
			}
			continue
		}
		// Org-wide knowledge has no owner to check, and that is not the same as
		// having no audience. It is readable by anyone with an account here, so
		// the caller must be scoped to something in this deployment; an
		// unscoped read is nobody asking on nobody's behalf.
		if len(owners) == 0 {
			return nil, errs.New(op, errs.CodeValidationFailed).
				WithDetail("recalling %s memory needs a goal, project or user id. It is readable by "+
					"anyone with an account in this deployment, which means the caller has to be one — "+
					"an unscoped read is nobody asking on nobody's behalf.", sc)
		}
	}

	now := s.clock.Now()
	out := []Recalled{}
	seen := map[string]bool{}

	// Exact keys first, across every requested layer, narrowest first. An
	// explicitly requested key outranks anything a sweep happens to surface.
	for _, key := range rc.Keys {
		for _, sc := range scopes {
			layer, _ := LayerOf(sc)
			item, err := s.repo.FindByKey(ctx, s.pool, sc, owners[layer.Owner], key)
			if err != nil {
				if errs.Is(err, errs.CodeNotFound) {
					continue
				}
				return nil, err
			}
			// The same retention rule the sweep applies, so an exact key cannot
			// be a way around expiry or a user's deletion.
			if item.Live(now) != nil || seen[item.ID] {
				continue
			}
			seen[item.ID] = true
			out = append(out, Recalled{Item: *item, Why: ReasonExactKey,
				Detail: explain(ReasonExactKey, item, key)})
		}
	}

	items, err := s.repo.List(ctx, s.pool, Query{
		Scopes: scopes, Owners: owners, Prefix: rc.Prefix, Limit: rc.Limit,
	}, now)
	if err != nil {
		return nil, err
	}
	for i := range items {
		item := items[i]
		if seen[item.ID] {
			continue
		}
		seen[item.ID] = true

		// Which predicate put this row in the result, in the order that makes
		// the most specific true statement about it.
		why, query := ReasonLayer, ""
		switch {
		case item.Pinned:
			why = ReasonPinned
		case rc.Prefix != "":
			why, query = ReasonPrefix, rc.Prefix
		}
		out = append(out, Recalled{Item: item, Why: why, Detail: explain(why, &item, query)})
	}
	return out, nil
}

// readableScopes returns the layers a caller with these owners may read,
// narrowest first.
//
// Org-wide knowledge is included only for a caller scoped to SOMETHING — a goal,
// a project or a user. It used to be included unconditionally, on the reasoning
// that it has no owner to check against, which left an unscoped Recall reading
// org knowledge with no identity at all. There is no organisation entity to
// check membership against and there does not need to be: the audience is
// everybody with an account in this deployment, and being scoped to anything in
// it is what "having an account" looks like from here.
func readableScopes(owners map[Owner]string) []Scope {
	var out []Scope
	for _, l := range layers {
		if l.Owner == OwnerNone {
			if len(owners) > 0 {
				out = append(out, l.Scope)
			}
			continue
		}
		if _, ok := owners[l.Owner]; ok {
			out = append(out, l.Scope)
		}
	}
	return out
}

// Inspect returns everything held in a layer for one owner, including forgotten
// and expired items (PRD MEM-02).
func (s *Service) Inspect(ctx context.Context, scope Scope, owner string) ([]Item, error) {
	return s.repo.ListAll(ctx, s.pool, scope, owner)
}

// Correct replaces an item's value, keeping its identity, its pin and its age.
//
// A correction is not a new memory. Deleting and re-adding would reset
// created_at, and "FORGE has believed this since Tuesday" is a fact a user reads
// when deciding whether to trust it.
func (s *Service) Correct(ctx context.Context, itemID string, value any, how claim.Epistemic, source string) (*Item, error) {
	const op = "memory.Service.Correct"

	item, err := s.repo.FindByID(ctx, s.pool, itemID)
	if err != nil {
		return nil, err
	}
	if item.Forgotten() {
		return nil, errs.New(op, errs.CodeMemoryForgotten).
			WithDetail("memory %q was forgotten; correcting it would bring back what a user deleted", item.Key)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, errs.Wrap(op, errs.CodeSerializationFail, err).
			WithDetail("the corrected value for %q cannot be encoded as JSON", item.Key)
	}
	item.Value, item.How, item.Source, item.UpdatedAt = encoded, how, source, s.clock.Now()
	if err := s.repo.Upsert(ctx, s.pool, item); err != nil {
		return nil, err
	}
	return item, nil
}

// Pin keeps an item past its layer's lifetime; unpinning restores it.
func (s *Service) Pin(ctx context.Context, itemID string, pinned bool) error {
	return s.repo.SetPinned(ctx, s.pool, itemID, pinned, s.clock.Now())
}

// Expire sets when an item stops being returned. A nil time means never.
func (s *Service) Expire(ctx context.Context, itemID string, at *time.Time) error {
	return s.repo.SetExpiry(ctx, s.pool, itemID, at, s.clock.Now())
}

// ExpireIn is Expire relative to now, which is how a person says it.
func (s *Service) ExpireIn(ctx context.Context, itemID string, d time.Duration) error {
	t := s.clock.Now().Add(d)
	return s.repo.SetExpiry(ctx, s.pool, itemID, &t, s.clock.Now())
}

// Forget deletes an item at a user's request, and makes the deletion hold.
func (s *Service) Forget(ctx context.Context, itemID, byUserID, reason string) error {
	if err := s.repo.Forget(ctx, s.pool, itemID, byUserID, reason, s.clock.Now()); err != nil {
		return err
	}
	s.log.Info(ctx, logx.EventMemoryForgotten, "item_id", itemID, "by", byUserID, "reason", reason)
	return nil
}

// Purge removes a forgotten item entirely, re-opening its key.
func (s *Service) Purge(ctx context.Context, itemID string) error {
	if err := s.repo.Purge(ctx, s.pool, itemID); err != nil {
		return err
	}
	s.log.Warn(ctx, logx.EventMemoryPurged, "item_id", itemID)
	return nil
}

// Sweep reclaims expired rows. Reads already exclude them; this frees the space.
func (s *Service) Sweep(ctx context.Context) (int64, error) {
	n, err := s.repo.Sweep(ctx, s.pool, s.clock.Now())
	if err != nil {
		return 0, err
	}
	if n > 0 {
		s.log.Info(ctx, logx.EventMemorySwept, "removed", n)
	}
	return n, nil
}

// Export is one layer's contents in a form a user can keep (PRD MEM-02).
type Export struct {
	Scope      Scope        `json:"scope"`
	Layer      string       `json:"layer"`
	Owner      string       `json:"owner,omitempty"`
	ExportedAt string       `json:"exported_at"`
	Items      []ExportItem `json:"items"`
}

// ExportItem is one item, rendered rather than dumped: the epistemic label
// carries its gloss, because an export a person opens in six months has to
// explain its own vocabulary.
type ExportItem struct {
	Key       string          `json:"key"`
	Value     json.RawMessage `json:"value"`
	How       string          `json:"how"`
	HowMeans  string          `json:"how_means"`
	Source    string          `json:"source,omitempty"`
	Pinned    bool            `json:"pinned,omitempty"`
	ExpiresAt string          `json:"expires_at,omitempty"`
	// Forgotten items appear in the export saying so. Omitting them would tell
	// the reader their deletion left nothing behind, which is not true: the key
	// is still claimed, and that is what stops it being re-learned.
	ForgottenAt     string `json:"forgotten_at,omitempty"`
	ForgottenReason string `json:"forgotten_reason,omitempty"`
	CreatedAt       string `json:"created_at"`
	UpdatedAt       string `json:"updated_at"`
}

// ExportLayer renders everything held in a layer for one owner.
func (s *Service) ExportLayer(ctx context.Context, scope Scope, owner string) (*Export, error) {
	layer, err := LayerOf(scope)
	if err != nil {
		return nil, err
	}
	items, err := s.repo.ListAll(ctx, s.pool, scope, owner)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].Key < items[j].Key })

	out := &Export{
		Scope: scope, Layer: layer.PRDName, Owner: owner,
		ExportedAt: s.clock.Now().UTC().Format(time.RFC3339),
		Items:      make([]ExportItem, 0, len(items)),
	}
	for i := range items {
		it := items[i]
		out.Items = append(out.Items, ExportItem{
			Key: it.Key, Value: json.RawMessage(it.Value),
			How: string(it.How), HowMeans: it.How.Gloss(), Source: it.Source,
			Pinned: it.Pinned, ExpiresAt: expiryString(it.ExpiresAt),
			ForgottenAt:     expiryString(it.ForgottenAt),
			ForgottenReason: it.ForgottenReason,
			CreatedAt:       it.CreatedAt.UTC().Format(time.RFC3339),
			UpdatedAt:       it.UpdatedAt.UTC().Format(time.RFC3339),
		})
	}
	return out, nil
}

func expiryString(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
