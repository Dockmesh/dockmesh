package templates

import (
	"context"
	"crypto/rand"
	"database/sql"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

var (
	ErrNotFound         = errors.New("template not found")
	ErrBuiltinImmutable = errors.New("built-in templates cannot be modified; create a copy instead")
	ErrDuplicateSlug    = errors.New("template slug already in use")
)

// Template is what the HTTP API returns.
type Template struct {
	ID          int64      `json:"id"`
	Slug        string     `json:"slug"`
	Name        string     `json:"name"`
	Description string     `json:"description,omitempty"`
	IconURL     string     `json:"icon_url,omitempty"`
	Compose     string     `json:"compose"`
	EnvTemplate string     `json:"env,omitempty"`
	Parameters  []ParamDef `json:"parameters"`
	Author      string     `json:"author,omitempty"`
	Version     string     `json:"version,omitempty"`
	Builtin     bool       `json:"builtin"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// Input is the create/update payload (user-defined templates only).
type Input struct {
	Slug        string     `json:"slug"`
	Name        string     `json:"name"`
	Description string     `json:"description,omitempty"`
	IconURL     string     `json:"icon_url,omitempty"`
	Compose     string     `json:"compose"`
	EnvTemplate string     `json:"env,omitempty"`
	Parameters  []ParamDef `json:"parameters,omitempty"`
	Author      string     `json:"author,omitempty"`
	Version     string     `json:"version,omitempty"`
}

// builtinFS embeds the YAML files under builtin/.
//
//go:embed builtin/*.yaml
var builtinFS embed.FS

// builtinRecord is the on-disk YAML shape for a built-in template.
type builtinRecord struct {
	Slug        string     `yaml:"slug"`
	Name        string     `yaml:"name"`
	Description string     `yaml:"description"`
	IconURL     string     `yaml:"icon_url"`
	Compose     string     `yaml:"compose"`
	Env         string     `yaml:"env"`
	Parameters  []ParamDef `yaml:"parameters"`
	Author      string     `yaml:"author"`
	Version     string     `yaml:"version"`
}

type Service struct {
	db *sql.DB
}

func New(db *sql.DB) *Service { return &Service{db: db} }

// SeedBuiltins reads every embedded YAML file and upserts it into the
// stack_templates table with builtin=1. Safe to call on every boot —
// existing rows are replaced so template fixes ship with the binary.
func (s *Service) SeedBuiltins(ctx context.Context) error {
	entries, err := fs.ReadDir(builtinFS, "builtin")
	if err != nil {
		return fmt.Errorf("read builtin dir: %w", err)
	}
	count := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		data, err := builtinFS.ReadFile("builtin/" + e.Name())
		if err != nil {
			slog.Warn("builtin template read", "name", e.Name(), "err", err)
			continue
		}
		var rec builtinRecord
		if err := yaml.Unmarshal(data, &rec); err != nil {
			slog.Warn("builtin template parse", "name", e.Name(), "err", err)
			continue
		}
		params, err := Parse(rec.Compose+"\n"+rec.Env, rec.Parameters)
		if err != nil {
			slog.Warn("builtin template param parse", "slug", rec.Slug, "err", err)
			continue
		}
		paramsJSON, _ := json.Marshal(params)
		_, err = s.db.ExecContext(ctx, `
			INSERT INTO stack_templates
			  (slug, name, description, icon_url, compose, env_tmpl, parameters, author, version, builtin)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 1)
			ON CONFLICT(slug) DO UPDATE SET
			  name = excluded.name,
			  description = excluded.description,
			  icon_url = excluded.icon_url,
			  compose = excluded.compose,
			  env_tmpl = excluded.env_tmpl,
			  parameters = excluded.parameters,
			  author = excluded.author,
			  version = excluded.version,
			  builtin = 1,
			  updated_at = CURRENT_TIMESTAMP
			WHERE stack_templates.builtin = 1`,
			rec.Slug, rec.Name, rec.Description, rec.IconURL,
			rec.Compose, rec.Env, string(paramsJSON),
			rec.Author, rec.Version)
		if err != nil {
			slog.Warn("builtin template upsert", "slug", rec.Slug, "err", err)
			continue
		}
		count++
	}
	slog.Info("stack templates seeded", "count", count)
	return nil
}

// -----------------------------------------------------------------------------
// CRUD
// -----------------------------------------------------------------------------

func (s *Service) List(ctx context.Context) ([]Template, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, slug, name, COALESCE(description, ''), COALESCE(icon_url, ''),
		       compose, COALESCE(env_tmpl, ''), parameters,
		       COALESCE(author, ''), COALESCE(version, ''), builtin,
		       created_at, updated_at
		  FROM stack_templates
		 ORDER BY builtin DESC, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Template{}
	for rows.Next() {
		t, err := scanTemplate(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

func (s *Service) Get(ctx context.Context, id int64) (*Template, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, slug, name, COALESCE(description, ''), COALESCE(icon_url, ''),
		       compose, COALESCE(env_tmpl, ''), parameters,
		       COALESCE(author, ''), COALESCE(version, ''), builtin,
		       created_at, updated_at
		  FROM stack_templates WHERE id = ?`, id)
	t, err := scanTemplate(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return t, err
}

func (s *Service) Create(ctx context.Context, in Input) (*Template, error) {
	if err := validateInput(in); err != nil {
		return nil, err
	}
	params, err := Parse(in.Compose+"\n"+in.EnvTemplate, in.Parameters)
	if err != nil {
		return nil, err
	}
	paramsJSON, _ := json.Marshal(params)
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO stack_templates
		  (slug, name, description, icon_url, compose, env_tmpl, parameters, author, version, builtin)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 0)`,
		in.Slug, in.Name, nullable(in.Description), nullable(in.IconURL),
		in.Compose, nullable(in.EnvTemplate), string(paramsJSON),
		nullable(in.Author), nullable(in.Version))
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, ErrDuplicateSlug
		}
		return nil, err
	}
	id, _ := res.LastInsertId()
	return s.Get(ctx, id)
}

func (s *Service) Update(ctx context.Context, id int64, in Input) (*Template, error) {
	existing, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if existing.Builtin {
		return nil, ErrBuiltinImmutable
	}
	if err := validateInput(in); err != nil {
		return nil, err
	}
	params, err := Parse(in.Compose+"\n"+in.EnvTemplate, in.Parameters)
	if err != nil {
		return nil, err
	}
	paramsJSON, _ := json.Marshal(params)
	_, err = s.db.ExecContext(ctx, `
		UPDATE stack_templates SET
		  slug = ?, name = ?, description = ?, icon_url = ?,
		  compose = ?, env_tmpl = ?, parameters = ?,
		  author = ?, version = ?,
		  updated_at = CURRENT_TIMESTAMP
		WHERE id = ? AND builtin = 0`,
		in.Slug, in.Name, nullable(in.Description), nullable(in.IconURL),
		in.Compose, nullable(in.EnvTemplate), string(paramsJSON),
		nullable(in.Author), nullable(in.Version), id)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, ErrDuplicateSlug
		}
		return nil, err
	}
	return s.Get(ctx, id)
}

func (s *Service) Delete(ctx context.Context, id int64) error {
	existing, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if existing.Builtin {
		return ErrBuiltinImmutable
	}
	_, err = s.db.ExecContext(ctx, `DELETE FROM stack_templates WHERE id = ? AND builtin = 0`, id)
	return err
}

// -----------------------------------------------------------------------------
// Deploy — render the template, hand off the compose+env to the caller
// -----------------------------------------------------------------------------

// DeployRequest is what the handler forwards after Render succeeds.
type DeployRequest struct {
	StackName string            `json:"stack_name"`
	HostID    string            `json:"host_id,omitempty"`
	Values    map[string]string `json:"values,omitempty"`
}

// Materialize renders the template compose+env with the given values
// (plus auto-generated secrets for missing `secret: true` params) and
// returns the finalised strings plus the full value map the caller
// can stash for audit / later display.
func (s *Service) Materialize(ctx context.Context, id int64, values map[string]string) (compose, env string, resolved map[string]string, err error) {
	t, err := s.Get(ctx, id)
	if err != nil {
		return "", "", nil, err
	}
	resolved = make(map[string]string, len(values)+len(t.Parameters))
	for k, v := range values {
		resolved[k] = v
	}
	// Auto-generate unfilled secrets before rendering.
	for _, p := range t.Parameters {
		if p.Secret {
			if v, ok := resolved[p.Name]; !ok || v == "" {
				gen, err := genSecret(32)
				if err != nil {
					return "", "", nil, err
				}
				resolved[p.Name] = gen
			}
		} else if _, ok := resolved[p.Name]; !ok && p.Default != "" {
			resolved[p.Name] = p.Default
		}
	}
	compose, err = Render(t.Compose, t.Parameters, resolved)
	if err != nil {
		return "", "", nil, err
	}
	if t.EnvTemplate != "" {
		env, err = Render(t.EnvTemplate, t.Parameters, resolved)
		if err != nil {
			return "", "", nil, err
		}
	}
	return compose, env, resolved, nil
}

// -----------------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------------

type rowScanner interface {
	Scan(dest ...any) error
}

func scanTemplate(r rowScanner) (*Template, error) {
	var t Template
	var paramsJSON string
	var builtin int
	if err := r.Scan(&t.ID, &t.Slug, &t.Name, &t.Description, &t.IconURL,
		&t.Compose, &t.EnvTemplate, &paramsJSON,
		&t.Author, &t.Version, &builtin,
		&t.CreatedAt, &t.UpdatedAt); err != nil {
		return nil, err
	}
	t.Builtin = builtin == 1
	if paramsJSON != "" {
		_ = json.Unmarshal([]byte(paramsJSON), &t.Parameters)
	}
	if t.Parameters == nil {
		t.Parameters = []ParamDef{}
	}
	return &t, nil
}

func validateInput(in Input) error {
	if strings.TrimSpace(in.Slug) == "" {
		return errors.New("slug is required")
	}
	if strings.TrimSpace(in.Name) == "" {
		return errors.New("name is required")
	}
	if strings.TrimSpace(in.Compose) == "" {
		return errors.New("compose is required")
	}
	return nil
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func genSecret(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// Export returns the template as YAML — same shape as the on-disk
// built-in files, so operators can version-control user templates.
func (s *Service) Export(ctx context.Context, id int64) ([]byte, error) {
	t, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	rec := builtinRecord{
		Slug:        t.Slug,
		Name:        t.Name,
		Description: t.Description,
		IconURL:     t.IconURL,
		Compose:     t.Compose,
		Env:         t.EnvTemplate,
		Parameters:  t.Parameters,
		Author:      t.Author,
		Version:     t.Version,
	}
	return yaml.Marshal(rec)
}
