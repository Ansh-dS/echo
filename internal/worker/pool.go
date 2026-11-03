package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/hibiken/asynq"
)

// TaskTypeProcess matches the task type enqueued by the API gateway.
const TaskTypeProcess = "task:process"

// TaskPayload represents the deserialized data received from the Redis queue.
type TaskPayload struct {
	ID        string    `json:"id"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TaskProcessor represents the main working cycle function that processes a task.
type TaskProcessor func(ctx context.Context, task *TaskPayload) error

// PoolConfig holds concurrency limits and timeout settings for the worker pool.
type PoolConfig struct {
	RedisAddr   string
	RedisPass   string
	Concurrency int           // Limit on simultaneous tasks processed
	TaskTimeout time.Duration // Max time per task before forced termination
}

// WorkerPool manages the background workers, queue listening, and execution lifecycle.
type WorkerPool struct {
	server      *asynq.Server
	mux         *asynq.ServeMux
	processor   TaskProcessor
	taskTimeout time.Duration
}

// NewWorkerPool creates a new worker pool satisfying the 5 concurrency and lifecycle requirements:
// 1. Listens continuously on Redis queues.
// 2. Enforces strict concurrency limit via asynq.Config.Concurrency.
// 3. Automatically assigns arriving tasks to available worker routines.
// 4. Deserializes task payload details and hands them to the TaskProcessor working cycle.
// 5. Enforces hard timeout and cancellation signal handling on worker execution.
func NewWorkerPool(cfg PoolConfig, processor TaskProcessor) *WorkerPool {
	// Fallback to safe defaults if not specified
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = 5 // Default limit: 5 concurrent tasks max
	}
	if cfg.TaskTimeout <= 0 {
		cfg.TaskTimeout = 5 * time.Minute // Default task timeout: 5 minutes
	}

	redisOpt := asynq.RedisClientOpt{
		Addr:     cfg.RedisAddr,
		Password: cfg.RedisPass,
	}

	// 2. Enforce strict concurrency limit so the server never overloads
	server := asynq.NewServer(redisOpt, asynq.Config{
		Concurrency: cfg.Concurrency,
		Queues: map[string]int{
			"default": 1,
		},
		ShutdownTimeout: 10 * time.Second,
	})

	pool := &WorkerPool{
		server:      server,
		mux:         asynq.NewServeMux(),
		processor:   processor,
		taskTimeout: cfg.TaskTimeout,
	}

	// 3. Register route to assign incoming tasks to the handler
	pool.mux.HandleFunc(TaskTypeProcess, pool.processTask)

	return pool
}

// Start begins listening continuously for new tasks in the background without blocking.
func (p *WorkerPool) Start() error {
	// 1. Listen continuously for new tasks waiting in line
	slog.Info("worker pool started listening for tasks")
	return p.server.Start(p.mux)
}

// Run begins listening continuously and blocks until an interrupt or shutdown signal is received.
func (p *WorkerPool) Run() error {
	// 1. Listen continuously for new tasks waiting in line (blocking)
	slog.Info("worker pool running")
	return p.server.Run(p.mux)
}

// Stop gracefully shuts down the worker pool, waiting for active workers to complete or abort.
func (p *WorkerPool) Stop() {
	slog.Info("shutting down worker pool...")
	p.server.Shutdown()
}

// processTask handles task extraction, timeout enforcement, and execution in the working cycle.
func (p *WorkerPool) processTask(ctx context.Context, asynqTask *asynq.Task) error {
	// 4. Read the details of the task from the queue payload
	var task TaskPayload
	if err := json.Unmarshal(asynqTask.Payload(), &task); err != nil {
		slog.Error("failed to deserialize task payload", "error", err)
		return fmt.Errorf("invalid task payload: %w", err)
	}

	slog.Info("worker assigned task", "task_id", task.ID)

	// 5. Force the worker to stop immediately if it takes too long or cancellation signal arrives
	taskCtx, cancelTask := context.WithTimeout(ctx, p.taskTimeout)
	defer cancelTask()

	resultChan := make(chan error, 1)

	// Launch the main working cycle on an available worker
	go func() {
		if p.processor != nil {
			// 4. Pass task details over to the main working cycle
			resultChan <- p.processor(taskCtx, &task)
		} else {
			resultChan <- nil
		}
	}()

	// 5. Monitor for timeout, cancellation, or successful completion
	select {
	case <-taskCtx.Done():
		if errors.Is(taskCtx.Err(), context.DeadlineExceeded) {
			slog.Error("task exceeded maximum allowed execution time, worker stopped immediately",
				"task_id", task.ID,
				"timeout", p.taskTimeout,
			)
			return fmt.Errorf("task %s timed out after %v", task.ID, p.taskTimeout)
		}

		slog.Warn("task cancelled by system signal, worker stopped immediately",
			"task_id", task.ID,
		)
		return taskCtx.Err()

	case err := <-resultChan:
		if err != nil {
			slog.Error("task working cycle failed", "task_id", task.ID, "error", err)
			return err
		}

		slog.Info("task completed successfully by worker", "task_id", task.ID)
		return nil
	}
}
