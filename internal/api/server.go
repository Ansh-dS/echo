package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hibiken/asynq"
	"github.com/jackc/pgx/v5/pgxpool"
)

// =============================================================================
// 1. Configuration
// =============================================================================

// Config encapsulates all runtime environment configuration.
type Config struct {
	HTTPPort    string
	DatabaseURL string
	RedisAddr   string
	RedisPass   string
}

// loadConfig reads configuration from the environment, applying sensible local defaults.
func loadConfig() Config {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		// Defaults matching docker-compose: echo_admin / echo_password / echo_db
		dbURL = "postgres://echo_admin:echo_password@localhost:5432/echo_db?sslmode=disable"
	}

	redisAddr := os.Getenv("REDIS_ADDR")
	if redisAddr == "" {
		redisAddr = "localhost:6379"
	}

	redisPass := os.Getenv("REDIS_PASSWORD")
	if redisPass == "" {
		redisPass = "echo_password"
	}

	return Config{
		HTTPPort:    port,
		DatabaseURL: dbURL,
		RedisAddr:   redisAddr,
		RedisPass:   redisPass,
	}
}

// =============================================================================
// 2. Domain Models
// =============================================================================

// Task represents a task entity stored in PostgreSQL.
type Task struct {
	ID        string    `json:"id"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// =============================================================================
// 3. Repository Layer (PostgreSQL Data Access)
// =============================================================================

// TaskRepository handles database queries for the tasks table.
type TaskRepository struct {
	pool *pgxpool.Pool
}

// NewTaskRepository initializes a connection pool and verifies database reachability.
func NewTaskRepository(ctx context.Context, databaseURL string) (*TaskRepository, error) {
	poolConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse database config: %w", err)
	}

	poolConfig.MaxConns = 50
	poolConfig.MinConns = 10
	poolConfig.MaxConnIdleTime = 5 * time.Minute
	poolConfig.MaxConnLifetime = 1 * time.Hour

	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create database pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		slog.Warn("database ping failed on startup (verify PostgreSQL container is running)", "error", err)
	} else {
		slog.Info("connected to PostgreSQL successfully")
	}

	return &TaskRepository{pool: pool}, nil
}

// Close gracefully terminates all active database connections.
func (r *TaskRepository) Close() {
	if r.pool != nil {
		r.pool.Close()
	}
}

// Create inserts a new task in 'created' status and returns the generated record.
func (r *TaskRepository) Create(ctx context.Context) (*Task, error) {
	query := `
		INSERT INTO tasks (status)
		VALUES ('created')
		RETURNING id, status, created_at, updated_at;
	`

	var task Task
	err := r.pool.QueryRow(ctx, query).Scan(
		&task.ID,
		&task.Status,
		&task.CreatedAt,
		&task.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to insert task: %w", err)
	}

	return &task, nil
}

// TransitionToQueued atomically moves a task from 'created' to 'queued'.
// The PostgreSQL trigger enforces that this transition is valid.
func (r *TaskRepository) TransitionToQueued(ctx context.Context, taskID string) error {
	query := `
		UPDATE tasks
		SET status = 'queued', updated_at = NOW()
		WHERE id = $1 AND status = 'created';
	`
	tag, err := r.pool.Exec(ctx, query, taskID)
	if err != nil {
		return fmt.Errorf("failed to update task status to queued: %w", err)
	}

	if tag.RowsAffected() == 0 {
		return fmt.Errorf("task %s not found or not in 'created' state", taskID)
	}

	return nil
}

// =============================================================================
// 4. Queue Layer (Redis / Asynq Producer)
// =============================================================================

const TaskTypeProcess = "task:process"

// TaskQueue manages scheduling background jobs into Redis.
type TaskQueue struct {
	client *asynq.Client
}

// NewTaskQueue connects to the Redis task queue broker.
func NewTaskQueue(redisAddr, redisPassword string) *TaskQueue {
	client := asynq.NewClient(asynq.RedisClientOpt{
		Addr:     redisAddr,
		Password: redisPassword,
	})
	slog.Info("connected to Redis task queue client successfully")
	return &TaskQueue{client: client}
}

// Close shuts down the Asynq client connection.
func (q *TaskQueue) Close() error {
	if q.client != nil {
		return q.client.Close()
	}
	return nil
}

// Enqueue serializes the task into a job payload and pushes it onto the default queue.
func (q *TaskQueue) Enqueue(ctx context.Context, task *Task) error {
	payload, err := json.Marshal(task)
	if err != nil {
		return fmt.Errorf("failed to marshal task payload: %w", err)
	}

	job := asynq.NewTask(TaskTypeProcess, payload)
	info, err := q.client.EnqueueContext(ctx, job)
	if err != nil {
		return fmt.Errorf("failed to enqueue job into Redis: %w", err)
	}

	slog.Info("task scheduled in Redis queue",
		"task_id", task.ID,
		"asynq_job_id", info.ID,
		"queue", info.Queue,
	)
	return nil
}

// =============================================================================
// 5. HTTP Handler & Response Helpers
// =============================================================================

// TaskHandler orchestrates HTTP requests for task operations.
type TaskHandler struct {
	repo  *TaskRepository
	queue *TaskQueue
}

// NewTaskHandler constructs a handler with its dependencies.
func NewTaskHandler(repo *TaskRepository, queue *TaskQueue) *TaskHandler {
	return &TaskHandler{
		repo:  repo,
		queue: queue,
	}
}

// RegisterRoutes binds HTTP endpoints to handler methods.
func (h *TaskHandler) RegisterRoutes(router *http.ServeMux) {
	router.HandleFunc("POST /task", h.CreateTask)
}

// CreateTask orchestrates:
// 1. Database insert ('created')
// 2. Queue dispatch (Redis)
// 3. Database state transition ('created' -> 'queued')
// 4. Returns HTTP 202 Accepted with the queued task payload
func (h *TaskHandler) CreateTask(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Step 1: Persist initial task in Postgres
	task, err := h.repo.Create(ctx)
	if err != nil {
		slog.Error("failed to create task", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "failed to create task in database")
		return
	}

	// Step 2: Push task to Redis queue for asynchronous execution
	if err := h.queue.Enqueue(ctx, task); err != nil {
		slog.Error("failed to enqueue task to Redis", "task_id", task.ID, "error", err)
		writeJSONError(w, http.StatusInternalServerError, "failed to enqueue task")
		return
	}

	// Step 3: Transition state to 'queued' in Postgres
	if err := h.repo.TransitionToQueued(ctx, task.ID); err != nil {
		slog.Error("failed to mark task as queued in database", "task_id", task.ID, "error", err)
	} else {
		task.Status = "queued"
	}

	// Step 4: Return 202 Accepted
	writeJSON(w, http.StatusAccepted, task)
}

// writeJSON serializes data to the response with Content-Type: application/json.
func writeJSON(w http.ResponseWriter, statusCode int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		slog.Error("failed to encode JSON response", "error", err)
	}
}

// writeJSONError formats an error message consistently as JSON.
func writeJSONError(w http.ResponseWriter, statusCode int, message string) {
	writeJSON(w, statusCode, map[string]string{"error": message})
}

// =============================================================================
// 6. Application Startup & Graceful Shutdown
// =============================================================================

func main() {
	if err := run(); err != nil {
		slog.Error("application encountered fatal error", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg := loadConfig()

	// Root context listening for interrupt signals (Ctrl+C, SIGTERM)
	rootCtx, stopSignal := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignal()

	// 1. Initialize Database Repository
	taskRepo, err := NewTaskRepository(rootCtx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("database initialization failed: %w", err)
	}
	defer taskRepo.Close()

	// 2. Initialize Queue Service
	taskQueue := NewTaskQueue(cfg.RedisAddr, cfg.RedisPass)
	defer taskQueue.Close()

	// 3. Register HTTP Routes
	router := http.NewServeMux()
	taskHandler := NewTaskHandler(taskRepo, taskQueue)
	taskHandler.RegisterRoutes(router)

	// 4. Configure HTTP Server
	server := &http.Server{
		Addr:         ":" + cfg.HTTPPort,
		Handler:      router,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	// Run server in background goroutine so main can listen for shutdown signals
	serverErrors := make(chan error, 1)
	go func() {
		slog.Info("HTTP server listening", "port", cfg.HTTPPort)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErrors <- err
		}
	}()

	// Wait for shutdown signal or fatal startup error
	select {
	case err := <-serverErrors:
		return fmt.Errorf("server failed to start: %w", err)
	case <-rootCtx.Done():
		slog.Info("shutdown signal received, commencing graceful shutdown...")
	}

	// Allow up to 10 seconds for active in-flight requests to complete
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelShutdown()

	if err := server.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("server forced shutdown failed: %w", err)
	}

	slog.Info("server stopped gracefully")
	return nil
}
