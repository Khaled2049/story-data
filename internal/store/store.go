package store

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNotFound = errors.New("not found")
var ErrForbidden = errors.New("forbidden")
var ErrConflict = errors.New("revision conflict")
var ErrUsernameTaken = errors.New("username already taken")
var ErrLimit = errors.New("story limit exceeded")
var ErrRateLimit = errors.New("rate limit exceeded")
var ErrValidation = errors.New("invalid input")

const chapterLimit = 50
const wordLimit = 5000

type Story struct {
	ID             string    `json:"id"`
	OwnerID        string    `json:"ownerId"`
	Title          string    `json:"title"`
	Description    string    `json:"description"`
	AuthorName     string    `json:"authorName"`
	Category       string    `json:"category"`
	TargetAudience string    `json:"targetAudience"`
	Language       string    `json:"language"`
	Copyright      string    `json:"copyright"`
	CoverImageURL  string    `json:"coverImageUrl"`
	ThumbnailURL   string    `json:"thumbnailUrl"`
	Tags           []string  `json:"tags"`
	Published      bool      `json:"published"`
	Revision       int64     `json:"revision"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}
type Chapter struct {
	ID        string    `json:"id"`
	StoryID   string    `json:"storyId"`
	Title     string    `json:"title"`
	Content   string    `json:"content"`
	Position  float64   `json:"position"`
	WordCount int       `json:"wordCount"`
	Revision  int64     `json:"revision"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	Summary   string    `json:"summary,omitempty"`
}
type ChapterSummaryInput struct {
	Summary string `json:"summary"`
}
type StoryInput struct {
	Title          string   `json:"title"`
	Description    string   `json:"description"`
	AuthorName     string   `json:"authorName"`
	Category       string   `json:"category"`
	TargetAudience string   `json:"targetAudience"`
	Language       string   `json:"language"`
	Copyright      string   `json:"copyright"`
	CoverImageURL  string   `json:"coverImageUrl"`
	ThumbnailURL   string   `json:"thumbnailUrl"`
	Tags           []string `json:"tags"`
	Published      bool     `json:"published"`
}
type ChapterInput struct {
	Title    string  `json:"title"`
	Content  string  `json:"content"`
	Position float64 `json:"position"`
}
type Context struct {
	Story    Story
	Chapters []Chapter
}

type Store struct{ db *pgxpool.Pool }

func New(db *pgxpool.Pool) *Store { return &Store{db: db} }

func (s *Store) CreateStory(ctx context.Context, owner string, in StoryInput) (Story, error) {
	id := uuid.New()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Story{}, err
	}
	defer tx.Rollback(ctx)
	row := tx.QueryRow(ctx, `INSERT INTO stories (id, owner_id, title, description, author_name, is_published, category, target_audience, language, copyright, cover_image_url, thumbnail_url)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) RETURNING id, owner_id, title, description, author_name, is_published, COALESCE(category,''), COALESCE(target_audience,''), COALESCE(language,''), COALESCE(copyright,''), COALESCE(cover_image_url,''), COALESCE(thumbnail_url,''), revision, created_at, updated_at`, id, owner, in.Title, in.Description, in.AuthorName, in.Published, emptyToNull(in.Category), emptyToNull(in.TargetAudience), emptyToNull(in.Language), emptyToNull(in.Copyright), emptyToNull(in.CoverImageURL), emptyToNull(in.ThumbnailURL))
	story, err := scanStory(row)
	if err != nil {
		return Story{}, err
	}
	if err := replaceTags(ctx, tx, id, in.Tags); err != nil {
		return Story{}, err
	}
	story.Tags = in.Tags
	chapterID := uuid.New()
	if _, err = tx.Exec(ctx, `INSERT INTO chapters (id, story_id, title, content, position, word_count) VALUES ($1,$2,'Chapter 1','',0,0)`, chapterID, id); err != nil {
		return Story{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Story{}, err
	}
	return story, nil
}

func (s *Store) ListStories(ctx context.Context, owner string) ([]Story, error) {
	rows, err := s.db.Query(ctx, `SELECT id, owner_id, title, description, author_name, is_published, COALESCE(category,''), COALESCE(target_audience,''), COALESCE(language,''), COALESCE(copyright,''), COALESCE(cover_image_url,''), COALESCE(thumbnail_url,''), revision, created_at, updated_at FROM stories WHERE owner_id=$1 ORDER BY updated_at DESC`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	stories, err := collectStories(rows)
	if err != nil {
		return nil, err
	}
	for i := range stories {
		if err := s.hydrateTags(ctx, &stories[i]); err != nil {
			return nil, err
		}
	}
	return stories, nil
}

func (s *Store) GetStory(ctx context.Context, id, caller string) (Story, error) {
	row := s.db.QueryRow(ctx, `SELECT id, owner_id, title, description, author_name, is_published, COALESCE(category,''), COALESCE(target_audience,''), COALESCE(language,''), COALESCE(copyright,''), COALESCE(cover_image_url,''), COALESCE(thumbnail_url,''), revision, created_at, updated_at FROM stories WHERE id=$1`, id)
	story, err := scanStory(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Story{}, ErrNotFound
	}
	if err != nil {
		return Story{}, err
	}
	if story.OwnerID != caller && !story.Published {
		return Story{}, ErrForbidden
	}
	if err := s.hydrateTags(ctx, &story); err != nil {
		return Story{}, err
	}
	return story, nil
}
func (s *Store) hydrateTags(ctx context.Context, story *Story) error {
	rows, err := s.db.Query(ctx, `SELECT tag FROM story_tags WHERE story_id=$1 ORDER BY tag`, story.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	story.Tags = []string{}
	for rows.Next() {
		var tag string
		if err := rows.Scan(&tag); err != nil {
			return err
		}
		story.Tags = append(story.Tags, tag)
	}
	return rows.Err()
}

func (s *Store) UpdateStory(ctx context.Context, id, owner string, rev int64, in StoryInput) (Story, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Story{}, err
	}
	defer tx.Rollback(ctx)
	row := tx.QueryRow(ctx, `UPDATE stories SET title=$1, description=$2, author_name=$3, is_published=$4, category=$5, target_audience=$6, language=$7, copyright=$8, cover_image_url=$9, thumbnail_url=$10, revision=revision+1, updated_at=now() WHERE id=$11 AND owner_id=$12 AND revision=$13 RETURNING id, owner_id, title, description, author_name, is_published, COALESCE(category,''), COALESCE(target_audience,''), COALESCE(language,''), COALESCE(copyright,''), COALESCE(cover_image_url,''), COALESCE(thumbnail_url,''), revision, created_at, updated_at`, in.Title, in.Description, in.AuthorName, in.Published, emptyToNull(in.Category), emptyToNull(in.TargetAudience), emptyToNull(in.Language), emptyToNull(in.Copyright), emptyToNull(in.CoverImageURL), emptyToNull(in.ThumbnailURL), id, owner, rev)
	story, err := scanStory(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Story{}, s.classifyWrite(ctx, "stories", id, owner)
	}
	if err != nil {
		return Story{}, err
	}
	if err = replaceTags(ctx, tx, mustUUID(id), in.Tags); err != nil {
		return Story{}, err
	}
	story.Tags = in.Tags
	if err = tx.Commit(ctx); err != nil {
		return Story{}, err
	}
	return story, nil
}

func (s *Store) DeleteStory(ctx context.Context, id, owner string, rev int64) error {
	cmd, err := s.db.Exec(ctx, `DELETE FROM stories WHERE id=$1 AND owner_id=$2 AND revision=$3`, id, owner, rev)
	if err != nil {
		return err
	}
	if cmd.RowsAffected() == 0 {
		return s.classifyWrite(ctx, "stories", id, owner)
	}
	return nil
}

func (s *Store) ListChapters(ctx context.Context, storyID, caller string) ([]Chapter, error) {
	if _, err := s.GetStory(ctx, storyID, caller); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, `SELECT id, story_id, title, content, position, word_count, revision, created_at, updated_at FROM chapters WHERE story_id=$1 ORDER BY position`, storyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectChapters(rows)
}
func (s *Store) GetChapter(ctx context.Context, storyID, id, caller string) (Chapter, error) {
	if _, err := s.GetStory(ctx, storyID, caller); err != nil {
		return Chapter{}, err
	}
	row := s.db.QueryRow(ctx, `SELECT c.id,c.story_id,c.title,c.content,c.position,c.word_count,c.revision,c.created_at,c.updated_at,COALESCE(cs.summary,'') FROM chapters c LEFT JOIN chapter_summaries cs ON cs.chapter_id=c.id WHERE c.story_id=$1 AND c.id=$2`, storyID, id)
	chapter, err := scanChapterWithSummary(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Chapter{}, ErrNotFound
	}
	return chapter, err
}
func (s *Store) PutChapterSummary(ctx context.Context, storyID, id, owner string, sourceRevision int64, in ChapterSummaryInput) (Chapter, error) {
	if err := s.owner(ctx, storyID, owner); err != nil {
		return Chapter{}, err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Chapter{}, err
	}
	defer tx.Rollback(ctx)
	var actual int64
	if err = tx.QueryRow(ctx, `SELECT revision FROM chapters WHERE id=$1 AND story_id=$2`, id, storyID).Scan(&actual); errors.Is(err, pgx.ErrNoRows) {
		return Chapter{}, ErrNotFound
	} else if err != nil {
		return Chapter{}, err
	}
	if actual != sourceRevision {
		return Chapter{}, ErrConflict
	}
	if _, err = tx.Exec(ctx, `INSERT INTO chapter_summaries(chapter_id,source_revision,summary) VALUES($1,$2,$3) ON CONFLICT(chapter_id) DO UPDATE SET source_revision=EXCLUDED.source_revision,summary=EXCLUDED.summary,updated_at=now()`, id, sourceRevision, in.Summary); err != nil {
		return Chapter{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Chapter{}, err
	}
	return s.GetChapter(ctx, storyID, id, owner)
}

func (s *Store) CreateChapter(ctx context.Context, storyID, owner string, in ChapterInput) (Chapter, error) {
	story, err := s.GetStory(ctx, storyID, owner)
	if err != nil {
		return Chapter{}, err
	}
	if story.OwnerID != owner {
		return Chapter{}, ErrForbidden
	}
	if wordCount(in.Content) > wordLimit {
		return Chapter{}, ErrLimit
	}
	var count int
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM chapters WHERE story_id=$1`, storyID).Scan(&count); err != nil {
		return Chapter{}, err
	}
	if count >= chapterLimit {
		return Chapter{}, ErrLimit
	}
	id := uuid.New()
	words := wordCount(in.Content)
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Chapter{}, err
	}
	defer tx.Rollback(ctx)
	row := tx.QueryRow(ctx, `INSERT INTO chapters (id, story_id, title, content, position, word_count) VALUES ($1,$2,$3,$4,$5,$6) RETURNING id, story_id, title, content, position, word_count, revision, created_at, updated_at`, id, storyID, in.Title, in.Content, in.Position, words)
	chapter, err := scanChapter(row)
	if err != nil {
		return Chapter{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO indexing_outbox (id, aggregate_type, aggregate_id, story_id, operation, revision) VALUES ($1,'chapter',$2,$3,'upsert',$4)`, uuid.New(), id, storyID, chapter.Revision)
	if err != nil {
		return Chapter{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Chapter{}, err
	}
	return chapter, nil
}

func (s *Store) UpdateChapter(ctx context.Context, storyID, id, owner string, rev int64, in ChapterInput) (Chapter, error) {
	if wordCount(in.Content) > wordLimit {
		return Chapter{}, ErrLimit
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Chapter{}, err
	}
	defer tx.Rollback(ctx)
	row := tx.QueryRow(ctx, `UPDATE chapters c SET title=$1, content=$2, position=$3, word_count=$4, revision=c.revision+1, updated_at=now() FROM stories s WHERE c.id=$5 AND c.story_id=$6 AND c.story_id=s.id AND s.owner_id=$7 AND c.revision=$8 RETURNING c.id, c.story_id, c.title, c.content, c.position, c.word_count, c.revision, c.created_at, c.updated_at`, in.Title, in.Content, in.Position, wordCount(in.Content), id, storyID, owner, rev)
	chapter, err := scanChapter(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Chapter{}, s.classifyChapterWrite(ctx, storyID, id, owner)
	}
	if err != nil {
		return Chapter{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO indexing_outbox (id, aggregate_type, aggregate_id, story_id, operation, revision) VALUES ($1,'chapter',$2,$3,'upsert',$4)`, uuid.New(), id, storyID, chapter.Revision)
	if err != nil {
		return Chapter{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Chapter{}, err
	}
	return chapter, nil
}

func (s *Store) DeleteChapter(ctx context.Context, storyID, id, owner string, rev int64) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	row := tx.QueryRow(ctx, `DELETE FROM chapters c USING stories s WHERE c.id=$1 AND c.story_id=$2 AND c.story_id=s.id AND s.owner_id=$3 AND c.revision=$4 RETURNING c.id`, id, storyID, owner, rev)
	var deleted string
	if err := row.Scan(&deleted); errors.Is(err, pgx.ErrNoRows) {
		return s.classifyChapterWrite(ctx, storyID, id, owner)
	} else if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO indexing_outbox (id, aggregate_type, aggregate_id, story_id, operation, revision) VALUES ($1,'chapter',$2,$3,'delete',$4)`, uuid.New(), id, storyID, rev)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) StoryContext(ctx context.Context, id, caller string) (Context, error) {
	st, err := s.GetStory(ctx, id, caller)
	if err != nil {
		return Context{}, err
	}
	ch, err := s.ListChapters(ctx, id, caller)
	return Context{Story: st, Chapters: ch}, err
}
func (s *Store) classifyWrite(ctx context.Context, table, id, owner string) error {
	var actualOwner string
	err := s.db.QueryRow(ctx, `SELECT owner_id FROM `+table+` WHERE id=$1`, id).Scan(&actualOwner)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if actualOwner != owner {
		return ErrForbidden
	}
	return ErrConflict
}
func (s *Store) classifyChapterWrite(ctx context.Context, storyID, id, owner string) error {
	var actualOwner string
	err := s.db.QueryRow(ctx, `SELECT s.owner_id FROM chapters c JOIN stories s ON s.id=c.story_id WHERE c.id=$1 AND c.story_id=$2`, id, storyID).Scan(&actualOwner)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if actualOwner != owner {
		return ErrForbidden
	}
	return ErrConflict
}
func scanStory(row pgx.Row) (Story, error) {
	var x Story
	var id uuid.UUID
	err := row.Scan(&id, &x.OwnerID, &x.Title, &x.Description, &x.AuthorName, &x.Published, &x.Category, &x.TargetAudience, &x.Language, &x.Copyright, &x.CoverImageURL, &x.ThumbnailURL, &x.Revision, &x.CreatedAt, &x.UpdatedAt)
	x.ID = id.String()
	return x, err
}
func emptyToNull(v string) any {
	if v == "" {
		return nil
	}
	return v
}
func mustUUID(v string) uuid.UUID { id, _ := uuid.Parse(v); return id }
func replaceTags(ctx context.Context, tx pgx.Tx, storyID uuid.UUID, tags []string) error {
	if _, err := tx.Exec(ctx, `DELETE FROM story_tags WHERE story_id=$1`, storyID); err != nil {
		return err
	}
	for _, tag := range tags {
		if tag == "" {
			continue
		}
		if _, err := tx.Exec(ctx, `INSERT INTO story_tags (story_id,tag) VALUES ($1,$2) ON CONFLICT DO NOTHING`, storyID, tag); err != nil {
			return err
		}
	}
	return nil
}
func scanChapter(row pgx.Row) (Chapter, error) {
	var x Chapter
	var id, sid uuid.UUID
	err := row.Scan(&id, &sid, &x.Title, &x.Content, &x.Position, &x.WordCount, &x.Revision, &x.CreatedAt, &x.UpdatedAt)
	x.ID = id.String()
	x.StoryID = sid.String()
	return x, err
}
func scanChapterWithSummary(row pgx.Row) (Chapter, error) {
	var x Chapter
	var id, sid uuid.UUID
	err := row.Scan(&id, &sid, &x.Title, &x.Content, &x.Position, &x.WordCount, &x.Revision, &x.CreatedAt, &x.UpdatedAt, &x.Summary)
	x.ID = id.String()
	x.StoryID = sid.String()
	return x, err
}
func collectStories(rows pgx.Rows) ([]Story, error) {
	var out []Story
	for rows.Next() {
		x, err := scanStory(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}
func collectChapters(rows pgx.Rows) ([]Chapter, error) {
	var out []Chapter
	for rows.Next() {
		x, err := scanChapter(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}
func wordCount(s string) int {
	n := 0
	in := false
	for _, r := range s {
		if r == ' ' || r == '\n' || r == '\t' || r == '\r' {
			in = false
		} else if !in {
			n++
			in = true
		}
	}
	return n
}
