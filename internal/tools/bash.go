package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github/Ansh-dS/echo/internal/agent"
)

// SandboxRunner interface so tools can run commands in sandbox.
type SandboxRunner interface {
	CommandsOutput(ctx context.Context, command string) (string, string, error)
}

// BashTool runs shell commands inside the sandbox with timeout and output truncation.
type BashTool struct {
	runner SandboxRunner
}

// NewBashTool creates a BashTool with the given sandbox runner.
func NewBashTool(runner SandboxRunner) *BashTool {
	return &BashTool{runner: runner}
}

// Run executes a shell command in the sandbox, applying a 30-second timeout and
// truncating output longer than 50 lines.
func (t *BashTool) Run(ctx context.Context, command string) (string, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	stdout, stderr, err := t.runner.CommandsOutput(cmdCtx, command)

	combined := stdout
	if stderr != "" {
		if combined != "" {
			combined += "\n[stderr]:\n" + stderr
		} else {
			combined = "[stderr]:\n" + stderr
		}
	}

	combined = truncateOutput(combined)

	if err != nil {
		return fmt.Sprintf("Command failed.\n%s", combined), err
	}

	if combined == "" {
		return "Command succeeded with no output.", nil
	}

	return fmt.Sprintf("Command succeeded.\n%s", combined), nil
}

// truncateOutput truncates output longer than 50 lines, keeping first 20 and last 20.
func truncateOutput(output string) string {
	lines := strings.Split(output, "\n")
	if len(lines) <= 50 {
		return output
	}

	truncated := len(lines) - 40
	result := make([]string, 0, 41)
	result = append(result, lines[:20]...)
	result = append(result, fmt.Sprintf("... [%d lines truncated] ...", truncated))
	result = append(result, lines[len(lines)-20:]...)
	return strings.Join(result, "\n")
}

// ToolDispatcher implements agent.ToolRunner and dispatches to the right tool.
type ToolDispatcher struct {
	bash     *BashTool
	reader   *ReadTool
	editor   *EditTool
	searcher *SearchTool
}

// NewToolDispatcher creates a ToolDispatcher wired up with all tool implementations.
func NewToolDispatcher(runner SandboxRunner) *ToolDispatcher {
	bash := NewBashTool(runner)
	return &ToolDispatcher{
		bash:     bash,
		reader:   NewReadTool(bash),
		editor:   NewEditTool(bash),
		searcher: NewSearchTool(bash),
	}
}

// ExecuteTool dispatches a ToolCall to the appropriate tool implementation.
func (d *ToolDispatcher) ExecuteTool(ctx context.Context, call agent.ToolCall) (string, error) {
	switch call.Name {
	case "bash", "execute_command":
		cmd := call.Command
		if cmd == "" {
			if val, ok := call.Arguments["command"].(string); ok {
				cmd = val
			}
		}
		if cmd == "" {
			return "", fmt.Errorf("bash tool requires a command")
		}
		return d.bash.Run(ctx, cmd)

	case "read_file":
		return d.reader.Run(ctx, call)

	case "edit_file":
		return d.editor.Run(ctx, call)

	case "search", "search_code":
		return d.searcher.Run(ctx, call)

	default:
		return "", fmt.Errorf("unknown tool: %s", call.Name)
	}
}

