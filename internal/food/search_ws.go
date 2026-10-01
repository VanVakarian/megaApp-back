package food

import (
	"context"

	clockplatform "megaapp-back/internal/platform/clock"
	"megaapp-back/internal/ws"
)

func NewSearchWSHandler(service *Service, clk clockplatform.Clock) ws.MessageHandler {
	return func(client *ws.Client, message map[string]any) error {
		query, _ := message["query"].(string)
		archived, _ := message["archived"].(bool)
		sequenceNumber := int64(0)
		switch typed := message["sequenceNumber"].(type) {
		case float64:
			sequenceNumber = int64(typed)
		case int64:
			sequenceNumber = typed
		case int:
			sequenceNumber = int64(typed)
		}

		ids, appliedArchived, err := service.SearchCatalogueRealtime(context.Background(), client.UserID(), query, archived)
		if err != nil {
			return err
		}

		return client.SendJSON(map[string]any{
			"type": "SEARCH_RESULTS",
			"payload": map[string]any{
				"query":          query,
				"archived":       appliedArchived,
				"catalogueIds":   ids,
				"timestamp":      clk.Now().UnixMilli(),
				"sequenceNumber": sequenceNumber,
			},
		})
	}
}
