package connect

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/bufbuild/protocompile"
	"github.com/jumppad-labs/polymorph/internal/config"
	"github.com/jumppad-labs/polymorph/internal/fetch"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// protoRoute holds a pre-generated mock response for a single RPC method.
type protoRoute struct {
	path     string // "/<package>.<Service>/<Method>"
	response []byte // pre-generated JSON
}

// protoService describes a service discovered from proto files.
type protoService struct {
	name    string
	methods []protoMethod
}

// protoMethod describes a single RPC method.
type protoMethod struct {
	name   string
	output protoreflect.MessageDescriptor
}

// ProtoHandler parses .proto files and serves pre-generated mock ConnectRPC responses.
type ProtoHandler struct {
	routes   []*protoRoute
	services []protoService
	pkg      string // proto package name
	logger   *slog.Logger
}

// NewProtoHandler compiles proto files and builds mock routes.
// The proto path can be a local file/directory or a remote source (HTTP, git, S3, GCS, etc.).
func NewProtoHandler(cfg *config.ProtoConfig, packageName string, logger *slog.Logger) (*ProtoHandler, error) {
	result, err := fetch.Path(cfg.Path)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch proto source %q: %w", cfg.Path, err)
	}
	defer result.Close()

	protoFiles, importDir, err := discoverProtoFiles(result.Path)
	if err != nil {
		return nil, fmt.Errorf("failed to discover proto files: %w", err)
	}
	if len(protoFiles) == 0 {
		return nil, fmt.Errorf("no .proto files found at %q", cfg.Path)
	}

	compiler := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(
			&protocompile.SourceResolver{
				ImportPaths: []string{importDir},
			},
		),
	}

	compiled, err := compiler.Compile(context.Background(), protoFiles...)
	if err != nil {
		return nil, fmt.Errorf("failed to compile proto files: %w", err)
	}

	rows := 10
	if cfg.Rows != nil {
		rows = *cfg.Rows
	}

	var rng *rand.Rand
	if cfg.Seed != nil {
		rng = rand.New(rand.NewSource(*cfg.Seed))
	} else {
		rng = rand.New(rand.NewSource(time.Now().UnixNano()))
	}

	ph := &ProtoHandler{logger: logger}

	for _, fd := range compiled {
		if ph.pkg == "" {
			ph.pkg = string(fd.Package())
		}

		svcs := fd.Services()
		for i := 0; i < svcs.Len(); i++ {
			svc := svcs.Get(i)
			ps := protoService{name: string(svc.Name())}

			methods := svc.Methods()
			for j := 0; j < methods.Len(); j++ {
				m := methods.Get(j)
				ps.methods = append(ps.methods, protoMethod{
					name:   string(m.Name()),
					output: m.Output(),
				})

				// Determine the package to use for the route path
				pkg := packageName
				if pkg == "" {
					pkg = string(fd.Package())
				}

				path := fmt.Sprintf("/%s.%s/%s", pkg, svc.Name(), m.Name())
				resp, err := generateMockJSON(m.Output(), rows, rng)
				if err != nil {
					logger.Warn("failed to generate mock response",
						"method", path, "error", err)
					resp = []byte("{}")
				}

				ph.routes = append(ph.routes, &protoRoute{
					path:     path,
					response: resp,
				})

				logger.Info("registered proto route",
					"path", path,
					"responseSize", len(resp))
			}

			ph.services = append(ph.services, ps)
		}
	}

	return ph, nil
}

// Package returns the proto package name from the first compiled file.
func (ph *ProtoHandler) Package() string {
	return ph.pkg
}

// Services returns the discovered proto services.
func (ph *ProtoHandler) Services() []protoService {
	return ph.services
}

// RegisterRoutes registers all proto-derived routes on the given mux.
// Paths in skipPaths are not registered (already handled by custom handlers).
func (ph *ProtoHandler) RegisterRoutes(mux *http.ServeMux, skipPaths map[string]bool) {
	for _, route := range ph.routes {
		if skipPaths[route.path] {
			ph.logger.Info("skipping proto route (overridden by handle block)", "path", route.path)
			continue
		}
		r := route // capture
		mux.HandleFunc(r.path, func(w http.ResponseWriter, req *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write(r.response)
		})
	}
}

// discoverProtoFiles finds .proto files from a path (file or directory).
// Returns the list of proto file names (relative to importDir) and the import directory.
func discoverProtoFiles(path string) (files []string, importDir string, err error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, "", fmt.Errorf("cannot access %q: %w", path, err)
	}

	if !info.IsDir() {
		// Single file
		dir := filepath.Dir(path)
		name := filepath.Base(path)
		return []string{name}, dir, nil
	}

	// Directory: walk for .proto files
	var names []string
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, "", fmt.Errorf("cannot read directory %q: %w", path, err)
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".proto") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	return names, path, nil
}

// generateMockJSON produces a JSON object from a protobuf message descriptor.
func generateMockJSON(msg protoreflect.MessageDescriptor, rows int, rng *rand.Rand) ([]byte, error) {
	obj := buildMockObject(msg, rows, rng, 0)
	return json.MarshalIndent(obj, "", "  ")
}

const maxDepth = 5

// buildMockObject recursively builds a mock object from a message descriptor.
func buildMockObject(msg protoreflect.MessageDescriptor, rows int, rng *rand.Rand, depth int) map[string]any {
	if depth > maxDepth {
		return map[string]any{}
	}

	obj := make(map[string]any)
	seenOneofs := make(map[string]bool)

	fields := msg.Fields()
	for i := 0; i < fields.Len(); i++ {
		field := fields.Get(i)

		// For oneof fields, only generate the first field in each oneof group
		if oneof := field.ContainingOneof(); oneof != nil {
			key := string(oneof.Name())
			if seenOneofs[key] {
				continue
			}
			seenOneofs[key] = true
		}

		jsonName := field.JSONName()

		if field.IsMap() {
			obj[jsonName] = buildMockMap(field, rows, rng, depth)
		} else if field.IsList() {
			obj[jsonName] = buildMockList(field, rows, rng, depth)
		} else {
			obj[jsonName] = buildMockValue(field, rows, rng, depth)
		}
	}

	return obj
}

func buildMockMap(field protoreflect.FieldDescriptor, rows int, rng *rand.Rand, depth int) map[string]any {
	m := make(map[string]any)
	count := 3
	for i := 0; i < count; i++ {
		key := fmt.Sprintf("key_%d", i+1)
		valDesc := field.MapValue()
		if valDesc.Kind() == protoreflect.MessageKind {
			m[key] = buildMockObject(valDesc.Message(), rows, rng, depth+1)
		} else {
			m[key] = mockScalar(valDesc.Kind(), "value", rng)
		}
	}
	return m
}

func buildMockList(field protoreflect.FieldDescriptor, rows int, rng *rand.Rand, depth int) []any {
	items := make([]any, rows)
	for i := 0; i < rows; i++ {
		if field.Kind() == protoreflect.MessageKind {
			items[i] = buildMockObject(field.Message(), rows, rng, depth+1)
		} else if field.Kind() == protoreflect.EnumKind {
			items[i] = mockEnum(field.Enum(), rng)
		} else {
			items[i] = mockScalar(field.Kind(), string(field.Name()), rng)
		}
	}
	return items
}

func buildMockValue(field protoreflect.FieldDescriptor, rows int, rng *rand.Rand, depth int) any {
	switch field.Kind() {
	case protoreflect.MessageKind:
		return buildMockMessage(field.Message(), rows, rng, depth)
	case protoreflect.EnumKind:
		return mockEnum(field.Enum(), rng)
	default:
		return mockScalar(field.Kind(), string(field.Name()), rng)
	}
}

func buildMockMessage(msg protoreflect.MessageDescriptor, rows int, rng *rand.Rand, depth int) any {
	fullName := string(msg.FullName())
	switch fullName {
	case "google.protobuf.Timestamp":
		return time.Now().UTC().Format(time.RFC3339)
	case "google.protobuf.Duration":
		return "1.500s"
	case "google.protobuf.Struct":
		return map[string]any{}
	case "google.protobuf.Value":
		return nil
	case "google.protobuf.Any":
		return map[string]any{"@type": "type.googleapis.com/google.protobuf.Empty"}
	default:
		return buildMockObject(msg, rows, rng, depth+1)
	}
}

func mockEnum(enum protoreflect.EnumDescriptor, rng *rand.Rand) string {
	values := enum.Values()
	if values.Len() == 0 {
		return ""
	}
	idx := rng.Intn(values.Len())
	return string(values.Get(idx).Name())
}

func mockScalar(kind protoreflect.Kind, fieldName string, rng *rand.Rand) any {
	switch kind {
	case protoreflect.StringKind:
		if fieldName == "id" {
			return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
				rng.Uint32(), rng.Uint32()&0xFFFF, rng.Uint32()&0xFFFF,
				rng.Uint32()&0xFFFF, rng.Int63()&0xFFFFFFFFFFFF)
		}
		return fmt.Sprintf("sample_%s", fieldName)
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		return rng.Int31n(1000)
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		return rng.Int63n(10000)
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		return rng.Uint32() % 1000
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		return rng.Uint64() % 10000
	case protoreflect.FloatKind:
		return float32(rng.Float64() * 100)
	case protoreflect.DoubleKind:
		return rng.Float64() * 1000
	case protoreflect.BoolKind:
		return rng.Intn(2) == 1
	case protoreflect.BytesKind:
		b := make([]byte, 8)
		rng.Read(b)
		return b
	default:
		return nil
	}
}
