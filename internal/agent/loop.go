package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github/Ansh-dS/echo/internal/sandbox"
	"github/Ansh-dS/echo/internal/store"
)

// ToolCall represents a specific tool invocation requested by the AI.
type ToolCall struct {
	Name      string         `json:"name"`                // Name of the tool (e.g., "bash", "execute_command")
	Command   string         `json:"command"`             // Shell command to execute (if terminal tool)
	Arguments map[string]any `json:"arguments,omitempty"` // Extra parameters/arguments
}

// AIResponse contains the decision returned by the AI for the current step.
type AIResponse struct {
	Thought    string    // Internal reasoning / thought process
	IsFinished bool      // True if the AI determines the work is complete
	FinalReply string    // Summary or message when task is finished
	ToolCall   *ToolCall // Non-nil if the AI decides to run a tool
}

// AIClient defines the interface to interact with the LLM.
type AIClient interface {
	// DecideNextStep sends instructions and historical checkpoints to the AI and waits for its reply.
	DecideNextStep(ctx context.Context, instructions string, history []store.CheckpointItem) (*AIResponse, error)
}

// ToolRunner defines the interface for executing tools requested by the AI.
type ToolRunner interface {
	// ExecuteTool runs the requested tool and returns the textual outcome/output.
	ExecuteTool(ctx context.Context, call ToolCall) (string, error)
}

// TaskStatusUpdater updates the overall status of the task in the database.
type TaskStatusUpdater interface {
	// UpdateStatus transitions the task's state in PostgreSQL (e.g. running, completed, failed).
	UpdateStatus(ctx context.Context, taskID string, status string) error
}

// AgentLoop orchestrates the continuous reasoning and execution cycle for a task.
type AgentLoop struct {
	store         store.CheckpointStore
	ai            AIClient
	toolRunner    ToolRunner
	statusUpdater TaskStatusUpdater
	maxSteps      int
}

// LoopConfig configures the dependencies and constraints of the AgentLoop.
type LoopConfig struct {
	Store         store.CheckpointStore
	AI            AIClient
	ToolRunner    ToolRunner
	StatusUpdater TaskStatusUpdater
	MaxSteps      int // Maximum iterations allowed before terminating (defaults to 50)
}

// NewAgentLoop creates an instance of the agent loop with all necessary components.
func NewAgentLoop(cfg LoopConfig) *AgentLoop {
	maxSteps := cfg.MaxSteps
	if maxSteps <= 0 {
		maxSteps = 50 // Safe default to prevent runaway loops
	}

	return &AgentLoop{
		store:         cfg.Store,
		ai:            cfg.AI,
		toolRunner:    cfg.ToolRunner,
		statusUpdater: cfg.StatusUpdater,
		maxSteps:      maxSteps,
	}
}

// Run executes the complete working cycle for a task:
// 1. Starts the continuous cycle and marks the task as 'running'.
// 2. Loads historical checkpoints to resume cleanly if the system previously crashed.
// 3. Repeatedly queries the AI with instructions and history.
// 4. Determines whether to execute a tool or finish the task.
// 5. If tool: executes tool, saves AI request & output to checkpoint store, and repeats.
// 6. If finished: updates overall task status to 'completed' and exits.
func (l *AgentLoop) Run(ctx context.Context, taskID string, instructions string) error {
	slog.Info("starting agent loop cycle", "task_id", taskID)

	// Step 1: Update overall status to 'running' if status updater is provided
	if l.statusUpdater != nil {
		if err := l.statusUpdater.UpdateStatus(ctx, taskID, "running"); err != nil {
			slog.Warn("failed to update task status to running", "task_id", taskID, "error", err)
		}
	}

	// Step 2: Load previous steps from checkpoint store to resume from where it left off
	history, err := l.store.GetHistory(ctx, taskID)
	if err != nil {
		return fmt.Errorf("failed to load checkpoint history for task %s: %w", taskID, err)
	}

	if len(history) > 0 {
		slog.Info("resuming task from previous checkpoints",
			"task_id", taskID,
			"resumed_step", history[len(history)-1].Step,
			"total_prior_actions", len(history),
		)
	} else {
		// Record initial task start milestone
		_, _ = l.store.Append(ctx, store.CheckpointItem{
			TaskID:  taskID,
			Type:    store.CheckpointTypeSystem,
			Content: fmt.Sprintf("Task initialized with instructions: %s", instructions),
		})
		history, _ = l.store.GetHistory(ctx, taskID)
	}

	// Main Working Cycle
	for stepCount := 1; stepCount <= l.maxSteps; stepCount++ {
		// Check for cancellation signal or timeout before starting step
		select {
		case <-ctx.Done():
			slog.Warn("agent loop cancelled by context", "task_id", taskID, "error", ctx.Err())
			return ctx.Err()
		default:
		}

		slog.Info("agent loop executing step", "task_id", taskID, "step", stepCount)

		// Step 3: Send instructions and action history to AI and wait for reply
		aiReply, err := l.ai.DecideNextStep(ctx, instructions, history)
		if err != nil {
			slog.Error("AI decision failed", "task_id", taskID, "step", stepCount, "error", err)
			return fmt.Errorf("AI next step decision failed at step %d: %w", stepCount, err)
		}

		// Save any AI thoughts/reasoning to the checkpoint store
		if aiReply.Thought != "" {
			_, err := l.store.Append(ctx, store.CheckpointItem{
				TaskID:  taskID,
				Type:    store.CheckpointTypeThought,
				Content: aiReply.Thought,
			})
			if err != nil {
				slog.Error("failed to save AI thought checkpoint", "task_id", taskID, "error", err)
			}
		}

		// Step 4: Determine if work is finished OR if AI wants to use a tool
		if aiReply.IsFinished {
			// Step 6: Work is finished. Save completion action and mark task completed.
			slog.Info("AI declared task complete", "task_id", taskID, "final_reply", aiReply.FinalReply)

			_, _ = l.store.Append(ctx, store.CheckpointItem{
				TaskID:  taskID,
				Type:    store.CheckpointTypeAction,
				Content: fmt.Sprintf("Task completed: %s", aiReply.FinalReply),
			})

			if l.statusUpdater != nil {
				if err := l.statusUpdater.UpdateStatus(ctx, taskID, "completed"); err != nil {
					return fmt.Errorf("failed to update task status to completed: %w", err)
				}
			}

			return nil // Stop the cycle
		}

		// Step 5: If AI wants to use a tool
		if aiReply.ToolCall != nil {
			toolCall := *aiReply.ToolCall

			// 5a. Save the AI's tool request to the checkpoint store
			toolReqContent := toolCall.Command
			if toolReqContent == "" {
				toolReqContent = fmt.Sprintf("tool: %s", toolCall.Name)
			}

			metadata := map[string]any{
				"tool_name": toolCall.Name,
			}
			if len(toolCall.Arguments) > 0 {
				metadata["arguments"] = toolCall.Arguments
			}

			_, err = l.store.Append(ctx, store.CheckpointItem{
				TaskID:   taskID,
				Type:     store.CheckpointTypeToolCall,
				Content:  toolReqContent,
				Metadata: metadata,
			})
			if err != nil {
				slog.Error("failed to save tool call checkpoint", "task_id", taskID, "error", err)
			}

			// 5b. Run the tool
			if l.toolRunner == nil {
				return errors.New("tool runner is not configured to execute requested tool")
			}

			slog.Info("executing tool", "task_id", taskID, "tool", toolCall.Name, "command", toolCall.Command)
			toolOutput, runErr := l.toolRunner.ExecuteTool(ctx, toolCall)
			if runErr != nil {
				toolOutput = fmt.Sprintf("tool error: %v\noutput: %s", runErr, toolOutput)
			}

			// 5c. Save the tool's output to the checkpoint store
			_, err = l.store.Append(ctx, store.CheckpointItem{
				TaskID:  taskID,
				Type:    store.CheckpointTypeToolResult,
				Content: toolOutput,
				Metadata: map[string]any{
					"tool_name": toolCall.Name,
					"success":   runErr == nil,
				},
			})
			if err != nil {
				slog.Error("failed to save tool result checkpoint", "task_id", taskID, "error", err)
			}

			// 5d. Reload history and repeat from step 3
			history, err = l.store.GetHistory(ctx, taskID)
			if err != nil {
				return fmt.Errorf("failed to refresh checkpoint history: %w", err)
			}

			continue
		}

		// If AI returned neither a tool call nor finished, record warning and proceed
		slog.Warn("AI returned without tool call or completion", "task_id", taskID, "step", stepCount)
	}

	// If maximum step limit was reached without completion
	if l.statusUpdater != nil {
		_ = l.statusUpdater.UpdateStatus(ctx, taskID, "failed")
	}
	return fmt.Errorf("task %s exceeded maximum step limit of %d", taskID, l.maxSteps)
}

// =============================================================================
// Tool Runner Adapters
// =============================================================================

// SandboxToolRunner adapts our E2B sandbox to the ToolRunner interface.
type SandboxToolRunner struct {
	sandbox *sandbox.Sandbox
}

// NewSandboxToolRunner constructs a tool runner targeting an active E2B sandbox.
func NewSandboxToolRunner(sbx *sandbox.Sandbox) *SandboxToolRunner {
	return &SandboxToolRunner{sandbox: sbx}
}

// ExecuteTool runs the command string inside the E2B sandbox terminal.
func (r *SandboxToolRunner) ExecuteTool(ctx context.Context, call ToolCall) (string, error) {
	if r.sandbox == nil {
		return "", errors.New("sandbox instance is not available")
	}

	cmd := call.Command
	if cmd == "" && call.Name == "bash" {
		if val, ok := call.Arguments["command"].(string); ok {
			cmd = val
		}
	}

	if cmd == "" {
		return "", fmt.Errorf("no command provided for tool %s", call.Name)
	}

	stdout, stderr, err := r.sandbox.CommandsOutput(ctx, cmd)
	if err != nil {
		return fmt.Sprintf("stdout: %s\nstderr: %s", stdout, stderr), err
	}

	if stderr != "" {
		return fmt.Sprintf("%s\n[stderr]: %s", stdout, stderr), nil
	}

	return stdout, nil
}

