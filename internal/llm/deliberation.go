package llm

import (
	"context"
	"net/url"
	"strings"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// Deliberation: whether a model is allowed to think out loud before it answers.
//
// # The defect this exists for
//
// Every model this deployment's provider now serves is a REASONING model, and
// every one of them deliberates by default. Measured 2026-09-06 against
// token-plan.cn-beijing.maas.aliyuncs.com, on the eval suite's own bracket
// prompt, time to the first CONTENT token:
//
//	qwen3.7-plus    28693 ms thinking · 735 ms not
//	qwen3.8-flash    3140 ms thinking · 507 ms not
//	qwen3.6-flash   15538 ms thinking · 270 ms not
//	qwen3.8-max      4119 ms thinking · 486 ms not
//
// PRD AUD-02 asks for first audio inside 700 ms. A conversation role that
// deliberates misses it by a factor of FORTY, and does so silently: the tokens
// are produced, they simply arrive on `reasoning_content`, which this client has
// always ignored and should keep ignoring. Nothing was broken. The reply just
// took half a minute to begin.
//
// # Why it is a property of the ROLE and not a setting
//
// llm.go already says it, in the comment on RoleConverse: "an executor should
// think hard and may take a minute; a conversation is someone waiting
// mid-sentence". That sentence is the rule. Before this file it was a rule
// nothing enforced — the role picked a different MODEL and then let it behave
// like the executor's.
//
// An environment variable was the obvious alternative and is worse: it makes a
// property of the product ("conversation is latency-bound") into a property of
// one deployment's config file, so a fresh install gets 29-second replies until
// somebody reads a footnote. There is exactly one right answer per role, so the
// table holds it.
//
// # Why the conversation and the vision roles
//
// The conversation is waited on by a person mid-sentence. Vision is waited on by
// the SAME person, one step further back: the visual check runs inside a turn,
// before the geometry is emitted, so every millisecond it spends is a
// millisecond before the model appears on screen.
//
// Measured 2026-09-09 on the visual check's own contact sheet, qwen3.8-max
// answering one closed question about a picture:
//
//	123000 ms thinking · 1500 ms not
//
// 6562 reasoning tokens against 29 tokens of answer, and the same answer either
// way — correct in both. Deliberating turned a check worth having into one
// nobody could afford to run.
//
// The planner, the executor and the verifier are all better for thinking, and
// the transcriber and speaker are not chat models at all. A role added later
// gets thinking unless somebody puts it in this table, which is the safe
// direction: the cost of wrongly deliberating is latency, and the cost of
// wrongly not is a worse answer.
var latencyBound = map[Role]bool{
	RoleConverse: true,
	RoleVision:   true,
}

// deliberationField is the request field that turns deliberation off, per
// endpoint family.
//
// # Why this is keyed on the HOST rather than sent everywhere
//
// `enable_thinking` is a DashScope extension, not part of the OpenAI wire
// format. Strict endpoints reject a request carrying an unknown parameter
// outright — so sending it unconditionally would trade a slow conversation for
// no conversation at all, on every deployment pointed somewhere else. Failing to
// send it costs latency; sending it where it is not understood costs the turn.
//
// A suffix match on the host, because a provider's regional endpoints are
// separate hostnames under one domain (dashscope.aliyuncs.com,
// dashscope-intl.aliyuncs.com, token-plan.cn-beijing.maas.aliyuncs.com are all
// the same API), and enumerating hosts would go stale the first time one is
// added.
var deliberationField = map[string]string{
	"aliyuncs.com": "enable_thinking",
}

// noDeliberation returns the request field that switches a model's visible
// deliberation off at this endpoint, and whether this endpoint has one.
func noDeliberation(baseURL string) (string, bool) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return "", false
	}
	host := strings.ToLower(u.Hostname())
	for domain, field := range deliberationField {
		if host == domain || strings.HasSuffix(host, "."+domain) {
			return field, true
		}
	}
	return "", false
}

// applyDeliberation adds the field that stops a latency-bound role thinking out
// loud, and says so once when this endpoint offers no way to.
//
// Once, not per call: an operator needs to know their conversation model is
// deliberating, and does not need to be told on every turn.
func (c *OpenAICompatible) applyDeliberation(ctx context.Context, role Role, body map[string]any) {
	if !latencyBound[role] {
		return
	}
	if c.thinkingField != "" {
		body[c.thinkingField] = false
		return
	}
	c.warnedThinking.Do(func() {
		c.log.Warn(ctx, logx.EventLLMDeliberating,
			"role", string(role), "model", c.models[role], "endpoint", c.baseURL,
			"detail", "this endpoint is not known to accept a field that turns model deliberation "+
				"off, so the conversation model may spend seconds thinking before its first word "+
				"(PRD AUD-02 asks for 700ms). If it is OpenAI-compatible and does accept one, add "+
				"its host to deliberationField in internal/llm/deliberation.go")
	})
}
