package registry

import "time"

// Status is the lifecycle state of a session ownership record.
type Status string

const (
	StatusActive     Status = "ACTIVE"
	StatusFinalizing Status = "FINALIZING"
)

// Record is the ownership record for a single session: which StreamBridge
// instance owns it, and what generation that ownership is at. Stored at
// {prefix}session:{sessionID} in Redis as JSON.
//
// WorkerID must match the identity Fleet Controller's streambridge
// LifecycleController.List() discovers this instance as — the Origin
// Router's liveness cross-check (D4) looks up this same WorkerID against
// Fleet Controller's GetInstance.
type Record struct {
	SessionID      string    `json:"sessionId"`
	WorkerID       string    `json:"workerId"`
	Origin         string    `json:"origin"`
	Generation     int64     `json:"generation"`
	Status         Status    `json:"status"`
	LeaseExpiresAt time.Time `json:"leaseExpiresAt"`
}
