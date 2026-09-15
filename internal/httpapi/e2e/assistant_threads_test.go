package e2e

import (
	"context"
	"net/http"
	"testing"
)

func TestAssistantThreadsPersistRichMessages(t *testing.T) {
	reset(t)
	story := newStory(t, alice, "The Remembering House")
	storyID := story["id"].(string)
	base := "/v1/stories/" + storyID + "/assistant-threads"

	thread := call(t, http.MethodPost, base, alice, map[string]any{}).
		expect(http.StatusCreated).json()
	threadID := thread["id"].(string)
	if thread["title"] != "New conversation" {
		t.Fatalf("default title = %v", thread["title"])
	}
	if thread["messageCount"].(float64) != 0 {
		t.Fatalf("new thread message count = %v", thread["messageCount"])
	}

	listed := get(t, base, alice).expect(http.StatusOK).json()
	if len(listed["threads"].([]any)) != 1 {
		t.Fatalf("thread list = %v", listed)
	}

	messages := base + "/" + threadID + "/messages"
	userBody := map[string]any{
		"role":           "user",
		"idempotencyKey": "client-message-1",
		"parts": []map[string]any{
			{"type": "text", "text": "Where did Mina leave the key?"},
		},
		"metadata": map[string]any{"client": "assistant-ui"},
	}
	first := call(t, http.MethodPost, messages, alice, userBody).
		expect(http.StatusCreated).json()
	if first["sequence"].(float64) != 1 || first["role"] != "user" {
		t.Fatalf("first message = %v", first)
	}

	// A network retry returns the original row instead of duplicating the turn.
	replay := call(t, http.MethodPost, messages, alice, userBody).
		expect(http.StatusOK).json()
	if replay["id"] != first["id"] || replay["idempotentReplay"] != true {
		t.Fatalf("idempotent replay = %v", replay)
	}
	call(t, http.MethodPost, messages, alice, map[string]any{
		"role": "user", "idempotencyKey": "client-message-1",
		"parts": []map[string]any{{"type": "text", "text": "A different request"}},
	}).expect(http.StatusConflict)

	assistantBody := map[string]any{
		"role":           "assistant",
		"idempotencyKey": "run-1:assistant",
		"runId":          "run-1",
		"status":         "incomplete",
		"parts": []map[string]any{
			{"type": "text", "text": "Checking the manuscript…"},
		},
	}
	draft := call(t, http.MethodPost, messages, alice, assistantBody).
		expect(http.StatusCreated).json()
	if draft["sequence"].(float64) != 2 || draft["status"] != "incomplete" {
		t.Fatalf("assistant draft = %v", draft)
	}

	finished := call(t, http.MethodPatch, messages+"/"+draft["id"].(string), alice, map[string]any{
		"status": "complete",
		"parts": []map[string]any{
			{"type": "tool_call", "toolCallId": "call-1", "name": "search_story", "arguments": map[string]any{"query": "key"}, "result": []any{}},
			{"type": "text", "text": "Mina left it beneath the blue cup."},
			{"type": "source", "sourceId": "chunk-1", "kind": "story", "title": "Chapter 2"},
		},
		"metadata": map[string]any{"finishReason": "stop", "usage": map[string]any{"credits": 3}},
	}, ifMatch(int64(draft["revision"].(float64)))).expect(http.StatusOK).json()
	if finished["status"] != "complete" || finished["revision"].(float64) != 2 {
		t.Fatalf("finished message = %v", finished)
	}

	page := get(t, messages+"?limit=1", alice).expect(http.StatusOK).json()
	if len(page["messages"].([]any)) != 1 || page["nextCursor"] != "1" {
		t.Fatalf("first message page = %v", page)
	}
	second := get(t, messages+"?cursor=1", alice).expect(http.StatusOK).json()
	rows := second["messages"].([]any)
	if len(rows) != 1 || rows[0].(map[string]any)["status"] != "complete" {
		t.Fatalf("second message page = %v", second)
	}

	loaded := get(t, base+"/"+threadID, alice).expect(http.StatusOK).json()
	if loaded["messageCount"].(float64) != 2 {
		t.Fatalf("persisted message count = %v", loaded["messageCount"])
	}
}

func TestAssistantThreadsEnforceOwnershipRevisionsAndCascade(t *testing.T) {
	reset(t)
	story := newStory(t, alice, "Private Notes")
	storyID := story["id"].(string)
	base := "/v1/stories/" + storyID + "/assistant-threads"
	thread := call(t, http.MethodPost, base, alice, map[string]any{"title": "Draft notes"}).
		expect(http.StatusCreated).json()
	threadID := thread["id"].(string)

	// Thread existence is not disclosed through either collection or resource routes.
	get(t, base, bob).expect(http.StatusNotFound)
	get(t, base+"/"+threadID, bob).expect(http.StatusNotFound)

	rev := int64(thread["revision"].(float64))
	updated := call(t, http.MethodPatch, base+"/"+threadID, alice, map[string]any{
		"title": "Act one notes", "archived": true,
	}, ifMatch(rev)).expect(http.StatusOK).json()
	if updated["title"] != "Act one notes" || updated["archivedAt"] == nil {
		t.Fatalf("updated thread = %v", updated)
	}
	call(t, http.MethodPatch, base+"/"+threadID, alice,
		map[string]any{"title": "Stale"}, ifMatch(rev)).expect(http.StatusConflict)

	call(t, http.MethodPost, base+"/"+threadID+"/messages", alice, map[string]any{
		"role": "user", "idempotencyKey": "one", "parts": []map[string]any{{"type": "text", "text": "hello"}},
	}).expect(http.StatusCreated)

	storyRev := int64(story["revision"].(float64))
	call(t, http.MethodDelete, "/v1/stories/"+storyID, alice, nil, ifMatch(storyRev)).
		expect(http.StatusNoContent)
	var threads, messages int
	if err := testPool.QueryRow(context.Background(), "SELECT count(*) FROM assistant_threads").Scan(&threads); err != nil {
		t.Fatal(err)
	}
	if err := testPool.QueryRow(context.Background(), "SELECT count(*) FROM assistant_messages").Scan(&messages); err != nil {
		t.Fatal(err)
	}
	if threads != 0 || messages != 0 {
		t.Fatalf("cascade left threads=%d messages=%d", threads, messages)
	}
}

func TestAssistantMessageValidation(t *testing.T) {
	reset(t)
	story := newStory(t, alice, "Validation")
	base := "/v1/stories/" + story["id"].(string) + "/assistant-threads"
	thread := call(t, http.MethodPost, base, alice, map[string]any{}).
		expect(http.StatusCreated).json()
	messages := base + "/" + thread["id"].(string) + "/messages"

	for _, body := range []map[string]any{
		{"role": "system", "idempotencyKey": "bad-role", "parts": []any{map[string]any{"type": "text"}}},
		{"role": "user", "idempotencyKey": "bad-status", "status": "incomplete", "parts": []any{map[string]any{"type": "text"}}},
		{"role": "assistant", "idempotencyKey": "empty", "parts": []any{}},
		{"role": "assistant", "idempotencyKey": "bad-metadata", "parts": []any{map[string]any{"type": "text"}}, "metadata": []any{}},
	} {
		call(t, http.MethodPost, messages, alice, body).expect(http.StatusUnprocessableEntity)
	}
	get(t, messages+"?cursor=not-a-number", alice).expect(http.StatusUnprocessableEntity)
	get(t, base+"?limit=101", alice).expect(http.StatusBadRequest)
}
