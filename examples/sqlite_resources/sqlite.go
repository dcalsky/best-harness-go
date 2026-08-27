package sqliteresources

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schema string

func Open(path string) (*sql.DB, error) {
	if path == "" {
		return nil, errors.New("sqlite path is required")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

func Initialize(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return errors.New("sqlite database is required")
	}
	_, err := db.ExecContext(ctx, schema)
	return err
}

type SQLiteStore struct {
	DB *sql.DB
}

func (s SQLiteStore) Load(ctx context.Context, projectKey string) ([]Rule, []Skill, error) {
	if s.DB == nil {
		return nil, nil, errors.New("sqlite database is required")
	}
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()

	rules, err := loadRules(ctx, tx, projectKey)
	if err != nil {
		return nil, nil, err
	}
	skills, err := loadSkills(ctx, tx, projectKey)
	if err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	return rules, skills, nil
}

func loadRules(ctx context.Context, tx *sql.Tx, projectKey string) ([]Rule, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT id, project_key, name, content, priority
FROM agent_rules
WHERE enabled = 1
  AND (project_key IS NULL OR project_key = NULLIF(?, ''))
ORDER BY
  CASE WHEN project_key IS NULL THEN 0 ELSE 1 END,
  priority,
  id`, projectKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []Rule
	for rows.Next() {
		var row Rule
		var key sql.NullString
		if err := rows.Scan(&row.ID, &key, &row.Name, &row.Content, &row.Priority); err != nil {
			return nil, err
		}
		if key.Valid {
			value := key.String
			row.ProjectKey = &value
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func loadSkills(ctx context.Context, tx *sql.Tx, projectKey string) ([]Skill, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT id, project_key, name, description, content, priority
FROM agent_skills
WHERE enabled = 1
  AND (project_key IS NULL OR project_key = NULLIF(?, ''))
ORDER BY
  CASE WHEN project_key IS NULL THEN 0 ELSE 1 END,
  priority,
  id`, projectKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []Skill
	for rows.Next() {
		var row Skill
		var key sql.NullString
		if err := rows.Scan(&row.ID, &key, &row.Name, &row.Description, &row.Content, &row.Priority); err != nil {
			return nil, err
		}
		if key.Valid {
			value := key.String
			row.ProjectKey = &value
		}
		result = append(result, row)
	}
	return result, rows.Err()
}
