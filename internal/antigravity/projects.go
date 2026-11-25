package antigravity

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// LoadProjects reads the read-only Antigravity project metadata directory and
// maps project IDs to display names. Unreadable or malformed entries are
// skipped so a corrupt mapping never hides conversations.
func LoadProjects(dir string) map[string]Project {
	projects := make(map[string]Project)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return projects
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".json") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil || len(data) == 0 || len(data) > maxRequestBodyBytes {
			continue
		}
		var raw map[string]any
		if err := json.Unmarshal(data, &raw); err != nil {
			continue
		}
		id := firstAnyString(raw, "id", "projectId", "project_id")
		if id == "" {
			id = strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
		}
		if id == "" {
			continue
		}
		name := firstAnyString(raw, "name", "title")
		if name == "" {
			name = projectResourceName(raw)
		}
		if name == "" || projectNameOutside(name) {
			name = "未归属项目"
		}
		projects[id] = Project{ID: id, Name: name}
	}
	return projects
}

// ProjectName returns the mapped display name, falling back to the project ID
// and finally to the unattributed label.
func ProjectName(projects map[string]Project, projectID string) string {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" || projectNameOutside(projectID) {
		return "未归属项目"
	}
	if project, ok := projects[projectID]; ok && strings.TrimSpace(project.Name) != "" {
		return project.Name
	}
	return projectID
}

func projectResourceName(raw map[string]any) string {
	container, ok := raw["projectResources"].(map[string]any)
	if !ok {
		return ""
	}
	resources, ok := container["resources"].([]any)
	if !ok || len(resources) == 0 {
		return ""
	}
	resource, ok := resources[0].(map[string]any)
	if !ok {
		return ""
	}
	uri := firstAnyString(resource, "folderUri", "folder_uri", "uri")
	uri = strings.TrimRight(strings.TrimSpace(uri), "/")
	if uri == "" {
		return ""
	}
	base := filepath.Base(uri)
	if base == "/" || base == "." {
		return ""
	}
	return base
}

func firstAnyString(raw map[string]any, keys ...string) string {
	for _, key := range keys {
		value, ok := raw[key].(string)
		if ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func projectNameOutside(name string) bool {
	normalized := strings.ToLower(strings.TrimSpace(name))
	return normalized == "" || normalized == "outside-of-project" || normalized == "outside_of_project"
}

func conversationProjectName(conversations []Conversation, projects map[string]Project) {
	for index := range conversations {
		conversations[index].ProjectName = ProjectName(projects, conversations[index].ProjectID)
	}
}

// ProjectGroups returns the first-seen order of project buckets.
func ProjectGroups(conversations []Conversation) []Project {
	seen := make(map[string]bool)
	groups := make([]Project, 0, len(conversations))
	for _, conversation := range conversations {
		id := strings.TrimSpace(conversation.ProjectID)
		if seen[id] {
			continue
		}
		seen[id] = true
		name := strings.TrimSpace(conversation.ProjectName)
		if name == "" {
			name = ProjectName(nil, id)
		}
		groups = append(groups, Project{ID: id, Name: name})
	}
	return groups
}
