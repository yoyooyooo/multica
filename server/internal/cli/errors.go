package cli

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"os"
	"strings"
	"syscall"
)

// ErrorKind is a coarse, user-facing classification of an error. The CLI's
// many internal error strings ("resolve issue: ...", raw net/http messages,
// JSON bodies) are not meaningful to end users; FormatError collapses them
// into one of these kinds and renders a friendly, localized message.
//
// The zero value is intentionally KindNetworkTimeout-adjacent only by index;
// always classify explicitly rather than relying on the zero value.
type ErrorKind int

const (
	// Network / transport layer (errors returned by http.Client.Do).
	KindNetworkTimeout ErrorKind = iota // context deadline exceeded / i/o timeout
	KindNetworkDNS                      // no such host
	KindNetworkRefused                  // connection refused
	KindNetworkTLS                      // x509 / tls handshake failures
	KindNetworkOffline                  // catch-all: host unreachable, reset, etc.

	// HTTP status layer.
	KindAuthRequired // 401
	KindForbidden    // 403
	KindNotFound     // 404
	KindConflict     // 409
	KindValidation   // 400 / 422
	KindRateLimited  // 429
	KindServerError  // 5xx

	// Anything we could not classify.
	KindUnknown
)

// Tiered process exit codes. Stable so users can branch on them in scripts.
const (
	ExitGeneric    = 1 // anything not covered below
	ExitNetwork    = 2 // any KindNetwork*
	ExitAuth       = 3 // 401 / 403
	ExitNotFound   = 4 // 404
	ExitValidation = 5 // 400 / 422
	ExitConflict   = 6 // strict typed product conflicts
)

// IsNetwork reports whether the kind is a transport-layer failure.
func (k ErrorKind) IsNetwork() bool {
	switch k {
	case KindNetworkTimeout, KindNetworkDNS, KindNetworkRefused, KindNetworkTLS, KindNetworkOffline:
		return true
	default:
		return false
	}
}

// String returns a stable, snake_case identifier for the kind. It is used in
// --debug output and is safe to log or branch on; it is not user-facing copy
// (see kindMessages / messageFor for that).
func (k ErrorKind) String() string {
	switch k {
	case KindNetworkTimeout:
		return "network_timeout"
	case KindNetworkDNS:
		return "network_dns"
	case KindNetworkRefused:
		return "network_refused"
	case KindNetworkTLS:
		return "network_tls"
	case KindNetworkOffline:
		return "network_offline"
	case KindAuthRequired:
		return "auth_required"
	case KindForbidden:
		return "forbidden"
	case KindNotFound:
		return "not_found"
	case KindConflict:
		return "conflict"
	case KindValidation:
		return "validation"
	case KindRateLimited:
		return "rate_limited"
	case KindServerError:
		return "server_error"
	case KindUnknown:
		return "unknown"
	default:
		return fmt.Sprintf("ErrorKind(%d)", int(k))
	}
}

// NetworkError wraps a transport-layer error (the error returned by
// http.Client.Do, before any HTTP status is available). It strips the raw
// URL out of the user-facing message while preserving the original error for
// --debug output and errors.Is/As inspection.
type NetworkError struct {
	Kind ErrorKind
	Op   string // e.g. "GET /api/issues/abc" — shown only in --debug
	Err  error  // the original net/http error
}

func (e *NetworkError) Error() string {
	if e.Op != "" {
		return fmt.Sprintf("%s: %s", e.Op, e.Err.Error())
	}
	return e.Err.Error()
}

func (e *NetworkError) Unwrap() error { return e.Err }

// UserMessageError attaches a command-specific, user-facing message to an
// underlying error. FormatError shows Msg verbatim (in preference to the
// generic kind-based copy it would otherwise derive from a wrapped
// *NetworkError / *HTTPError), so command-level guidance — e.g. a `multica
// login` failure that is more helpful than the generic 401/timeout line — is
// visible in the default (non-debug) output.
//
// It preserves Unwrap(), so ExitCodeFor still classifies by the underlying
// typed error and --debug still prints the full original chain.
type UserMessageError struct {
	Msg string
	Err error
}

func (e *UserMessageError) Error() string {
	if e.Err != nil {
		return e.Msg + ": " + e.Err.Error()
	}
	return e.Msg
}

func (e *UserMessageError) Unwrap() error { return e.Err }

// ProductError is a strict, safe product-level error envelope. It is only
// constructed when the endpoint, content type, status, and code all match the
// coordination contract; generic or malformed HTTP errors keep legacy handling.
type ProductError struct {
	StatusCode int
	Code       string
	Message    string
}

func (e *ProductError) Error() string { return e.Code + ": " + e.Message }

func (e *ProductError) JSON() string {
	payload := struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}{}
	payload.Error.Code = e.Code
	payload.Error.Message = e.Message
	encoded, _ := json.Marshal(payload)
	return string(encoded)
}

var coordinationStatusByCode = map[string]int{
	"coordination_not_found":                 http.StatusNotFound,
	"coordination_cross_workspace":           http.StatusForbidden,
	"coordination_forbidden":                 http.StatusForbidden,
	"coordination_invalid_payload":           http.StatusBadRequest,
	"coordination_capacity_exceeded":         http.StatusConflict,
	"coordination_self_dependency":           http.StatusUnprocessableEntity,
	"coordination_cycle":                     http.StatusUnprocessableEntity,
	"coordination_revision_conflict":         http.StatusConflict,
	"coordination_idempotency_conflict":      http.StatusConflict,
	"coordination_dependency_scope_conflict": http.StatusConflict,
	"coordination_delete_blocked":            http.StatusConflict,
	"coordination_internal":                  http.StatusInternalServerError,
}

// CoordinationProductError upgrades a strict, route-allowlisted coordination
// envelope to a ProductError. Every mismatch returns the original error.
func CoordinationProductError(err error) error {
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || !coordinationRouteRegistered(httpErr.Method, httpErr.Path) {
		return err
	}
	mediaType, _, parseErr := mime.ParseMediaType(httpErr.ContentType)
	if parseErr != nil || mediaType != "application/json" {
		return err
	}
	var envelope struct {
		Error struct {
			Code    string          `json:"code"`
			Message string          `json:"message"`
			Details json.RawMessage `json:"details,omitempty"`
		} `json:"error"`
	}
	if shapeErr := validateCoordinationJSONShape([]byte(httpErr.Body)); shapeErr != nil {
		return err
	}
	decoder := json.NewDecoder(strings.NewReader(httpErr.Body))
	decoder.DisallowUnknownFields()
	if decodeErr := decoder.Decode(&envelope); decodeErr != nil {
		return err
	}
	var trailing any
	if decodeErr := decoder.Decode(&trailing); !errors.Is(decodeErr, io.EOF) {
		return err
	}
	expectedStatus, known := coordinationStatusByCode[envelope.Error.Code]
	if !known || expectedStatus != httpErr.StatusCode || !coordinationRouteAllowsCode(httpErr.Method, httpErr.Path, envelope.Error.Code) || envelope.Error.Message == "" || len(envelope.Error.Message) > 1024 || strings.TrimSpace(envelope.Error.Message) != envelope.Error.Message {
		return err
	}
	if len(envelope.Error.Details) > 0 && string(envelope.Error.Details) != "null" {
		var details map[string]json.RawMessage
		if json.Unmarshal(envelope.Error.Details, &details) != nil || len(details) != 0 {
			return err
		}
	}
	return &ProductError{StatusCode: httpErr.StatusCode, Code: envelope.Error.Code, Message: envelope.Error.Message}
}

func coordinationRouteRegistered(method, rawPath string) bool {
	path := strings.SplitN(rawPath, "?", 2)[0]
	switch {
	case method == http.MethodPost && path == "/api/coordination/scopes":
		return true
	case method == http.MethodGet && path == "/api/coordination/scopes/by-root":
		return true
	case (method == http.MethodPost || method == http.MethodGet) && isCoordinationDependencyCollectionPath(path):
		return true
	case method == http.MethodPost && isCoordinationDependencyResolvePath(path):
		return true
	case (method == http.MethodPost || method == http.MethodGet) && isCoordinationBlockerCollectionPath(path):
		return true
	case method == http.MethodPost && isCoordinationBlockerResolvePath(path):
		return true
	case method == http.MethodGet && hasOnePathValue(path, "/api/coordination/scopes/"):
		return true
	case method == http.MethodDelete && hasOnePathValue(path, "/api/issues/"):
		return true
	case method == http.MethodPost && path == "/api/issues/batch-delete":
		return true
	case method == http.MethodDelete && hasOnePathValue(path, "/api/workspaces/"):
		return true
	default:
		return false
	}
}

func coordinationRouteAllowsCode(method, rawPath, code string) bool {
	path := strings.SplitN(rawPath, "?", 2)[0]
	baseCodes := map[string]struct{}{
		"coordination_not_found":       {},
		"coordination_cross_workspace": {},
		"coordination_forbidden":       {},
		"coordination_invalid_payload": {},
		"coordination_internal":        {},
	}
	if _, ok := baseCodes[code]; ok {
		return true
	}
	if method == http.MethodPost && path == "/api/coordination/scopes" {
		return code == "coordination_idempotency_conflict"
	}
	if isCoordinationDependencyCollectionPath(path) {
		if method == http.MethodGet {
			return code == "coordination_revision_conflict"
		}
		if method == http.MethodPost {
			switch code {
			case "coordination_capacity_exceeded", "coordination_revision_conflict", "coordination_idempotency_conflict", "coordination_dependency_scope_conflict", "coordination_self_dependency", "coordination_cycle":
				return true
			}
		}
	}
	if method == http.MethodPost && isCoordinationDependencyResolvePath(path) {
		switch code {
		case "coordination_revision_conflict", "coordination_idempotency_conflict", "coordination_dependency_scope_conflict":
			return true
		}
	}
	if isCoordinationBlockerCollectionPath(path) {
		if method == http.MethodGet {
			return code == "coordination_revision_conflict"
		}
		if method == http.MethodPost {
			switch code {
			case "coordination_capacity_exceeded", "coordination_revision_conflict", "coordination_idempotency_conflict", "coordination_dependency_scope_conflict":
				return true
			}
		}
	}
	if method == http.MethodPost && isCoordinationBlockerResolvePath(path) {
		return code == "coordination_revision_conflict" || code == "coordination_idempotency_conflict"
	}
	if method == http.MethodDelete && hasOnePathValue(path, "/api/issues/") {
		return code == "coordination_delete_blocked"
	}
	if method == http.MethodPost && path == "/api/issues/batch-delete" {
		return code == "coordination_delete_blocked"
	}
	if method == http.MethodDelete && hasOnePathValue(path, "/api/workspaces/") {
		return code == "coordination_delete_blocked"
	}
	return false
}

func isCoordinationDependencyCollectionPath(path string) bool {
	parts, ok := coordinationPathParts(path)
	return ok && len(parts) == 5 && parts[0] == "api" && parts[1] == "coordination" && parts[2] == "scopes" && parts[3] != "" && parts[4] == "dependencies"
}

func isCoordinationDependencyResolvePath(path string) bool {
	parts, ok := coordinationPathParts(path)
	return ok && len(parts) == 7 && parts[0] == "api" && parts[1] == "coordination" && parts[2] == "scopes" && parts[3] != "" && parts[4] == "dependencies" && parts[5] != "" && parts[6] == "resolve"
}

func isCoordinationBlockerCollectionPath(path string) bool {
	parts, ok := coordinationPathParts(path)
	return ok && len(parts) == 5 && parts[0] == "api" && parts[1] == "coordination" && parts[2] == "scopes" && parts[3] != "" && parts[4] == "blockers"
}

func isCoordinationBlockerResolvePath(path string) bool {
	parts, ok := coordinationPathParts(path)
	return ok && len(parts) == 7 && parts[0] == "api" && parts[1] == "coordination" && parts[2] == "scopes" && parts[3] != "" && parts[4] == "blockers" && parts[5] != "" && parts[6] == "resolve"
}

func coordinationPathParts(path string) ([]string, bool) {
	if !strings.HasPrefix(path, "/") || path == "/" || strings.HasSuffix(path, "/") || strings.Contains(path, "//") {
		return nil, false
	}
	return strings.Split(strings.TrimPrefix(path, "/"), "/"), true
}

func hasOnePathValue(path, prefix string) bool {
	if !strings.HasPrefix(path, prefix) {
		return false
	}
	value := strings.TrimPrefix(path, prefix)
	return value != "" && !strings.Contains(value, "/")
}

// DecodeStrictCoordinationJSON rejects duplicate keys, unknown fields, trailing
// values, and malformed JSON before a coordination CLI request is sent.
func DecodeStrictCoordinationJSON(data []byte, dst any) error {
	if err := validateCoordinationJSONShape(data); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return err
	}
	return nil
}

func validateCoordinationJSONShape(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	first, err := decoder.Token()
	if err != nil {
		return err
	}
	if err := consumeCoordinationJSONValue(decoder, first); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return err
	}
	return nil
}

func consumeCoordinationJSONValue(decoder *json.Decoder, token json.Token) error {
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]struct{}{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("invalid JSON object key")
			}
			if _, duplicate := seen[key]; duplicate {
				return errors.New("duplicate JSON object key")
			}
			seen[key] = struct{}{}
			valueToken, err := decoder.Token()
			if err != nil {
				return err
			}
			if err := consumeCoordinationJSONValue(decoder, valueToken); err != nil {
				return err
			}
		}
		_, err := decoder.Token()
		return err
	case '[':
		for decoder.More() {
			valueToken, err := decoder.Token()
			if err != nil {
				return err
			}
			if err := consumeCoordinationJSONValue(decoder, valueToken); err != nil {
				return err
			}
		}
		_, err := decoder.Token()
		return err
	default:
		return errors.New("unexpected JSON delimiter")
	}
}

// WithUserMessage wraps err with a user-facing message that FormatError will
// surface by default. It returns nil when err is nil so it can be used inline
// in a `return` without an extra check.
func WithUserMessage(msg string, err error) error {
	if err == nil {
		return nil
	}
	return &UserMessageError{Msg: msg, Err: err}
}

// Kind maps an HTTPError's status code onto an ErrorKind.
func (e *HTTPError) Kind() ErrorKind {
	switch e.StatusCode {
	case 401:
		return KindAuthRequired
	case 403:
		return KindForbidden
	case 404:
		return KindNotFound
	case 409:
		return KindConflict
	case 400, 422:
		return KindValidation
	case 429:
		return KindRateLimited
	default:
		if e.StatusCode >= 500 {
			return KindServerError
		}
		return KindUnknown
	}
}

// classifyNetworkError inspects a transport-layer error and returns the
// matching network ErrorKind. It prefers typed inspection (errors.As /
// errors.Is) and falls back to string matching for cases the standard library
// does not expose as distinct types.
func classifyNetworkError(err error) ErrorKind {
	if err == nil {
		return KindUnknown
	}

	// Timeouts (context deadline or socket i/o timeout).
	if errors.Is(err, context.DeadlineExceeded) {
		return KindNetworkTimeout
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return KindNetworkTimeout
	}

	// DNS resolution failures.
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return KindNetworkDNS
	}

	// TLS / certificate failures.
	var certVerifyErr *tls.CertificateVerificationError
	if errors.As(err, &certVerifyErr) {
		return KindNetworkTLS
	}
	var unknownAuthorityErr x509.UnknownAuthorityError
	if errors.As(err, &unknownAuthorityErr) {
		return KindNetworkTLS
	}
	var hostnameErr x509.HostnameError
	if errors.As(err, &hostnameErr) {
		return KindNetworkTLS
	}
	var certInvalidErr x509.CertificateInvalidError
	if errors.As(err, &certInvalidErr) {
		return KindNetworkTLS
	}

	// Connection refused.
	if errors.Is(err, syscall.ECONNREFUSED) {
		return KindNetworkRefused
	}

	// String fallbacks for anything not surfaced as a typed error.
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "context deadline exceeded"), strings.Contains(msg, "timeout"), strings.Contains(msg, "timed out"):
		return KindNetworkTimeout
	case strings.Contains(msg, "no such host"), strings.Contains(msg, "server misbehaving"), strings.Contains(msg, "name resolution"):
		return KindNetworkDNS
	case strings.Contains(msg, "connection refused"):
		return KindNetworkRefused
	case strings.Contains(msg, "x509"), strings.Contains(msg, "certificate"), strings.Contains(msg, "tls"):
		return KindNetworkTLS
	}
	return KindNetworkOffline
}

// wrapTransport converts a raw transport error returned by http.Client.Do
// into a *NetworkError. It returns nil when err is nil so call sites can
// reassign unconditionally:
//
//	resp, err := c.HTTPClient.Do(req)
//	err = wrapTransport(req, err)
//	if err != nil { return err }
func wrapTransport(req *http.Request, err error) error {
	if err == nil {
		return nil
	}
	op := ""
	if req != nil && req.URL != nil {
		op = req.Method + " " + req.URL.Path
	}
	return &NetworkError{Kind: classifyNetworkError(err), Op: op, Err: err}
}

// Language is the language FormatError renders messages in.
type Language int

const (
	LangEN Language = iota
	LangZH
)

// DetectLanguage chooses the output language from the environment. English is
// the default (matching the CLI's help output); a Chinese locale in LC_ALL,
// LC_MESSAGES, or LANG (in that precedence order) switches to Chinese.
func DetectLanguage() Language {
	for _, key := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		v := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
		if v == "" {
			continue
		}
		if strings.HasPrefix(v, "zh") {
			return LangZH
		}
		// First locale variable that is set wins; if it is not Chinese we
		// fall through to English without consulting lower-precedence vars.
		return LangEN
	}
	return LangEN
}

// kindMessages holds the {English, Chinese} user-facing message for each kind.
var kindMessages = map[ErrorKind][2]string{
	KindNetworkTimeout: {
		"Request timed out: the server did not respond in time. Check your network connection or try again later. You can raise the limit with MULTICA_HTTP_TIMEOUT.",
		"请求超时：服务器未在规定时间内响应。请检查网络连接或稍后重试。可通过 MULTICA_HTTP_TIMEOUT 调高超时时间。",
	},
	KindNetworkDNS: {
		"Could not resolve the Multica server address. Check your network connection or the --server-url setting.",
		"无法解析 Multica 服务器地址。请检查网络连接或 --server-url 配置。",
	},
	KindNetworkRefused: {
		"Could not connect to the Multica server. Make sure the server address is correct and reachable.",
		"无法连接到 Multica 服务器。请确认服务器地址正确且网络可达。",
	},
	KindNetworkTLS: {
		"Could not establish a secure connection to the Multica server (TLS/certificate error). Check your system clock and CA certificates.",
		"无法与 Multica 服务器建立安全连接（TLS/证书错误）。请检查系统时间和 CA 证书。",
	},
	KindNetworkOffline: {
		"Could not reach the Multica server. Check your network connection.",
		"无法访问 Multica 服务器。请检查网络连接。",
	},
	KindAuthRequired: {
		"Your session has expired or you are not signed in. Run `multica login` to sign in again. On a self-hosted or non-OAuth setup, ask your administrator for valid credentials.",
		"登录已过期或尚未登录。请运行 `multica login` 重新登录。自托管或非 OAuth 场景请联系管理员获取有效凭证。",
	},
	KindForbidden: {
		"You do not have permission to access this resource. Check that you are in the right workspace, or ask an administrator to grant access.",
		"无权访问该资源。请确认当前 workspace 是否正确，或联系管理员授予权限。",
	},
	KindNotFound: {
		"The requested resource was not found. Check the ID, or run the matching `list` command to see what exists in this workspace.",
		"未找到请求的资源。请核对 ID，或运行对应的 list 命令查看当前 workspace 中已有的内容。",
	},
	KindConflict: {
		"The request conflicts with the current state of the resource (it may already exist or have changed since you last fetched it). Re-fetch the latest state and try again.",
		"请求与资源的当前状态冲突（可能已存在，或自上次获取后已被修改）。请重新获取最新状态后再试。",
	},
	KindValidation: {
		"The request was invalid. Check the values you provided; run the command with --help to see the expected format.",
		"请求无效。请检查所填写的参数；可用 --help 查看期望的格式。",
	},
	KindRateLimited: {
		"Too many requests. Please wait a moment and try again; if this keeps happening, reduce how frequently you call the API.",
		"请求过于频繁。请稍候重试；若持续出现，请降低 API 调用频率。",
	},
	KindServerError: {
		"The Multica service is temporarily unavailable (server error). Please try again later; if it persists, contact support. Re-run with --debug to see the raw server response.",
		"Multica 服务暂时不可用（服务器错误）。请稍后重试；若持续出现请联系支持。可加 --debug 查看服务器原始响应。",
	},
	KindUnknown: {
		"An unexpected error occurred.",
		"发生未知错误。",
	},
}

// messageFor returns the localized message for a kind.
func messageFor(kind ErrorKind, lang Language) string {
	m, ok := kindMessages[kind]
	if !ok {
		m = kindMessages[KindUnknown]
	}
	if lang == LangZH {
		return m[1]
	}
	return m[0]
}

// FormatError translates an error into a single user-facing line (or a
// detailed multi-line block when debug is set). It is the only user-facing
// translation entry point and is meant to be called once, at the top level
// (main.go), on the error bubbling up from a command.
//
// When debug is false it skips the internal verb chain ("resolve issue: ...")
// and the raw URL/JSON body, showing only the friendly message. When debug is
// true (or MULTICA_DEBUG is set) it additionally prints the full original
// error chain for troubleshooting.
func FormatError(err error, debug bool) string {
	if err == nil {
		return ""
	}
	var productErr *ProductError
	if errors.As(err, &productErr) {
		return productErr.JSON()
	}
	lang := DetectLanguage()
	base := userMessage(err, lang)
	if debug || debugEnabled() {
		return base + "\n\n" + debugDetail(err)
	}
	return base
}

// userMessage produces the friendly message for the root cause of err.
func userMessage(err error, lang Language) string {
	// A command-supplied user-facing message takes precedence over the generic
	// kind-based copy, so command-specific guidance (e.g. sign-in failures) is
	// visible by default. Unwrap() is preserved, so ExitCodeFor and --debug
	// still see the underlying typed error.
	var um *UserMessageError
	if errors.As(err, &um) {
		return um.Msg
	}

	// Transport-layer failure.
	var netErr *NetworkError
	if errors.As(err, &netErr) {
		return messageFor(netErr.Kind, lang)
	}

	// HTTP status failure.
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		kind := httpErr.Kind()
		// Validation errors usually carry a useful server-provided message;
		// surface it instead of the generic line.
		if kind == KindValidation {
			if serverMsg := extractServerMessage(httpErr.Body); serverMsg != "" {
				if lang == LangZH {
					return "请求无效：" + serverMsg
				}
				return "Invalid request: " + serverMsg
			}
		}
		return messageFor(kind, lang)
	}

	// Not a recognized typed error: this is typically a local/business error
	// whose message is already meant for the user (e.g. a missing argument or
	// a validation message constructed in a command). Show it as-is.
	return strings.TrimSpace(err.Error())
}

// extractServerMessage tries to pull a human-readable message out of a JSON
// error body like {"error":"..."} or {"message":"..."}. Returns "" if the
// body is not JSON or has no recognizable message field.
func extractServerMessage(body string) string {
	body = strings.TrimSpace(body)
	if body == "" || body[0] != '{' {
		return ""
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		return ""
	}
	for _, key := range []string{"error", "message", "detail", "title"} {
		if v, ok := parsed[key]; ok {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s)
			}
		}
	}
	return ""
}

// debugDetail renders the full original error chain plus any structured
// details from typed errors, for --debug / MULTICA_DEBUG output.
func debugDetail(err error) string {
	var sb strings.Builder
	sb.WriteString("[debug] ")
	sb.WriteString(err.Error())

	var netErr *NetworkError
	if errors.As(err, &netErr) {
		fmt.Fprintf(&sb, "\n[debug] network: op=%q kind=%s cause=%v", netErr.Op, netErr.Kind, netErr.Err)
	}
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		fmt.Fprintf(&sb, "\n[debug] http: %s %s status=%d body=%s",
			httpErr.Method, httpErr.Path, httpErr.StatusCode, strings.TrimSpace(httpErr.Body))
	}
	return sb.String()
}

// debugEnabled reports whether MULTICA_DEBUG requests debug output.
func debugEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("MULTICA_DEBUG"))) {
	case "", "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

// ExitCodeFor maps an error onto a tiered process exit code so callers can
// branch in scripts: network=2, auth(401/403)=3, not-found(404)=4,
// validation(400/422)=5, everything else=1.
func ExitCodeFor(err error) int {
	if err == nil {
		return 0
	}

	var productErr *ProductError
	if errors.As(err, &productErr) {
		switch productErr.StatusCode {
		case http.StatusForbidden:
			return ExitAuth
		case http.StatusNotFound:
			return ExitNotFound
		case http.StatusBadRequest, http.StatusUnprocessableEntity:
			return ExitValidation
		case http.StatusConflict:
			return ExitConflict
		default:
			return ExitGeneric
		}
	}

	var netErr *NetworkError
	if errors.As(err, &netErr) {
		return ExitNetwork
	}

	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		// Coordination endpoints only get the special exit classes after a strict
		// ProductError upgrade. Legacy bodies, unknown codes, and status/code
		// mismatches must keep the safe fallback exit 1.
		if strings.HasPrefix(httpErr.Path, "/api/coordination/") {
			switch httpErr.StatusCode {
			case http.StatusBadRequest, http.StatusUnprocessableEntity:
				return ExitValidation
			default:
				return ExitGeneric
			}
		}
		switch httpErr.Kind() {
		case KindAuthRequired, KindForbidden:
			return ExitAuth
		case KindNotFound:
			return ExitNotFound
		case KindValidation:
			return ExitValidation
		default:
			return ExitGeneric
		}
	}

	return ExitGeneric
}
