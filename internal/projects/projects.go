package projects

import (
	"fmt"
	"os"
	"sort"

	"gopkg.in/yaml.v3"
)

// Project holds data for a single portfolio project.
type Project struct {
	Title       string   `yaml:"title"`
	Description string   `yaml:"description"`
	Stack       []string `yaml:"stack"`
	Href        string   `yaml:"href"`
	Date        string   `yaml:"date"`
	Order       int      `yaml:"order"`
}

// LoadProjectsFile reads a YAML list of projects from path and returns them
// sorted ascending by Order, then by Title for ties.
func LoadProjectsFile(path string) ([]Project, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading projects file %s: %w", path, err)
	}

	var list []Project
	if err := yaml.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("parsing projects file %s: %w", path, err)
	}

	for i, p := range list {
		if p.Title == "" {
			return nil, fmt.Errorf("projects[%d]: missing required field 'title'", i)
		}
	}

	sort.Slice(list, func(i, j int) bool {
		if list[i].Order != list[j].Order {
			return list[i].Order < list[j].Order
		}
		return list[i].Title < list[j].Title
	})

	return list, nil
}

// ToSiteDataList converts a slice of Project to the []any map format expected
// by Go templates via the dig/range template functions.
func ToSiteDataList(list []Project) []any {
	out := make([]any, len(list))
	for i, p := range list {
		stack := make([]any, len(p.Stack))
		for j, s := range p.Stack {
			stack[j] = s
		}
		out[i] = map[string]any{
			"title":       p.Title,
			"description": p.Description,
			"stack":       stack,
			"href":        p.Href,
			"date":        p.Date,
			"order":       p.Order,
		}
	}
	return out
}
