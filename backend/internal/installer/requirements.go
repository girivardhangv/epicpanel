package installer

// Host requirement checks. Platform specifics are isolated in files with
// build tags so cross-compilation for Linux and Windows agents stays clean.
//
// The panel deliberately avoids fabricating values: when a host figure cannot
// be determined the check reports "unknown" rather than inventing one.

import (
	"context"
	"fmt"
	"runtime"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Severity string

const (
	SeverityOK    Severity = "ok"
	SeverityWarn  Severity = "warn"
	SeverityError Severity = "error"
)

type Check struct {
	Name     string   `json:"name"`
	Severity Severity `json:"severity"`
	Passed   bool     `json:"passed"`
	Message  string   `json:"message"`
	Value    string   `json:"value,omitempty"`
}

type Report struct {
	Version string  `json:"version"`
	OS      string  `json:"os"`
	Arch    string  `json:"arch"`
	Checks  []Check `json:"checks"`
}

func RunChecks(version string) *Report {
	report := &Report{Version: version, OS: runtime.GOOS, Arch: runtime.GOARCH}
	report.Checks = append(report.Checks, osCheck())
	cpuTotal := runtime.NumCPU()
	c := Check{
		Name:    "cpu_cores",
		Value:   fmt.Sprintf("%d", cpuTotal),
		Passed:  cpuTotal >= 2,
		Message: fmt.Sprintf("Found %d logical cores; 2+ recommended", cpuTotal),
	}
	if cpuTotal == 0 {
		c.Severity = SeverityWarn
		c.Passed = false
	} else if cpuTotal >= 2 {
		c.Severity = SeverityOK
	} else {
		c.Severity = SeverityWarn
	}
	report.Checks = append(report.Checks, c)

	totalMB := totalMemoryMB()
	memCheck := Check{Name: "memory", Value: humanMemory(totalMB), Passed: true, Message: ""}
	switch {
	case totalMB <= 0:
		memCheck = Check{Name: "memory", Severity: SeverityWarn, Value: "unknown",
			Passed: false, Message: "Could not determine total memory on this host"}
	case totalMB < 512:
		memCheck = Check{Name: "memory", Severity: SeverityError, Value: humanMemory(totalMB),
			Passed: false, Message: "At least 512 MB RAM is required"}
	case totalMB < 1024:
		memCheck = Check{Name: "memory", Severity: SeverityWarn, Value: humanMemory(totalMB),
			Passed: true, Message: "1 GB+ RAM recommended for comfortable operation"}
	default:
		memCheck = Check{Name: "memory", Severity: SeverityOK, Value: humanMemory(totalMB),
			Passed: true, Message: "" }
	}
	report.Checks = append(report.Checks, memCheck)

	freeMB := dataDirFreeDiskMB()
	diskCheck := Check{Name: "disk_free", Value: humanDisk(freeMB)}
	switch {
	case freeMB <= 0 && runtime.GOOS != "linux": // statfs unavailable -> unknown, not fatal
		diskCheck.Severity = SeverityWarn
		diskCheck.Message = "Free space could not be determined automatically"
	case freeMB < 2048:
		diskCheck.Severity = SeverityError
		diskCheck.Passed = false
		diskCheck.Message = "At least 2 GB of free disk space is required"
	case freeMB < 10240:
		diskCheck.Severity = SeverityWarn
		diskCheck.Passed = true
		diskCheck.Message = "10 GB+ free space recommended"
	default:
		diskCheck.Severity = SeverityOK
		diskCheck.Passed = true
	}
	report.Checks = append(report.Checks, diskCheck)

	return report
}

func osCheck() Check {
	switch runtime.GOOS {
	case "linux":
		return Check{Name: "operating_system", Severity: SeverityOK, Passed: true,
			Value: "linux", Message: "Supported production target"}
	case "windows":
		return Check{Name: "operating_system", Severity: SeverityOK, Passed: true,
			Value: "windows", Message: "Supported production target"}
	default:
		return Check{Name: "operating_system", Severity: SeverityWarn, Passed: true,
			Value: runtime.GOOS, Message: "Not a supported production OS; suitable for development only"}
	}
}

func humanMemory(mb int64) string {
	if mb <= 0 {
		return "unknown"
	}
	if mb >= 1024 {
		return fmt.Sprintf("%.1f GB", float64(mb)/1024)
	}
	return fmt.Sprintf("%d MB", mb)
}

func humanDisk(freeMB int64) string {
	if freeMB <= 0 {
		return "unknown"
	}
	if freeMB >= 1024 {
		return fmt.Sprintf("%.1f GB", float64(freeMB)/1024)
	}
	return fmt.Sprintf("%d MB", freeMB)
}

// minimumPostgresVersion guards against running on ancient servers.
const minimumPostgresMajor = 13

func verifyDSN(ctx context.Context, dsn string) error {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return fmt.Errorf("invalid connection string")
	}
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return fmt.Errorf("unable to create connection")
	}
	defer pool.Close()
	var version string
	if err := pool.QueryRow(ctx, `SELECT version()`).Scan(&version); err != nil {
		return fmt.Errorf("cannot query server")
	}
	if !strings.Contains(strings.ToLower(version), "postgresql") &&
		!strings.Contains(strings.ToLower(version), "postgres") {
		return fmt.Errorf("server is not PostgreSQL")
	}
	if major, ok := extractPGMajor(version); ok && major < minimumPostgresMajor {
		return fmt.Errorf("PostgreSQL %d detected; %d or newer required", major, minimumPostgresMajor)
	}
	return nil
}

// extractPGMajor parses the leading server version out of version():
// "PostgreSQL 16.4 (Debian ...) on x86_64" -> 16.
func extractPGMajor(version string) (int, bool) {
	const marker = "postgres"
	lower := strings.ToLower(version)
	idx := strings.Index(lower, marker)
	if idx < 0 {
		return 0, false
	}
	rest := version[idx+len(marker):]
	for strings.ContainsAny(rest[:1], " ()-") && len(rest) > 0 {
		rest = rest[1:]
		if len(rest) == 0 {
			return 0, false
		}
	}
	var major int
	n, err := fmt.Sscanf(rest, "%d", &major)
	if err != nil || n != 1 || major <= 0 || major > 100 {
		return 0, false
	}
	return major, true
}
