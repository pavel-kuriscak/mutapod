// Package portrelay prepares in-container TCP relays for services that bind
// only to container loopback addresses during local-style development.
package portrelay

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/mutapod/mutapod/internal/compose"
	"github.com/mutapod/mutapod/internal/config"
	"github.com/mutapod/mutapod/internal/profiles"
	"github.com/mutapod/mutapod/internal/provider"
)

// Ensure starts best-effort relays inside the primary service container for the
// container-side ports in ports.
func Ensure(ctx context.Context, p provider.Provider, cfg *config.Config, activeProfiles []profiles.Spec, ports []int) error {
	ports = normalizePorts(ports)
	if cfg.Compose.PrimaryService == "" || len(ports) == 0 {
		return nil
	}
	return compose.ExecInPrimaryService(ctx, p, cfg, activeProfiles, Script(ports))
}

// Script returns the POSIX shell used inside the primary service container.
func Script(ports []int) string {
	ports = normalizePorts(ports)
	if len(ports) == 0 {
		return ":"
	}

	parts := make([]string, 0, len(ports))
	for _, port := range ports {
		parts = append(parts, fmt.Sprintf("%d", port))
	}
	script := strings.ReplaceAll(relayScriptTemplate, "__PORTS__", shellQuote(strings.Join(parts, " ")))
	return strings.ReplaceAll(script, "__PYTHON_RELAY__", pythonRelayProgram)
}

func normalizePorts(ports []int) []int {
	if len(ports) == 0 {
		return nil
	}
	seen := make(map[int]bool, len(ports))
	normalized := make([]int, 0, len(ports))
	for _, port := range ports {
		if port <= 0 || port > 65535 || seen[port] {
			continue
		}
		seen[port] = true
		normalized = append(normalized, port)
	}
	slices.Sort(normalized)
	return normalized
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

const relayScriptTemplate = `set -eu
relay_dir=/tmp/mutapod-port-relays
ports=__PORTS__
mkdir -p "$relay_dir"

stop_pidfile() {
  pidfile="$1"
  [ -f "$pidfile" ] || return 0
  pid="$(cat "$pidfile" 2>/dev/null || true)"
  case "$pid" in
    ''|*[!0-9]*) rm -f "$pidfile"; return 0 ;;
  esac
  if kill -0 "$pid" 2>/dev/null; then
    kill "$pid" 2>/dev/null || true
  fi
  rm -f "$pidfile"
}

for pidfile in "$relay_dir"/*.pid; do
  [ -e "$pidfile" ] || continue
  old_port="${pidfile##*/}"
  old_port="${old_port%.pid}"
  case " $ports " in
    *" $old_port "*) ;;
    *) stop_pidfile "$pidfile" ;;
  esac
done

pick_python() {
  if command -v python3 >/dev/null 2>&1; then
    command -v python3
    return 0
  fi
  if command -v python >/dev/null 2>&1; then
    command -v python
    return 0
  fi
  return 1
}

discover_bind_ip_shell() {
  if command -v hostname >/dev/null 2>&1; then
    for candidate in $(hostname -i 2>/dev/null || true); do
      case "$candidate" in
        ""|127.*|::1|*:*) ;;
        *) echo "$candidate"; return 0 ;;
      esac
    done
  fi
  return 1
}

py="$(pick_python || true)"
if [ -z "$py" ] && ! command -v socat >/dev/null 2>&1; then
  echo "mutapod: no python3, python, or socat found; skipping loopback port relay" >&2
  exit 2
fi

for port in $ports; do
  pidfile="$relay_dir/$port.pid"
  log="$relay_dir/$port.log"
  stop_pidfile "$pidfile"
  if [ -n "$py" ]; then
    helper="$relay_dir/relay-$port.py"
    cat > "$helper" <<'PY'
__PYTHON_RELAY__
PY
    MUTAPOD_RELAY_PORT="$port" nohup "$py" "$helper" >"$log" 2>&1 &
  else
    bind_ip="$(discover_bind_ip_shell || true)"
    if [ -z "$bind_ip" ]; then
      echo "mutapod: could not discover container IP for port $port; skipping relay" >"$log"
      continue
    fi
    nohup socat TCP-LISTEN:"$port",bind="$bind_ip",fork,reuseaddr TCP:127.0.0.1:"$port" >"$log" 2>&1 &
  fi
  pid="$!"
  echo "$pid" > "$pidfile"
  sleep 0.2
  if ! kill -0 "$pid" 2>/dev/null; then
    rm -f "$pidfile"
  fi
done`

const pythonRelayProgram = `import errno
import os
import socket
import sys
import threading
import time

listen_port = int(os.environ["MUTAPOD_RELAY_PORT"])
target_port = listen_port

def discover_bind_ip():
    override = os.environ.get("MUTAPOD_RELAY_BIND_IP")
    if override:
        return override

    try:
        sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        try:
            sock.connect(("192.0.2.1", 80))
            candidate = sock.getsockname()[0]
        finally:
            sock.close()
        if candidate and not candidate.startswith("127."):
            return candidate
    except OSError:
        pass

    for info in socket.getaddrinfo(socket.gethostname(), None, socket.AF_INET):
        candidate = info[4][0]
        if candidate and not candidate.startswith("127."):
            return candidate

    raise RuntimeError("could not discover a non-loopback container IP")

def close_socket(sock):
    try:
        sock.shutdown(socket.SHUT_RDWR)
    except OSError:
        pass
    try:
        sock.close()
    except OSError:
        pass

def pipe(src, dst):
    try:
        while True:
            data = src.recv(65536)
            if not data:
                break
            dst.sendall(data)
    except OSError:
        pass
    finally:
        close_socket(src)
        close_socket(dst)

def target_reachable(timeout=1):
    try:
        probe = socket.create_connection(("127.0.0.1", target_port), timeout=timeout)
    except OSError:
        return False
    close_socket(probe)
    return True

def handle(client):
    try:
        upstream = socket.create_connection(("127.0.0.1", target_port), timeout=5)
    except OSError as exc:
        print(f"mutapod relay: 127.0.0.1:{target_port} unavailable: {exc}", file=sys.stderr, flush=True)
        close_socket(client)
        return
    upstream.settimeout(None)
    client.settimeout(None)

    threading.Thread(target=pipe, args=(client, upstream), daemon=True).start()
    threading.Thread(target=pipe, args=(upstream, client), daemon=True).start()

def wait_for_target():
    print(f"mutapod relay: waiting for 127.0.0.1:{target_port}", flush=True)
    while not target_reachable():
        time.sleep(0.5)

def serve(bind_ip):
    server = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    server.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    try:
        server.bind((bind_ip, listen_port))
    except OSError as exc:
        if exc.errno == errno.EADDRINUSE:
            print(f"mutapod relay: {bind_ip}:{listen_port} is already in use; relay not needed", flush=True)
            return "busy"
        raise
    server.listen(128)
    server.settimeout(1)
    print(f"mutapod relay: {bind_ip}:{listen_port} -> 127.0.0.1:{target_port}", flush=True)
    missed_probes = 0
    while True:
        try:
            client, _ = server.accept()
        except socket.timeout:
            if target_reachable():
                missed_probes = 0
            else:
                missed_probes += 1
                if missed_probes >= 5:
                    close_socket(server)
                    return "target-down"
            continue
        threading.Thread(target=handle, args=(client,), daemon=True).start()

def main():
    bind_ip = discover_bind_ip()
    while True:
        wait_for_target()
        result = serve(bind_ip)
        if result == "busy":
            time.sleep(10)
        else:
            time.sleep(0.5)

if __name__ == "__main__":
    main()
`
