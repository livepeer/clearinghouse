package tokenexchange

import "fmt"

// Error is an RFC 6749 / RFC 8693 token endpoint error.
type Error struct {
	Code              string
	Description       string
	PublicDescription string
	Status            int
}

func (e *Error) Error() string {
	if e.PublicDescription != "" {
		return e.PublicDescription
	}
	if e.Description != "" {
		return e.Description
	}
	return e.Code
}

func newError(code, description, public string, status int) *Error {
	return &Error{
		Code:              code,
		Description:       description,
		PublicDescription: public,
		Status:            status,
	}
}

func invalidRequest(description string) *Error {
	return newError("invalid_request", description, description, 400)
}

func invalidClient(description string) *Error {
	return newError("invalid_client", description, description, 401)
}

func invalidGrant(description string) *Error {
	return newError("invalid_grant", description, description, 400)
}

func invalidTarget(description string) *Error {
	return newError("invalid_target", description, description, 400)
}

func unsupportedTokenType(description string) *Error {
	return newError("unsupported_token_type", description, description, 400)
}

func insufficientAllowance(description string) *Error {
	return newError("insufficient_allowance", description, description, 402)
}

func serverError(description string) *Error {
	return newError("server_error", description, description, 500)
}

// wrapServerError keeps the underlying failure in Description for logs and
// returns a generic PublicDescription. The wrapped error can carry upstream
// URLs and response bodies, and Error() — which is what the token endpoint
// renders — prefers PublicDescription.
func wrapServerError(err error) *Error {
	return newError("server_error", fmt.Sprintf("internal error: %v", err), "internal error", 500)
}
