package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type Relationship struct {
	CharacterID string `json:"characterId"`
	Name        string `json:"name"`
	Type        string `json:"type"`
	Description string `json:"description,omitempty"`
}
type Character struct {
	ID            string         `json:"id"`
	StoryID       string         `json:"storyId"`
	Name          string         `json:"name"`
	Age           *int           `json:"age,omitempty"`
	ArtURL        string         `json:"artUrl,omitempty"`
	Soul          string         `json:"soul,omitempty"`
	Personality   string         `json:"personality,omitempty"`
	Voice         string         `json:"voice,omitempty"`
	Backstory     string         `json:"backstory,omitempty"`
	Affiliations  string         `json:"affiliations,omitempty"`
	Notes         string         `json:"notes,omitempty"`
	Relationships []Relationship `json:"relationships"`
	Revision      int64          `json:"revision"`
}
type CharacterInput struct {
	Name          string         `json:"name"`
	Age           *int           `json:"age"`
	ArtURL        string         `json:"artUrl"`
	Soul          string         `json:"soul"`
	Personality   string         `json:"personality"`
	Voice         string         `json:"voice"`
	Backstory     string         `json:"backstory"`
	Affiliations  string         `json:"affiliations"`
	Notes         string         `json:"notes"`
	Relationships []Relationship `json:"relationships"`
}
type Place struct {
	ID           string `json:"id"`
	StoryID      string `json:"storyId"`
	Name         string `json:"name"`
	ImageURL     string `json:"imageUrl,omitempty"`
	Description  string `json:"description,omitempty"`
	Atmosphere   string `json:"atmosphere,omitempty"`
	Geography    string `json:"geography,omitempty"`
	History      string `json:"history,omitempty"`
	Significance string `json:"significance,omitempty"`
	Notes        string `json:"notes,omitempty"`
	Revision     int64  `json:"revision"`
}
type PlaceInput struct {
	Name         string `json:"name"`
	ImageURL     string `json:"imageUrl"`
	Description  string `json:"description"`
	Atmosphere   string `json:"atmosphere"`
	Geography    string `json:"geography"`
	History      string `json:"history"`
	Significance string `json:"significance"`
	Notes        string `json:"notes"`
}
type Dependency struct {
	EventID          string `json:"eventId"`
	PlotLineID       string `json:"plotLineId"`
	RelationshipType string `json:"relationshipType"`
	Description      string `json:"description,omitempty"`
}
type PlotEvent struct {
	ID             string          `json:"id"`
	Name           string          `json:"name"`
	Content        string          `json:"content"`
	CharacterIDs   []string        `json:"characterIds"`
	LocationID     *string         `json:"locationId"`
	Dependencies   []Dependency    `json:"dependencies"`
	Dependents     []Dependency    `json:"dependents"`
	TensionLevel   int             `json:"tensionLevel"`
	Pacing         string          `json:"pacing"`
	StoryBeat      string          `json:"storyBeat"`
	EmotionalTone  string          `json:"emotionalTone,omitempty"`
	TimeConstraint json.RawMessage `json:"timeConstraint,omitempty"`
	OrderIndex     int             `json:"orderIndex"`
	ChapterNumber  *int            `json:"chapterNumber,omitempty"`
	Notes          string          `json:"notes,omitempty"`
	Revision       int64           `json:"revision"`
}
type PlotEventInput struct {
	Name           string          `json:"name"`
	Content        string          `json:"content"`
	CharacterIDs   []string        `json:"characterIds"`
	LocationID     *string         `json:"locationId"`
	Dependencies   []Dependency    `json:"dependencies"`
	TensionLevel   int             `json:"tensionLevel"`
	Pacing         string          `json:"pacing"`
	StoryBeat      string          `json:"storyBeat"`
	EmotionalTone  string          `json:"emotionalTone"`
	TimeConstraint json.RawMessage `json:"timeConstraint"`
	OrderIndex     int             `json:"orderIndex"`
	ChapterNumber  *int            `json:"chapterNumber"`
	Notes          string          `json:"notes"`
}
type PlotLine struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Events      []PlotEvent `json:"events"`
	Revision    int64       `json:"revision"`
}
type PlotLineInput struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

func (s *Store) owner(ctx context.Context, storyID, uid string) error {
	st, err := s.GetStory(ctx, storyID, uid)
	if err != nil {
		return err
	}
	if st.OwnerID != uid {
		return ErrForbidden
	}
	return nil
}
func (s *Store) ListCharacters(ctx context.Context, storyID, uid string) ([]Character, error) {
	if err := s.owner(ctx, storyID, uid); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, `SELECT id,story_id,name,age,COALESCE(art_url,''),COALESCE(soul,''),COALESCE(personality,''),COALESCE(voice,''),COALESCE(backstory,''),COALESCE(affiliations,''),COALESCE(notes,''),revision FROM characters WHERE story_id=$1 ORDER BY name`, storyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Character{}
	for rows.Next() {
		x, e := scanCharacter(rows)
		if e != nil {
			return nil, e
		}
		if e = s.hydrateRelationships(ctx, &x); e != nil {
			return nil, e
		}
		out = append(out, x)
	}
	return out, rows.Err()
}
func scanCharacter(row pgx.Row) (Character, error) {
	var x Character
	var id, sid uuid.UUID
	e := row.Scan(&id, &sid, &x.Name, &x.Age, &x.ArtURL, &x.Soul, &x.Personality, &x.Voice, &x.Backstory, &x.Affiliations, &x.Notes, &x.Revision)
	x.ID = id.String()
	x.StoryID = sid.String()
	x.Relationships = []Relationship{}
	return x, e
}
func (s *Store) hydrateRelationships(ctx context.Context, x *Character) error {
	rows, e := s.db.Query(ctx, `SELECT r.related_character_id,c.name,r.relationship_type,COALESCE(r.description,'') FROM character_relationships r JOIN characters c ON c.id=r.related_character_id WHERE r.character_id=$1 ORDER BY c.name`, x.ID)
	if e != nil {
		return e
	}
	defer rows.Close()
	x.Relationships = []Relationship{}
	for rows.Next() {
		var r Relationship
		if e = rows.Scan(&r.CharacterID, &r.Name, &r.Type, &r.Description); e != nil {
			return e
		}
		x.Relationships = append(x.Relationships, r)
	}
	return rows.Err()
}
func (s *Store) character(ctx context.Context, storyID, id, uid string) (Character, error) {
	if e := s.owner(ctx, storyID, uid); e != nil {
		return Character{}, e
	}
	x, e := scanCharacter(s.db.QueryRow(ctx, `SELECT id,story_id,name,age,COALESCE(art_url,''),COALESCE(soul,''),COALESCE(personality,''),COALESCE(voice,''),COALESCE(backstory,''),COALESCE(affiliations,''),COALESCE(notes,''),revision FROM characters WHERE id=$1 AND story_id=$2`, id, storyID))
	if errors.Is(e, pgx.ErrNoRows) {
		return Character{}, ErrNotFound
	}
	if e != nil {
		return Character{}, e
	}
	return x, s.hydrateRelationships(ctx, &x)
}
func (s *Store) replaceRelationships(ctx context.Context, tx pgx.Tx, storyID, id string, rs []Relationship) error {
	if _, e := tx.Exec(ctx, `DELETE FROM character_relationships WHERE character_id=$1`, id); e != nil {
		return e
	}
	seen := map[string]bool{}
	for _, r := range rs {
		if r.CharacterID == id || seen[r.CharacterID] {
			return fmt.Errorf("invalid character relationship")
		}
		seen[r.CharacterID] = true
		var ok bool
		e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM characters WHERE id=$1 AND story_id=$2)`, r.CharacterID, storyID).Scan(&ok)
		if e != nil || !ok {
			return fmt.Errorf("relationship character is not in this story")
		}
		if _, e = tx.Exec(ctx, `INSERT INTO character_relationships(character_id,related_character_id,relationship_type,description) VALUES($1,$2,$3,$4)`, id, r.CharacterID, r.Type, emptyToNull(r.Description)); e != nil {
			return e
		}
	}
	return nil
}
func (s *Store) CreateCharacter(ctx context.Context, storyID, uid string, in CharacterInput) (Character, error) {
	if e := s.owner(ctx, storyID, uid); e != nil {
		return Character{}, e
	}
	tx, e := s.db.Begin(ctx)
	if e != nil {
		return Character{}, e
	}
	defer tx.Rollback(ctx)
	id := uuid.New()
	x, e := scanCharacter(tx.QueryRow(ctx, `INSERT INTO characters(id,story_id,name,age,art_url,soul,personality,voice,backstory,affiliations,notes) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) RETURNING id,story_id,name,age,COALESCE(art_url,''),COALESCE(soul,''),COALESCE(personality,''),COALESCE(voice,''),COALESCE(backstory,''),COALESCE(affiliations,''),COALESCE(notes,''),revision`, id, storyID, in.Name, in.Age, emptyToNull(in.ArtURL), emptyToNull(in.Soul), emptyToNull(in.Personality), emptyToNull(in.Voice), emptyToNull(in.Backstory), emptyToNull(in.Affiliations), emptyToNull(in.Notes)))
	if e != nil {
		return Character{}, e
	}
	if e = s.replaceRelationships(ctx, tx, storyID, x.ID, in.Relationships); e != nil {
		return Character{}, e
	}
	if e = s.outbox(ctx, tx, "character", x.ID, storyID, "upsert", x.Revision); e != nil {
		return Character{}, e
	}
	if e = tx.Commit(ctx); e != nil {
		return Character{}, e
	}
	return s.character(ctx, storyID, x.ID, uid)
}
func (s *Store) UpdateCharacter(ctx context.Context, storyID, id, uid string, rev int64, in CharacterInput) (Character, error) {
	if e := s.owner(ctx, storyID, uid); e != nil {
		return Character{}, e
	}
	tx, e := s.db.Begin(ctx)
	if e != nil {
		return Character{}, e
	}
	defer tx.Rollback(ctx)
	x, e := scanCharacter(tx.QueryRow(ctx, `UPDATE characters SET name=$1,age=$2,art_url=$3,soul=$4,personality=$5,voice=$6,backstory=$7,affiliations=$8,notes=$9,revision=revision+1,updated_at=now() WHERE id=$10 AND story_id=$11 AND revision=$12 RETURNING id,story_id,name,age,COALESCE(art_url,''),COALESCE(soul,''),COALESCE(personality,''),COALESCE(voice,''),COALESCE(backstory,''),COALESCE(affiliations,''),COALESCE(notes,''),revision`, in.Name, in.Age, emptyToNull(in.ArtURL), emptyToNull(in.Soul), emptyToNull(in.Personality), emptyToNull(in.Voice), emptyToNull(in.Backstory), emptyToNull(in.Affiliations), emptyToNull(in.Notes), id, storyID, rev))
	if errors.Is(e, pgx.ErrNoRows) {
		return Character{}, s.classifyWorldWrite(ctx, "characters", storyID, id, uid)
	}
	if e != nil {
		return Character{}, e
	}
	if e = s.replaceRelationships(ctx, tx, storyID, id, in.Relationships); e != nil {
		return Character{}, e
	}
	if e = s.outbox(ctx, tx, "character", id, storyID, "upsert", x.Revision); e != nil {
		return Character{}, e
	}
	if e = tx.Commit(ctx); e != nil {
		return Character{}, e
	}
	return s.character(ctx, storyID, id, uid)
}
func (s *Store) DeleteCharacter(ctx context.Context, storyID, id, uid string, rev int64) error {
	return s.deleteWorld(ctx, "characters", storyID, id, uid, rev, "character")
}

func (s *Store) ListPlaces(ctx context.Context, storyID, uid string) ([]Place, error) {
	if e := s.owner(ctx, storyID, uid); e != nil {
		return nil, e
	}
	rows, e := s.db.Query(ctx, `SELECT id,story_id,name,COALESCE(image_url,''),COALESCE(description,''),COALESCE(atmosphere,''),COALESCE(geography,''),COALESCE(history,''),COALESCE(significance,''),COALESCE(notes,''),revision FROM places WHERE story_id=$1 ORDER BY name`, storyID)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Place{}
	for rows.Next() {
		x, e := scanPlace(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, x)
	}
	return out, rows.Err()
}
func scanPlace(row pgx.Row) (Place, error) {
	var x Place
	var id, sid uuid.UUID
	e := row.Scan(&id, &sid, &x.Name, &x.ImageURL, &x.Description, &x.Atmosphere, &x.Geography, &x.History, &x.Significance, &x.Notes, &x.Revision)
	x.ID = id.String()
	x.StoryID = sid.String()
	return x, e
}
func (s *Store) CreatePlace(ctx context.Context, storyID, uid string, in PlaceInput) (Place, error) {
	if e := s.owner(ctx, storyID, uid); e != nil {
		return Place{}, e
	}
	id := uuid.New()
	x, e := scanPlace(s.db.QueryRow(ctx, `INSERT INTO places(id,story_id,name,image_url,description,atmosphere,geography,history,significance,notes) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING id,story_id,name,COALESCE(image_url,''),COALESCE(description,''),COALESCE(atmosphere,''),COALESCE(geography,''),COALESCE(history,''),COALESCE(significance,''),COALESCE(notes,''),revision`, id, storyID, in.Name, emptyToNull(in.ImageURL), emptyToNull(in.Description), emptyToNull(in.Atmosphere), emptyToNull(in.Geography), emptyToNull(in.History), emptyToNull(in.Significance), emptyToNull(in.Notes)))
	if e != nil {
		return Place{}, e
	}
	return x, s.writeOutbox(ctx, "place", x.ID, storyID, "upsert", x.Revision)
}
func (s *Store) UpdatePlace(ctx context.Context, storyID, id, uid string, rev int64, in PlaceInput) (Place, error) {
	if e := s.owner(ctx, storyID, uid); e != nil {
		return Place{}, e
	}
	x, e := scanPlace(s.db.QueryRow(ctx, `UPDATE places SET name=$1,image_url=$2,description=$3,atmosphere=$4,geography=$5,history=$6,significance=$7,notes=$8,revision=revision+1,updated_at=now() WHERE id=$9 AND story_id=$10 AND revision=$11 RETURNING id,story_id,name,COALESCE(image_url,''),COALESCE(description,''),COALESCE(atmosphere,''),COALESCE(geography,''),COALESCE(history,''),COALESCE(significance,''),COALESCE(notes,''),revision`, in.Name, emptyToNull(in.ImageURL), emptyToNull(in.Description), emptyToNull(in.Atmosphere), emptyToNull(in.Geography), emptyToNull(in.History), emptyToNull(in.Significance), emptyToNull(in.Notes), id, storyID, rev))
	if errors.Is(e, pgx.ErrNoRows) {
		return Place{}, s.classifyWorldWrite(ctx, "places", storyID, id, uid)
	}
	if e != nil {
		return Place{}, e
	}
	return x, s.writeOutbox(ctx, "place", id, storyID, "upsert", x.Revision)
}
func (s *Store) DeletePlace(ctx context.Context, storyID, id, uid string, rev int64) error {
	return s.deleteWorld(ctx, "places", storyID, id, uid, rev, "place")
}

func (s *Store) outbox(ctx context.Context, tx pgx.Tx, typ, id, storyID, op string, rev int64) error {
	_, e := tx.Exec(ctx, `INSERT INTO indexing_outbox(id,aggregate_type,aggregate_id,story_id,operation,revision) VALUES($1,$2,$3,$4,$5,$6)`, uuid.New(), typ, id, storyID, op, rev)
	return e
}
func (s *Store) writeOutbox(ctx context.Context, typ, id, storyID, op string, rev int64) error {
	tx, e := s.db.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = s.outbox(ctx, tx, typ, id, storyID, op, rev); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (s *Store) classifyWorldWrite(ctx context.Context, table, storyID, id, uid string) error {
	if e := s.owner(ctx, storyID, uid); e != nil {
		return e
	}
	var exists bool
	e := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM `+table+` WHERE id=$1 AND story_id=$2)`, id, storyID).Scan(&exists)
	if e != nil {
		return e
	}
	if !exists {
		return ErrNotFound
	}
	return ErrConflict
}
func (s *Store) deleteWorld(ctx context.Context, table, storyID, id, uid string, rev int64, typ string) error {
	if e := s.owner(ctx, storyID, uid); e != nil {
		return e
	}
	tx, e := s.db.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	cmd, e := tx.Exec(ctx, `DELETE FROM `+table+` WHERE id=$1 AND story_id=$2 AND revision=$3`, id, storyID, rev)
	if e != nil {
		return e
	}
	if cmd.RowsAffected() == 0 {
		return s.classifyWorldWrite(ctx, table, storyID, id, uid)
	}
	if e = s.outbox(ctx, tx, typ, id, storyID, "delete", rev); e != nil {
		return e
	}
	return tx.Commit(ctx)
}

// Plot storage is deliberately normalized, while reads return the existing nested UI shape.
func (s *Store) ListPlots(ctx context.Context, storyID, uid string) ([]PlotLine, error) {
	if e := s.owner(ctx, storyID, uid); e != nil {
		return nil, e
	}
	rows, e := s.db.Query(ctx, `SELECT id,name,description,revision FROM plot_lines WHERE story_id=$1 ORDER BY created_at`, storyID)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []PlotLine{}
	for rows.Next() {
		var x PlotLine
		if e = rows.Scan(&x.ID, &x.Name, &x.Description, &x.Revision); e != nil {
			return nil, e
		}
		if x.Events, e = s.events(ctx, storyID, x.ID); e != nil {
			return nil, e
		}
		out = append(out, x)
	}
	return out, rows.Err()
}
func (s *Store) events(ctx context.Context, storyID, lineID string) ([]PlotEvent, error) {
	rows, e := s.db.Query(ctx, `SELECT e.id,e.name,e.content,e.location_id,e.tension_level,e.pacing,e.story_beat,COALESCE(e.emotional_tone,''),e.time_constraint,e.position,e.chapter_number,COALESCE(e.notes,''),e.revision FROM plot_events e JOIN plot_lines l ON l.id=e.plot_line_id WHERE e.plot_line_id=$1 AND l.story_id=$2 ORDER BY e.position`, lineID, storyID)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []PlotEvent{}
	for rows.Next() {
		var x PlotEvent
		var lid *uuid.UUID
		var tc []byte
		var pos float64
		if e = rows.Scan(&x.ID, &x.Name, &x.Content, &lid, &x.TensionLevel, &x.Pacing, &x.StoryBeat, &x.EmotionalTone, &tc, &pos, &x.ChapterNumber, &x.Notes, &x.Revision); e != nil {
			return nil, e
		}
		if lid != nil {
			v := lid.String()
			x.LocationID = &v
		}
		x.TimeConstraint = tc
		x.OrderIndex = int(pos)
		x.CharacterIDs = []string{}
		x.Dependencies = []Dependency{}
		x.Dependents = []Dependency{}
		cr, e := s.db.Query(ctx, `SELECT character_id FROM plot_event_characters WHERE plot_event_id=$1`, x.ID)
		if e != nil {
			return nil, e
		}
		for cr.Next() {
			var v uuid.UUID
			if e = cr.Scan(&v); e != nil {
				cr.Close()
				return nil, e
			}
			x.CharacterIDs = append(x.CharacterIDs, v.String())
		}
		cr.Close()
		dr, e := s.db.Query(ctx, `SELECT d.depends_on_event_id,l.id,d.relationship_type,COALESCE(d.description,'') FROM plot_event_dependencies d JOIN plot_events e ON e.id=d.depends_on_event_id JOIN plot_lines l ON l.id=e.plot_line_id WHERE d.plot_event_id=$1`, x.ID)
		if e != nil {
			return nil, e
		}
		for dr.Next() {
			var d Dependency
			if e = dr.Scan(&d.EventID, &d.PlotLineID, &d.RelationshipType, &d.Description); e != nil {
				dr.Close()
				return nil, e
			}
			x.Dependencies = append(x.Dependencies, d)
		}
		dr.Close()
		out = append(out, x)
	}
	return out, rows.Err()
}
func (s *Store) CreatePlot(ctx context.Context, storyID, uid string, in PlotLineInput) (PlotLine, error) {
	if e := s.owner(ctx, storyID, uid); e != nil {
		return PlotLine{}, e
	}
	id := uuid.New()
	var x PlotLine
	e := s.db.QueryRow(ctx, `INSERT INTO plot_lines(id,story_id,name,description) VALUES($1,$2,$3,$4) RETURNING id,name,description,revision`, id, storyID, in.Name, in.Description).Scan(&x.ID, &x.Name, &x.Description, &x.Revision)
	x.Events = []PlotEvent{}
	return x, e
}
func (s *Store) UpdatePlot(ctx context.Context, storyID, id, uid string, rev int64, in PlotLineInput) (PlotLine, error) {
	if e := s.owner(ctx, storyID, uid); e != nil {
		return PlotLine{}, e
	}
	var x PlotLine
	e := s.db.QueryRow(ctx, `UPDATE plot_lines SET name=$1,description=$2,revision=revision+1,updated_at=now() WHERE id=$3 AND story_id=$4 AND revision=$5 RETURNING id,name,description,revision`, in.Name, in.Description, id, storyID, rev).Scan(&x.ID, &x.Name, &x.Description, &x.Revision)
	if errors.Is(e, pgx.ErrNoRows) {
		return x, s.classifyWorldWrite(ctx, "plot_lines", storyID, id, uid)
	}
	if e == nil {
		x.Events, _ = s.events(ctx, storyID, id)
	}
	return x, e
}
func (s *Store) DeletePlot(ctx context.Context, storyID, id, uid string, rev int64) error {
	if e := s.owner(ctx, storyID, uid); e != nil {
		return e
	}
	tx, e := s.db.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	rows, e := tx.Query(ctx, `SELECT id, revision FROM plot_events WHERE plot_line_id=$1`, id)
	if e != nil {
		return e
	}
	type eventRef struct {
		id       string
		revision int64
	}
	var events []eventRef
	for rows.Next() {
		var eventID uuid.UUID
		var eventRevision int64
		if e = rows.Scan(&eventID, &eventRevision); e != nil {
			rows.Close()
			return e
		}
		events = append(events, eventRef{eventID.String(), eventRevision})
	}
	rows.Close()
	cmd, e := tx.Exec(ctx, `DELETE FROM plot_lines WHERE id=$1 AND story_id=$2 AND revision=$3`, id, storyID, rev)
	if e != nil {
		return e
	}
	if cmd.RowsAffected() == 0 {
		return s.classifyWorldWrite(ctx, "plot_lines", storyID, id, uid)
	}
	for _, event := range events {
		if e = s.outbox(ctx, tx, "plot_event", event.id, storyID, "delete", event.revision); e != nil {
			return e
		}
	}
	return tx.Commit(ctx)
}
func validEvent(in *PlotEventInput) error {
	if in.TensionLevel == 0 {
		in.TensionLevel = 5
	}
	if in.TensionLevel < 1 || in.TensionLevel > 10 {
		return fmt.Errorf("tensionLevel must be between 1 and 10")
	}
	if in.Pacing == "" {
		in.Pacing = "moderate"
	}
	if in.StoryBeat == "" {
		in.StoryBeat = "rising_action"
	}
	return nil
}
func (s *Store) eventLine(ctx context.Context, storyID, lineID, uid string) error {
	if e := s.owner(ctx, storyID, uid); e != nil {
		return e
	}
	var ok bool
	e := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM plot_lines WHERE id=$1 AND story_id=$2)`, lineID, storyID).Scan(&ok)
	if e != nil {
		return e
	}
	if !ok {
		return ErrNotFound
	}
	return nil
}
func (s *Store) writeEventRefs(ctx context.Context, tx pgx.Tx, storyID, eventID string, in PlotEventInput) error {
	if _, e := tx.Exec(ctx, `DELETE FROM plot_event_characters WHERE plot_event_id=$1`, eventID); e != nil {
		return e
	}
	for _, id := range in.CharacterIDs {
		var ok bool
		if e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM characters WHERE id=$1 AND story_id=$2)`, id, storyID).Scan(&ok); e != nil || !ok {
			return fmt.Errorf("event character is not in this story")
		}
		if _, e := tx.Exec(ctx, `INSERT INTO plot_event_characters(plot_event_id,character_id) VALUES($1,$2)`, eventID, id); e != nil {
			return e
		}
	}
	if _, e := tx.Exec(ctx, `DELETE FROM plot_event_dependencies WHERE plot_event_id=$1`, eventID); e != nil {
		return e
	}
	for _, d := range in.Dependencies {
		if d.EventID == eventID {
			return fmt.Errorf("event cannot depend on itself")
		}
		var ok bool
		if e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM plot_events e JOIN plot_lines l ON l.id=e.plot_line_id WHERE e.id=$1 AND l.story_id=$2)`, d.EventID, storyID).Scan(&ok); e != nil || !ok {
			return fmt.Errorf("dependency event is not in this story")
		}
		if _, e := tx.Exec(ctx, `INSERT INTO plot_event_dependencies(plot_event_id,depends_on_event_id,relationship_type,description) VALUES($1,$2,$3,$4)`, eventID, d.EventID, d.RelationshipType, emptyToNull(d.Description)); e != nil {
			return e
		}
	}
	return nil
}
func (s *Store) CreateEvent(ctx context.Context, storyID, lineID, uid string, in PlotEventInput) (PlotEvent, error) {
	if e := s.eventLine(ctx, storyID, lineID, uid); e != nil {
		return PlotEvent{}, e
	}
	if e := validEvent(&in); e != nil {
		return PlotEvent{}, e
	}
	tx, e := s.db.Begin(ctx)
	if e != nil {
		return PlotEvent{}, e
	}
	defer tx.Rollback(ctx)
	var n int
	if e = tx.QueryRow(ctx, `SELECT count(*) FROM plot_events WHERE plot_line_id=$1`, lineID).Scan(&n); e != nil {
		return PlotEvent{}, e
	}
	id := uuid.New()
	_, e = tx.Exec(ctx, `INSERT INTO plot_events(id,plot_line_id,name,content,location_id,tension_level,pacing,story_beat,emotional_tone,time_constraint,position,chapter_number,notes) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`, id, lineID, in.Name, in.Content, in.LocationID, in.TensionLevel, in.Pacing, in.StoryBeat, emptyToNull(in.EmotionalTone), nullJSON(in.TimeConstraint), n, in.ChapterNumber, emptyToNull(in.Notes))
	if e != nil {
		return PlotEvent{}, e
	}
	if e = s.writeEventRefs(ctx, tx, storyID, id.String(), in); e != nil {
		return PlotEvent{}, e
	}
	if e = s.outbox(ctx, tx, "plot_event", id.String(), storyID, "upsert", 1); e != nil {
		return PlotEvent{}, e
	}
	if e = tx.Commit(ctx); e != nil {
		return PlotEvent{}, e
	}
	return s.findEvent(ctx, storyID, lineID, id.String())
}
func nullJSON(x json.RawMessage) any {
	if len(x) == 0 || string(x) == "null" {
		return nil
	}
	return x
}
func (s *Store) findEvent(ctx context.Context, storyID, lineID, id string) (PlotEvent, error) {
	xs, e := s.events(ctx, storyID, lineID)
	if e != nil {
		return PlotEvent{}, e
	}
	for _, x := range xs {
		if x.ID == id {
			return x, nil
		}
	}
	return PlotEvent{}, ErrNotFound
}
func (s *Store) UpdateEvent(ctx context.Context, storyID, lineID, id, uid string, rev int64, in PlotEventInput) (PlotEvent, error) {
	if e := s.eventLine(ctx, storyID, lineID, uid); e != nil {
		return PlotEvent{}, e
	}
	if e := validEvent(&in); e != nil {
		return PlotEvent{}, e
	}
	tx, e := s.db.Begin(ctx)
	if e != nil {
		return PlotEvent{}, e
	}
	defer tx.Rollback(ctx)
	cmd, e := tx.Exec(ctx, `UPDATE plot_events SET name=$1,content=$2,location_id=$3,tension_level=$4,pacing=$5,story_beat=$6,emotional_tone=$7,time_constraint=$8,chapter_number=$9,notes=$10,revision=revision+1,updated_at=now() WHERE id=$11 AND plot_line_id=$12 AND revision=$13`, in.Name, in.Content, in.LocationID, in.TensionLevel, in.Pacing, in.StoryBeat, emptyToNull(in.EmotionalTone), nullJSON(in.TimeConstraint), in.ChapterNumber, emptyToNull(in.Notes), id, lineID, rev)
	if e != nil {
		return PlotEvent{}, e
	}
	if cmd.RowsAffected() == 0 {
		return PlotEvent{}, s.classifyEventWrite(ctx, storyID, lineID, id, uid)
	}
	if e = s.writeEventRefs(ctx, tx, storyID, id, in); e != nil {
		return PlotEvent{}, e
	}
	if e = s.outbox(ctx, tx, "plot_event", id, storyID, "upsert", rev+1); e != nil {
		return PlotEvent{}, e
	}
	if e = tx.Commit(ctx); e != nil {
		return PlotEvent{}, e
	}
	return s.findEvent(ctx, storyID, lineID, id)
}
func (s *Store) classifyEventWrite(ctx context.Context, storyID, lineID, id, uid string) error {
	if e := s.eventLine(ctx, storyID, lineID, uid); e != nil {
		return e
	}
	var ok bool
	e := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM plot_events WHERE id=$1 AND plot_line_id=$2)`, id, lineID).Scan(&ok)
	if e != nil {
		return e
	}
	if !ok {
		return ErrNotFound
	}
	return ErrConflict
}
func (s *Store) DeleteEvent(ctx context.Context, storyID, lineID, id, uid string, rev int64) ([]PlotEvent, error) {
	if e := s.eventLine(ctx, storyID, lineID, uid); e != nil {
		return nil, e
	}
	tx, e := s.db.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(ctx)
	cmd, e := tx.Exec(ctx, `DELETE FROM plot_events WHERE id=$1 AND plot_line_id=$2 AND revision=$3`, id, lineID, rev)
	if e != nil {
		return nil, e
	}
	if cmd.RowsAffected() == 0 {
		return nil, s.classifyEventWrite(ctx, storyID, lineID, id, uid)
	}
	if e = s.renumberEvents(ctx, tx, lineID, nil); e != nil {
		return nil, e
	}
	if e = s.outbox(ctx, tx, "plot_event", id, storyID, "delete", rev); e != nil {
		return nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return s.events(ctx, storyID, lineID)
}
func (s *Store) renumberEvents(ctx context.Context, tx pgx.Tx, lineID string, first []string) error {
	rows, e := tx.Query(ctx, `SELECT id FROM plot_events WHERE plot_line_id=$1 ORDER BY position FOR UPDATE`, lineID)
	if e != nil {
		return e
	}
	var ids []string
	for rows.Next() {
		var id uuid.UUID
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return e
		}
		ids = append(ids, id.String())
	}
	rows.Close()
	seen := map[string]bool{}
	next := []string{}
	for _, id := range first {
		for _, x := range ids {
			if id == x && !seen[id] {
				seen[id] = true
				next = append(next, id)
			}
		}
	}
	for _, id := range ids {
		if !seen[id] {
			next = append(next, id)
		}
	}
	for i, id := range next {
		if _, e = tx.Exec(ctx, `UPDATE plot_events SET position=$1,revision=revision+1,updated_at=now() WHERE id=$2`, i+1000000, id); e != nil {
			return e
		}
	}
	for i, id := range next {
		if _, e = tx.Exec(ctx, `UPDATE plot_events SET position=$1 WHERE id=$2`, i, id); e != nil {
			return e
		}
	}
	return nil
}
func (s *Store) ReorderEvents(ctx context.Context, storyID, lineID, uid string, rev int64, ordered []string) ([]PlotEvent, error) {
	if e := s.eventLine(ctx, storyID, lineID, uid); e != nil {
		return nil, e
	}
	tx, e := s.db.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(ctx)
	var current int64
	if e = tx.QueryRow(ctx, `SELECT revision FROM plot_lines WHERE id=$1 FOR UPDATE`, lineID).Scan(&current); e != nil {
		return nil, e
	}
	if current != rev {
		return nil, ErrConflict
	}
	if e = s.renumberEvents(ctx, tx, lineID, ordered); e != nil {
		return nil, e
	}
	if _, e = tx.Exec(ctx, `UPDATE plot_lines SET revision=revision+1,updated_at=now() WHERE id=$1`, lineID); e != nil {
		return nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return s.events(ctx, storyID, lineID)
}
