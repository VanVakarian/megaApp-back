package food

import (
	clockplatform "megaapp-back/internal/platform/clock"
	"megaapp-back/internal/ws"
)

type RealtimePublisher interface {
	MarkUserUpdated(userID int64)
	PublishDiaryEntryCreated(userID int64, entry DiaryEntry, excludeClientID string)
	PublishDiaryEntryUpdated(userID int64, entry DiaryEntry, historyEntry HistoryEntry, excludeClientID string)
	PublishDiaryEntryDeleted(userID int64, diaryID int64, excludeClientID string)
	PublishDiaryDayDeleted(userID int64, dateISO string, excludeClientID string)
	PublishBodyWeightUpdated(userID int64, dateISO string, bodyWeight float64, excludeClientID string)
	PublishCatalogueEntrySaved(userID int64, entry CatalogueEntry, excludeClientID string)
	PublishCatalogueEntryDeleted(catalogueID int64, excludeClientID string)
	PublishCatalogueImageGenerated(catalogueID int64, imageVersion int64, excludeClientID string)
	PublishCatalogueImageFailed(catalogueID int64)
}

type WSRealtimePublisher struct {
	hub   *ws.Hub
	clock clockplatform.Clock
}

func NewWSRealtimePublisher(hub *ws.Hub, clk clockplatform.Clock) *WSRealtimePublisher {
	return &WSRealtimePublisher{hub: hub, clock: clk}
}

// wsEnvelope is the single shape every outgoing food realtime message is wrapped in — payload is
// always one of the named *Payload structs below, never an inline map literal, so a typo'd or
// mistyped field is a compile error instead of a silent client-side no-op.
type wsEnvelope struct {
	Type    string `json:"type"`
	Payload any    `json:"payload"`
}

type diaryEntryUpdatedPayload struct {
	ID              int64        `json:"id"`
	NewFoodWeight   int64        `json:"newFoodWeight"`
	NewKcals        int64        `json:"newKcals"`
	NewHistoryEntry HistoryEntry `json:"newHistoryEntry"`
	Version         int64        `json:"version"`
}

type diaryEntryDeletedPayload struct {
	DeletedDiaryEntryID int64 `json:"deletedDiaryEntryId"`
}

type diaryDayDeletedPayload struct {
	DateISO string `json:"dateISO"`
}

type bodyWeightUpdatedPayload struct {
	DateISO       string  `json:"dateISO"`
	NewBodyWeight float64 `json:"newBodyWeight"`
}

type catalogueEntryDeletedPayload struct {
	CatalogueID int64 `json:"catalogueId"`
}

type catalogueImageGeneratedPayload struct {
	CatalogueID  int64 `json:"catalogueId"`
	ImageVersion int64 `json:"imageVersion"`
}

type catalogueImageFailedPayload struct {
	CatalogueID int64 `json:"catalogueId"`
}

func (p *WSRealtimePublisher) MarkUserUpdated(userID int64) {
	p.hub.SetSyncState(userID, p.clock.Now().UnixMilli())
}

func (p *WSRealtimePublisher) PublishDiaryEntryCreated(userID int64, entry DiaryEntry, excludeClientID string) {
	p.hub.BroadcastToUser(userID, wsEnvelope{Type: "DIARY_ENTRY_CREATED", Payload: entry}, excludeClientID)
}

func (p *WSRealtimePublisher) PublishDiaryEntryUpdated(userID int64, entry DiaryEntry, historyEntry HistoryEntry, excludeClientID string) {
	p.hub.BroadcastToUser(userID, wsEnvelope{Type: "DIARY_ENTRY_UPDATED", Payload: diaryEntryUpdatedPayload{
		ID:              entry.ID,
		NewFoodWeight:   entry.FoodWeight,
		NewKcals:        entry.Kcals,
		NewHistoryEntry: historyEntry,
		Version:         entry.Version,
	}}, excludeClientID)
}

func (p *WSRealtimePublisher) PublishDiaryEntryDeleted(userID int64, diaryID int64, excludeClientID string) {
	p.hub.BroadcastToUser(userID, wsEnvelope{Type: "DIARY_ENTRY_DELETED", Payload: diaryEntryDeletedPayload{DeletedDiaryEntryID: diaryID}}, excludeClientID)
}

func (p *WSRealtimePublisher) PublishDiaryDayDeleted(userID int64, dateISO string, excludeClientID string) {
	p.hub.BroadcastToUser(userID, wsEnvelope{Type: "DIARY_DAY_DELETED", Payload: diaryDayDeletedPayload{DateISO: dateISO}}, excludeClientID)
}

func (p *WSRealtimePublisher) PublishBodyWeightUpdated(userID int64, dateISO string, bodyWeight float64, excludeClientID string) {
	p.hub.BroadcastToUser(userID, wsEnvelope{Type: "BODY_WEIGHT_UPDATED", Payload: bodyWeightUpdatedPayload{DateISO: dateISO, NewBodyWeight: bodyWeight}}, excludeClientID)
}

func (p *WSRealtimePublisher) PublishCatalogueEntrySaved(userID int64, entry CatalogueEntry, excludeClientID string) {
	p.hub.BroadcastToAll(wsEnvelope{Type: "CATALOGUE_ENTRY_SAVED", Payload: entry}, excludeClientID)
}

func (p *WSRealtimePublisher) PublishCatalogueEntryDeleted(catalogueID int64, excludeClientID string) {
	p.hub.BroadcastToAll(wsEnvelope{Type: "CATALOGUE_ENTRY_DELETED", Payload: catalogueEntryDeletedPayload{CatalogueID: catalogueID}}, excludeClientID)
}

func (p *WSRealtimePublisher) PublishCatalogueImageGenerated(catalogueID int64, imageVersion int64, excludeClientID string) {
	p.hub.BroadcastToAll(wsEnvelope{Type: "CATALOGUE_IMAGE_GENERATED", Payload: catalogueImageGeneratedPayload{CatalogueID: catalogueID, ImageVersion: imageVersion}}, excludeClientID)
}

func (p *WSRealtimePublisher) PublishCatalogueImageFailed(catalogueID int64) {
	p.hub.BroadcastToAll(wsEnvelope{Type: "CATALOGUE_IMAGE_FAILED", Payload: catalogueImageFailedPayload{CatalogueID: catalogueID}}, "")
}
