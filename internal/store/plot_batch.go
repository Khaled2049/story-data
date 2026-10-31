package store

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// CreateEvents appends a template in one transaction. A failed row or reference
// rolls back every earlier event and its indexing outbox entry.
func (s *Store) CreateEvents(ctx context.Context, storyID, lineID, uid string, inputs []PlotEventInput) ([]PlotEvent, error) {
	if len(inputs) == 0 || len(inputs) > 50 {
		return nil, fmt.Errorf("%w: events must contain 1 to 50 items", ErrValidation)
	}
	if e := s.eventLine(ctx, storyID, lineID, uid); e != nil {
		return nil, e
	}
	tx, e := s.db.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, `SELECT id FROM plot_lines WHERE id=$1 AND story_id=$2 FOR UPDATE`, lineID, storyID); e != nil {
		return nil, e
	}
	ids, e := s.insertEventBatch(ctx, tx, storyID, lineID, inputs)
	if e != nil {
		return nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	all, e := s.events(ctx, storyID, lineID)
	if e != nil {
		return nil, e
	}
	byID := make(map[string]PlotEvent, len(ids))
	for _, event := range all {
		byID[event.ID] = event
	}
	created := make([]PlotEvent, 0, len(ids))
	for _, id := range ids {
		created = append(created, byID[id])
	}
	return created, nil
}

// CreatePlotWithEvents creates the line and all selected template events atomically.
func (s *Store) CreatePlotWithEvents(ctx context.Context, storyID, uid string, line PlotLineInput, inputs []PlotEventInput) (PlotLine, error) {
	if !validPlotLineInput(line) || len(inputs) > 50 {
		return PlotLine{}, ErrValidation
	}
	if e := s.owner(ctx, storyID, uid); e != nil {
		return PlotLine{}, e
	}
	tx, e := s.db.Begin(ctx)
	if e != nil {
		return PlotLine{}, e
	}
	defer tx.Rollback(ctx)
	// Serialize new-line capacity checks for calls through this endpoint.
	if _, e = tx.Exec(ctx, `SELECT id FROM stories WHERE id=$1 FOR UPDATE`, storyID); e != nil {
		return PlotLine{}, e
	}
	var count int
	if e = tx.QueryRow(ctx, `SELECT count(*) FROM plot_lines WHERE story_id=$1`, storyID).Scan(&count); e != nil {
		return PlotLine{}, e
	}
	if count >= maxEntitiesPerStory {
		return PlotLine{}, ErrLimit
	}
	id := uuid.New().String()
	var out PlotLine
	if e = tx.QueryRow(ctx, `INSERT INTO plot_lines(id,story_id,name,description) VALUES($1,$2,$3,$4) RETURNING id,name,description,revision`, id, storyID, line.Name, line.Description).Scan(&out.ID, &out.Name, &out.Description, &out.Revision); e != nil {
		return PlotLine{}, e
	}
	if _, e = s.insertEventBatch(ctx, tx, storyID, id, inputs); e != nil {
		return PlotLine{}, e
	}
	if e = tx.Commit(ctx); e != nil {
		return PlotLine{}, e
	}
	out.Events, e = s.events(ctx, storyID, id)
	return out, e
}

func (s *Store) insertEventBatch(ctx context.Context, tx pgx.Tx, storyID, lineID string, inputs []PlotEventInput) ([]string, error) {
	var count int
	if e := tx.QueryRow(ctx, `SELECT count(*) FROM plot_events WHERE plot_line_id=$1`, lineID).Scan(&count); e != nil {
		return nil, e
	}
	if count+len(inputs) > maxEntitiesPerStory {
		return nil, ErrLimit
	}
	ids := make([]string, 0, len(inputs))
	for i := range inputs {
		in := &inputs[i]
		if e := validEvent(in); e != nil {
			return nil, e
		}
		if e := eventChapter(ctx, tx, storyID, in.ChapterID); e != nil {
			return nil, e
		}
		chapterPos, e := appendToChapter(ctx, tx, in.ChapterID)
		if e != nil {
			return nil, e
		}
		id := uuid.New().String()
		ids = append(ids, id)
		_, e = tx.Exec(ctx, `INSERT INTO plot_events(id,plot_line_id,name,content,location_id,tension_level,pacing,story_beat,emotional_tone,time_constraint,position,chapter_id,chapter_position,status,notes) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`, id, lineID, in.Name, in.Content, in.LocationID, in.TensionLevel, in.Pacing, in.StoryBeat, emptyToNull(in.EmotionalTone), nullJSON(in.TimeConstraint), count+i, in.ChapterID, chapterPos, in.Status, emptyToNull(in.Notes))
		if e != nil {
			return nil, e
		}
		if e = s.writeEventRefs(ctx, tx, storyID, id, *in); e != nil {
			return nil, e
		}
		if e = s.outbox(ctx, tx, "plot_event", id, storyID, "upsert", 1); e != nil {
			return nil, e
		}
	}
	return ids, nil
}
