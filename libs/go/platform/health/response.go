package health

import "time"

func NewLiveness(service, version, commit string) Response {
	return Response{
		Service:   defaultString(service, "unknown"),
		Probe:     ProbeLiveness,
		Status:    StatusOK,
		Version:   defaultString(version, "0.0.0"),
		Commit:    defaultString(commit, "dev"),
		CheckedAt: nowRFC3339(),
	}
}

func NewReadiness(service, version, commit string, checks []CheckResult) Response {
	return Response{
		Service:   defaultString(service, "unknown"),
		Probe:     ProbeReadiness,
		Status:    readinessStatus(checks),
		Version:   defaultString(version, "0.0.0"),
		Commit:    defaultString(commit, "dev"),
		CheckedAt: nowRFC3339(),
		Checks:    checks,
	}
}

func readinessStatus(checks []CheckResult) Status {
	status := StatusReady

	for _, check := range checks {
		if check.Status == CheckFail && check.Critical {
			return StatusUnready
		}
		if check.Status == CheckFail || check.Status == CheckWarn {
			status = StatusDegraded
		}
	}

	return status
}

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func nowRFC3339() string {
	return time.Now().UTC().Format(time.RFC3339)
}
