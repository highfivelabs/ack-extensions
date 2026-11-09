// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package resource

import (
	"errors"
	"fmt"

	ackerr "github.com/aws-controllers-k8s/runtime/pkg/errors"
	cctypes "github.com/aws/aws-sdk-go-v2/service/cloudcontrol/types"
	"github.com/aws/smithy-go"
)

// HandlerError is a Cloud Control request that ended FAILED: the
// resource handler's error code (HandlerErrorCode) and message. It is a
// smithy.APIError so ACK's runtime treats it as an AWS error (a create
// that fails leaves the object unmanaged, as for ACK's generated kinds).
type HandlerError struct {
	Operation, Code, Message string
}

func (e *HandlerError) Error() string {
	return fmt.Sprintf("Cloud Control %s failed: %s: %s", e.Operation, e.Code, e.Message)
}
func (e *HandlerError) ErrorCode() string             { return e.Code }
func (e *HandlerError) ErrorMessage() string          { return e.Message }
func (e *HandlerError) ErrorFault() smithy.ErrorFault { return smithy.FaultServer }

var _ smithy.APIError = &HandlerError{}

// terminalHandlerCodes are handler errors that a retry cannot fix: the
// spec must change. Everything else (AccessDenied, Throttling,
// NotStabilized, ServiceInternalError, ...) is recoverable: a role may be
// granted, a limit raised, a dependency may become ready.
var terminalHandlerCodes = map[string]bool{
	string(cctypes.HandlerErrorCodeInvalidRequest): true,
	string(cctypes.HandlerErrorCodeNotUpdatable):   true,
	string(cctypes.HandlerErrorCodeAlreadyExists):  true,
	"UnsupportedTarget":                            true,
}

// terminalAPICodes are the Cloud Control API's own exceptions that a
// retry cannot fix.
var terminalAPICodes = map[string]bool{
	"AlreadyExistsException":       true,
	"InvalidRequestException":      true,
	"TypeNotFoundException":        true,
	"UnsupportedActionException":   true,
	"ClientTokenConflictException": true,
	"NotUpdatableException":        true,
	"PrivateTypeException":         true,
	"ValidationException":          true,
}

func apiCode(err error) string {
	var ae smithy.APIError
	if errors.As(err, &ae) {
		return ae.ErrorCode()
	}
	return ""
}

func isNotFound(err error) bool {
	c := apiCode(err)
	return c == "ResourceNotFoundException" || c == string(cctypes.HandlerErrorCodeNotFound)
}

// classify makes err terminal when no retry can fix it.
func classify(err error) error {
	if err == nil {
		return nil
	}
	var he *HandlerError
	if errors.As(err, &he) && terminalHandlerCodes[he.Code] {
		return ackerr.NewTerminalError(err)
	}
	var co *CreateOnlyChange
	if errors.As(err, &co) {
		return ackerr.NewTerminalError(err)
	}
	if terminalAPICodes[apiCode(err)] {
		return ackerr.NewTerminalError(err)
	}
	return err
}
