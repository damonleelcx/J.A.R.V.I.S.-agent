# The provider retired the models behind three roles, and it read as an unpaid bill

**Found** 2026-09-06. **Partly fixed** the same day; the audio half is a
deployment decision and is still open (see below).
**Fence:** `internal/llm/deliberation_test.go`.

## What was seen

`make eval` failed, every live agent test failed, and the workbench could not
hold a conversation. The provider's reply, in full:

```json
{"error":{"message":"Model not exist.","code":"model_not_found"}}
```

The implementation plan recorded this under **"Operational — blocking"** as
*"The model provider account is in arrears — `Arrearage` / overdue payment, then
`Model not exist`"*, and named it as the thing preventing every measurement in
the evaluation section.

## What was actually happening

The account was fine. `GET /models` on the same host with the same key answered
200 and listed twelve models. What had gone was **`qwen-plus`**.

Every other default in `config.go` had been moved to the 3.8 generation at some
earlier point. `Converse` had not — so the only role a person actually waits on
was the only one pointing at a model that no longer existed. Two more roles were
in the same state and nothing had noticed, because their tests only run when
`FORGE_LLM_API_KEY` is set:

| role | configured | on this endpoint |
|---|---|---|
| converse | `qwen-plus` | gone |
| transcriber | `qwen3-asr-flash-2026-02-10` | gone |
| speaker | `qwen3-omni-flash` | gone |

## Impact

Total, for conversation. And the diagnosis was wrong in a way that cost a day:
"Model not exist" beside a memory of an overdue invoice reads as an unpaid bill,
and an unpaid bill is not something a developer can fix by reading code.

## The fix

**The model.** `FORGE_LLM_CONVERSE_MODEL` defaults to `qwen3.7-plus`.

**The message.** A 404 now ASKS the endpoint what it serves and says so — one
GET on a path that has already failed permanently. The same sentence is used by
the transcription and speech paths, which classify their own errors and would
otherwise have been left behind: `whatIsServed` in `openai_compatible.go`.

**The latency, which the swap exposed.** Every model this endpoint now serves is
a reasoning model and deliberates by default. Measured on the eval suite's own
bracket prompt, time to the first *content* token:

| model | thinking | not |
|---|---|---|
| qwen3.7-plus | 28 693 ms | 735 ms |
| qwen3.8-flash | 3 140 ms | 507 ms |
| qwen3.6-flash | 15 538 ms | 270 ms |
| qwen3.8-max | 4 119 ms | 486 ms |

PRD AUD-02 asks for first audio inside 700 ms. Nothing was broken — the tokens
were produced, they simply arrived on `reasoning_content`, which this client has
always ignored and should keep ignoring. The reply just took half a minute to
begin, silently.

`internal/llm/deliberation.go` turns deliberation off for the conversation role
and only for it, and only at endpoints known to understand the field — it is a
DashScope extension, and a strict endpoint rejects a request carrying an unknown
parameter outright, which would trade a slow conversation for no conversation.

## What is still OPEN

**This endpoint serves no reachable ASR or TTS model.** It offers
`qwen-audio-3.0-realtime-plus` and `qwen-audio-3.0-tts-plus`; the first answers
the chat-completions transcription shape with `{"status_message":"Success"}` and
no transcript, and the second returns 500. So `make test-asr` cannot pass here,
and FORGE has no voice on this deployment.

That is a **deployment decision, not a code fix**: either point
`FORGE_LLM_BASE_URL` at an endpoint that serves them (with the key issued for it
— this key is host-specific), or accept that this deployment is text-only and say
so. Nothing has been guessed in the meantime; the config's existing behaviour is
already to declare an absent capability rather than substitute something.

## Regression

`make test` — `internal/llm/deliberation_test.go`, six fences, five mutation
drills in `scripts/drill-fences.sh` under "Latency and the model catalogue",
each confirmed red then restored.

`TestAMissingModelNamesWhatTheEndpointDoesServe` is the one that matters most:
it holds that the not-found message names the survivors, which is what would
have turned this from a day into ten minutes.
