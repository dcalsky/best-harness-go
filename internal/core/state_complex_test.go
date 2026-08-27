package core_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/dcalsky/best-harness-go"
)

type complexUnitTask struct {
	ID       string            `json:"id"`
	Labels   []string          `json:"labels"`
	Metadata map[string]string `json:"metadata"`
}

type complexUnitWorkspace struct {
	Tasks  map[string]*complexUnitTask `json:"tasks"`
	Scores [][]int                     `json:"scores"`
}

type complexUnitCursor struct {
	Workspace string `json:"workspace"`
	Task      string `json:"task"`
}

type complexUnitState struct {
	Owner      string                           `json:"owner"`
	Workspaces map[string]*complexUnitWorkspace `json:"workspaces"`
	Timeline   []map[string][]string            `json:"timeline"`
	Cursor     *complexUnitCursor               `json:"cursor"`
	UpdatedAt  time.Time                        `json:"updatedAt"`
}

func initialComplexUnitState() complexUnitState {
	return complexUnitState{
		Owner: "alice",
		Workspaces: map[string]*complexUnitWorkspace{
			"main": {
				Tasks: map[string]*complexUnitTask{
					"t1": {ID: "t1", Labels: []string{"new"}, Metadata: map[string]string{"source": "user"}},
				},
				Scores: [][]int{{1, 2}, {3, 4}},
			},
		},
		Timeline:  []map[string][]string{{"created": {"t1"}}},
		Cursor:    &complexUnitCursor{Workspace: "main", Task: "t1"},
		UpdatedAt: time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC),
	}
}

func TestComplexStateIsDeepCopiedPersistedAndRestored(t *testing.T) {
	ctx := context.Background()
	h, err := harness.New[complexUnitState](harness.Options{})
	if err != nil {
		t.Fatal(err)
	}
	initial := initialComplexUnitState()
	persistence, err := harness.NewFilePersistence(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	session, err := h.NewSession(ctx, persistence, harness.SessionOptions{}, initial)
	if err != nil {
		t.Fatal(err)
	}

	copy := session.State()
	copy.Workspaces["main"].Tasks["t1"].Labels[0] = "leaked"
	copy.Workspaces["main"].Tasks["t1"].Metadata["source"] = "leaked"
	copy.Workspaces["main"].Scores[0][0] = 99
	copy.Timeline[0]["created"][0] = "leaked"
	copy.Cursor.Task = "leaked"
	if got := session.State(); !reflect.DeepEqual(got, initial) {
		t.Fatalf("State returned a shallow copy:\n got=%#v\nwant=%#v", got, initial)
	}

	want := initialComplexUnitState()
	want.Workspaces["main"].Tasks["t1"].Labels = append(want.Workspaces["main"].Tasks["t1"].Labels, "ready")
	want.Workspaces["main"].Tasks["t2"] = &complexUnitTask{ID: "t2", Labels: []string{"queued"}, Metadata: map[string]string{"source": "tool"}}
	want.Workspaces["main"].Scores[1] = append(want.Workspaces["main"].Scores[1], 5)
	want.Timeline = append(want.Timeline, map[string][]string{"updated": {"t1", "t2"}})
	want.Cursor = &complexUnitCursor{Workspace: "main", Task: "t2"}
	want.UpdatedAt = time.Date(2026, 8, 24, 11, 0, 0, 0, time.UTC)

	if err := session.Context().UpdateState(func(state *complexUnitState) {
		state.Workspaces["main"].Tasks["t1"].Labels = append(state.Workspaces["main"].Tasks["t1"].Labels, "ready")
		state.Workspaces["main"].Tasks["t2"] = &complexUnitTask{ID: "t2", Labels: []string{"queued"}, Metadata: map[string]string{"source": "tool"}}
		state.Workspaces["main"].Scores[1] = append(state.Workspaces["main"].Scores[1], 5)
		state.Timeline = append(state.Timeline, map[string][]string{"updated": {"t1", "t2"}})
		state.Cursor = &complexUnitCursor{Workspace: "main", Task: "t2"}
		state.UpdatedAt = want.UpdatedAt
	}); err != nil {
		t.Fatal(err)
	}
	if got := session.State(); !reflect.DeepEqual(got, want) {
		t.Fatalf("updated state:\n got=%#v\nwant=%#v", got, want)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}

	path := session.Location()
	restored, err := h.OpenSession(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if got := restored.State(); !reflect.DeepEqual(got, want) {
		t.Fatalf("restored state:\n got=%#v\nwant=%#v", got, want)
	}
}
