package zooid

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"fiatjaf.com/nostr"
)

func (store *BlossomStore) saveReport(event nostr.Event) error {
	encoded, err := json.Marshal(event)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = GetDb().ExecContext(ctx,
		fmt.Sprintf("INSERT INTO %s (event_id, pubkey, created_at, event_json) VALUES ($1, $2, $3, $4) ON CONFLICT (event_id) DO NOTHING", store.Schema.Prefix("blossom_reports")),
		event.ID.Hex(), event.PubKey.Hex(), int64(event.CreatedAt), encoded)
	return err
}
