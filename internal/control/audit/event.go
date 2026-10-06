package audit

import (
	"time"

	"github.com/hongle/hl-panel/internal/idgen"
)

type Event struct {
	ID           string         `json:"id"`
	ActorType    string         `json:"actor_type"`
	ActorID      string         `json:"actor_id"`
	Action       string         `json:"action"`
	ResourceType string         `json:"resource_type"`
	ResourceID   string         `json:"resource_id"`
	Outcome      string         `json:"outcome"`
	Metadata     map[string]any `json:"metadata"`
	CreatedAt    time.Time      `json:"created_at"`
}

func NewEvent(now time.Time, actorType, actorID, action, resourceType, resourceID, outcome string, metadata map[string]any) (Event, error) {
	id, err := idgen.New("aud")
	if err != nil {
		return Event{}, err
	}
	if metadata == nil {
		metadata = map[string]any{}
	}
	return Event{
		ID:           id,
		ActorType:    actorType,
		ActorID:      actorID,
		Action:       action,
		ResourceType: resourceType,
		ResourceID:   resourceID,
		Outcome:      outcome,
		Metadata:     metadata,
		CreatedAt:    now,
	}, nil
}
