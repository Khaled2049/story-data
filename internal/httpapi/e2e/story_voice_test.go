package e2e

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pressly/goose/v3"
)

func TestStoryVoiceNotesHistoryAndUndo(t *testing.T) {
	reset(t)
	story := newStory(t, alice, "Voice test")
	storyID := story["id"].(string)
	path := "/v1/stories/" + storyID + "/voice-profile"
	history := path + "/history"
	noteID := "daab4f49-a40b-442c-a30e-166a7271f955"

	empty := get(t, path, alice).expect(http.StatusOK)
	if empty.json()["revision"] != float64(0) || empty.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("unexpected empty profile: %s, cache=%q", empty.Body, empty.Header.Get("Cache-Control"))
	}
	if notes := empty.json()["notes"].([]any); len(notes) != 0 {
		t.Fatalf("empty notes = %v", notes)
	}
	get(t, path, bob).expect(http.StatusNotFound)
	get(t, history, bob).expect(http.StatusNotFound)
	call(t, "PUT", path, bob, map[string]any{
		"pointOfView": "first", "tense": "past", "notes": []any{},
	}, ifMatch(0)).expect(http.StatusNotFound)

	note := map[string]any{"id": noteID, "text": "Keep dialogue spare.", "origin": "writer", "pinned": true}
	input := map[string]any{"pointOfView": "first", "tense": "past", "notes": []any{note}}
	call(t, "PUT", path, alice, input).expect(http.StatusPreconditionRequired)
	created := call(t, "PUT", path, alice, input, ifMatch(0)).expect(http.StatusOK).json()
	if created["revision"] != float64(1) || created["notes"].([]any)[0].(map[string]any)["pinned"] != true {
		t.Fatalf("unexpected saved profile: %v", created)
	}
	call(t, "PUT", path, alice, input, ifMatch(0)).expect(http.StatusConflict)

	spoofed := map[string]any{"id": noteID, "text": "Changed", "origin": "agent", "pinned": true}
	call(t, "PUT", path, alice, map[string]any{
		"pointOfView": "first", "tense": "past", "notes": []any{spoofed},
	}, ifMatch(1)).expect(http.StatusUnprocessableEntity)

	updatedNote := map[string]any{"id": noteID, "text": "Let setting carry the mood.", "origin": "writer", "pinned": false}
	input["notes"] = []any{updatedNote}
	updated := call(t, "PUT", path, alice, input, ifMatch(1)).expect(http.StatusOK).json()
	if updated["revision"] != float64(2) || get(t, path, alice).expect(http.StatusOK).json()["notes"].([]any)[0].(map[string]any)["text"] != updatedNote["text"] {
		t.Fatalf("profile update was lost: %v", updated)
	}

	changes := get(t, history, alice).expect(http.StatusOK)
	if changes.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("history must not be cached")
	}
	rows := changes.list()
	if len(rows) != 2 || rows[0]["revisionAfter"] != float64(2) || rows[0]["before"].(map[string]any)["notes"].([]any)[0].(map[string]any)["text"] != note["text"] {
		t.Fatalf("history = %v", rows)
	}
	call(t, "POST", history+"/"+rows[1]["id"].(string)+"/undo", alice, nil, ifMatch(2)).expect(http.StatusConflict)
	call(t, "POST", history+"/"+rows[0]["id"].(string)+"/undo", bob, nil, ifMatch(2)).expect(http.StatusNotFound)
	undone := call(t, "POST", history+"/"+rows[0]["id"].(string)+"/undo", alice, nil, ifMatch(2)).expect(http.StatusOK).json()
	if undone["revision"] != float64(3) || undone["notes"].([]any)[0].(map[string]any)["text"] != note["text"] {
		t.Fatalf("undo failed: %v", undone)
	}
	if latest := get(t, history, alice).expect(http.StatusOK).list()[0]; latest["action"] != "undo" || latest["undoOf"] != rows[0]["id"] {
		t.Fatalf("undo not recorded: %v", latest)
	}
	call(t, "POST", history+"/"+rows[0]["id"].(string)+"/undo", alice, nil, ifMatch(2)).expect(http.StatusConflict)

	// The profile is never projected into ordinary story reads.
	if _, ok := get(t, "/v1/stories/"+storyID, alice).expect(http.StatusOK).json()["notes"]; ok {
		t.Fatal("voice notes leaked into story response")
	}
	call(t, "DELETE", "/v1/stories/"+storyID, alice, nil, ifMatch(rev(t, story))).expect(http.StatusNoContent)
	get(t, path, alice).expect(http.StatusNotFound)
	var remaining int
	if err := testPool.QueryRow(context.Background(),
		`SELECT count(*) FROM story_voice_profile_changes WHERE story_id=$1`, storyID).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("voice history survived story deletion: count=%d error=%v", remaining, err)
	}
}

func TestStoryVoiceReferencesAndScopedAgentUpdates(t *testing.T) {
	reset(t)
	story := newStory(t, alice, "Voice references")
	storyID := story["id"].(string)
	chapter := newChapter(t, alice, storyID, "Sample", 2)
	otherStory := newStory(t, alice, "Other voice")
	otherChapter := newChapter(t, alice, otherStory["id"].(string), "Other sample", 2)
	path := "/v1/stories/" + storyID + "/voice-profile"
	ref := map[string]any{"chapterId": chapter["id"], "sourceRevision": chapter["revision"], "start": 0, "end": 7}
	input := map[string]any{"pointOfView": "first", "tense": "past", "notes": []any{}, "learnFromWriting": false, "references": []any{ref}}
	bad := map[string]any{"pointOfView": "first", "tense": "past", "notes": []any{}, "references": []any{map[string]any{"chapterId": otherChapter["id"], "sourceRevision": 1, "start": 0, "end": 7}}}
	call(t, "PUT", path, alice, bad, ifMatch(0)).expect(http.StatusNotFound)
	created := call(t, "PUT", path, alice, input, ifMatch(0)).expect(http.StatusOK).json()
	if created["learnFromWriting"] != false || len(created["references"].([]any)) != 1 {
		t.Fatalf("profile fields lost: %v", created)
	}
	op := map[string]any{"operation": "add", "text": "Let tension emerge through dialogue.", "reason": "The writer gave a durable story-wide instruction.", "source": "explicit", "evidence": []any{}, "idempotencyKey": "8c95fd1c-7d5c-468c-aec4-5a8a9fca1784"}
	call(t, "POST", path+"/agent-note", alice, op, ifMatch(1)).expect(http.StatusForbidden)
	service := serviceHeaders(testServiceToken, alice)
	service["If-Match"] = "1"
	added := call(t, "POST", path+"/agent-note", "", op, service).expect(http.StatusOK).json()
	if added["applied"] != true || added["revision"] != float64(2) {
		t.Fatalf("agent update failed: %v", added)
	}
	service["If-Match"] = "1"
	replay := call(t, "POST", path+"/agent-note", "", op, service).expect(http.StatusOK).json()
	if replay["changeId"] != added["changeId"] {
		t.Fatalf("idempotent replay changed result: %v", replay)
	}
	service["X-User-ID"] = bob
	call(t, "POST", path+"/agent-note", "", op, service).expect(http.StatusNotFound)
	service["X-User-ID"] = alice
	service["If-Match"] = "2"
	op["idempotencyKey"] = "8c95fd1c-7d5c-468c-aec4-5a8a9fca1785"
	op["source"] = "passive"
	op["evidence"] = []any{ref, ref, ref}
	call(t, "POST", path+"/agent-note", "", op, service).expect(http.StatusUnprocessableEntity)
	latest := get(t, path+"/history", alice).expect(http.StatusOK).list()[0]
	if latest["action"] != "agent" || latest["reason"] != "The writer gave a durable story-wide instruction." {
		t.Fatalf("agent history missing: %v", latest)
	}
	undone := call(t, "POST", path+"/history/"+added["changeId"].(string)+"/undo", alice, nil, ifMatch(2)).expect(http.StatusOK).json()
	if len(undone["notes"].([]any)) != 0 {
		t.Fatalf("undo did not remove agent note: %v", undone)
	}
}

func TestStoryVoiceLearningJobFollowsSuccessfulChapterSave(t *testing.T) {
	reset(t)
	storyID := newStory(t, alice, "Learning queue")["id"].(string)
	chapter := newChapter(t, alice, storyID, "Saved prose", 2)
	chapterID := chapter["id"].(string)
	base := "/v1/stories/" + storyID + "/chapters/" + chapterID
	rev := int64(chapter["revision"].(float64))
	content := "<p>" + strings.Repeat("A long quiet passage. ", 20) + "</p>"
	saved := call(t, "PATCH", base, alice, map[string]any{"title": "Saved prose", "content": content, "position": 2}, ifMatch(rev)).expect(http.StatusOK).json()
	call(t, "PATCH", base, alice, map[string]any{"title": "Nope", "content": content, "position": 2}, ifMatch(rev)).expect(http.StatusConflict)
	var jobs int
	var sourceRevision int64
	if err := testPool.QueryRow(context.Background(), `SELECT count(*),max(source_revision) FROM story_voice_learning_jobs WHERE chapter_id=$1`, chapterID).Scan(&jobs, &sourceRevision); err != nil {
		t.Fatal(err)
	}
	if jobs != 1 || sourceRevision != int64(saved["revision"].(float64)) {
		t.Fatalf("learning jobs=%d revision=%d saved=%v", jobs, sourceRevision, saved)
	}
}

func TestAgentCanCreateFirstVoiceNoteAtRevisionZero(t *testing.T) {
	reset(t)
	storyID := newStory(t, alice, "First voice note")["id"].(string)
	path := "/v1/stories/" + storyID + "/voice-profile/agent-note"
	headers := serviceHeaders(testServiceToken, alice)
	headers["If-Match"] = "0"
	result := call(t, "POST", path, "", map[string]any{
		"operation": "add", "text": "Keep dialogue understated.", "reason": "The writer explicitly requested this for the story.",
		"source": "explicit", "evidence": []any{}, "idempotencyKey": "62b6e343-0266-4abc-8fb8-84008b0d4d8d",
	}, headers).expect(http.StatusOK).json()
	if result["revision"] != float64(1) || result["applied"] != true {
		t.Fatalf("first agent note failed: %v", result)
	}
	profile := get(t, "/v1/stories/"+storyID+"/voice-profile", alice).expect(http.StatusOK).json()
	if profile["notes"].([]any)[0].(map[string]any)["origin"] != "agent" {
		t.Fatalf("agent note origin missing: %v", profile)
	}
}

func TestStoryVoiceMigrationPreservesLegacyNote(t *testing.T) {
	ctx := context.Background()
	adminURL := os.Getenv("TEST_DATABASE_URL")
	if adminURL == "" {
		adminURL = defaultAdminURL
	}
	admin, err := sql.Open("pgx", adminURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	dbName := fmt.Sprintf("story_voice_migration_%d", os.Getpid())
	if _, err = admin.ExecContext(ctx, fmt.Sprintf("CREATE DATABASE %q", dbName)); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("pgx", swapDatabase(adminURL, dbName))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		db.Close()
		admin.ExecContext(ctx, fmt.Sprintf("SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '%s'", dbName))
		admin.ExecContext(ctx, fmt.Sprintf("DROP DATABASE IF EXISTS %q", dbName))
	}()
	dir, err := filepath.Abs(filepath.Join("..", "..", "..", "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	if err := goose.UpToContext(ctx, db, dir, 40); err != nil {
		t.Fatal(err)
	}
	storyID := "b0a30c41-52d4-4fa0-9187-8c0480ab8585"
	if _, err := db.ExecContext(ctx, `INSERT INTO stories (id,owner_id,title) VALUES ($1,'migration-test','Legacy')`, storyID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO story_voice_profiles (story_id,point_of_view,tense,notes)
		VALUES ($1,'first','past','Keep the rhythm uneven.')`, storyID); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpContext(ctx, db, dir); err != nil {
		t.Fatal(err)
	}
	var text, origin string
	var pinned bool
	if err := db.QueryRowContext(ctx, `SELECT voice_notes->0->>'text',voice_notes->0->>'origin',
		(voice_notes->0->>'pinned')::bool FROM story_voice_profiles WHERE story_id=$1`, storyID).
		Scan(&text, &origin, &pinned); err != nil {
		t.Fatal(err)
	}
	if text != "Keep the rhythm uneven." || origin != "writer" || pinned {
		t.Fatalf("legacy note lost in migration: %q %q pinned=%t", text, origin, pinned)
	}
}
