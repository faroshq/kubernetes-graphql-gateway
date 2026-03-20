package watcher

import (
	"context"
	"fmt"
	"io"

	"github.com/platform-mesh/kubernetes-graphql-gateway/sdk"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"sigs.k8s.io/controller-runtime/pkg/log"
)

// GRPCWatcher watches for schema changes via gRPC streaming from a listener.
// It implements the Watcher interface.
type GRPCWatcher struct {
	conn    *grpc.ClientConn
	client  sdk.SchemaHandlerClient
	handler SchemaEventHandler
}

// GRPCWatcherConfig holds configuration for the gRPC watcher.
type GRPCWatcherConfig struct {
	// Address is the gRPC server address (e.g., "localhost:50051")
	Address string
}

// NewGRPCWatcher creates a new gRPC watcher that connects to the given address
// and notifies the handler when schemas change.
func NewGRPCWatcher(config GRPCWatcherConfig, handler SchemaEventHandler) (*GRPCWatcher, error) {
	// TODO: Add proper TLS configuration for production
	conn, err := grpc.NewClient(
		config.Address,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to gRPC server: %w", err)
	}

	client := sdk.NewSchemaHandlerClient(conn)

	return &GRPCWatcher{
		conn:    conn,
		client:  client,
		handler: handler,
	}, nil
}

// Close closes the underlying gRPC client connection.
func (w *GRPCWatcher) Close() error {
	if w.conn != nil {
		return w.conn.Close()
	}
	return nil
}

// Run starts the gRPC watcher and blocks until the context is cancelled.
// It subscribes to schema updates from the listener and processes them.
// Subscribe is retried with backoff so the gateway handles the case where
// the listener gRPC server is not yet ready when the gateway starts.
func (w *GRPCWatcher) Run(ctx context.Context) error {
	logger := log.FromContext(ctx)
	defer func() {
		if err := w.Close(); err != nil {
			logger.Error(err, "Failed to close gRPC connection")
		}
	}()

	// Retry Subscribe with backoff to tolerate the listener starting after the gateway.
	// grpc.WaitForReady causes the RPC to block until the connection is READY,
	// which handles the common startup ordering issue where the gateway pod
	// starts before the listener's gRPC server has bound port 50051.
	stream, err := w.client.Subscribe(ctx, &sdk.SubscribeRequest{}, grpc.WaitForReady(true))
	if err != nil {
		return fmt.Errorf("failed to subscribe to schema updates: %w", err)
	}

	logger.Info("Connected to gRPC schema handler, waiting for updates")

	for {
		res, err := stream.Recv()
		if err == io.EOF {
			logger.Info("gRPC stream closed by server")
			return nil
		}
		if err != nil {
			// Check if context was cancelled
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("error receiving from stream: %w", err)
		}

		switch res.EventType {
		case sdk.SubscribeResponse_CREATED, sdk.SubscribeResponse_UPDATED:
			logger.V(4).Info("Received schema update",
				"cluster", res.ClusterName,
				"event", res.EventType.String(),
			)
			w.handler.OnSchemaChanged(ctx, res.ClusterName, res.Schema)

		case sdk.SubscribeResponse_REMOVED:
			logger.V(4).Info("Received schema deletion",
				"cluster", res.ClusterName,
			)
			w.handler.OnSchemaDeleted(ctx, res.ClusterName)
		}
	}
}
