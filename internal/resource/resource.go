// Package resource loads caller-approved instructions, prompts, and skills.
package resource

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
)

type LoadRequest struct{ Cwd string }
type Source struct{ Name, Path, Content string }
type Skill struct{ Name, Description, Location, Content string }
type PromptTemplate struct{ Name, Description, ArgumentHint, Template string }
type Diagnostic struct{ Level, Message, Source string }
type Snapshot struct {
	ProjectInstructions []Source
	SystemPrompt        string
	AppendSystemPrompt  []Source
	Skills              map[string]Skill
	PromptTemplates     map[string]PromptTemplate
	Diagnostics         []Diagnostic
	Sources             []Source
}
type Loader interface {
	Load(context.Context, LoadRequest) (Snapshot, error)
}
type ProgramLoader struct{ Snapshot Snapshot }

func (p ProgramLoader) Load(context.Context, LoadRequest) (Snapshot, error) {
	return clone(p.Snapshot), nil
}

type Registry struct {
	mu      sync.RWMutex
	loaders []Loader
}

func NewRegistry() *Registry { return &Registry{} }
func (r *Registry) Register(loader Loader) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.loaders = append(r.loaders, loader)
}
func (r *Registry) Load(ctx context.Context, req LoadRequest) (Snapshot, error) {
	r.mu.RLock()
	loaders := append([]Loader(nil), r.loaders...)
	r.mu.RUnlock()
	out := Snapshot{Skills: map[string]Skill{}, PromptTemplates: map[string]PromptTemplate{}}
	for _, loader := range loaders {
		s, err := loader.Load(ctx, req)
		if err != nil {
			return Snapshot{}, err
		}
		out.ProjectInstructions = append(out.ProjectInstructions, s.ProjectInstructions...)
		if s.SystemPrompt != "" {
			if out.SystemPrompt != "" {
				out.Diagnostics = append(out.Diagnostics, Diagnostic{Level: "warning", Message: "system prompt replaced by a later loader"})
			}
			out.SystemPrompt = s.SystemPrompt
		}
		out.AppendSystemPrompt = append(out.AppendSystemPrompt, s.AppendSystemPrompt...)
		for n, v := range s.Skills {
			if _, ok := out.Skills[n]; ok {
				out.Diagnostics = append(out.Diagnostics, Diagnostic{Level: "warning", Message: fmt.Sprintf("skill %q replaced by a later loader", n), Source: v.Location})
			}
			out.Skills[n] = v
		}
		for n, v := range s.PromptTemplates {
			if _, ok := out.PromptTemplates[n]; ok {
				out.Diagnostics = append(out.Diagnostics, Diagnostic{Level: "warning", Message: fmt.Sprintf("prompt %q replaced by a later loader", n)})
			}
			out.PromptTemplates[n] = v
		}
		out.Diagnostics = append(out.Diagnostics, s.Diagnostics...)
		out.Sources = append(out.Sources, s.Sources...)
	}
	return out, nil
}
func clone(s Snapshot) Snapshot {
	o := s
	o.ProjectInstructions = append([]Source(nil), s.ProjectInstructions...)
	o.AppendSystemPrompt = append([]Source(nil), s.AppendSystemPrompt...)
	o.Diagnostics = append([]Diagnostic(nil), s.Diagnostics...)
	o.Sources = append([]Source(nil), s.Sources...)
	o.Skills = map[string]Skill{}
	for k, v := range s.Skills {
		o.Skills[k] = v
	}
	o.PromptTemplates = map[string]PromptTemplate{}
	for k, v := range s.PromptTemplates {
		o.PromptTemplates[k] = v
	}
	return o
}

type PromptOptions struct {
	Cwd      string
	Tools    []string
	Snapshot Snapshot
}

func BuildSystemPrompt(o PromptOptions) string {
	var b strings.Builder
	if o.Snapshot.SystemPrompt != "" {
		b.WriteString(strings.TrimSpace(o.Snapshot.SystemPrompt))
	} else {
		b.WriteString("You are an expert coding assistant operating inside pi, a coding agent harness. You help users by reading files, executing commands, editing code, and writing new files.\n\nGuidelines:\n- Be concise in your responses\n- Show file paths clearly when working with files")
	}
	for _, s := range o.Snapshot.AppendSystemPrompt {
		b.WriteString("\n\n" + strings.TrimSpace(s.Content))
	}
	if len(o.Snapshot.ProjectInstructions) > 0 {
		b.WriteString("\n\n<project_context>\n\nProject-specific instructions and guidelines:\n\n")
		for _, s := range o.Snapshot.ProjectInstructions {
			fmt.Fprintf(&b, "<project_instructions path=\"%s\">\n%s\n</project_instructions>\n\n", escapeXML(s.Path), s.Content)
		}
		b.WriteString("</project_context>")
	}
	hasRead := false
	for _, name := range o.Tools {
		if name == "read" {
			hasRead = true
			break
		}
	}
	if hasRead && len(o.Snapshot.Skills) > 0 {
		names := make([]string, 0, len(o.Snapshot.Skills))
		for name := range o.Snapshot.Skills {
			names = append(names, name)
		}
		sort.Strings(names)
		b.WriteString("\n\nThe following skills provide specialized instructions for specific tasks.\nUse the read tool to load a skill's file when the task matches its description.\n\n<available_skills>")
		for _, name := range names {
			skill := o.Snapshot.Skills[name]
			fmt.Fprintf(&b, "\n  <skill>\n    <name>%s</name>\n    <description>%s</description>\n    <location>%s</location>\n  </skill>", escapeXML(skill.Name), escapeXML(skill.Description), escapeXML(skill.Location))
		}
		b.WriteString("\n</available_skills>")
	}
	if o.Cwd != "" {
		fmt.Fprintf(&b, "\nCurrent working directory: %s", strings.ReplaceAll(o.Cwd, "\\", "/"))
	}
	return strings.TrimSpace(b.String())
}

func escapeXML(value string) string {
	replacer := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;")
	return replacer.Replace(value)
}
func Expand(template PromptTemplate, values map[string]string) (string, error) {
	out := template.Template
	for k, v := range values {
		out = strings.ReplaceAll(out, "{{"+k+"}}", v)
	}
	if i := strings.Index(out, "{{"); i >= 0 {
		return "", fmt.Errorf("template %q has an unresolved value", template.Name)
	}
	return out, nil
}

func ParseCommandArgs(input string) []string {
	var args []string
	var current strings.Builder
	var quote rune
	for _, char := range input {
		if quote != 0 {
			if char == quote {
				quote = 0
			} else {
				current.WriteRune(char)
			}
			continue
		}
		if char == '\'' || char == '"' {
			quote = char
		} else if char == ' ' || char == '\t' || char == '\n' || char == '\r' {
			if current.Len() > 0 {
				args = append(args, current.String())
				current.Reset()
			}
		} else {
			current.WriteRune(char)
		}
	}
	if current.Len() > 0 {
		args = append(args, current.String())
	}
	return args
}

var argumentPattern = regexp.MustCompile(`\$\{(\d+|ARGUMENTS|@):-([^}]*)\}|\$\{@:(\d+)(:(\d+))?\}|\$(ARGUMENTS|@|\d+)`)

func ExpandArgs(template PromptTemplate, args []string) string {
	all := strings.Join(args, " ")
	return argumentPattern.ReplaceAllStringFunc(template.Template, func(match string) string {
		groups := argumentPattern.FindStringSubmatch(match)
		if groups[1] != "" {
			value := argumentValue(groups[1], args, all)
			if value == "" {
				return groups[2]
			}
			return value
		}
		if groups[3] != "" {
			start, _ := strconv.Atoi(groups[3])
			if start < 1 {
				start = 1
			}
			start--
			end := len(args)
			if groups[5] != "" {
				length, _ := strconv.Atoi(groups[5])
				end = min(len(args), start+length)
			}
			if start >= len(args) {
				return ""
			}
			return strings.Join(args[start:end], " ")
		}
		return argumentValue(groups[6], args, all)
	})
}

func argumentValue(target string, args []string, all string) string {
	if target == "@" || target == "ARGUMENTS" {
		return all
	}
	index, _ := strconv.Atoi(target)
	if index < 1 || index > len(args) {
		return ""
	}
	return args[index-1]
}
