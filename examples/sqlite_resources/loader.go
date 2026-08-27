package sqliteresources

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/dcalsky/best-harness-go"
)

type Rule struct {
	ID         int64
	ProjectKey *string
	Name       string
	Content    string
	Priority   int
}

type Skill struct {
	ID          int64
	ProjectKey  *string
	Name        string
	Description string
	Content     string
	Priority    int
}

type Store interface {
	Load(context.Context, string) ([]Rule, []Skill, error)
}

type Loader struct {
	Store      Store
	ProjectKey string
	CacheDir   string
}

func (l Loader) Load(ctx context.Context, _ harness.ResourceLoadRequest) (harness.ResourceSnapshot, error) {
	if l.Store == nil {
		return harness.ResourceSnapshot{}, errors.New("sqlite resource store is required")
	}
	if l.CacheDir == "" {
		return harness.ResourceSnapshot{}, errors.New("skill cache directory is required")
	}

	rules, skills, err := l.Store.Load(ctx, l.ProjectKey)
	if err != nil {
		return harness.ResourceSnapshot{}, fmt.Errorf("load sqlite resources: %w", err)
	}
	sortRules(rules)
	sortSkills(skills)

	out := harness.ResourceSnapshot{
		Skills:          make(map[string]harness.ResourceSkill),
		PromptTemplates: make(map[string]harness.PromptTemplate),
	}
	for _, rule := range rules {
		if err := ctx.Err(); err != nil {
			return harness.ResourceSnapshot{}, err
		}
		path := fmt.Sprintf("sqlite://agent_rules/%d", rule.ID)
		source := harness.ResourceSource{Name: rule.Name, Path: path, Content: rule.Content}
		out.ProjectInstructions = append(out.ProjectInstructions, source)
		out.Sources = append(out.Sources, source)
	}

	for _, skill := range skills {
		if err := ctx.Err(); err != nil {
			return harness.ResourceSnapshot{}, err
		}
		digest := sha256.Sum256([]byte(skill.Content))
		directory := fmt.Sprintf("%d-%x", skill.ID, digest[:8])
		location := filepath.Join(l.CacheDir, directory, "SKILL.md")
		if err := writeSkill(location, skill.Content); err != nil {
			return harness.ResourceSnapshot{}, fmt.Errorf("cache skill %q: %w", skill.Name, err)
		}
		if previous, exists := out.Skills[skill.Name]; exists {
			out.Diagnostics = append(out.Diagnostics, harness.ResourceDiagnostic{
				Level:   "warning",
				Message: fmt.Sprintf("skill %q replaced by a project-specific row", skill.Name),
				Source:  previous.Location,
			})
		}
		loaded := harness.ResourceSkill{
			Name:        skill.Name,
			Description: skill.Description,
			Location:    location,
			Content:     skill.Content,
		}
		out.Skills[skill.Name] = loaded
		out.Sources = append(out.Sources, harness.ResourceSource{
			Name:    skill.Name,
			Path:    location,
			Content: skill.Content,
		})
	}
	return out, nil
}

func sortRules(rows []Rule) {
	sort.SliceStable(rows, func(i, j int) bool {
		if (rows[i].ProjectKey == nil) != (rows[j].ProjectKey == nil) {
			return rows[i].ProjectKey == nil
		}
		if rows[i].Priority != rows[j].Priority {
			return rows[i].Priority < rows[j].Priority
		}
		return rows[i].ID < rows[j].ID
	})
}

func sortSkills(rows []Skill) {
	sort.SliceStable(rows, func(i, j int) bool {
		if (rows[i].ProjectKey == nil) != (rows[j].ProjectKey == nil) {
			return rows[i].ProjectKey == nil
		}
		if rows[i].Priority != rows[j].Priority {
			return rows[i].Priority < rows[j].Priority
		}
		return rows[i].ID < rows[j].ID
	})
}

func writeSkill(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".skill-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(tmpName, path)
}
