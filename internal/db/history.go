package db

import (
	"encoding/json"
	"reflect"
	"time"
)

type HistoryEntry struct {
	Action    string         `json:"action"`
	Changes   map[string]any `json:"changes,omitempty"`
	ActorID   any            `json:"actor_id,omitempty"`
	ActorName string         `json:"actor_name,omitempty"`
	At        time.Time      `json:"at"`
}

func historyFieldIndex(self any) (int, bool) {
	val := reflect.ValueOf(self)
	if val.Kind() == reflect.Ptr {
		if val.IsNil() {
			return -1, false
		}
		val = val.Elem()
	}
	if val.Kind() != reflect.Struct {
		return -1, false
	}
	field, ok := val.Type().FieldByName("History")
	if !ok || field.Type.Kind() != reflect.String {
		return -1, false
	}
	return field.Index[0], true
}

func getHistoryString(self any, idx int) string {
	val := reflect.ValueOf(self)
	if val.Kind() == reflect.Ptr {
		val = val.Elem()
	}
	return val.Field(idx).String()
}

func setHistoryString(self any, idx int, s string) {
	val := reflect.ValueOf(self)
	if val.Kind() == reflect.Ptr {
		val = val.Elem()
	}
	f := val.Field(idx)
	if f.CanSet() {
		f.SetString(s)
	}
}

func appendHistoryJSON(existingJSON string, entry HistoryEntry) string {
	var entries []HistoryEntry
	if existingJSON != "" {
		_ = json.Unmarshal([]byte(existingJSON), &entries)
	}
	entries = append(entries, entry)

	b, err := json.Marshal(entries)
	if err != nil {
		return existingJSON
	}
	return string(b)
}

func buildHistoryEntry(action string, changes map[string]any, actorID any, actorName string) HistoryEntry {
	return HistoryEntry{
		Action:    action,
		Changes:   changes,
		ActorID:   actorID,
		ActorName: actorName,
		At:        time.Now(),
	}
}

func diffForHistory(oldSelf, newSelf any, cols []string) (map[string]any, error) {
	newMap, err := StructToHistoryMap(newSelf, cols)
	if err != nil {
		return nil, err
	}
	oldMap, err := StructToHistoryMap(oldSelf, cols)
	if err != nil {
		return nil, err
	}

	changes := make(map[string]any)
	for k, newVal := range newMap {
		oldVal, existed := oldMap[k]
		if !existed || !reflect.DeepEqual(oldVal, newVal) {
			changes[k] = newVal
		}
	}
	return changes, nil
}

func setActorField(self any, fieldName, value string) {
	if value == "" {
		return
	}
	val := reflect.ValueOf(self)
	if val.Kind() == reflect.Ptr {
		val = val.Elem()
	}
	f := val.FieldByName(fieldName)
	if f.IsValid() && f.CanSet() && f.Kind() == reflect.String {
		f.SetString(value)
	}
}

func hasField(self any, name string) bool {
	val := reflect.ValueOf(self)
	if val.Kind() == reflect.Ptr {
		val = val.Elem()
	}
	return val.FieldByName(name).IsValid()
}
