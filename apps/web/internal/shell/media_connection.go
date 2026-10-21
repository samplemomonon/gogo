// Media connection wiring for the gateway.
package shell

import (
	"fmt"
	"log/slog"

	"google.golang.org/grpc"

	"gogo/internal/shared/config"
	"gogo/internal/shared/grpcx"
)

// connectMedia dials media-service, the platform service that owns object
// metadata and hands out presigned MinIO URLs. The address is required, as it is
// for every product-platform service: a gateway that renders no images looks like
// finished work, and the seller console would otherwise accept media ids it could
// never verify. The credential is the product platform's service token —
// media-service is platform-internal and validates the same identity.
//
// The media connection is dialed and closed independently of the product
// platform: the storefront must keep browsing products when only their images are
// down.
func connectMedia(cfg *config.AppConfig, logger *slog.Logger) (*grpc.ClientConn, func(), error) {
	if cfg.MediaAddress == "" {
		return nil, nil, fmt.Errorf("gateway: media service address is not configured")
	}
	conn, err := grpcx.Dial(cfg.MediaAddress, cfg.ProductPlatformServiceToken)
	if err != nil {
		return nil, nil, fmt.Errorf("gateway: dial media service %q: %w", cfg.MediaAddress, err)
	}
	logger.Info("gateway connected to media service", "address", cfg.MediaAddress)
	return conn, func() {
		if conn != nil {
			_ = conn.Close()
		}
	}, nil
}
