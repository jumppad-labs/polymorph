package connect

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/jumppad-labs/polymorph/internal/config"
	"github.com/stretchr/testify/require"
)

func TestNewProtoHandler_SingleFile(t *testing.T) {
	ph, err := NewProtoHandler(&config.ProtoConfig{
		Path: "testdata/simple.proto",
	}, "", slog.New(slog.NewTextHandler(os.Stderr, nil)))
	require.NoError(t, err)

	require.Equal(t, "test.v1", ph.Package())
	require.Len(t, ph.Services(), 2)

	// UserService has 3 methods, OrderService has 1
	var userSvc, orderSvc protoService
	for _, s := range ph.Services() {
		switch s.name {
		case "UserService":
			userSvc = s
		case "OrderService":
			orderSvc = s
		}
	}
	require.Equal(t, "UserService", userSvc.name)
	require.Len(t, userSvc.methods, 3)
	require.Equal(t, "OrderService", orderSvc.name)
	require.Len(t, orderSvc.methods, 1)
}

func TestNewProtoHandler_Directory(t *testing.T) {
	// Create a temp directory with only valid proto files
	dir := t.TempDir()
	protoContent := `syntax = "proto3";
package myapi.v1;
service GreetService {
  rpc SayHello(HelloRequest) returns (HelloResponse);
}
message HelloRequest { string name = 1; }
message HelloResponse { string message = 1; }
`
	err := os.WriteFile(filepath.Join(dir, "greet.proto"), []byte(protoContent), 0644)
	require.NoError(t, err)

	ph, err := NewProtoHandler(&config.ProtoConfig{
		Path: dir,
	}, "", slog.New(slog.NewTextHandler(os.Stderr, nil)))
	require.NoError(t, err)
	require.Equal(t, "myapi.v1", ph.Package())
	require.Len(t, ph.Services(), 1)
	require.Equal(t, "GreetService", ph.Services()[0].name)
	require.Len(t, ph.Services()[0].methods, 1)
	require.Equal(t, "SayHello", ph.Services()[0].methods[0].name)
}

func TestNewProtoHandler_NonexistentPath(t *testing.T) {
	_, err := NewProtoHandler(&config.ProtoConfig{
		Path: "/nonexistent/path",
	}, "", slog.New(slog.NewTextHandler(os.Stderr, nil)))
	require.Error(t, err)
	require.Contains(t, err.Error(), "cannot access")
}

func TestNewProtoHandler_InvalidProto(t *testing.T) {
	_, err := NewProtoHandler(&config.ProtoConfig{
		Path: "testdata/invalid.proto",
	}, "", slog.New(slog.NewTextHandler(os.Stderr, nil)))
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to compile")
}

func TestNewProtoHandler_WellKnownTypes(t *testing.T) {
	// simple.proto imports google/protobuf/timestamp.proto
	ph, err := NewProtoHandler(&config.ProtoConfig{
		Path: "testdata/simple.proto",
	}, "", slog.New(slog.NewTextHandler(os.Stderr, nil)))
	require.NoError(t, err)
	require.NotNil(t, ph)

	// Find GetUser method and verify it has routes
	require.True(t, len(ph.routes) > 0)
}

func TestNewProtoHandler_PackageOverride(t *testing.T) {
	ph, err := NewProtoHandler(&config.ProtoConfig{
		Path: "testdata/simple.proto",
	}, "custom.pkg", slog.New(slog.NewTextHandler(os.Stderr, nil)))
	require.NoError(t, err)

	// Routes should use the override package
	found := false
	for _, r := range ph.routes {
		if r.path == "/custom.pkg.UserService/GetUser" {
			found = true
			break
		}
	}
	require.True(t, found, "expected route with custom package, got routes: %v", routePaths(ph.routes))
}

func TestNewProtoHandler_RoutePaths(t *testing.T) {
	ph, err := NewProtoHandler(&config.ProtoConfig{
		Path: "testdata/simple.proto",
	}, "", slog.New(slog.NewTextHandler(os.Stderr, nil)))
	require.NoError(t, err)

	paths := routePaths(ph.routes)
	require.Contains(t, paths, "/test.v1.UserService/GetUser")
	require.Contains(t, paths, "/test.v1.UserService/ListUsers")
	require.Contains(t, paths, "/test.v1.UserService/CreateUser")
	require.Contains(t, paths, "/test.v1.OrderService/GetOrder")
}

func TestNewProtoHandler_DeterministicSeed(t *testing.T) {
	seed := int64(42)
	ph1, err := NewProtoHandler(&config.ProtoConfig{
		Path: "testdata/simple.proto",
		Seed: &seed,
	}, "", slog.New(slog.NewTextHandler(os.Stderr, nil)))
	require.NoError(t, err)

	ph2, err := NewProtoHandler(&config.ProtoConfig{
		Path: "testdata/simple.proto",
		Seed: &seed,
	}, "", slog.New(slog.NewTextHandler(os.Stderr, nil)))
	require.NoError(t, err)

	// Same seed should produce identical responses
	require.Equal(t, len(ph1.routes), len(ph2.routes))
	for i := range ph1.routes {
		require.Equal(t, ph1.routes[i].path, ph2.routes[i].path)
		require.Equal(t, string(ph1.routes[i].response), string(ph2.routes[i].response))
	}
}

func TestNewProtoHandler_MockResponseFields(t *testing.T) {
	seed := int64(42)
	ph, err := NewProtoHandler(&config.ProtoConfig{
		Path: "testdata/simple.proto",
		Seed: &seed,
	}, "", slog.New(slog.NewTextHandler(os.Stderr, nil)))
	require.NoError(t, err)

	// Find GetUser response
	var getUserResp []byte
	for _, r := range ph.routes {
		if r.path == "/test.v1.UserService/GetUser" {
			getUserResp = r.response
			break
		}
	}
	require.NotNil(t, getUserResp)

	// Should be valid JSON with expected fields
	require.Contains(t, string(getUserResp), `"id"`)
	require.Contains(t, string(getUserResp), `"name"`)
	require.Contains(t, string(getUserResp), `"email"`)
	require.Contains(t, string(getUserResp), `"age"`)
	require.Contains(t, string(getUserResp), `"active"`)
	require.Contains(t, string(getUserResp), `"status"`) // enum
	require.Contains(t, string(getUserResp), `"address"`) // nested message
	require.Contains(t, string(getUserResp), `"createdAt"`) // timestamp (JSON camelCase)
	require.Contains(t, string(getUserResp), `"tags"`)      // repeated
	require.Contains(t, string(getUserResp), `"metadata"`)  // map
}

func TestNewProtoHandler_CustomRows(t *testing.T) {
	rows := 3
	seed := int64(42)
	ph, err := NewProtoHandler(&config.ProtoConfig{
		Path: "testdata/simple.proto",
		Rows: &rows,
		Seed: &seed,
	}, "", slog.New(slog.NewTextHandler(os.Stderr, nil)))
	require.NoError(t, err)

	// Find ListUsers response - it has repeated GetUserResponse
	var listResp []byte
	for _, r := range ph.routes {
		if r.path == "/test.v1.UserService/ListUsers" {
			listResp = r.response
			break
		}
	}
	require.NotNil(t, listResp)
	// The "users" field should be an array with 3 items
	require.Contains(t, string(listResp), `"users"`)
}

func TestNewProtoHandler_NoPackageProto(t *testing.T) {
	dir := t.TempDir()
	// Proto with no package declaration
	protoContent := `syntax = "proto3";
service NoPackageService {
  rpc Ping(PingRequest) returns (PingResponse);
}
message PingRequest {}
message PingResponse { string pong = 1; }
`
	err := os.WriteFile(filepath.Join(dir, "nopackage.proto"), []byte(protoContent), 0644)
	require.NoError(t, err)

	ph, err := NewProtoHandler(&config.ProtoConfig{
		Path: dir,
	}, "", slog.New(slog.NewTextHandler(os.Stderr, nil)))
	require.NoError(t, err)
	// Package should be empty since proto has no package declaration
	require.Equal(t, "", ph.Package())
}

func TestNewProtoHandler_ExplicitPackageOverridesProto(t *testing.T) {
	ph, err := NewProtoHandler(&config.ProtoConfig{
		Path: "testdata/simple.proto",
	}, "override.v2", slog.New(slog.NewTextHandler(os.Stderr, nil)))
	require.NoError(t, err)

	// Proto package is test.v1, but override.v2 should be used in routes
	for _, r := range ph.routes {
		require.Contains(t, r.path, "override.v2", "route %s should use override package", r.path)
	}
}

func routePaths(routes []*protoRoute) []string {
	paths := make([]string, len(routes))
	for i, r := range routes {
		paths[i] = r.path
	}
	return paths
}
