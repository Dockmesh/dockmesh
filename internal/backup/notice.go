package backup

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/dockmesh/dockmesh/internal/i18n"
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
	if body.Key == "" {
		return n.Resolve(ctx, nameCollisionNoticeKey)
	}
	// An open notice keeps its text on Raise, so reopen it when the list
	// of affected jobs changed.
	open, err := n.Open(ctx)
	if err != nil {
		return err
	}
	for _, o := range open {
		if o.Key == nameCollisionNoticeKey && o.Body != body.In(i18n.Default) {
			if err := n.Resolve(ctx, nameCollisionNoticeKey); err != nil {
				return err
			}
		}
	}
	return n.Raise(ctx, notices.RaiseInput{
		Key:      nameCollisionNoticeKey,
		Audience: notices.Admins(),
		Severity: notices.SevWarning,
		Title:    i18n.Msg("notice.backup_collision.title"),
		Body:     body,
		Link:     "/backups",
	})
}

// nameCollisions describes every job whose archive folder is shared or
// reserved, or returns an empty Text when there are none.
func nameCollisions(jobs []Job) i18n.Text {
	byDir := map[string][]string{}
	for _, j := range jobs {
		dir := sanitize(j.Name)
		byDir[dir] = append(byDir[dir], fmt.Sprintf("%q", j.Name))
	}
	var lines []i18n.Text
	for dir, names := range byDir {
		sort.Strings(names)
		switch {
		case dir == "":
			lines = append(lines, i18n.Msg("notice.backup_collision.no_name", strings.Join(names, ", ")))
		case len(names) > 1:
			lines = append(lines, i18n.Msg("notice.backup_collision.same", strings.Join(names, ", ")))
		}
	}
	sys := sanitize(DefaultSystemJobName)
	for _, j := range jobs {
		if sanitize(j.Name) == sys && !HasSystemSource(j.Sources) {
			lines = append(lines, i18n.Msg("notice.backup_collision.system", fmt.Sprintf("%q", j.Name)))
		}
	}
	if len(lines) == 0 {
		return i18n.Text{}
	}
	sort.Slice(lines, func(a, b int) bool { return lines[a].In(i18n.Default) < lines[b].In(i18n.Default) })
	return i18n.Msg("notice.backup_collision.body", notices.Bullets(lines))
}
