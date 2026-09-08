# CSP blocked her own voice, and it looked like a broken codec

**Found:** 2026-09-08, reported as *"Using the browser's voice instead of
FORGE's: this browser could not decode the audio (audio/mpeg)"*.
**Severity:** high — FORGE never used her own voice on the workbench, on every
browser, since the voice endpoint shipped. The autoplay unlock never ran either.
**Owner:** httpapi (security headers).

## Symptom

Every reply was read in the browser's own voice. `/v1/speech` answered 200 with
real audio and the page said it could not be decoded.

## Root cause

`SecurityHeaders` set:

```
default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; …
```

**No `media-src`.** So media inherited `default-src 'self'`, and both of the
sources FORGE's voice actually uses are not "self":

- the synthesised reply is played through a **`blob:`** URL, and
- the one-time autoplay unlock is a **`data:`** URL.

Both were blocked before any decoder saw them:

```
Loading media from 'blob:https://forge.heros-agent.space/eb51aa63…' violates the
following Content Security Policy directive: "default-src 'self'". Note that
'media-src' was not explicitly set, so 'default-src' is used as a fallback.
ERROR code 4  net 3
```

Somebody had already hit the same edge for images and widened `img-src` with
`data:`. Audio was simply never considered.

## Why it cost so much to find

**The browser reports a blocked media load as `MEDIA_ERR_SRC_NOT_SUPPORTED`
(code 4)** — the same code as a genuinely unplayable file. So the audio was
blamed, and four subsystems were excluded one at a time before anyone looked at
the policy:

| Link | How it was tested | Result |
|---|---|---|
| vendor → server | walked all 479 MP3 frames | valid, 0 stray bytes |
| server → socket | access log `bytes` (counts real `Write` returns) | `status:200 bytes:147956` — every byte written |
| ingress → browser | SHA-256 of 120KB assets, local vs served | byte-identical |
| decoder | played that exact file in Chromium | `decodeAudioData: OK`, `play(): resolved` |

Every layer was sound, because every layer *was* sound.

Two things made it invisible:

1. **The block leaves no server-side trace at all.** 200, `forge.tts.spoke`,
   every byte written to the socket. Nothing anywhere on the server is wrong.
   The only evidence in existence is one console line, in one browser.
2. **`jobs.heros-agent.space` plays the same vendor's audio, from the same Fish
   account and the same voice** (`11-externalsecret.yaml` reads FORGE's key from
   `opportunity-bridge/model` on purpose, so there is one credential to rotate).
   Comparing the two products eliminated the vendor, the voice, the request
   parameters, the response headers and the client shape — correctly. The
   difference was that **jobs sets no CSP at all**, which is not in either
   product's audio code.

The fallback message itself was part of the cost: *"could not decode the audio
(audio/mpeg)"* reported the content type, which was never in doubt, and not the
size, which nobody could see. It asserted a decoder verdict the browser had not
actually reached.

Two wrong theories were pursued and disproved before the console line settled
it: that the endpoint mislabelled PCM as MP3 (the frame walk disproved it), and
that the reporting Chrome needed relaunching after a pending update (a restart
disproved it).

## The fix

```
media-src 'self' blob: data:
```

Stated explicitly, because an inherited default is exactly the bug.

## Verification

`TestCSPAllowsHerVoice` in `internal/httpapi/pages_test.go` asserts the
directive is **present** — not merely permissive — and carries both schemes,
with a message naming what breaks for each. Proven red against the pre-fix
policy:

```
--- FAIL: TestCSPAllowsHerVoice
    the policy states no media-src, so audio falls back to default-src 'self'
    and every blob: and data: source is blocked.
```

Adding a media source that is not `'self'` — a CDN, a vendor streaming URL —
needs a new entry in that directive, and the symptom if you forget will again
look like broken audio rather than policy.

## Related

- `docs/bugfix/2026-09-02-csp-blocked-inline-page-script.md` — the same header,
  the same shape of failure: correct-looking page, silently dead behaviour.
  Second time this policy has broken a feature with no server-side signal.
- `docs/bugfix/2026-09-08-she-said-every-reply-twice.md` — the doubling this
  block triggered downstream, and the diagnostics added so a fallback now
  reports size, declared length, truncation and the media error code.
