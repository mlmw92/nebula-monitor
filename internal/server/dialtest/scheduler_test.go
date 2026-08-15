package dialtest

import (
	"testing"
	"time"
)

func TestSchedulerHonorsPerTaskInterval(t *testing.T) {
	store := NewStore(t.TempDir() + "/dialtest.yaml")
	store.Create(Task{ID: "fast", Name: "fast", Type: TaskTypeHTTP, Target: "fast.example", Interval: 10, Enabled: true})
	store.Create(Task{ID: "slow", Name: "slow", Type: TaskTypeHTTP, Target: "slow.example", Interval: 60, Enabled: true})

	s := NewScheduler(store, nil)
	calls := map[string]int{}
	s.run = func(task Task) Result {
		calls[task.ID]++
		return Result{TaskID: task.ID, Up: true}
	}

	start := time.Unix(1000, 0)
	s.runDue(start)
	if calls["fast"] != 1 || calls["slow"] != 1 {
		t.Fatalf("initial calls = %#v, want one call per task", calls)
	}
	s.runDue(start.Add(9 * time.Second))
	if calls["fast"] != 1 || calls["slow"] != 1 {
		t.Fatalf("calls before interval = %#v, want unchanged", calls)
	}
	s.runDue(start.Add(10 * time.Second))
	if calls["fast"] != 2 || calls["slow"] != 1 {
		t.Fatalf("calls at fast interval = %#v, want fast=2 slow=1", calls)
	}
	s.runDue(start.Add(60 * time.Second))
	if calls["fast"] != 3 || calls["slow"] != 2 {
		t.Fatalf("calls at slow interval = %#v, want fast=3 slow=2", calls)
	}
}

func TestSchedulerIntervalChangeRunsImmediately(t *testing.T) {
	store := NewStore(t.TempDir() + "/dialtest.yaml")
	store.Create(Task{ID: "task", Name: "task", Type: TaskTypeHTTP, Target: "example", Interval: 60, Enabled: true})
	s := NewScheduler(store, nil)
	calls := 0
	s.run = func(task Task) Result {
		calls++
		return Result{TaskID: task.ID, Up: true}
	}

	start := time.Unix(2000, 0)
	s.runDue(start)
	if calls != 1 {
		t.Fatalf("initial calls = %d, want 1", calls)
	}
	task, _ := store.Get("task")
	task.Interval = 5
	if err := store.Update(task); err != nil {
		t.Fatal(err)
	}
	s.runDue(start.Add(time.Second))
	if calls != 2 {
		t.Fatalf("calls after interval change = %d, want immediate second call", calls)
	}
}
