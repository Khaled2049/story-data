package store

import (
	"net/url"
	"strings"
	"unicode/utf8"
)

// Field ceilings. This service is the system of record: a value it accepts is
// a value every client, exporter and indexer downstream has to cope with, and
// the only bound before these was the 1 MB HTTP body cap. They are set well
// above what the editor and the MCP write tools allow — those are the product
// contract, these are the outer wall — so nothing a client legitimately
// produced is now rejected.
//
// Text lengths are counted in runes, matching the char_length() the SQL CHECK
// constraints use. Counting bytes would reject a title in Japanese at a third
// of the length it allows in English, which is not a limit anyone chose. URLs
// and JSON blobs are bounded in bytes, where the encoded size is the point.
const (
	maxTitleChars       = 500
	maxDescriptionChars = 5000
	maxNameChars        = 200
	maxShortFieldChars  = 100
	maxProseChars       = 20000
	maxURLChars         = 2048
	maxTagsPerRecord    = 20
	maxTagChars         = 100
	maxJSONBytes        = 16384

	// Per-story ceilings for the worldbuilding entities, alongside the
	// existing chapterLimit and storyLimit. A story with two hundred
	// characters is not being written, it is being used as storage.
	maxEntitiesPerStory = 200
)

// validURL accepts only absolute http(s) URLs, by allowlist. A denylist would
// have to enumerate javascript:, data:, vbscript: and every obfuscation of
// them; one predicate rejects the whole class. Empty means "not set", which is
// how every URL field here is optional.
//
// This is a storage rule, not a rendering one: whether a stored javascript:
// URI becomes executable depends on the client, and an API whose schema calls
// a column a URL should not hold an executable URI either way.
func validURL(v string) bool {
	if v == "" {
		return true
	}
	// A leading space would make url.Parse read the scheme differently from a
	// browser, so reject anything that is not already clean.
	if v != strings.TrimSpace(v) || len(v) > maxURLChars {
		return false
	}
	u, e := url.Parse(v)
	if e != nil {
		return false
	}
	return (u.Scheme == "https" || u.Scheme == "http") && u.Host != ""
}

// validText bounds an optional free-text field.
func validText(v string, maximum int) bool { return utf8.RuneCountInString(v) <= maximum }

// validRequiredText bounds a field that must also be non-blank.
func validRequiredText(v string, maximum int) bool {
	return strings.TrimSpace(v) != "" && validText(v, maximum)
}

// validTags bounds both the number of tags and each one. Unbounded tag lists
// are the cheapest way to multiply rows: every tag is its own row in
// story_tags.
func validTags(tags []string) bool {
	if len(tags) > maxTagsPerRecord {
		return false
	}
	for _, t := range tags {
		if !validText(t, maxTagChars) {
			return false
		}
	}
	return true
}

func validJSONSize(v []byte) bool { return len(v) <= maxJSONBytes }

// validStoryInput is applied on create and update alike — a ceiling that only
// guards creation is not a ceiling.
func validStoryInput(in StoryInput) bool {
	return validRequiredText(in.Title, maxTitleChars) &&
		validText(in.Description, maxDescriptionChars) &&
		validText(in.AuthorName, maxNameChars) &&
		validText(in.Category, maxShortFieldChars) &&
		validText(in.TargetAudience, maxShortFieldChars) &&
		validText(in.Language, maxShortFieldChars) &&
		validText(in.Copyright, maxShortFieldChars) &&
		validURL(in.CoverImageURL) &&
		validURL(in.ThumbnailURL) &&
		validTags(in.Tags)
}

func validCharacterInput(in CharacterInput) bool {
	if !validRequiredText(in.Name, maxNameChars) || !validURL(in.ArtURL) {
		return false
	}
	for _, v := range []string{in.Soul, in.Personality, in.Voice, in.Backstory, in.Affiliations, in.Notes} {
		if !validText(v, maxProseChars) {
			return false
		}
	}
	if len(in.Relationships) > maxEntitiesPerStory {
		return false
	}
	for _, r := range in.Relationships {
		if !validText(r.Type, maxShortFieldChars) || !validText(r.Description, maxProseChars) {
			return false
		}
	}
	return true
}

func validPlaceInput(in PlaceInput) bool {
	if !validRequiredText(in.Name, maxNameChars) || !validURL(in.ImageURL) {
		return false
	}
	for _, v := range []string{in.Description, in.Atmosphere, in.Geography, in.History, in.Significance, in.Notes} {
		if !validText(v, maxProseChars) {
			return false
		}
	}
	return true
}

func validPlotLineInput(in PlotLineInput) bool {
	return validRequiredText(in.Name, maxNameChars) &&
		validText(in.Description, maxDescriptionChars)
}

func validPlotEventInput(in PlotEventInput) bool {
	if !validRequiredText(in.Name, maxNameChars) {
		return false
	}
	if !validText(in.Content, maxProseChars) || !validText(in.Notes, maxProseChars) {
		return false
	}
	for _, v := range []string{in.Pacing, in.StoryBeat, in.EmotionalTone} {
		if !validText(v, maxShortFieldChars) {
			return false
		}
	}
	if len(in.CharacterIDs) > maxEntitiesPerStory || len(in.Dependencies) > maxEntitiesPerStory {
		return false
	}
	for _, d := range in.Dependencies {
		if !validText(d.RelationshipType, maxShortFieldChars) || !validText(d.Description, maxProseChars) {
			return false
		}
	}
	return validJSONSize(in.TimeConstraint)
}

func validCompetitionInput(in CompetitionInput) bool {
	if in.MaxParticipants != nil && (*in.MaxParticipants < 1 || *in.MaxParticipants > 100000) {
		return false
	}
	return validText(in.Title, maxTitleChars) &&
		validText(in.Description, maxDescriptionChars) &&
		validText(in.Category, maxShortFieldChars) &&
		validText(in.CreatorName, maxNameChars) &&
		validTags(in.Tags)
}
