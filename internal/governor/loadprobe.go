package governor

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// numCPU is a seam over runtime.NumCPU so the normalization is testable; it is a
// package var (not a const) only so a test can pin it deterministically.
var numCPU = runtime.NumCPU

// SystemLoadProbe is the default LoadProbe. It reads the OS 1-minute load
// average using only the standard library, with no cgo and no third-party
// dependency:
//
//   - Linux: parse /proc/loadavg (field 1 is the 1-minute average).
//   - Other unix (macOS/BSD): shell out to `uptime` and parse its
//     "load average: a, b, c" tail. This avoids cgo getloadavg / the
//     x/sys/unix sysctl dependency while still reading the real value.
//   - Anything else, or any parse/exec failure: report ok=false, which the
//     governor treats as permissive (the host-load limit never false-denies on
//     a platform whose load cannot be read).
//
// The probe is read-only and best-effort; it never blocks indefinitely (the
// uptime fallback is bounded by a short context timeout).
type SystemLoadProbe struct{}

// Load1 implements LoadProbe.
func (SystemLoadProbe) Load1() (float64, bool) {
	if runtime.GOOS == "linux" {
		if v, ok := loadFromProc(); ok {
			return v, true
		}
	}
	// Non-Linux unix (and a Linux fallback if /proc was unreadable).
	if runtime.GOOS != "windows" {
		if v, ok := loadFromUptime(); ok {
			return v, true
		}
	}
	return 0, false
}

// loadFromProc reads the 1-minute load average from /proc/loadavg (Linux).
func loadFromProc() (float64, bool) {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, false
	}
	fields := strings.Fields(string(b))
	if len(fields) == 0 {
		return 0, false
	}
	v, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// loadFromUptime parses the 1-minute load average out of the `uptime` command,
// whose output ends with "... load average: 1.23, 1.10, 0.95" (commas are
// locale-dependent; we split on whitespace and strip a trailing comma).
func loadFromUptime() (float64, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, "uptime").Output()
	if err != nil {
		return 0, false
	}
	s := string(out)
	idx := strings.LastIndex(s, "load average")
	if idx < 0 {
		// Some locales/systems use "load averages" (plural, BSD).
		idx = strings.LastIndex(s, "load averages")
		if idx < 0 {
			return 0, false
		}
	}
	rest := s[idx:]
	// Take the token after the colon.
	colon := strings.IndexByte(rest, ':')
	if colon < 0 {
		return 0, false
	}
	fields := strings.Fields(rest[colon+1:])
	if len(fields) == 0 {
		return 0, false
	}
	first := strings.TrimSuffix(strings.TrimSpace(fields[0]), ",")
	// Some locales use a comma as the decimal separator; normalize.
	first = strings.ReplaceAll(first, ",", ".")
	v, err := strconv.ParseFloat(first, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}
