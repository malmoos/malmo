package api

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// Job is the brain-side job (BRAIN_UI_PROTOCOL.md Pattern B). The brain owns
// its own job-id space. Skeleton: in-memory, no persistence across restarts.
type Job struct {
	ID        string         `json:"job_id"`
	Kind      string         `json:"kind"`
	Status    string         `json:"status"` // running|completed|failed|cancelled|cancelling|stalled
	Step      string         `json:"step,omitempty"`
	Progress  float64        `json:"progress"`
	Result    map[string]any `json:"result,omitempty"`
	Error     *JobError      `json:"error,omitempty"`
	StartedAt time.Time      `json:"started_at"`

	mu sync.Mutex
}

type JobError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (j *Job) setStep(s string) {
	j.mu.Lock()
	j.Step = s
	j.mu.Unlock()
}

func (j *Job) snapshot() Job {
	j.mu.Lock()
	defer j.mu.Unlock()
	return Job{
		ID: j.ID, Kind: j.Kind, Status: j.Status, Step: j.Step,
		Progress: j.Progress, Result: j.Result, Error: j.Error, StartedAt: j.StartedAt,
	}
}

type Jobs struct {
	mu sync.Mutex
	m  map[string]*Job
}

func newJobs() *Jobs { return &Jobs{m: map[string]*Job{}} }

// run creates a job and executes fn in a goroutine. fn reports progress via the
// passed *Job and returns a result map or an error.
func (js *Jobs) run(kind string, fn func(job *Job) (map[string]any, error)) *Job {
	job := &Job{ID: newJobID(), Kind: kind, Status: "running", StartedAt: time.Now()}
	js.mu.Lock()
	js.m[job.ID] = job
	js.mu.Unlock()

	go func() {
		result, err := fn(job)
		job.mu.Lock()
		if err != nil {
			job.Status = "failed"
			job.Error = &JobError{Code: "job-failed", Message: err.Error()}
		} else {
			job.Status = "completed"
			job.Progress = 1
			job.Result = result
		}
		job.mu.Unlock()
	}()
	return job
}

func (js *Jobs) get(id string) (*Job, bool) {
	js.mu.Lock()
	defer js.mu.Unlock()
	j, ok := js.m[id]
	return j, ok
}

func newJobID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return "j_" + hex.EncodeToString(b)
}
