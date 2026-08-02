package server

import (
	"testing"

	"mcpx/internal/skill"
)

func TestExtensionQueryFiltersNamesAndDescriptions(t *testing.T) {
	skills := []skill.Skill{
		{Manifest: skill.Manifest{Name: "ui-ux-pro-max", Description: "UI and UX design guidance"}},
		{Manifest: skill.Manifest{Name: "pdf", Description: "Read and create PDF files"}},
	}
	filtered := filterSkillsByQuery(skills, "ui ux")
	if len(filtered) != 1 || filtered[0].Manifest.Name != "ui-ux-pro-max" {
		t.Fatalf("skill filter = %+v", filtered)
	}
	servers := []map[string]any{{"name": "browser"}, {"name": "database"}}
	filteredServers := filterExtensionItemsByQuery(servers, "data")
	if len(filteredServers) != 1 || filteredServers[0]["name"] != "database" {
		t.Fatalf("server filter = %+v", filteredServers)
	}
	if !extensionQueryMatches("ui-ux-pro-max", "ui-ux-pro-max") || extensionQueryMatches("ui ux", "ui-only") {
		t.Fatal("extension query matching is not deterministic")
	}
}
