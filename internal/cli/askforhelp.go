package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jguan/aima/internal/support"
)

type supportAskForHelpFunc func(ctx context.Context, description, endpoint, inviteCode, workerCode, recoveryCode, referralCode string) (json.RawMessage, error)

type askForHelpCall struct {
	description  string
	endpoint     string
	inviteCode   string
	workerCode   string
	recoveryCode string
	referralCode string
}

type askForHelpUI struct {
	reader     *bufio.Reader
	out        io.Writer
	pretty     bool
	promptable bool
}

func newAskForHelpCmd(app *App) *cobra.Command {
	var (
		endpoint     string
		inviteCode   string
		workerCode   string
		recoveryCode string
		referralCode string
		noWait       bool
		wait         bool
		jsonOutput   bool
	)

	cmd := &cobra.Command{
		Use:   "askforhelp [request]",
		Short: "Connect to the support service and optionally create a remote help task",
		Long:  "Register this AIMA instance as a support device (https://aimaserver.com), then optionally create a help task from a natural-language request.",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if app.ToolDeps == nil || app.ToolDeps.SupportAskForHelp == nil {
				return fmt.Errorf("askforhelp not wired")
			}

			ui := newAskForHelpUI(cmd.InOrStdin(), cmd.OutOrStdout(), jsonOutput)
			call := askForHelpCall{
				description:  strings.TrimSpace(strings.Join(args, " ")),
				endpoint:     endpoint,
				inviteCode:   inviteCode,
				workerCode:   workerCode,
				recoveryCode: recoveryCode,
				referralCode: referralCode,
			}

			data, result, err := executeAskForHelp(cmd.Context(), app.ToolDeps.SupportAskForHelp, ui, &call)
			if err != nil {
				return fmt.Errorf("askforhelp: %w", err)
			}

			if ui.pretty {
				renderAskForHelpLinked(ui.out, result)
			} else {
				fmt.Fprintln(ui.out, formatJSON(data))
			}

			if ui.pretty && call.description == "" && result.TaskID == "" && !noWait {
				description, err := ui.promptInitialRequest()
				if err != nil {
					return fmt.Errorf("askforhelp: %w", err)
				}
				if description != "" {
					call.description = description
					if _, result, err = executeAskForHelp(cmd.Context(), app.ToolDeps.SupportAskForHelp, ui, &call); err != nil {
						return fmt.Errorf("askforhelp: %w", err)
					}
				}
			}

			if ui.pretty {
				renderAskForHelpTask(ui.out, result)
			}

			shouldWait := wait || (!noWait && (call.description != "" || ui.pretty))
			if !shouldWait || app.Support == nil {
				return nil
			}
			if ui.pretty {
				renderAskForHelpForegroundWait(ui.out, result.TaskID != "")
			}

			runErr := app.Support.Run(cmd.Context(), support.RunOptions{
				StopWhenIdle: call.description != "" || !wait,
				Prompt:       supportPrompt(ui),
				Notify:       supportNotify(ui),
			})
			if errors.Is(runErr, context.Canceled) {
				return nil
			}
			if runErr != nil {
				return fmt.Errorf("askforhelp wait: %w", runErr)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&endpoint, "endpoint", "", "Support service base URL (persisted as support.endpoint)")
	cmd.Flags().StringVar(&inviteCode, "invite-code", "", "Invite code for first-time device registration (persisted as support.invite_code)")
	cmd.Flags().StringVar(&workerCode, "worker-code", "", "Worker enrollment code for first-time device registration (persisted as support.worker_code)")
	cmd.Flags().StringVar(&recoveryCode, "recovery-code", "", "Saved recovery code for refreshing an older registration")
	cmd.Flags().StringVar(&referralCode, "referral-code", "", "Referral code for self-service registration")
	cmd.Flags().BoolVar(&noWait, "no-wait", false, "Create the task and return immediately instead of waiting for completion")
	cmd.Flags().BoolVar(&wait, "wait", false, "Keep polling in the foreground even without a new request")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Force JSON output instead of the interactive terminal UX")

	return cmd
}

func newAskForHelpUI(in io.Reader, out io.Writer, jsonOutput bool) *askForHelpUI {
	return &askForHelpUI{
		reader:     bufio.NewReader(in),
		out:        out,
		pretty:     !jsonOutput && isTerminalWriter(out),
		promptable: !jsonOutput && isTerminalReader(in) && isTerminalWriter(out),
	}
}

func executeAskForHelp(ctx context.Context, ask supportAskForHelpFunc, ui *askForHelpUI, call *askForHelpCall) (json.RawMessage, support.AskResult, error) {
	for {
		data, err := ask(ctx, call.description, call.endpoint, call.inviteCode, call.workerCode, call.recoveryCode, call.referralCode)
		if err == nil {
			var result support.AskResult
			if decodeErr := json.Unmarshal(data, &result); decodeErr != nil {
				return nil, support.AskResult{}, fmt.Errorf("decode askforhelp result: %w", decodeErr)
			}
			return data, result, nil
		}

		var promptErr *support.RegistrationPromptError
		if !ui.promptable || !errors.As(err, &promptErr) {
			return nil, support.AskResult{}, err
		}

		switch promptErr.Kind {
		case support.RegistrationPromptInviteOrWorker:
			code, promptErr := ui.promptInviteOrWorker(invitePromptReason(call.referralCode, promptErr.Detail))
			if promptErr != nil {
				return nil, support.AskResult{}, promptErr
			}
			call.inviteCode = code
			call.workerCode = ""
		case support.RegistrationPromptRecovery:
			code, promptErr := ui.promptRecoveryCode(recoveryPromptReason(promptErr.Detail))
			if promptErr != nil {
				return nil, support.AskResult{}, promptErr
			}
			call.recoveryCode = code
		default:
			return nil, support.AskResult{}, err
		}
	}
}

func renderAskForHelpLinked(out io.Writer, result support.AskResult) {
	fmt.Fprintln(out, "Device linked 设备已连接!")
	if result.DeviceID != "" {
		fmt.Fprintf(out, "Device ID: %s\n", result.DeviceID)
	}
	if result.BudgetUSD > 0 {
		fmt.Fprintf(out, "Budget 额度: $%.2f/$%.2f USD", result.SpentUSD, result.BudgetUSD)
		if result.MaxTasks > 0 {
			fmt.Fprintf(out, " (%d/%d tasks 任务)", result.UsedTasks, result.MaxTasks)
		}
		fmt.Fprintln(out)
	} else if result.MaxTasks > 0 {
		fmt.Fprintf(out, "Budget 额度: %d/%d tasks 任务\n", result.UsedTasks, result.MaxTasks)
	}
	if result.IsBound {
		fmt.Fprintln(out, "Account bound 已绑定账户")
	}
	if result.ReferralCode != "" {
		fmt.Fprintf(out, "Your referral code 你的推荐码: %s\n", result.ReferralCode)
	}
}

func renderAskForHelpTask(out io.Writer, result support.AskResult) {
	if result.TaskID == "" {
		return
	}
	switch {
	case result.Created:
		fmt.Fprintf(out, "\nSupport task active 任务已创建: %s (%s)\n", result.TaskID, displayOr(result.TaskStatus, "created"))
	case result.ReusedActiveTask:
		fmt.Fprintf(out, "\nSupport task active 任务继续中: %s (%s)\n", result.TaskID, displayOr(result.TaskStatus, "created"))
	default:
		fmt.Fprintf(out, "\nSupport task: %s (%s)\n", result.TaskID, displayOr(result.TaskStatus, "created"))
	}
	if result.TaskTarget != "" {
		fmt.Fprintf(out, "Target 目标: %s\n", result.TaskTarget)
	}
}

func renderAskForHelpForegroundWait(out io.Writer, hasTask bool) {
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "Attached support UI 已进入前台等待")
	fmt.Fprintln(out, "Ctrl+C exits this UI / Ctrl+C 退出前台界面")
	if !hasTask {
		fmt.Fprintln(out, "AIMA is ready. Waiting for tasks... / 准备就绪，等待指令")
	}
}

func invitePromptReason(referralCode, detail string) string {
	detail = strings.TrimSpace(detail)
	if detail == "" {
		return ""
	}
	lower := strings.ToLower(detail)
	if referralCode != "" && strings.Contains(lower, "referral") {
		return fmt.Sprintf("Referral link needs a fresh invite or worker code 推荐链接当前需要新的邀请码或 Worker 接入码: %s", detail)
	}
	return fmt.Sprintf("Platform needs an invite or worker code 平台需要邀请码或 Worker 接入码: %s", detail)
}

func recoveryPromptReason(detail string) string {
	detail = strings.TrimSpace(detail)
	if detail == "" {
		return "This device was previously registered but no local recovery code was found. Enter the saved recovery code to continue 此设备之前已经注册过，但本地没有找到恢复码。请输入已保存的恢复码后继续。"
	}
	return fmt.Sprintf("This device needs the saved recovery code to continue 此设备需要已保存的恢复码后才能继续: %s", detail)
}

func supportPrompt(ui *askForHelpUI) support.PromptFunc {
	return func(ctx context.Context, prompt support.Prompt) (string, error) {
		_ = ctx
		if prompt.Question == "" {
			return "", nil
		}
		if ui.pretty {
			fmt.Fprintf(ui.out, "\nNeed your input 需要你的输入\n%s\n> ", prompt.Question)
		} else {
			fmt.Fprintf(ui.out, "\n[askforhelp] %s\n> ", prompt.Question)
		}
		answer, err := ui.readLine()
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		return strings.TrimSpace(answer), nil
	}
}

func supportNotify(ui *askForHelpUI) support.NotifyFunc {
	return func(ctx context.Context, notification support.Notification) {
		_ = ctx
		if ui.pretty {
			switch {
			case notification.TaskID != "":
				fmt.Fprintf(ui.out, "\nSupport task %s finished: %s\n", notification.TaskID, displayOr(notification.TaskStatus, "finished"))
				if notification.BudgetUSDTotal > 0 {
					fmt.Fprintf(ui.out, "Budget remaining 剩余额度: $%.2f/$%.2f USD\n", notification.BudgetUSDRemaining, notification.BudgetUSDTotal)
				} else if notification.BudgetTasksRemaining > 0 || notification.BudgetTasksTotal > 0 {
					fmt.Fprintf(ui.out, "Tasks remaining 剩余额度: %d/%d\n", notification.BudgetTasksRemaining, notification.BudgetTasksTotal)
				}
				if notification.ReferralCode != "" {
					fmt.Fprintf(ui.out, "Referral code 推荐码: %s\n", notification.ReferralCode)
				}
				if notification.ShareText != "" {
					fmt.Fprintf(ui.out, "Share 分享: %s\n", notification.ShareText)
				}
			case notification.Message != "":
				fmt.Fprintf(ui.out, "\n[support] %s\n", notification.Message)
			}
			return
		}

		switch {
		case notification.TaskID != "":
			fmt.Fprintf(ui.out, "\n[askforhelp] task %s finished: %s\n", notification.TaskID, notification.TaskStatus)
		case notification.Message != "":
			fmt.Fprintf(ui.out, "\n[askforhelp] %s\n", notification.Message)
		}
	}
}

func (ui *askForHelpUI) promptInitialRequest() (string, error) {
	if !ui.promptable {
		return "", nil
	}
	fmt.Fprintln(ui.out, "")
	fmt.Fprintln(ui.out, "AIMA is ready. Waiting for tasks... / 准备就绪，等待指令")
	fmt.Fprintln(ui.out, "Type a request below, or press Enter to just wait / 输入需求，或直接回车进入等待")
	fmt.Fprintln(ui.out, "What would you like me to do? / 请问需要我做什么？")
	fmt.Fprint(ui.out, "> ")
	answer, err := ui.readLine()
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimSpace(answer), nil
}

func (ui *askForHelpUI) promptInviteOrWorker(reason string) (string, error) {
	return ui.promptRequired(
		reason,
		"Please enter your invite or worker code 请输入邀请码或 Worker 接入码:",
		"invite or worker code is required 需要邀请码或 Worker 接入码",
	)
}

func (ui *askForHelpUI) promptRecoveryCode(reason string) (string, error) {
	return ui.promptRequired(
		reason,
		"Please enter your saved recovery code 请输入已保存的恢复码:",
		"recovery code is required 需要恢复码",
	)
}

func (ui *askForHelpUI) promptRequired(reason, question, emptyErr string) (string, error) {
	if !ui.promptable {
		return "", errors.New(emptyErr)
	}
	if strings.TrimSpace(reason) != "" {
		fmt.Fprintln(ui.out, "")
		fmt.Fprintln(ui.out, reason)
	}
	fmt.Fprintln(ui.out, "")
	fmt.Fprintln(ui.out, question)
	fmt.Fprint(ui.out, "> ")
	answer, err := ui.readLine()
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return "", errors.New(emptyErr)
	}
	return answer, nil
}

func (ui *askForHelpUI) readLine() (string, error) {
	return ui.reader.ReadString('\n')
}

func isTerminalReader(r io.Reader) bool {
	file, ok := r.(*os.File)
	if !ok {
		return false
	}
	return isCharDevice(file)
}

func isTerminalWriter(w io.Writer) bool {
	file, ok := w.(*os.File)
	if !ok {
		return false
	}
	return isCharDevice(file)
}

func isCharDevice(file *os.File) bool {
	info, err := file.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

func displayOr(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
