package client

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

// Kind is the typed classification of a kernel error. The running kernel does
// NOT return a typed error code in the body — verified live, the body is simply
// {"error":"<human message>"} — so the Kind is derived from the HTTP status
// code, with the message preserved for field-level detail and logging.
type Kind int

const (
	KindUnknown           Kind = iota
	KindUnauthenticated        // 401 (missing/invalid/expired token, suspended user)
	KindUnauthorized           // 403
	KindNotFound               // 404
	KindInvalidInput           // 422 (input validation incl. schema violations)
	KindInvalidState           // 409 (process closed, service not configured, ...)
	KindInsufficientFunds      // 402
	KindExecutionFailed        // 500 (action failed after execution began)
	KindTimeout                // 504
	KindRateLimited            // 429 (per-IP auth/user-create limiting, R5.6)
	KindInternal               // 500 (kernel bug / unrecoverable)
)

// KernelError is the typed error returned by every client call on a non-2xx
// response. It never carries kernel tokens or secrets. The raw HTTP status is
// recorded for logs but is never shown to the user (R7.1, R7.2).
type KernelError struct {
	Kind          Kind
	Status        int
	KernelMessage string // raw {"error":...} text; safe to log, used as form detail
	Field         string // best-effort offending field for 422 validation errors
}

func (e *KernelError) Error() string {
	return fmt.Sprintf("kernel error kind=%s status=%d msg=%q", e.Kind, e.Status, e.KernelMessage)
}

// Is matches sentinel errors by Kind so handlers can write
// errors.Is(err, ErrNotFound) regardless of the status/message detail.
func (e *KernelError) Is(target error) bool {
	t, ok := target.(*KernelError)
	if !ok {
		return false
	}
	return t.Kind == e.Kind
}

// Sentinel errors for errors.Is matching.
var (
	ErrUnauthenticated   = &KernelError{Kind: KindUnauthenticated}
	ErrUnauthorized      = &KernelError{Kind: KindUnauthorized}
	ErrNotFound          = &KernelError{Kind: KindNotFound}
	ErrInvalidInput      = &KernelError{Kind: KindInvalidInput}
	ErrInvalidState      = &KernelError{Kind: KindInvalidState}
	ErrInsufficientFunds = &KernelError{Kind: KindInsufficientFunds}
	ErrExecutionFailed   = &KernelError{Kind: KindExecutionFailed}
	ErrTimeout           = &KernelError{Kind: KindTimeout}
	ErrRateLimited       = &KernelError{Kind: KindRateLimited}
	ErrInternal          = &KernelError{Kind: KindInternal}
)

func (k Kind) String() string {
	switch k {
	case KindUnauthenticated:
		return "unauthenticated"
	case KindUnauthorized:
		return "unauthorized"
	case KindNotFound:
		return "not_found"
	case KindInvalidInput:
		return "invalid_input"
	case KindInvalidState:
		return "invalid_state"
	case KindInsufficientFunds:
		return "insufficient_funds"
	case KindExecutionFailed:
		return "execution_failed"
	case KindTimeout:
		return "timeout"
	case KindRateLimited:
		return "rate_limited"
	case KindInternal:
		return "internal"
	default:
		return "unknown"
	}
}

// fieldRe extracts the offending field from kernel validation messages such as
// "field #.query: required field missing" (verified live).
var fieldRe = regexp.MustCompile(`field #\.([^:\s]+)`)

// classify maps an HTTP status and raw error body to a *KernelError. It returns
// nil for 2xx responses. Body is the raw response payload; the kernel encodes
// errors as {"error":"<message>"}.
func classify(status int, body []byte) error {
	if status >= 200 && status < 300 {
		return nil
	}
	var wire struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(body, &wire)
	msg := strings.TrimSpace(wire.Error)
	if msg == "" {
		msg = strings.TrimSpace(string(body))
	}

	e := &KernelError{Status: status, KernelMessage: msg}
	switch status {
	case http.StatusUnauthorized:
		e.Kind = KindUnauthenticated
	case http.StatusPaymentRequired:
		e.Kind = KindInsufficientFunds
	case http.StatusForbidden:
		e.Kind = KindUnauthorized
	case http.StatusNotFound:
		e.Kind = KindNotFound
	case http.StatusConflict:
		e.Kind = KindInvalidState
	case http.StatusUnprocessableEntity, http.StatusBadRequest:
		e.Kind = KindInvalidInput
		if m := fieldRe.FindStringSubmatch(msg); m != nil {
			e.Field = m[1]
		}
	case http.StatusTooManyRequests:
		e.Kind = KindRateLimited
	case http.StatusGatewayTimeout:
		e.Kind = KindTimeout
	case http.StatusInternalServerError:
		e.Kind = KindExecutionFailed
	default:
		e.Kind = KindInternal
	}
	return e
}

// UserMessage returns an actionable, user-facing message. It never includes the
// raw HTTP status (R7.1). For input and state errors the kernel's own message
// is surfaced because it is human-readable and contains no secrets (e.g. "name
// is required", "lookup requires an embedding service"); other classes get a
// fixed actionable message and the kernel detail is confined to logs (R7.2).
func (e *KernelError) UserMessage() string {
	switch e.Kind {
	case KindUnauthenticated:
		return "Your session has expired. Please sign in again."
	case KindUnauthorized:
		return "You don't have permission to do that."
	case KindNotFound:
		return "That item could not be found."
	case KindInvalidInput:
		if e.KernelMessage != "" {
			return "Please check your input: " + e.KernelMessage
		}
		return "Please check your input and try again."
	case KindInvalidState:
		if e.KernelMessage != "" {
			return e.KernelMessage
		}
		return "This action isn't available right now."
	case KindInsufficientFunds:
		return "This session doesn't have enough credits. Credits are added out of band by an operator — contact them to top up your account."
	case KindExecutionFailed:
		return "The action failed to run. You were not charged — your funds were fully refunded."
	case KindTimeout:
		return "The action timed out. You were not charged — your funds were fully refunded."
	case KindRateLimited:
		return "Too many attempts. Please wait a moment and try again."
	default:
		return "Something went wrong. Please try again."
	}
}

// AsKernelError extracts a *KernelError from err, if present.
func AsKernelError(err error) (*KernelError, bool) {
	ke, ok := err.(*KernelError)
	return ke, ok
}
