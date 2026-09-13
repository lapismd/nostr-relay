package zooid

import (
	"encoding/json"
	"fiatjaf.com/nostr"
)

const channelCanvasKind nostr.Kind = 40100

func (g *GroupStore) checkCanvasWrite(event nostr.Event, metadata nostr.Event) string {
	count := 0
	for _, tag := range event.Tags {
		if len(tag) > 0 && tag[0] == "h" {
			count++
			if len(tag) != 2 || tag[1] == "" {
				return "invalid: canvas requires one channel tag"
			}
		}
	}
	if count != 1 {
		return "invalid: canvas requires one channel tag"
	}
	if len(event.Content) > 65536 {
		return "invalid: canvas exceeds 64 KiB"
	}
	var content struct {
		Archived bool `json:"archived"`
	}
	if metadata.Content != "" && json.Unmarshal([]byte(metadata.Content), &content) != nil {
		return "invalid: channel metadata"
	}
	if content.Archived || HasTag(metadata.Tags, "archived") {
		return "restricted: archived channel canvas is read-only"
	}
	group := GetGroupIDFromEvent(event)
	if !g.IsMember(group, event.PubKey) {
		return "restricted: canvas editing requires channel membership"
	}
	if g.IsPrivateGroup(group) && !g.Config.Groups.PrivateRelayAdminAccess {
		if !g.IsGroupCreator(group, event.PubKey) {
			return "restricted: canvas editing requires channel management permission"
		}
	} else if !g.Config.CanManage(event.PubKey) && !g.IsGroupCreator(group, event.PubKey) {
		return "restricted: canvas editing requires channel management permission"
	}
	return ""
}
