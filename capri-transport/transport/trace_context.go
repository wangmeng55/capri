package transport

import "context"

type tunnelTraceContextKey struct{}

// WithTunnelTraceObserver attaches a diagnostic observer to a request context.
// The observer is process-local and must not retain request bodies or headers.
func WithTunnelTraceObserver(ctx context.Context, observer func(TunnelTraceEvent)) context.Context {
	if observer == nil {
		return ctx
	}
	return context.WithValue(ctx, tunnelTraceContextKey{}, observer)
}

// HasTunnelTraceObserver reports whether request-local diagnostic collection is enabled.
func HasTunnelTraceObserver(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	observer, ok := ctx.Value(tunnelTraceContextKey{}).(func(TunnelTraceEvent))
	return ok && observer != nil
}

// ObserveTunnelTrace reports a request-local diagnostic event when an observer
// was attached. It is a no-op for ordinary requests.
func ObserveTunnelTrace(ctx context.Context, event TunnelTraceEvent) {
	if ctx == nil {
		return
	}
	if observer, ok := ctx.Value(tunnelTraceContextKey{}).(func(TunnelTraceEvent)); ok && observer != nil {
		observer(event)
	}
}
