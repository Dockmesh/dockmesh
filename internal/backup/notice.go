package backup

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/dockmesh/dockmesh/internal/notices"
)

const nameCollisionNoticeKey = "backups.job-name-collision"

// SyncNameCollisionNotice raises the admin notice while existing jobs
// share an archive folder (names created before they were checked) and
// resolves it once none do. Called at startup and after jobs change.
func (s *Service) SyncNameCollisionNotice(ctx context.Context, n *notices.Service) error {
	if n == nil {
		return nil
	}
	jobs, err := s.store.listJobs(ctx)
	if err != nil {
		return err
	}
	body := nameCollisions(jobs)
	if body == "" {
		return n.Resolve(ctx, nameCollisionNoticeKey)
	}
	// An open notice keeps its text on Raise, so reopen it when the list
	// of affected jobs changed.
	open, err := n.Open(ctx)
	if err != nil {
		return err
	}
	for _, o := range open {
		if o.Key == nameCollisionNoticeKey && o.Body != body {
			if err := n.Resolve(ctx, nameCollisionNoticeKey); err != nil {
				return err
			}
		}
	}
	return n.Raise(ctx, notices.RaiseInput{
		Key:      nameCollisionNoticeKey,
		Audience: notices.Admins(),
		Severity: notices.SevWarning,
		Title:    "Backup jobs share an archive folder",
		Body:     body,
		Link:     "/backups",
	})
}

// nameCollisions describes every job whose archive folder is shared or
// reserved, or returns "" when there are none.
func nameCollisions(jobs []Job) string {
	byDir := map[string][]string{}
	for _, j := range jobs {
		dir := sanitize(j.Name)
		byDir[dir] = append(byDir[dir], fmt.Sprintf("%q", j.Name))
	}
	var lines []string
	for dir, names := range byDir {
		sort.Strings(names)
		switch {
		case dir == "":
			lines = append(lines, "Without a letter or digit, these use the folder of every job: "+strings.Join(names, ", ")+".")
		case len(names) > 1:
			lines = append(lines, "Same folder: "+strings.Join(names, ", ")+".")
		}
	}
	sys := sanitize(DefaultSystemJobName)
	for _, j := range jobs {
		if sanitize(j.Name) == sys && !HasSystemSource(j.Sources) {
			lines = append(lines, fmt.Sprintf("%q is not a system backup but uses the system backup folder.", j.Name))
		}
	}
	if len(lines) == 0 {
		return ""
	}
	sort.Strings(lines)
	return "These backup jobs write to the same archive folder:" + notices.Bullets(lines) +
		"Rename one of them; until then their retention can delete each other's archives."
}
