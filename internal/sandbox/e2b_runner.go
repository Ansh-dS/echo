package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/eric642/e2b-go-sdk"
)

// E2BClient manages connectivity and sandbox lifecycle with E2B cloud.
type E2BClient struct {
	client *e2b.Client
}

// Sandbox wraps an active E2B remote cloud microVM instance.
type Sandbox struct {
	instance *e2b.Sandbox
}

// NewE2BClient reads E2B_API_KEY from the environment and initializes the E2B SDK client.
func NewE2BClient() (*E2BClient, error) {
	apiKey := os.Getenv("E2B_API_KEY")
	if apiKey == "" {
		return nil, errors.New("E2B_API_KEY environment variable is not set")
	}

	client, err := e2b.NewClient(e2b.Config{APIKey: apiKey})
	if err != nil {
		return nil, fmt.Errorf("failed to initialize E2B client: %w", err)
	}

	return &E2BClient{client: client}, nil
}

// CreateSandbox spins up a fresh sandbox environment in E2B cloud.
func (c *E2BClient) CreateSandbox(ctx context.Context) (*Sandbox, error) {
	sbx, err := c.client.Create(ctx, e2b.CreateOptions{
		Template: "base",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create sandbox: %w", err)
	}

	return &Sandbox{instance: sbx}, nil
}

// CommandsToSandbox executes a shell command inside the sandbox terminal and returns stdout, stderr, and any error.
func (s *Sandbox) commandsOutput(ctx context.Context, command string) (string, string, error) {
	if s.instance == nil {
		return "", "", errors.New("sandbox instance is not initialized")
	}

	// 1. Execute the command in the sandbox via bash
	handle, err := s.instance.Commands.Run(ctx, "bash", e2b.RunOptions{
		Args: []string{"-c", command},
	})
	if err != nil && handle == nil {
		return "", "", fmt.Errorf("failed to start command in sandbox: %w", err)
	}

	// 2. Collect stdout, stderr, and exit status
	result, waitErr := handle.Wait(ctx)
	if result != nil {
		return result.Stdout, result.Stderr, waitErr
	}

	return "", "", waitErr
}

// CloseSandbox terminates the active sandbox to free up cloud resources.
func (s *Sandbox) CloseSandbox(ctx context.Context) error {
	if s.instance == nil {
		return nil
	}

	if err := s.instance.Kill(ctx); err != nil {
		return fmt.Errorf("failed to terminate sandbox: %w", err)
	}

	return nil
}
