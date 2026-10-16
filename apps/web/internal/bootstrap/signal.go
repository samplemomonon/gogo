// Signal handling for graceful shutdown.
package bootstrap

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

// SignalContext returns a context cancelled on the standard shutdown signals
// (SIGINT, SIGTERM). Every gogo binary waits on it to begin its graceful
// shutdown.
func SignalContext(parent context.Context) (context.Context, context.CancelFunc) {
	return signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
}
