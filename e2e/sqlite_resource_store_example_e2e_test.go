package e2e_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	. "github.com/dcalsky/best-harness-go/examples/sqlite_resources"
)

func testDatabase(t *testing.T) *sql.DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "resources.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := Initialize(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestInitializeIsIdempotent(t *testing.T) {
	db := testDatabase(t)
	if err := Initialize(context.Background(), db); err != nil {
		t.Fatal(err)
	}
}

func TestSQLiteStoreFiltersOrdersAndOverrides(t *testing.T) {
	db := testDatabase(t)
	statements := []string{
		`INSERT INTO agent_rules(project_key,name,content,priority) VALUES(NULL,'global-late','global late',20)`,
		`INSERT INTO agent_rules(project_key,name,content,priority) VALUES(NULL,'global-first','global first',10)`,
		`INSERT INTO agent_rules(project_key,name,content,priority) VALUES('billing','project','billing rule',1)`,
		`INSERT INTO agent_rules(project_key,name,content,priority) VALUES('other','other','other rule',1)`,
		`INSERT INTO agent_rules(project_key,name,content,priority,enabled) VALUES(NULL,'disabled','hidden',1,0)`,
		`INSERT INTO agent_skills(project_key,name,description,content,priority) VALUES(NULL,'review','global','global skill',1)`,
		`INSERT INTO agent_skills(project_key,name,description,content,priority) VALUES('billing','review','project','project skill',1)`,
		`INSERT INTO agent_skills(project_key,name,description,content,priority) VALUES('other','deploy','other','hidden skill',1)`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}

	rules, skills, err := (SQLiteStore{DB: db}).Load(context.Background(), "billing")
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 3 || rules[0].Name != "global-first" || rules[1].Name != "global-late" || rules[2].Name != "project" {
		t.Fatalf("rules=%#v", rules)
	}
	if len(skills) != 2 || skills[0].Description != "global" || skills[1].Description != "project" {
		t.Fatalf("skills=%#v", skills)
	}

	globalRules, globalSkills, err := (SQLiteStore{DB: db}).Load(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(globalRules) != 2 || len(globalSkills) != 1 {
		t.Fatalf("global rules=%#v skills=%#v", globalRules, globalSkills)
	}
}

func TestSQLiteStoreErrorsAndCancellation(t *testing.T) {
	if _, _, err := (SQLiteStore{}).Load(context.Background(), "x"); err == nil {
		t.Fatal("nil database did not fail")
	}
	if _, err := Open(""); err == nil {
		t.Fatal("empty path did not fail")
	}
	if err := Initialize(context.Background(), nil); err == nil {
		t.Fatal("nil database initialization did not fail")
	}
	db := testDatabase(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := (SQLiteStore{DB: db}).Load(ctx, "x")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
}

func TestSQLiteStoreConcurrentReads(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO agent_rules(name,content) VALUES('global','rule')`); err != nil {
		t.Fatal(err)
	}
	store := SQLiteStore{DB: db}
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rules, _, err := store.Load(context.Background(), "project")
			if err == nil && len(rules) != 1 {
				err = errors.New("unexpected rule count")
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}
