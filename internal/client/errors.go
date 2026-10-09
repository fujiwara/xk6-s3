package client

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"strings"

	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

// Error kinds.
const (
	KindS3       = "s3"
	KindHTTP     = "http"
	KindTimeout  = "timeout"
	KindNetwork  = "network"
	KindCanceled = "canceled"
	KindOther    = "other"
)

// unknownErrorCode is the code the SDK uses when it cannot parse an error response.
const unknownErrorCode = "UnknownError"

// synthesizedCode returns the error code the SDK derives from the HTTP status
// text when the response has no parsable error body (e.g. "NotFound" for a
// HEAD request).
func synthesizedCode(status int) string {
	return strings.ReplaceAll(http.StatusText(status), " ", "")
}

// ErrorInfo is the classification of an operation error.
type ErrorInfo struct {
	Kind      string
	Code      string
	Status    int
	RequestID string
}

// Classify classifies an error returned by the SDK.
// It returns the zero value for a nil error.
func Classify(err error) ErrorInfo {
	if err == nil {
		return ErrorInfo{}
	}
	var info ErrorInfo

	if respErr, ok := errors.AsType[*awshttp.ResponseError](err); ok {
		info.Status = respErr.HTTPStatusCode()
		info.RequestID = respErr.ServiceRequestID()
	} else {
		if smithyRespErr, ok := errors.AsType[*smithyhttp.ResponseError](err); ok {
			info.Status = smithyRespErr.HTTPStatusCode()
		}
	}

	switch {
	case errors.Is(err, context.Canceled):
		info.Kind = KindCanceled
		return info
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, os.ErrDeadlineExceeded):
		info.Kind = KindTimeout
		return info
	}

	if info.Status > 0 {
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) && apiErr.ErrorCode() != "" &&
			apiErr.ErrorCode() != unknownErrorCode && apiErr.ErrorCode() != synthesizedCode(info.Status) {
			info.Kind = KindS3
			info.Code = apiErr.ErrorCode()
		} else {
			info.Kind = KindHTTP
		}
		return info
	}

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		info.Kind = KindTimeout
		return info
	}
	if isNetworkError(err) {
		info.Kind = KindNetwork
		return info
	}
	info.Kind = KindOther
	return info
}

func isNetworkError(err error) bool {
	var (
		netErr  net.Error
		sendErr *smithyhttp.RequestSendError
	)
	// RequestSendError also covers TLS handshake and certificate errors.
	return errors.As(err, &netErr) || errors.As(err, &sendErr)
}
