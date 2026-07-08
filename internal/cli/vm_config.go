package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/mutapod/mutapod/internal/config"
	"github.com/mutapod/mutapod/internal/provider"
	"github.com/mutapod/mutapod/internal/shell"
	"github.com/mutapod/mutapod/internal/state"
)

type vmUpOptions struct {
	Replace     bool
	Adopt       bool
	Interactive bool
	In          io.Reader
	Out         io.Writer
}

var (
	cleanupLocalWorkspaceForVM    = cleanupLocalWorkspace
	inspectReplacementLeasesForVM = inspectReplacementLeases
)

func prepareDeclarativeVM(ctx context.Context, cfg *config.Config, prov provider.Provider, st *state.State, opts vmUpOptions) (*state.State, error) {
	desiredFingerprint, err := cfg.VMConfigFingerprint()
	if err != nil {
		return nil, err
	}
	desiredID, err := prov.InstanceID(ctx)
	if err != nil {
		return nil, err
	}

	if targetChanged(cfg, st, desiredID) {
		fmt.Fprintf(opts.Out, "Warning: cloud target changed; the previous VM was preserved at %s.\n", previousInstanceDescription(st))
		if err := cleanupLocalWorkspaceForVM(ctx, cfg); err != nil {
			shell.Debugf("target change local cleanup: %v", err)
		}
		st = freshState(cfg.Name)
	}

	instanceState, err := prov.State(ctx)
	if err != nil {
		return nil, err
	}
	if instanceState == provider.StateNotFound {
		if opts.Adopt {
			return nil, fmt.Errorf("cannot adopt %s: the VM does not exist", cfg.InstanceName())
		}
		return st, nil
	}

	metadata, err := prov.InstanceMetadata(ctx)
	if err != nil {
		return nil, err
	}
	appliedFingerprint := strings.TrimSpace(metadata.ConfigFingerprint)
	switch {
	case appliedFingerprint == desiredFingerprint:
		if opts.Adopt {
			return nil, fmt.Errorf("cannot adopt %s: the VM is already managed with the current configuration", cfg.InstanceName())
		}
		return st, nil

	case appliedFingerprint == "" && opts.Adopt:
		step("Adopting legacy VM configuration...")
		if err := prov.AdoptInstance(ctx, desiredFingerprint); err != nil {
			return nil, err
		}
		ok("VM configuration adopted: %s", cfg.InstanceName())
		return st, nil

	case appliedFingerprint != "" && opts.Adopt:
		return nil, fmt.Errorf("cannot adopt %s: its stored VM configuration differs from mutapod.yaml; use --replace", cfg.InstanceName())
	}

	reason := "existing VM has no mutapod configuration fingerprint"
	if appliedFingerprint != "" {
		reason = "VM configuration in mutapod.yaml has changed"
	}
	fmt.Fprintf(opts.Out, "VM replacement required: %s.\n", reason)
	fmt.Fprintln(opts.Out, "Recreation permanently deletes the VM and any files stored only on its disk.")

	if !opts.Replace {
		if !opts.Interactive {
			return nil, fmt.Errorf("VM replacement requires confirmation; rerun `mutapod up --replace`")
		}
		confirmed, err := confirmYesNo(opts.In, opts.Out, "Recreate VM? [y/N]: ")
		if err != nil {
			return nil, err
		}
		if !confirmed {
			return nil, fmt.Errorf("VM replacement cancelled")
		}
	}

	inspectReplacementLeasesForVM(ctx, prov, instanceState, cfg, opts.Out)
	if err := cleanupLocalWorkspaceForVM(ctx, cfg); err != nil {
		shell.Debugf("replacement local cleanup: %v", err)
	}

	step("Deleting VM for configuration replacement...")
	if err := prov.DeleteInstance(ctx); err != nil {
		return nil, err
	}
	ok("VM deleted: %s", cfg.InstanceName())

	if err := state.Delete(cfg.Name); err != nil {
		return nil, err
	}
	return freshState(cfg.Name), nil
}

func inspectReplacementLeases(ctx context.Context, prov provider.Provider, instanceState provider.InstanceState, cfg *config.Config, out io.Writer) {
	inspectCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	leases, warning := inspectDestroyLeases(inspectCtx, prov, instanceState, cfg)
	if warning != "" {
		fmt.Fprintf(out, "Warning: %s\n", warning)
		return
	}
	other := otherWorkspaceLeases(cfg.Name, leases)
	if len(other) == 0 {
		return
	}
	fmt.Fprintln(out, "Warning: other active workspace leases will be interrupted by replacement:")
	for _, lease := range other {
		fmt.Fprintf(out, "  - %s (host=%s)\n", lease.Workspace, lease.HostID)
	}
}

func targetChanged(cfg *config.Config, st *state.State, desiredID string) bool {
	if st == nil {
		return false
	}
	if st.ProviderType != "" && st.ProviderType != cfg.Provider.Type {
		return true
	}
	desiredScope := targetScope(cfg, desiredID)
	if st.Instance.TargetScope != "" {
		if cfg.Provider.Type == "azure" {
			return !strings.EqualFold(st.Instance.TargetScope, desiredScope)
		}
		return st.Instance.TargetScope != desiredScope
	}
	if st.Instance.ID == "" {
		return false
	}
	if cfg.Provider.Type == "azure" {
		return !strings.EqualFold(st.Instance.ID, desiredID)
	}
	return st.Instance.ID != desiredID
}

func targetScope(cfg *config.Config, desiredID string) string {
	if cfg.Provider.Type == "azure" {
		return strings.Join([]string{
			"azure",
			strings.TrimSpace(cfg.Provider.Azure.Tenant),
			desiredID,
		}, "|")
	}
	return "gcp|" + desiredID
}

func previousInstanceDescription(st *state.State) string {
	if st.Instance.ID != "" {
		return st.Instance.ID
	}
	if st.Instance.Name != "" {
		return st.Instance.Name
	}
	return "the previous provider target"
}

func freshState(name string) *state.State {
	return &state.State{
		SchemaVersion: state.SchemaVersion,
		Name:          name,
	}
}

func confirmYesNo(in io.Reader, out io.Writer, prompt string) (bool, error) {
	return confirmYesNoDefault(in, out, prompt, false)
}

func confirmYesNoDefault(in io.Reader, out io.Writer, prompt string, defaultYes bool) (bool, error) {
	fmt.Fprint(out, prompt)
	line, err := readLine(in)
	if err != nil {
		return false, err
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, nil
	case "n", "no":
		return false, nil
	case "":
		return defaultYes, nil
	default:
		return false, nil
	}
}

func readLine(in io.Reader) (string, error) {
	var builder strings.Builder
	buffer := make([]byte, 1)
	for {
		n, err := in.Read(buffer)
		if n > 0 {
			if buffer[0] == '\n' {
				return builder.String(), nil
			}
			if buffer[0] != '\r' {
				builder.WriteByte(buffer[0])
			}
		}
		if err != nil {
			if err == io.EOF {
				return builder.String(), nil
			}
			return "", fmt.Errorf("read confirmation: %w", err)
		}
	}
}
