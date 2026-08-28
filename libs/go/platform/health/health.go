// Package health provides standard Kubernetes liveness and readiness helpers.
package health

import "context"

type Probe string

const (
	ProbeLiveness  Probe = "liveness"
	ProbeReadiness Probe = "readiness"
)

type Status string

const (
	StatusOK       Status = "ok"
	StatusReady    Status = "ready"
	StatusUnready  Status = "unready"
	StatusDegraded Status = "degraded"
)

type CheckStatus string

const (
	CheckPass CheckStatus = "pass"
	CheckWarn CheckStatus = "warn"
	CheckFail CheckStatus = "fail"
)

type CheckResult struct {
	Name       string      `json:"name"`
	Status     CheckStatus `json:"status"`
	Critical   bool        `json:"critical"`
	Message    string      `json:"message,omitempty"`
	DurationMS int64       `json:"durationMs"`
}

type Response struct {
	Service   string        `json:"service"`
	Probe     Probe         `json:"probe"`
	Status    Status        `json:"status"`
	Version   string        `json:"version"`
	Commit    string        `json:"commit"`
	CheckedAt string        `json:"checkedAt"`
	Checks    []CheckResult `json:"checks,omitempty"`
}

type Checker interface {
	Name() string
	Critical() bool
	Check(ctx context.Context) CheckResult
}

func New(service string) Response {
	return NewLiveness(service, "", "")
}
