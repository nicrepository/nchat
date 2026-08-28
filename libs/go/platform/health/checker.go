package health

import (
	"context"
	"net/http"
	"time"
)

type StaticChecker struct {
	name     string
	critical bool
	status   CheckStatus
	message  string
}

func NewStaticChecker(name string, critical bool, status CheckStatus, message string) Checker {
	return StaticChecker{
		name:     defaultString(name, "unnamed-check"),
		critical: critical,
		status:   defaultCheckStatus(status),
		message:  message,
	}
}

func (c StaticChecker) Name() string {
	return c.name
}

func (c StaticChecker) Critical() bool {
	return c.critical
}

func (c StaticChecker) Check(ctx context.Context) CheckResult {
	start := time.Now()
	status := c.status
	message := c.message
	if ctx.Err() != nil {
		status = CheckFail
		message = "check timed out"
	}

	return CheckResult{
		Name:       c.name,
		Status:     status,
		Critical:   c.critical,
		Message:    message,
		DurationMS: elapsedMS(start),
	}
}

func EvaluateReadiness(service, version, commit string, checks []Checker, timeout time.Duration) (Response, int) {
	ctx := context.Background()
	var cancel context.CancelFunc
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	results := make([]CheckResult, 0, len(checks))
	for _, checker := range checks {
		results = append(results, evaluateCheck(ctx, checker))
	}

	response := NewReadiness(service, version, commit, results)
	if response.Status == StatusUnready {
		return response, http.StatusServiceUnavailable
	}
	return response, http.StatusOK
}

func evaluateCheck(ctx context.Context, checker Checker) CheckResult {
	start := time.Now()
	if checker == nil {
		return CheckResult{
			Name:       "unknown-check",
			Status:     CheckFail,
			Critical:   true,
			DurationMS: elapsedMS(start),
		}
	}

	result := checker.Check(ctx)
	if result.Name == "" {
		result.Name = defaultString(checker.Name(), "unknown-check")
	}
	result.Critical = checker.Critical()
	if result.Status == "" {
		result.Status = CheckFail
	}
	if ctx.Err() != nil && result.Status == CheckPass {
		result.Status = CheckFail
		result.Message = "check timed out"
	}
	result.DurationMS = elapsedMS(start)
	return result
}

func defaultCheckStatus(status CheckStatus) CheckStatus {
	if status == "" {
		return CheckFail
	}
	return status
}

func elapsedMS(start time.Time) int64 {
	duration := time.Since(start).Milliseconds()
	if duration < 0 {
		return 0
	}
	return duration
}
