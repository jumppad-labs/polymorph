package connect

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"connectrpc.com/connect"
	"github.com/hashicorp/hcl/v2"
	"github.com/jumppad-labs/polymorph/internal/config"
	configconnect "github.com/jumppad-labs/polymorph/internal/config/connect"
	"github.com/jumppad-labs/polymorph/internal/step"
	"github.com/zclconf/go-cty/cty"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

// CustomMethodHandler handles custom Connect-RPC methods with steps
type CustomMethodHandler struct {
	method      *configconnect.Handler
	packageName string
	serviceName string
	prefix      string
	serviceVars map[string]cty.Value
	outputDesc  protoreflect.MessageDescriptor
}

// NewCustomMethodHandler creates a new custom method handler
func NewCustomMethodHandler(method *configconnect.Handler, packageName, serviceName, prefix string, serviceVars map[string]cty.Value) (*CustomMethodHandler, error) {
	return &CustomMethodHandler{
		method:      method,
		packageName: packageName,
		serviceName: serviceName,
		prefix:      prefix,
		serviceVars: serviceVars,
	}, nil
}

// SetOutputDescriptor sets the protobuf output message descriptor for proto-aware responses.
func (h *CustomMethodHandler) SetOutputDescriptor(desc protoreflect.MessageDescriptor) {
	h.outputDesc = desc
}

// RegisterHandler registers this custom method and returns the path and handler function
func (h *CustomMethodHandler) RegisterHandler() (string, http.HandlerFunc) {
	// Create the method path: /api.v1.UserService/MethodName
	methodPath := fmt.Sprintf("%s/%s.%s/%s", h.prefix, h.packageName, h.serviceName, h.method.Name)

	return methodPath, h.handleMethod
}

// handleMethod handles the custom RPC method
func (h *CustomMethodHandler) handleMethod(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		h.writeError(w, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid request: %w", err)))
		return
	}
	defer r.Body.Close() //nolint:errcheck

	// Parse request — for protobuf requests we can't easily decode arbitrary proto
	var req map[string]any
	if !wantsProto(r) {
		if err := json.Unmarshal(body, &req); err != nil {
			req = map[string]any{}
		}
	} else {
		req = map[string]any{}
	}

	evalCtx := buildEvalContext(req, h.serviceVars)

	if len(h.method.Steps) > 0 {
		executor := step.NewExecutor(h.method.Steps)
		if err := executor.Execute(r.Context(), evalCtx); err != nil {
			h.writeError(w, connect.NewError(connect.CodeInternal, fmt.Errorf("step execution failed: %w", err)))
			return
		}
	}

	// Evaluate response body expression to JSON bytes
	var jsonBytes []byte
	if h.method.Response != nil && h.method.Response.BodyExpr != nil {
		value, diags := h.method.Response.BodyExpr.Value(evalCtx)
		if diags.HasErrors() {
			h.writeError(w, connect.NewError(connect.CodeInternal, fmt.Errorf("response evaluation failed: %s", diags.Error())))
			return
		}
		jsonBytes = []byte(value.AsString())
	} else {
		jsonBytes = []byte("{}")
	}

	h.writeResponse(w, r, jsonBytes)
}

// wantsProto returns true when the caller expects a protobuf-encoded response.
func wantsProto(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Content-Type"), "proto")
}

// writeResponse writes a Connect-RPC response, negotiating the wire format.
func (h *CustomMethodHandler) writeResponse(w http.ResponseWriter, r *http.Request, jsonBytes []byte) {
	if wantsProto(r) && h.outputDesc != nil {
		msg := dynamicpb.NewMessage(h.outputDesc)
		if err := protojson.Unmarshal(jsonBytes, msg); err == nil {
			if data, err := proto.Marshal(msg); err == nil {
				w.Header().Set("Content-Type", "application/proto")
				w.WriteHeader(http.StatusOK)
				w.Write(data) //nolint:errcheck
				return
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(jsonBytes) //nolint:errcheck
}

// writeError writes a Connect-RPC error response
func (h *CustomMethodHandler) writeError(w http.ResponseWriter, err *connect.Error) {
	w.Header().Set("Content-Type", "application/json")

	// Map Connect code to HTTP status code
	httpStatus := http.StatusInternalServerError
	switch err.Code() {
	case connect.CodeInvalidArgument:
		httpStatus = http.StatusBadRequest
	case connect.CodeNotFound:
		httpStatus = http.StatusNotFound
	case connect.CodeAlreadyExists:
		httpStatus = http.StatusConflict
	case connect.CodePermissionDenied:
		httpStatus = http.StatusForbidden
	case connect.CodeUnauthenticated:
		httpStatus = http.StatusUnauthorized
	}

	w.WriteHeader(httpStatus)

	// Write error in Connect format
	errResp := map[string]any{
		"code":    err.Code().String(),
		"message": err.Message(),
	}

	data, _ := json.Marshal(errResp)
	w.Write(data)
}

// buildEvalContext builds an HCL evaluation context from the request
func buildEvalContext(req map[string]any, serviceVars map[string]cty.Value) *hcl.EvalContext {
	return config.BuildEvalContextFromMap(req, serviceVars)
}
