package backup

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/dockmesh/dockmesh/internal/notifications"
	"github.com/robfig/cron/v3"
)

// TimezoneSetting names the IANA time zone schedules are evaluated in.
// Empty means the server's own time zone.
const TimezoneSetting = "backups.timezone"

// ErrJobRunning is returned when a job is started while it still runs.
var ErrJobRunning = errors.New("this job is already running")

func newCron(loc *time.Location) *cron.Cron {
	return cron.New(cron.WithLocation(loc))
}

// CheckTimezone validates a value for TimezoneSetting.
func CheckTimezone(name string) error {
	if name == "" {
		return nil
	}
	if _, err := time.LoadLocation(name); err != nil {
		return fmt.Errorf("unknown time zone %q (use an IANA name such as Europe/Berlin)", name)
	}
	return nil
}

// ScheduleInfo describes the time zone backup schedules run in.
type ScheduleInfo struct {
	Timezone       string `json:"timezone"`
	Source         string `json:"source"` // "setting" | "server"
	ServerTimezone string `json:"server_timezone"`
}

func (s *Service) ScheduleInfo() ScheduleInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	info := ScheduleInfo{Timezone: s.tzName, Source: "setting", ServerTimezone: serverTimezone()}
	if info.Timezone == "" {
		info.Timezone, info.Source = info.ServerTimezone, "server"
	}
	return info
}

// SetTimezone switches the scheduler to the named zone ("" = server time
// zone) and re-registers every job. Runs in progress continue.
func (s *Service) SetTimezone(ctx context.Context, name string) error {
	loc := time.Local
	if name != "" {
		l, err := time.LoadLocation(name)
		if err != nil {
			return CheckTimezone(name)
		}
		loc = l
	}
	s.mu.Lock()
	old := s.cron
	s.tzName = name
	s.cron = newCron(loc)
	s.entryIDs = make(map[int64]cron.EntryID)
	if s.started {
		s.cron.Start()
	}
	s.mu.Unlock()
	old.Stop()
	return s.scheduleAll(ctx)
}

// Start moves inline job targets to saved targets, fails runs a previous
// process left in state running, then schedules the enabled jobs.
func (s *Service) Start(ctx context.Context) error {
	if n, err := s.store.migrateInlineTargets(ctx); err != nil {
		slog.Warn("backup: move job targets to saved targets", "err", err)
	} else if n > 0 {
		slog.Info("backup: job targets moved to saved targets", "jobs", n)
	}
	if n, err := s.store.failInterruptedRuns(ctx); err != nil {
		slog.Warn("backup: reconcile interrupted runs", "err", err)
	} else if n > 0 {
		slog.Warn("backup: marked runs interrupted by a restart as failed", "count", n)
	}
	s.mu.Lock()
	s.started = true
	s.cron.Start()
	s.mu.Unlock()
	return s.scheduleAll(ctx)
}

func (s *Service) scheduleAll(ctx context.Context) error {
	jobs, err := s.store.listJobs(ctx)
	if err != nil {
		return err
	}
	for i := range jobs {
		if err := s.schedule(&jobs[i]); err != nil {
			slog.Warn("backup: schedule job", "job", jobs[i].Name, "err", err)
		}
	}
	return nil
}

// Stop cancels runs in progress (their post-hooks still run and the runs
// are recorded as failed) and waits briefly for them to finish.
func (s *Service) Stop() {
	s.mu.Lock()
	c := s.cron
	s.mu.Unlock()
	s.cancelRun()
	stopped := c.Stop()
	done := make(chan struct{})
	go func() {
		<-stopped.Done()
		s.runs.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Minute):
		slog.Warn("backup: runs still in progress at shutdown")
	}
}

// schedule (re)registers a job's cron entry.
func (s *Service) schedule(j *Job) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id, ok := s.entryIDs[j.ID]; ok {
		s.cron.Remove(id)
		delete(s.entryIDs, j.ID)
	}
	if !j.Enabled || j.Schedule == "" {
		return nil
	}
	jobID, name := j.ID, j.Name
	id, err := s.cron.AddFunc(j.Schedule, func() { s.runScheduled(jobID, name) })
	if err != nil {
		return err
	}
	s.entryIDs[j.ID] = id
	return nil
}

func (s *Service) unschedule(id int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cid, ok := s.entryIDs[id]; ok {
		s.cron.Remove(cid)
		delete(s.entryIDs, id)
	}
}

func (s *Service) runScheduled(jobID int64, name string) {
	if !s.claim(jobID) {
		slog.Warn("backup: scheduled run skipped, the previous run is still in progress", "job", name)
		if n := s.exec.notifs; n != nil {
			_, _ = n.Emit(s.runCtx, notifications.EmitInput{
				Kind:     notifications.KindBackupFail,
				Severity: notifications.SevWarning,
				Title:    "Backup skipped: " + name,
				Body:     "The scheduled run was skipped because the previous run of this job is still in progress.",
				Link:     "/backups",
			})
		}
		return
	}
	defer s.release(jobID)
	s.runs.Add(1)
	defer s.runs.Done()
	job, err := s.store.getJob(s.runCtx, jobID)
	if err != nil {
		return
	}
	_, _ = s.exec.Run(s.runCtx, job)
}

func (s *Service) nextRun(jobID int64) *time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.entryIDs[jobID]
	if !ok {
		return nil
	}
	next := s.cron.Entry(id).Next
	if next.IsZero() {
		return nil
	}
	return &next
}

// claim marks a job as running; false if it already is.
func (s *Service) claim(jobID int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running[jobID] {
		return false
	}
	s.running[jobID] = true
	return true
}

func (s *Service) release(jobID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.running, jobID)
}

// serverTimezone names the zone cron falls back to, for display.
func serverTimezone() string {
	if tz := strings.TrimPrefix(os.Getenv("TZ"), ":"); tz != "" {
		return tz
	}
	if b, err := os.ReadFile("/etc/timezone"); err == nil {
		if tz := strings.TrimSpace(string(b)); tz != "" {
			return tz
		}
	}
	if p, err := os.Readlink("/etc/localtime"); err == nil {
		if i := strings.Index(p, "zoneinfo/"); i >= 0 {
			return p[i+len("zoneinfo/"):]
		}
	}
	return time.Local.String()
}
