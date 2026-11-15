package mediavalidation

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"syscall"
)

const helperArg = "__bounded_media_probe"

type boundedBuffer struct{ bytes.Buffer }

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > maxJSON {
		return 0, errors.New("probe_output_limit")
	}
	return b.Buffer.Write(p)
}

// ProcessProbe isolates ffprobe from the service's credentials and applies
// process-local resource limits in a fresh helper, never to the Go service.
func ProcessProbe(ctx context.Context, args []string) ([]byte, error) {
	exe, e := os.Executable()
	if e != nil {
		return nil, e
	}
	command := exec.CommandContext(ctx, exe, append([]string{helperArg}, args...)...)
	command.Env = []string{"PATH=/usr/bin:/bin", "LC_ALL=C"}
	var out, stderr boundedBuffer
	command.Stdout = &out
	command.Stderr = &stderr
	if e = command.Run(); e != nil {
		var exit *exec.ExitError
		if errors.As(e, &exit) && (exit.ExitCode() == 126 || exit.ExitCode() == 127) {
			return nil, exec.ErrNotFound
		}
		return nil, e
	}
	if stderr.Len() > 0 {
		return nil, errors.New("ffprobe_reported_errors")
	}
	return out.Bytes(), nil
}

// HandleProbeHelper must be called before Cobra/config/logging initialization.
// No credentials are parsed, no network requests run, and only a fixed binary
// is executed with an environment that contains no service secrets.
func HandleProbeHelper(args []string) bool {
	if len(args) < 2 || args[1] != helperArg {
		return false
	}
	if runtime.GOOS != "linux" {
		os.Exit(126)
	}
	for resource, limit := range map[int]uint64{syscall.RLIMIT_AS: 512 << 20, syscall.RLIMIT_DATA: 96 << 20, syscall.RLIMIT_CPU: 15, syscall.RLIMIT_FSIZE: 0, syscall.RLIMIT_NOFILE: 64} {
		if syscall.Setrlimit(resource, &syscall.Rlimit{Cur: limit, Max: limit}) != nil {
			os.Exit(126)
		}
	}
	if syscall.Setpriority(syscall.PRIO_PROCESS, 0, 10) != nil {
		os.Exit(126)
	}
	if syscall.Exec("/usr/bin/ffprobe", append([]string{"ffprobe"}, args[2:]...), []string{"PATH=/usr/bin:/bin", "LC_ALL=C"}) != nil {
		os.Exit(127)
	}
	return true
}
