// Package task implements the in-memory async OCR task queue: a fixed worker
// pool consuming submitted OCRRequests through the fallback engine. Task state
// lives only for the lifetime of the process.
package task

import (
	"context"
	"fmt"
	"math/rand"
	"sync"
	"time"

	"ocr-router/internal/ocr"
)

// TaskStatus represents the status of a task
type TaskStatus string

const (
	TaskStatusPending   TaskStatus = "pending"
	TaskStatusRunning   TaskStatus = "running"
	TaskStatusCompleted TaskStatus = "completed"
	TaskStatusFailed    TaskStatus = "failed"
	TaskStatusCancelled TaskStatus = "cancelled"
)

// Task represents an async OCR task
type Task struct {
	ID        string           `json:"id"`
	Status    TaskStatus       `json:"status"`
	Request   *ocr.OCRRequest  `json:"request"`
	Result    *ocr.OCRResult   `json:"result,omitempty"`
	Error     string           `json:"error,omitempty"`
	CreatedAt time.Time        `json:"created_at"`
	StartedAt *time.Time       `json:"started_at,omitempty"`
	EndedAt   *time.Time       `json:"ended_at,omitempty"`
	Provider  string           `json:"provider,omitempty"`
}

// TaskManager manages async OCR tasks
type TaskManager struct {
	tasks    sync.Map
	queue    chan *Task
	engine   *ocr.FallbackEngine
	workers  int
	timeout  time.Duration
	stopCh   chan struct{}
	wg       sync.WaitGroup
}

// NewTaskManager creates a new task manager
func NewTaskManager(engine *ocr.FallbackEngine, workers, queueSize int, timeout time.Duration) *TaskManager {
	return &TaskManager{
		queue:   make(chan *Task, queueSize),
		engine:  engine,
		workers: workers,
		timeout: timeout,
		stopCh:  make(chan struct{}),
	}
}

// Start starts the task manager workers
func (m *TaskManager) Start() {
	for i := 0; i < m.workers; i++ {
		m.wg.Add(1)
		go m.worker(i)
	}
}

// Stop stops the task manager
func (m *TaskManager) Stop() {
	close(m.stopCh)
	m.wg.Wait()
}

// Submit submits a new task
func (m *TaskManager) Submit(req *ocr.OCRRequest) *Task {
	task := &Task{
		ID:        generateTaskID(),
		Status:    TaskStatusPending,
		Request:   req,
		CreatedAt: time.Now(),
	}

	m.tasks.Store(task.ID, task)

	// Try to submit with timeout to avoid blocking forever
	select {
	case m.queue <- task:
		// Successfully queued
	case <-time.After(30 * time.Second):
		// Queue full, mark as failed
		task.Status = TaskStatusFailed
		task.Error = "task queue full, submission timed out"
		now := time.Now()
		task.EndedAt = &now
	}

	return task
}

// GetTask returns a task by ID
func (m *TaskManager) GetTask(id string) (*Task, bool) {
	if v, ok := m.tasks.Load(id); ok {
		return v.(*Task), true
	}
	return nil, false
}

// ListTasks returns all tasks
func (m *TaskManager) ListTasks(status TaskStatus) []*Task {
	var tasks []*Task

	m.tasks.Range(func(key, value interface{}) bool {
		task := value.(*Task)
		if status == "" || task.Status == status {
			tasks = append(tasks, task)
		}
		return true
	})

	return tasks
}

// CancelTask cancels a task
func (m *TaskManager) CancelTask(id string) bool {
	if v, ok := m.tasks.Load(id); ok {
		task := v.(*Task)
		if task.Status == TaskStatusPending || task.Status == TaskStatusRunning {
			task.Status = TaskStatusCancelled
			now := time.Now()
			task.EndedAt = &now
			return true
		}
	}
	return false
}

// worker processes tasks from the queue
func (m *TaskManager) worker(id int) {
	defer m.wg.Done()

	for {
		select {
		case <-m.stopCh:
			return
		case task := <-m.queue:
			m.processTask(task)
		}
	}
}

// processTask processes a single task
func (m *TaskManager) processTask(task *Task) {
	// Update status to running
	task.Status = TaskStatusRunning
	now := time.Now()
	task.StartedAt = &now

	// Create context with timeout
	ctx := context.Background()
	if m.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, m.timeout)
		defer cancel()
	}

	// Perform OCR
	result, err := m.engine.Recognize(ctx, task.Request)

	// Update task
	endNow := time.Now()
	task.EndedAt = &endNow

	if err != nil {
		// Check if task was cancelled
		if ctx.Err() == context.Canceled {
			task.Status = TaskStatusCancelled
			task.Error = "task cancelled"
		} else if ctx.Err() == context.DeadlineExceeded {
			task.Status = TaskStatusFailed
			task.Error = "task timed out"
		} else {
			task.Status = TaskStatusFailed
			task.Error = err.Error()
		}
	} else {
		task.Status = TaskStatusCompleted
		task.Result = result
		task.Provider = result.Provider
	}
}

// generateTaskID generates a unique task ID
func generateTaskID() string {
	return fmt.Sprintf("task_%d_%s", time.Now().UnixNano(), randomString(8))
}

// randomString generates a random string
func randomString(n int) string {
	const letters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, n)
	for i := range b {
		b[i] = letters[rand.Intn(len(letters))]
	}
	return string(b)
}
