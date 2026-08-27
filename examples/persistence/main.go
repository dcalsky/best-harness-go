package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/dcalsky/best-harness-go"
)

func main() {
	ctx := context.Background()
	directory := os.Getenv("SESSION_DIRECTORY")
	if directory == "" {
		directory = filepath.Join(os.TempDir(), "example-sessions")
	}
	if opened, err := harness.ResumeLatestFileSession(ctx, directory, ""); err == nil {
		defer opened.Close()
		messages := opened.Context().Messages
		fmt.Println(len(messages), messages[0].Text())
		return
	} else if err != io.EOF {
		panic(err)
	}
	persistence, _ := harness.NewFilePersistence(directory)
	m, _ := harness.NewSessionManager(persistence, harness.PersistenceOptions{})
	_, _ = m.AppendMessage(harness.User("hello"))
	_, _ = m.AppendMessage(harness.Message{Role: harness.RoleAssistant, Content: []harness.Content{harness.Text("hi")}})
	path := m.Location()
	_ = m.Close()
	opened, _ := harness.OpenFileSession(path)
	defer opened.Close()
	messages := opened.Context().Messages
	fmt.Println(len(messages), messages[0].Text())
}
