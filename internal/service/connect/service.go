package connect

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/jumppad-labs/polymorph/internal/config"
	configconnect "github.com/jumppad-labs/polymorph/internal/config/connect"
	"github.com/jumppad-labs/polymorph/internal/resource"
	"github.com/jumppad-labs/polymorph/internal/service"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

// ConnectService implements a Connect-RPC service
type ConnectService struct {
	name             string
	config           *configconnect.Service
	logger           *slog.Logger
	resourceStore    *resource.Store
	resourceHandlers []*ResourceHandler
	customHandlers   []*CustomMethodHandler
	server           *http.Server
	listener         net.Listener
	mux              *http.ServeMux
}

// NewConnectService creates a new Connect-RPC service
func NewConnectService(cfg *configconnect.Service, logger *slog.Logger) (*ConnectService, error) {
	if cfg.Package == "" && cfg.Proto == nil {
		return nil, fmt.Errorf("package is required for connect service")
	}

	// Create resource store if we have resources
	var resourceStore *resource.Store
	var resourceHandlers []*ResourceHandler

	if len(cfg.Resources) > 0 {
		resourceStore = resource.NewStore()

		// Create resource handlers
		for _, res := range cfg.Resources {
			rh, err := NewResourceHandler(res, resourceStore, cfg.Package)
			if err != nil {
				return nil, fmt.Errorf("failed to create resource handler for %q: %w", res.Name, err)
			}

			// Initialize the resource (create table and generate data)
			if err := rh.Initialize(); err != nil {
				return nil, fmt.Errorf("failed to initialize resource %q: %w", res.Name, err)
			}

			resourceHandlers = append(resourceHandlers, rh)
		}
	}

	// Parse proto files first (if configured) so we can infer package and service names
	var protoHandler *ProtoHandler
	if cfg.Proto != nil {
		var err error
		protoHandler, err = NewProtoHandler(cfg.Proto, cfg.Package, cfg.Prefix, logger)
		if err != nil {
			return nil, fmt.Errorf("failed to create proto handler: %w", err)
		}
		if cfg.Package == "" {
			cfg.Package = protoHandler.Package()
		}
	}

	svc := &ConnectService{
		name:             cfg.Name,
		config:           cfg,
		logger:           logger,
		resourceStore:    resourceStore,
		resourceHandlers: resourceHandlers,
		mux:              http.NewServeMux(),
	}

	// Determine service name for custom methods
	// Priority: first resource name > first proto service name > config name
	serviceName := cfg.Name + "Service"
	if len(cfg.Resources) > 0 {
		serviceName = capitalizeFirst(cfg.Resources[0].Name) + "Service"
	} else if protoHandler != nil && len(protoHandler.Services()) > 0 {
		serviceName = protoHandler.Services()[0].name
	}

	// Create custom method handlers from handle blocks
	var customHandlers []*CustomMethodHandler
	for _, handler := range cfg.Handlers {
		handlerServiceName := serviceName
		if handler.Service != "" {
			handlerServiceName = handler.Service
		}
		handlerPackage := cfg.Package
		if handler.Package != "" {
			handlerPackage = handler.Package
		}
		mh, err := NewCustomMethodHandler(handler, handlerPackage, handlerServiceName, cfg.Prefix, cfg.Vars)
		if err != nil {
			return nil, fmt.Errorf("failed to create custom method handler for %q: %w", handler.Name, err)
		}
		// Wire up proto output descriptor if available
		if protoHandler != nil {
			for _, ps := range protoHandler.Services() {
				if ps.name == handlerServiceName {
					for _, pm := range ps.methods {
						if pm.name == handler.Name {
							mh.SetOutputDescriptor(pm.output)
						}
					}
				}
			}
		}

		customHandlers = append(customHandlers, mh)
	}
	svc.customHandlers = customHandlers

	// Register all resource handlers as Connect-RPC endpoints
	for _, rh := range resourceHandlers {
		_, handler := rh.RegisterHandlers()
		// Wrap handler with h2c for HTTP/2 without TLS
		svc.mux.Handle("/", h2c.NewHandler(handler, &http2.Server{}))
	}

	// Register custom method handlers (higher priority than proto routes)
	customPaths := make(map[string]bool)
	for _, mh := range customHandlers {
		path, handler := mh.RegisterHandler()
		svc.mux.HandleFunc(path, handler)
		customPaths[path] = true
		svc.logger.Info("registered custom method", "path", path)
	}

	// Register proto-derived routes (lowest priority — skip paths already handled)
	if protoHandler != nil {
		protoHandler.RegisterRoutes(svc.mux, customPaths)
	}

	return svc, nil
}

// Name returns the service name
func (s *ConnectService) Name() string {
	return s.name
}

// Type returns the service type
func (s *ConnectService) Type() string {
	return "connect"
}

// Address returns the service listen address
func (s *ConnectService) Address() string {
	return s.config.Listen
}

// Upstreams returns the list of upstream service dependencies
func (s *ConnectService) Upstreams() []string {
	return s.config.Upstreams
}

// Start starts the Connect-RPC server
func (s *ConnectService) Start(ctx context.Context) error {
	// Create listener
	listener, err := net.Listen("tcp", s.config.Listen)
	if err != nil {
		return fmt.Errorf("failed to create listener: %w", err)
	}

	// Wrap with TLS if configured
	listener, err = service.WrapListenerTLS(listener, s.config.TLS)
	if err != nil {
		listener.Close()
		return fmt.Errorf("failed to configure TLS: %w", err)
	}
	s.listener = listener

	// Create HTTP server with h2c handler
	s.server = &http.Server{
		Handler: s.mux,
	}

	// Start server in background
	proto := "Connect-RPC"
	if s.config.TLS != nil {
		proto = "Connect-RPC (TLS)"
	}
	go func() {
		s.logger.Info("service listening", "proto", proto, "addr", s.config.Listen)
		if err := s.server.Serve(listener); err != nil && err != http.ErrServerClosed {
			s.logger.Error("server error", "error", err)
		}
	}()

	return nil
}

// Stop gracefully stops the Connect-RPC server
func (s *ConnectService) Stop(ctx context.Context) error {
	if s.server == nil {
		return nil
	}

	s.logger.Info("stopping service")

	// Use a timeout context for shutdown
	shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	if err := s.server.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("failed to shutdown server: %w", err)
	}

	return nil
}

// init registers the Connect service factory
func init() {
	service.RegisterFactory("connect", func(cfg config.Service, logger *slog.Logger) (service.Service, error) {
		c, ok := cfg.(*configconnect.Service)
		if !ok {
			return nil, fmt.Errorf("connect: unexpected config type %T", cfg)
		}
		return NewConnectService(c, logger)
	})
}
