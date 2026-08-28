// Package fleetclient is a thin client for Fleet Controller's
// FleetStatusService, used only for the Origin Router's liveness
// cross-check (Epic D4): before proxying a viewer request, confirm the
// instance that owns the session is still known-healthy, instead of
// duplicating Fleet Controller's own health tracking in a second registry.
//
// The generated stub in api/fleetpb is regenerated locally from a copy of
// Fleet Controller's fleet_status.proto (see Makefile's `fleetproto`
// target) — Fleet Controller's own generated code lives under its
// internal/ package and can't be imported across modules.
package fleetclient

import (
	"context"
	"fmt"

	"vt-stream-transcoder/api/fleetpb"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// Checker answers "is this worker instance still healthy?" — the seam the
// Origin Router uses for D4, so a disabled/unavailable Fleet Controller
// integration (NoopChecker) and a real one are interchangeable.
type Checker interface {
	IsHealthy(ctx context.Context, workerID string) (bool, error)
}

// NoopChecker always reports healthy — used when Fleet Controller
// integration (Epic B) isn't configured or isn't available yet, so the
// Origin Router still functions without it (D4 becomes a no-op rather than
// a hard dependency).
type NoopChecker struct{}

func (NoopChecker) IsHealthy(context.Context, string) (bool, error) { return true, nil }

// Client is a real Checker backed by Fleet Controller's gRPC
// FleetStatusService.
type Client struct {
	conn        *grpc.ClientConn
	stub        fleetpb.FleetStatusServiceClient
	serviceName string
}

// New dials Fleet Controller's gRPC address (lazily — grpc.NewClient
// doesn't block) for service_name (e.g. "streambridge").
func New(addr, serviceName string) (*Client, error) {
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("fleetclient: dial %s: %w", addr, err)
	}
	return &Client{
		conn:        conn,
		stub:        fleetpb.NewFleetStatusServiceClient(conn),
		serviceName: serviceName,
	}, nil
}

func (c *Client) Close() error { return c.conn.Close() }

// IsHealthy reports false for an instance Fleet Controller has marked
// UNHEALTHY, or that it doesn't know about at all (NotFound) — both mean
// "don't proxy here." Any other status (READY, BUSY, DRAINING, STARTING)
// is treated as healthy enough to keep serving a session it already owns —
// D4 is a liveness check, not a placement-eligibility check, so a draining
// instance (not accepting new sessions, but still serving existing ones)
// must not be treated as dead.
func (c *Client) IsHealthy(ctx context.Context, workerID string) (bool, error) {
	resp, err := c.stub.GetInstance(ctx, &fleetpb.GetInstanceRequest{
		ServiceName: c.serviceName,
		InstanceId:  workerID,
	})
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return false, nil
		}
		return false, fmt.Errorf("fleetclient: GetInstance(%s): %w", workerID, err)
	}
	return resp.GetInstance().GetStatus() != fleetpb.InstanceStatusState_INSTANCE_STATUS_STATE_UNHEALTHY, nil
}
