package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

// newDeviceCmd wires the `aima device` subcommand tree. Every subcommand is a
// thin wrapper over the corresponding MCP tool on app.ToolDeps, matching the
// project's INV-5 "single source of truth = MCP tool" pattern.
func newDeviceCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "device",
		Short: "Manage this edge's aima-service cloud identity",
		Long: "Manage the device_id / token / recovery_code that links this edge to aima-service.\n" +
			"On first boot `aima serve` auto-registers in the background; use these commands to\n" +
			"inspect state, force-register with an invite code, renew the token, or reset the identity.",
	}
	cmd.AddCommand(
		newDeviceRegisterCmd(app),
		newDeviceStatusCmd(app),
		newDeviceRenewCmd(app),
		newDeviceResetCmd(app),
	)
	return cmd
}

func newDeviceRegisterCmd(app *App) *cobra.Command {
	var (
		inviteCode   string
		recoveryCode string
		force        bool
	)
	cmd := &cobra.Command{
		Use:   "register",
		Short: "Register this edge with aima-service (first boot or after failure)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if app.ToolDeps == nil || app.ToolDeps.DeviceRegister == nil {
				return fmt.Errorf("device.register not wired")
			}
			// Fall back to the root-level --invite-code when the subcommand
			// flag is empty, so `aima --invite-code X device register` works.
			if inviteCode == "" {
				inviteCode = app.InviteCode
			}
			data, err := app.ToolDeps.DeviceRegister(cmd.Context(), inviteCode, recoveryCode, force)
			if err != nil {
				return fmt.Errorf("device register: %w", err)
			}
			fmt.Fprintln(cmd.OutOrStdout(), formatJSON(data))
			return nil
		},
	}
	cmd.Flags().StringVar(&inviteCode, "invite-code", "", "Invite code (required for new devices; falls back to root --invite-code or AIMA_INVITE_CODE env)")
	cmd.Flags().StringVar(&recoveryCode, "recovery-code", "", "Saved recovery code (required when re-registering the same hardware)")
	cmd.Flags().BoolVar(&force, "force", false, "Re-register even if a valid token already exists")
	return cmd
}

func newDeviceStatusCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show the current aima-service registration state",
		RunE: func(cmd *cobra.Command, args []string) error {
			if app.ToolDeps == nil || app.ToolDeps.DeviceStatus == nil {
				return fmt.Errorf("device.status not wired")
			}
			data, err := app.ToolDeps.DeviceStatus(cmd.Context())
			if err != nil {
				return fmt.Errorf("device status: %w", err)
			}
			fmt.Fprintln(cmd.OutOrStdout(), formatJSON(data))
			return nil
		},
	}
}

func newDeviceRenewCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "renew",
		Short: "Force-renew the aima-service bearer token",
		RunE: func(cmd *cobra.Command, args []string) error {
			if app.ToolDeps == nil || app.ToolDeps.DeviceRenew == nil {
				return fmt.Errorf("device.renew not wired")
			}
			data, err := app.ToolDeps.DeviceRenew(cmd.Context())
			if err != nil {
				return fmt.Errorf("device renew: %w", err)
			}
			fmt.Fprintln(cmd.OutOrStdout(), formatJSON(data))
			return nil
		},
	}
}

func newDeviceResetCmd(app *App) *cobra.Command {
	var confirm bool
	cmd := &cobra.Command{
		Use:   "reset",
		Short: "DESTRUCTIVE: clear this edge's cloud identity",
		Long: "Clear the local aima-service identity (device_id, token, recovery_code). " +
			"After this the edge behaves as unregistered until `aima device register` runs. " +
			"The server-side Device record is NOT deleted — the recovery_code path is lost.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if app.ToolDeps == nil || app.ToolDeps.DeviceReset == nil {
				return fmt.Errorf("device.reset not wired")
			}
			data, err := app.ToolDeps.DeviceReset(cmd.Context(), confirm)
			if err != nil {
				return fmt.Errorf("device reset: %w", err)
			}
			fmt.Fprintln(cmd.OutOrStdout(), formatJSON(data))
			return nil
		},
	}
	cmd.Flags().BoolVar(&confirm, "confirm", false, "Must be set to actually perform the reset")
	return cmd
}
