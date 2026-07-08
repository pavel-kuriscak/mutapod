package agents

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mutapod/mutapod/internal/config"
)

const (
	filename    = "AGENTS.md"
	beginMarker = "<!-- mutapod:begin -->"
	endMarker   = "<!-- mutapod:end -->"
)

// Status describes whether a project AGENTS.md file already contains the
// mutapod-managed section.
type Status struct {
	Path            string
	Exists          bool
	HasManagedBlock bool
}

// Inspect checks the AGENTS.md state without modifying it.
func Inspect(cfg *config.Config) (Status, error) {
	path := filepath.Join(cfg.Dir, filename)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Status{Path: path}, nil
		}
		return Status{}, fmt.Errorf("agents: read %s: %w", path, err)
	}
	return Status{
		Path:            path,
		Exists:          true,
		HasManagedBlock: hasManagedBlock(string(data)),
	}, nil
}

// Ensure writes or updates the mutapod-managed AGENTS.md section in the
// project directory. The managed block is always kept at the top, and any
// user-authored content outside the managed block is preserved below it.
func Ensure(cfg *config.Config) (string, error) {
	path := filepath.Join(cfg.Dir, filename)
	block := renderManagedBlock(cfg)

	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("agents: read %s: %w", path, err)
	}

	var updated string
	if os.IsNotExist(err) {
		updated = block
	} else {
		updated = mergeManagedBlock(string(data), block)
	}

	if err := os.WriteFile(path, []byte(updated), 0644); err != nil {
		return "", fmt.Errorf("agents: write %s: %w", path, err)
	}
	return path, nil
}

func renderManagedBlock(cfg *config.Config) string {
	composeFile := cfg.Compose.File
	if composeFile == "" {
		composeFile = "auto-detect (compose.yaml / docker-compose.yaml family)"
	}

	lines := []string{
		beginMarker,
		"## Mutapod",
		"",
		"mutapod keeps this repository's source on the local host, syncs it to a cloud VM, and usually runs Docker Compose on that VM. Codex or other agents may be running locally, on the VM, or inside the attached container, so identify the current environment before using lifecycle commands.",
		"",
		"This managed block must stay at the top of `AGENTS.md`. Do not edit it by hand; `mutapod up` verifies and rewrites it with Go code, not an LLM.",
		"",
		"Environment rules:",
		"- First check where you are running: current working directory, hostname, whether `mutapod` is on PATH, whether the path is the remote workspace, and whether the shell is inside a container.",
		"- If you are on the local host and `mutapod` is available, use `mutapod status`, `mutapod leases`, and `mutapod ssh` to inspect the VM.",
		"- `mutapod ssh` is VM-level: bare `mutapod ssh` opens an interactive VM shell, and `mutapod ssh -- <command>` runs a non-interactive command on the VM.",
		"- `mutapod exec -- <command>` is container-level: it runs the command inside `compose.primary_service` through Docker Compose on the VM.",
		"- Prefer `mutapod ssh -- <command>` or `mutapod exec -- <command>` over provider-specific SSH commands such as `gcloud compute ssh` or `az ssh vm`, unless you are debugging mutapod's provider integration.",
		"- From the local host, use `mutapod up`, `mutapod up local`, or `mutapod up headless` to start the environment, and `mutapod up --build` when container images need rebuilding.",
		"- If you are already inside the remote VM or attached container, do not run `mutapod up`, `mutapod down`, `mutapod reset`, or `mutapod destroy`; work in the current checkout/container and use normal project commands.",
		"- Do not assume Docker Compose controls the local machine. Inspect Docker context and Compose availability before running Docker commands.",
		"",
		"Project setup:",
		fmt.Sprintf("- Workspace name: `%s`", cfg.Name),
		fmt.Sprintf("- Provider: `%s`", cfg.Provider.Type),
		fmt.Sprintf("- Remote workspace path: `%s`", cfg.WorkspacePath()),
		fmt.Sprintf("- Sync mode: `%s`", cfg.Sync.Mode),
		fmt.Sprintf("- Compose file: `%s`", composeFile),
		fmt.Sprintf("- VS Code workspace wrapper: `%s`", "mutapod.code-workspace"),
		"",
		"Important troubleshooting notes:",
		"- Source of truth is `mutapod.yaml` in this repository.",
		"- Code is normally synced between the local host and remote VM with Mutagen.",
		"- Docker Compose normally runs on the remote VM, but attached-container shells may already be inside the runtime.",
		"- `mutapod up` waits for the initial sync flush and checks for Mutagen transition problems before building or opening VS Code.",
		"- If a remote build/runtime issue looks stale, rerun `mutapod up --build` from the local host.",
	}

	if cfg.Compose.PrimaryService != "" {
		lines = append(lines, fmt.Sprintf("- Primary service for attached-container workflows: `%s`", cfg.Compose.PrimaryService))
	}
	if cfg.Compose.WorkspaceFolder != "" {
		lines = append(lines, fmt.Sprintf("- In-container workspace folder: `%s`", cfg.Compose.WorkspaceFolder))
	}

	lines = append(lines,
		"",
		"This section is managed by mutapod. You can add your own instructions elsewhere in this file.",
		endMarker,
		"",
	)

	return strings.Join(lines, "\n")
}

func hasManagedBlock(text string) bool {
	_, ok := findManagedBlock(text)
	return ok
}

func mergeManagedBlock(existing, block string) string {
	existing = strings.ReplaceAll(existing, "\r\n", "\n")
	if span, ok := findManagedBlock(existing); ok {
		remaining := strings.Trim(existing[:span.start]+existing[span.end:], "\n")
		if remaining == "" {
			return normalizeSpacing(block)
		}
		return normalizeSpacing(block + "\n" + remaining)
	}

	existing = strings.TrimRight(existing, "\r\n")
	if existing == "" {
		return block
	}
	return normalizeSpacing(block + "\n" + existing)
}

type managedBlockSpan struct {
	start int
	end   int
}

func findManagedBlock(text string) (managedBlockSpan, bool) {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	start := strings.Index(text, beginMarker)
	end := strings.Index(text, endMarker)
	if start >= 0 && end >= 0 && end >= start {
		return managedBlockSpan{start: start, end: end + len(endMarker)}, true
	}
	return findTopMutapodBlock(text)
}

type textLine struct {
	start int
	end   int
	text  string
}

func findTopMutapodBlock(text string) (managedBlockSpan, bool) {
	lines := splitLines(text)
	first := firstContentLine(lines)
	if first < 0 || !isMutapodBlockStart(lines[first].text) {
		return managedBlockSpan{}, false
	}

	searchEnd := len(text)
	evidence := mutapodBlockEvidence(text[lines[first].start:searchEnd])
	for i := first; i < len(lines); i++ {
		if isMutapodEndMarkerLine(lines[i].text) {
			if evidence > 0 {
				return managedBlockSpan{start: lines[first].start, end: lines[i].end}, true
			}
			return managedBlockSpan{}, false
		}
		if isManagedByMutapodLine(lines[i].text) {
			end := lines[i].end
			if i+1 < len(lines) && isMutapodEndMarkerLine(lines[i+1].text) {
				end = lines[i+1].end
			}
			if evidence > 0 {
				return managedBlockSpan{start: lines[first].start, end: end}, true
			}
			return managedBlockSpan{}, false
		}
	}

	for i := first + 1; i < len(lines); i++ {
		if isMarkdownHeading(lines[i].text) && !isMutapodBlockStart(lines[i].text) {
			searchEnd = lines[i].start
			evidence = mutapodBlockEvidence(text[lines[first].start:searchEnd])
			if evidence > 0 {
				return managedBlockSpan{start: lines[first].start, end: searchEnd}, true
			}
			return managedBlockSpan{}, false
		}
	}

	if evidence >= 2 {
		return managedBlockSpan{start: lines[first].start, end: len(text)}, true
	}
	return managedBlockSpan{}, false
}

func splitLines(text string) []textLine {
	if text == "" {
		return nil
	}
	var lines []textLine
	start := 0
	for start < len(text) {
		next := strings.IndexByte(text[start:], '\n')
		if next < 0 {
			lines = append(lines, textLine{start: start, end: len(text), text: text[start:]})
			break
		}
		end := start + next + 1
		lines = append(lines, textLine{start: start, end: end, text: text[start:end]})
		start = end
	}
	return lines
}

func firstContentLine(lines []textLine) int {
	for i, line := range lines {
		if strings.TrimSpace(line.text) != "" {
			return i
		}
	}
	return -1
}

func isMutapodBlockStart(line string) bool {
	trimmed := strings.TrimSpace(line)
	lower := strings.ToLower(trimmed)
	if !strings.Contains(lower, "mutapod") {
		return false
	}
	return strings.HasPrefix(trimmed, "<!--") || strings.HasPrefix(trimmed, "#")
}

func isMutapodEndMarkerLine(line string) bool {
	lower := strings.ToLower(strings.TrimSpace(line))
	if !strings.Contains(lower, "mutapod") {
		return false
	}
	return strings.Contains(lower, "end")
}

func isManagedByMutapodLine(line string) bool {
	return strings.Contains(strings.ToLower(strings.TrimSpace(line)), "this section is managed by mutapod")
}

func isMarkdownHeading(line string) bool {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "#") {
		return false
	}
	return len(trimmed) == 1 || trimmed[1] == ' ' || trimmed[1] == '#'
}

func mutapodBlockEvidence(text string) int {
	lower := strings.ToLower(text)
	needles := []string{
		"managed by mutapod",
		"mutapod up",
		"workspace name:",
		"remote workspace path:",
		"mutapod.yaml",
		"docker compose",
		"mutagen",
		"attached-container",
	}
	count := 0
	for _, needle := range needles {
		if strings.Contains(lower, needle) {
			count++
		}
	}
	return count
}

func normalizeSpacing(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.TrimRight(s, "\n") + "\n"
	return s
}
