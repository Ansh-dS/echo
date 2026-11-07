package store

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// CheckpointType defines the categorization of an item saved in the history.
type CheckpointType string

const (
	CheckpointTypeThought    CheckpointType = "thought"     // Internal reasoning / thoughts from the AI
	CheckpointTypeAction     CheckpointType = "action"      // Action taken or decision made
	CheckpointTypeToolCall   CheckpointType = "tool_call"   // Tool execution request from the AI
	CheckpointTypeToolResult CheckpointType = "tool_result" // Outcome/output returned from running a tool
	CheckpointTypeSystem     CheckpointType = "system"      // System-level milestone (e.g. startup, status change)
)

// CheckpointItem represents a single permanently saved piece of information for a task.
type CheckpointItem struct {
	ID        string         `json:"id"`
	TaskID    string         `json:"task_id"`
	Step      int            `json:"step"`
	Type      CheckpointType `json:"type"`
	Content   string         `json:"content"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
}

// CheckpointStore specifies operations to permanently store and retrieve task action histories.
type CheckpointStore interface {
	// Append adds a new piece of information to the very end of the task's record.
	Append(ctx context.Context, item CheckpointItem) (*CheckpointItem, error)
	// GetHistory retrieves the entire chronological list of saved actions for a task.
	GetHistory(ctx context.Context, taskID string) ([]CheckpointItem, error)
}

// =============================================================================
// PostgreSQL Implementation
// =============================================================================

// PostgresCheckpointStore persists checkpoints in PostgreSQL.
type PostgresCheckpointStore struct {
	pool *pgxpool.Pool
}

// NewPostgresCheckpointStore constructs a PostgreSQL-backed checkpoint store.
func NewPostgresCheckpointStore(pool *pgxpool.Pool) *PostgresCheckpointStore {
	return &PostgresCheckpointStore{pool: pool}
}

// Append adds a new checkpoint to the end of the list inside PostgreSQL.
// It auto-computes the next sequential step number for the specific task.
func (s *PostgresCheckpointStore) Append(ctx context.Context, item CheckpointItem) (*CheckpointItem, error) {
	if item.TaskID == "" {
		return nil, fmt.Errorf("task_id is required to append a checkpoint")
	}

	metadataJSON := []byte("{}")
	if len(item.Metadata) > 0 {
		var err error
		metadataJSON, err = json.Marshal(item.Metadata)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal checkpoint metadata: %w", err)
		}
	}

	query := `
		INSERT INTO checkpoints (task_id, step, type, content, metadata, created_at)
		VALUES (
			$1,
			COALESCE((SELECT MAX(step) + 1 FROM checkpoints WHERE task_id = $1), 1),
			$2,
			$3,
			$4,
			NOW()
		)
		RETURNING id, step, created_at;
	`

	saved := item
	err := s.pool.QueryRow(ctx, query,
		item.TaskID,
		string(item.Type),
		item.Content,
		metadataJSON,
	).Scan(&saved.ID, &saved.Step, &saved.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("failed to append checkpoint for task %s: %w", item.TaskID, err)
	}

	return &saved, nil
}

// GetHistory retrieves the complete historical list of saved actions for the task, ordered by step.
func (s *PostgresCheckpointStore) GetHistory(ctx context.Context, taskID string) ([]CheckpointItem, error) {
	query := `
		SELECT id, task_id, step, type, content, metadata, created_at
		FROM checkpoints
		WHERE task_id = $1
		ORDER BY step ASC, created_at ASC;
	`

	rows, err := s.pool.Query(ctx, query, taskID)
	if err != nil {
		return nil, fmt.Errorf("failed to query checkpoints for task %s: %w", taskID, err)
	}
	defer rows.Close()

	var history []CheckpointItem
	for rows.Next() {
		var (
			item         CheckpointItem
			rawType      string
			metadataJSON []byte
		)

		err := rows.Scan(
			&item.ID,
			&item.TaskID,
			&item.Step,
			&rawType,
			&item.Content,
			&metadataJSON,
			&item.CreatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan checkpoint row: %w", err)
		}

		item.Type = CheckpointType(rawType)
		if len(metadataJSON) > 0 {
			_ = json.Unmarshal(metadataJSON, &item.Metadata)
		}

		history = append(history, item)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error reading checkpoint rows: %w", err)
	}

	return history, nil
}

// =============================================================================
// In-Memory Implementation (For Testing and Offline Mocking)
// =============================================================================

// MemoryCheckpointStore provides an in-memory thread-safe checkpoint store.
type MemoryCheckpointStore struct {
	mu      sync.RWMutex
	records map[string][]CheckpointItem
}

// NewMemoryCheckpointStore constructs an in-memory checkpoint store.
func NewMemoryCheckpointStore() *MemoryCheckpointStore {
	return &MemoryCheckpointStore{
		records: make(map[string][]CheckpointItem),
	}
}

// Append adds a new checkpoint to the end of the task's in-memory slice.
func (m *MemoryCheckpointStore) Append(_ context.Context, item CheckpointItem) (*CheckpointItem, error) {
	if item.TaskID == "" {
		return nil, fmt.Errorf("task_id is required to append a checkpoint")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	history := m.records[item.TaskID]
	saved := item
	saved.Step = len(history) + 1
	saved.CreatedAt = time.Now().UTC()
	if saved.ID == "" {
		saved.ID = fmt.Sprintf("cp-%d", saved.Step)
	}

	m.records[item.TaskID] = append(history, saved)
	return &saved, nil
}

// GetHistory returns a copy of the ordered action history for the task.
func (m *MemoryCheckpointStore) GetHistory(_ context.Context, taskID string) ([]CheckpointItem, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	history, exists := m.records[taskID]
	if !exists {
		return []CheckpointItem{}, nil
	}

	// Return a copy to prevent mutation
	copied := make([]CheckpointItem, len(history))
	copy(copied, history)
	return copied, nil
}
