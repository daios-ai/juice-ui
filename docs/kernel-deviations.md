# Kernel Deviations from Spec

Observed live behaviors that contradict `docs/API.md` or `docs/juice-requirements.md`.
Verified against the production kernel binary on 2026-06-14. The BFF encodes workarounds for each.

---

## 1. Error body has no typed code

**Spec** (`API.md` Design Rules): implies structured error responses.

**Actual behavior**: every error body is `{"error": "<human-readable message>"}` — no machine-readable code field. The BFF classifies errors by HTTP status:

| Status | Meaning |
|--------|---------|
| 401 | unauthenticated |
| 402 | insufficient funds |
| 403 | unauthorized |
| 404 | not found |
| 409 | invalid state |
| 422 | validation error (spec says 400) |
| 429 | rate-limited |
| 500 | execution failed |
| 504 | timeout |

---

## 2. PKCE authorize — redirect_uri changes the response shape

**Spec**: `POST /v1/auth/authorize` returns a redirect URL.

**Actual behavior**: if `redirect_uri` is omitted, the response is `200 {"redirect": "?code=CODE"}` rather than a 302. The BFF deliberately omits `redirect_uri` and parses the code from that JSON field.

---

## 3. List responses may be JSON `null` instead of `[]`

**Spec** (`API.md` R6): _"List responses are plain arrays."_

**Actual behavior**: empty lists can return JSON `null` rather than `[]`. The BFF client unmarshals into a Go slice, which remains `nil` for both — safe, but callers must treat nil and empty as equivalent.

---

## 4. Process identifier field name differs by endpoint

**Spec**: consistent `id` field on the Process resource.

**Actual behavior**:
- `POST /v1/processes` (create) → returns `process_id` (no `id`)
- `GET /v1/processes/{id}` (show) → returns `id` (no `process_id`)

**BFF workaround**: `client.Process.Ident()` returns whichever field is non-empty.

---

## 5. Steps `action` field has double-`@`

**Spec**: `action` field on a Step is `@owner/name`.

**Actual behavior**: `GET /v1/steps/{id}` returns `@@owner/name` (double `@`). The list endpoint `GET /v1/steps` omits the `action` field entirely.

**BFF workaround**: strips the leading `@` when the string starts with `@@`; shows `next_action_id` (truncated) in list views.

---

## 6. Auth endpoints are aggressively rate-limited

Login and token endpoints return 429 after a small number of attempts. The BFF surfaces this as a user-visible error (R5.6 compliance).

---

## 7. Lookup and chat services not configured

`@sys/lookup` → 409 "lookup requires an embedding service"  
`@sys/llm/chat` → 409 "chat service not configured"

Discovery falls back to catalog search; chat is shown as unavailable. Not a kernel bug per se — these require optional infrastructure.
