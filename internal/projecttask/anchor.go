package projecttask

import (
	"fmt"

	"fiatjaf.com/nostr"
)

// EventTemplate is unsigned by design. Signing stays in the calling client.
type EventTemplate struct {
	Kind    nostr.Kind
	Content string
	Tags    nostr.Tags
}

// BuildProjectTaskAnchor builds the optional repository-shaped task namespace
// used when a project-level issue has no natural external Git repository.
// It never emits clone or web tags and never performs filesystem or Git I/O.
func BuildProjectTaskAnchor(project nostr.Event) (EventTemplate, RepositoryCoordinate, error) {
	var empty EventTemplate
	if project.Kind != KindProject {
		return empty, RepositoryCoordinate{}, fmt.Errorf("task anchor requires a kind 30621 project")
	}
	if err := validateProject(project); err != nil {
		return empty, RepositoryCoordinate{}, err
	}

	id := project.Tags.GetD()
	coordinate := RepositoryCoordinate{Owner: project.PubKey, ID: id}
	tags := nostr.Tags{{"d", id}}
	content := ""
	for _, tag := range project.Tags {
		if len(tag) < 2 {
			continue
		}
		switch tag[0] {
		case "name":
			tags = append(tags, nostr.Tag{"name", tag[1] + " Tasks"})
		case "description":
			content = tag[1]
			tags = append(tags, nostr.Tag{"description", "Project-level task container"})
		case TagBuzzChannel:
			tags = append(tags, tag.Clone())
		}
	}
	if tags.Find("name") == nil {
		tags = append(tags, nostr.Tag{"name", id + " Tasks"})
	}
	if tags.Find("description") == nil {
		tags = append(tags, nostr.Tag{"description", "Project-level task container"})
	}

	return EventTemplate{
		Kind:    nostr.KindRepositoryAnnouncement,
		Content: content,
		Tags:    tags,
	}, coordinate, nil
}

// AddRepository adds a repository coordinate to a project template without
// discarding unknown extension tags. Duplicate coordinates are rejected.
func AddRepository(project nostr.Event, coordinate RepositoryCoordinate, relayHint string) (EventTemplate, error) {
	if project.Kind != KindProject {
		return EventTemplate{}, fmt.Errorf("repository membership requires a kind 30621 project")
	}
	if err := validateProject(project); err != nil {
		return EventTemplate{}, err
	}
	tags := project.Tags.CloneDeep()
	member := nostr.Tag{"a", coordinate.String()}
	if relayHint != "" {
		member = append(member, relayHint)
	}
	tags = append(tags, member)
	template := EventTemplate{Kind: KindProject, Content: project.Content, Tags: tags}
	candidate := project
	candidate.Tags = tags
	if err := validateProject(candidate); err != nil {
		return EventTemplate{}, err
	}
	return template, nil
}
