// Package health exposes /healthz (liveness) and /readyz (readiness) with a pluggable checker registry. Returns 200 when all checks pass, 503 otherwise.
package health

import (
	"context"       // For cancellation and timeout
	"encoding/json" // For JSON response encoding
	"net/http"      // For HTTP server and handlers
	"sync"          // For RWMutex (thread-safe map access)

	"github.com/go-logr/logr" // Structured logging
)

// ============================================================================
// CHECKER FUNCTION TYPE
// ============================================================================

// Checker defines a health check function
//
// FUNCTION TYPE:
//
//	This is a type alias for a function signature
//	Any function matching this signature is a Checker
//
// SIGNATURE:
//
//	Takes: context.Context (for timeout/cancellation)
//	Returns: error (nil = healthy, non-nil = unhealthy)
//
// USAGE EXAMPLES:
//
//  1. Function:
//     func checkMemory(ctx context.Context) error {
//     var m runtime.MemStats
//     runtime.ReadMemStats(&m)
//     if m.Alloc > maxMemory {
//     return fmt.Errorf("memory usage too high: %d bytes", m.Alloc)
//     }
//     return nil
//     }
//     server.AddReadinessCheck("memory", checkMemory)
//
//  2. Closure:
//     monitor := &k8sMonitorImpl{...}
//     server.AddReadinessCheck("k8s-monitor", func(ctx context.Context) error {
//     if !monitor.Ready() {
//     return fmt.Errorf("k8s monitor not ready")
//     }
//     return nil
//     })
//
//  3. Method:
//     type MyComponent struct { ... }
//     func (c *MyComponent) HealthCheck(ctx context.Context) error {
//     // Check component health
//     return nil
//     }
//     component := &MyComponent{...}
//     server.AddLivenessCheck("my-component", component.HealthCheck)
//
// WHY FUNCTION TYPE?
//   - Flexibility: Can pass functions, closures, or methods
//   - Simplicity: Just implement one function, no interface needed
//   - Testability: Easy to create mock checkers for tests
type Checker func(ctx context.Context) error

// ============================================================================
// SERVER STRUCTURE
// ============================================================================

// Server provides health check endpoints
//
// RESPONSIBILITIES:
//  1. HTTP server for /healthz and /readyz endpoints
//  2. Registry of health checks (liveness and readiness)
//  3. Execute checks and return HTTP responses
//  4. Thread-safe check registration
//
// LIFECYCLE:
//
//	server := health.NewServer(log, ":8081")
//	server.AddReadinessCheck("component", checkFunc)
//	go server.Run(ctx) // Start in goroutine
//
// THREAD SAFETY:
//
//	mu (RWMutex) protects livenessChecks and readinessChecks maps
//	Multiple goroutines can register checks concurrently
type Server struct {
	// Logging and configuration
	log  logr.Logger // Logger with "health" prefix
	addr string      // Listen address (e.g., ":8081", "0.0.0.0:8081")

	// HTTP server
	// *http.Server: Go's standard HTTP server
	// Created once in NewServer(), started in Run()
	server *http.Server

	// Thread safety for check registry
	// sync.RWMutex: Read-write lock
	// Multiple readers (handleHealthz, handleReadyz) OR one writer (Add*Check)
	mu sync.RWMutex

	// Health check registries
	// map[string]Checker: check name -> check function
	// Separate maps for liveness vs readiness (different concerns)
	livenessChecks  map[string]Checker // Checks for /healthz
	readinessChecks map[string]Checker // Checks for /readyz
}

// ============================================================================
// CONSTRUCTOR
// ============================================================================

// NewServer creates a new health server
//
// FACTORY FUNCTION:
//
//	Creates and initializes Server struct
//	Sets up HTTP routes
//	Does NOT start server (call Run() to start)
//
// Parameters:
//
//	log - Logger for health check logging
//	addr - Listen address (e.g., ":8081" for all interfaces port 8081)
//
// Returns:
//
//	*Server - Initialized server (ready to add checks and Run())
//
// INITIALIZATION:
//  1. Create Server struct with empty check maps
//  2. Create HTTP multiplexer (router)
//  3. Register handlers for /healthz and /readyz
//  4. Create http.Server with multiplexer
//  5. Return server
func NewServer(log logr.Logger, addr string) *Server {
	// Create server instance
	s := &Server{
		log:             log.WithName("health"),   // Child logger with "health" prefix
		addr:            addr,                     // Store listen address
		livenessChecks:  make(map[string]Checker), // Empty map for liveness checks
		readinessChecks: make(map[string]Checker), // Empty map for readiness checks
	}

	// Create HTTP multiplexer (router)
	// http.NewServeMux(): Standard Go HTTP router
	// Simpler than full routers (gorilla/mux, chi), sufficient for 2 endpoints
	mux := http.NewServeMux()

	// Register handlers
	// HandleFunc(pattern, handler): Register handler for URL pattern
	// Handler signature: func(http.ResponseWriter, *http.Request)
	mux.HandleFunc("/healthz", s.handleHealthz) // Liveness endpoint
	mux.HandleFunc("/readyz", s.handleReadyz)   // Readiness endpoint

	// Create HTTP server
	// http.Server: Go's built-in HTTP server
	// Not started yet, just configured
	s.server = &http.Server{
		Addr:    addr, // Listen address (e.g., ":8081")
		Handler: mux,  // HTTP handler (our multiplexer with 2 routes)
	}

	return s // Return configured server
}

// ============================================================================
// CHECK REGISTRATION
// ============================================================================

// AddLivenessCheck adds a liveness check
//
// LIVENESS CHECK:
//
//	Is the process alive and functioning?
//	Examples: Not deadlocked, not out of memory, not corrupted
//
// Parameters:
//
//	name - Check name (for logging and response JSON)
//	check - Check function (returns nil if healthy)
//
// THREAD SAFE:
//
//	Uses Lock (write lock) to protect map modification
//
// USAGE:
//
//	server.AddLivenessCheck("goroutines", func(ctx context.Context) error {
//	  if runtime.NumGoroutine() > 10000 {
//	    return fmt.Errorf("too many goroutines: %d", runtime.NumGoroutine())
//	  }
//	  return nil
//	})
func (s *Server) AddLivenessCheck(name string, check Checker) {
	s.mu.Lock()                    // Acquire write lock (exclusive access)
	defer s.mu.Unlock()            // Release lock on return
	s.livenessChecks[name] = check // Add check to map (or overwrite if name exists)
}

// AddReadinessCheck adds a readiness check
//
// READINESS CHECK:
//
//	Is the pod ready to accept traffic?
//	Examples: Informers synced, servers listening, scenarios loaded
//
// Parameters:
//
//	name - Check name (appears in response JSON)
//	check - Check function (returns nil if ready)
//
// THREAD SAFE:
//
//	Uses Lock (write lock) to protect map modification
//
// USAGE:
//
//	server.AddReadinessCheck("k8s-monitor", func(ctx context.Context) error {
//	  if !monitor.Ready() {
//	    return fmt.Errorf("k8s monitor not ready")
//	  }
//	  return nil
//	})
func (s *Server) AddReadinessCheck(name string, check Checker) {
	s.mu.Lock()                     // Acquire write lock
	defer s.mu.Unlock()             // Release lock on return
	s.readinessChecks[name] = check // Add check to map
}

// ============================================================================
// SERVER LIFECYCLE
// ============================================================================

// Run starts the health server and blocks until context is cancelled
//
// LIFECYCLE:
//  1. Start HTTP server in goroutine
//  2. Wait for ctx.Done() or server error
//  3. Shutdown server gracefully on context cancellation
//
// BLOCKING:
//
//	This method blocks until context is cancelled
//	Start in goroutine: go server.Run(ctx)
//
// GRACEFUL SHUTDOWN:
//
//	On ctx.Done(), calls server.Shutdown() (not immediate close)
//	Allows in-flight requests to complete
//
// Parameters:
//
//	ctx - Context for lifecycle control (Done() triggers shutdown)
//
// Returns:
//
//	error - Only if server fails to start or shutdown error
func (s *Server) Run(ctx context.Context) error {
	s.log.Info("Starting health server", "addr", s.addr)

	// Create error channel for server errors
	// Buffered channel (size 1) so goroutine doesn't block sending error
	errCh := make(chan error, 1)

	// Start HTTP server in goroutine
	// ListenAndServe blocks; run in goroutine so we can cancel via ctx.
	go func() {
		// Start server and listen for connections
		// ListenAndServe blocks until server stops or errors
		if err := s.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			// Server failed (not graceful shutdown)
			// http.ErrServerClosed: normal shutdown via Shutdown()
			// Any other error: actual problem (port in use, permission denied, etc.)
			errCh <- err // Send error to channel
		}
	}()

	// Wait for context cancellation or server error
	// select: multiplexing on channels (like switch but for channels)
	select {
	case <-ctx.Done():
		// Context cancelled (operator shutting down)
		// Gracefully shutdown HTTP server
		// server.Shutdown(): Stops accepting connections, waits for in-flight requests
		// Pass new context (old ctx is already cancelled)
		return s.server.Shutdown(context.Background())

	case err := <-errCh:
		// Server failed to start or had error
		return err
	}
}

// ============================================================================
// HTTP HANDLERS
// ============================================================================

// handleHealthz handles liveness probe requests
//
// HTTP HANDLER:
//
//	Called by HTTP server for GET/POST/etc requests to /healthz
//
// FLOW:
//  1. Copy check map (so we can release lock quickly)
//  2. Run checks via runChecks()
//  3. Return HTTP response
//
// Parameters:
//
//	w - Response writer (write HTTP response)
//	r - Request (HTTP request details)
//
// THREAD SAFE:
//
//	Uses RLock (read lock) to copy check map
//	Multiple requests can run concurrently
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	// Acquire read lock
	// Multiple handleHealthz calls can hold RLock simultaneously
	s.mu.RLock()

	// Copy checks map so we can release lock quickly
	// Don't want to hold lock during check execution (could be slow)
	// make(map[K]V, len): create map with initial capacity
	checks := make(map[string]Checker, len(s.livenessChecks))
	for k, v := range s.livenessChecks {
		checks[k] = v // Copy key-value pairs
	}

	// Release read lock
	// Now safe for others to Add*Check or handleHealthz
	s.mu.RUnlock()

	// Run checks and write response
	// Pass "liveness" for logging context
	s.runChecks(w, r, checks, "liveness")
}

// handleReadyz handles readiness probe requests
//
// HTTP HANDLER:
//
//	Called by HTTP server for requests to /readyz
//
// FLOW:
//
//	Same as handleHealthz but uses readinessChecks map
//
// Parameters:
//
//	w - Response writer
//	r - Request
//
// THREAD SAFE:
//
//	Uses RLock to copy check map
func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock() // Acquire read lock

	// Copy readiness checks
	checks := make(map[string]Checker, len(s.readinessChecks))
	for k, v := range s.readinessChecks {
		checks[k] = v
	}

	s.mu.RUnlock() // Release read lock

	// Run checks and write response
	s.runChecks(w, r, checks, "readiness")
}

// ============================================================================
// CHECK EXECUTION
// ============================================================================

// runChecks executes health checks and writes the HTTP response
//
// EXECUTION LOGIC:
//  1. Extract context from request (includes timeout)
//  2. Run all checks sequentially
//  3. Collect results (ok or error message)
//  4. Determine overall status (all healthy = ok, any failed = unhealthy)
//  5. Write JSON response with appropriate HTTP status code
//
// Parameters:
//
//	w - Response writer (for HTTP response)
//	r - Request (contains context with timeout)
//	checks - Map of checks to run
//	checkType - "liveness" or "readiness" (for logging)
//
// RESPONSE:
//
//	200 OK if all checks pass
//	503 Service Unavailable if any check fails
//	Body: JSON with status and per-check results
//
// CONTEXT:
//
//	Uses request context (r.Context())
//	Kubernetes can set timeout on probes (default 1 second)
//	Checks should respect context cancellation
func (s *Server) runChecks(w http.ResponseWriter, r *http.Request, checks map[string]Checker, checkType string) {
	// Extract context from request
	// HTTP request includes context with:
	//   - Cancellation (client disconnected)
	//   - Timeout (set by K8s probe configuration)
	ctx := r.Context()

	// Results map: check name -> result string
	// "ok" if passed, error message if failed
	results := make(map[string]string)

	// Track overall health
	// If any check fails, this becomes false
	allHealthy := true

	// Run each check
	// for k, v := range map: iterate over map
	// k = key (check name), v = value (check function)
	for name, check := range checks {
		// Call check function
		// Passing ctx (check should respect timeout/cancellation)
		if err := check(ctx); err != nil {
			// Check failed
			results[name] = err.Error() // Store error message
			allHealthy = false          // Mark overall status as unhealthy
		} else {
			// Check passed
			results[name] = "ok" // Store success
		}
	}

	// Handle no checks case
	// If operator hasn't registered any checks yet
	// Default to healthy (fail-open, not fail-closed)
	if len(checks) == 0 {
		results["default"] = "ok"
	}

	// Create response struct
	// Anonymous struct: struct without named type
	// Used for one-off data structures (like JSON responses)
	response := struct {
		Status string            `json:"status"` // "ok" or "unhealthy"
		Checks map[string]string `json:"checks"` // Check name -> result
	}{
		Checks: results, // Fill in checks map
	}

	// Set status and HTTP status code based on health
	if allHealthy {
		// All checks passed
		response.Status = "ok"       // JSON status field
		w.WriteHeader(http.StatusOK) // HTTP 200
	} else {
		// At least one check failed
		response.Status = "unhealthy"                // JSON status field
		w.WriteHeader(http.StatusServiceUnavailable) // HTTP 503
	}

	// Write JSON response
	// Set Content-Type header
	w.Header().Set("Content-Type", "application/json")

	// Encode response as JSON and write to response writer
	// json.NewEncoder(w): Create JSON encoder writing to w
	// .Encode(response): Encode struct as JSON
	// Automatically handles serialization (struct -> JSON)
	// Error ignored (can't do much if response write fails)
	json.NewEncoder(w).Encode(response)
}

// ============================================================================
// USAGE EXAMPLES
// ============================================================================
//
// Setup health server:
//
//   server := health.NewServer(log, ":8081")
//
//   // Add liveness checks
//   server.AddLivenessCheck("goroutines", func(ctx context.Context) error {
//     count := runtime.NumGoroutine()
//     if count > 10000 {
//       return fmt.Errorf("too many goroutines: %d", count)
//     }
//     return nil
//   })
//
//   // Add readiness checks
//   server.AddReadinessCheck("k8s-monitor", func(ctx context.Context) error {
//     if !monitor.Ready() {
//       return fmt.Errorf("k8s monitor not ready")
//     }
//     return nil
//   })
//
//   server.AddReadinessCheck("detection-engine", func(ctx context.Context) error {
//     if engine.GetLoadedScenarios() == 0 {
//       return fmt.Errorf("no scenarios loaded")
//     }
//     return nil
//   })
//
//   // Start server
//   go server.Run(ctx)
//
// Kubernetes pod spec:
//
//   livenessProbe:
//     httpGet:
//       path: /healthz
//       port: 8081
//     initialDelaySeconds: 10
//     periodSeconds: 10
//     timeoutSeconds: 1
//     failureThreshold: 3
//
//   readinessProbe:
//     httpGet:
//       path: /readyz
//       port: 8081
//     initialDelaySeconds: 5
//     periodSeconds: 5
//     timeoutSeconds: 1
//     failureThreshold: 3
//
// Manual testing:
//
//   $ curl http://localhost:8081/healthz
//   {"status":"ok","checks":{"goroutines":"ok"}}
//
//   $ curl http://localhost:8081/readyz
//   {"status":"unhealthy","checks":{"k8s-monitor":"k8s monitor not ready","detection-engine":"ok"}}
