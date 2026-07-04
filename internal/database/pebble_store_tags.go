// file: internal/database/pebble_store_tags.go
// version: 1.0.0
// guid: c2ad6d2b-75c3-446d-9f67-08cc517050e2
// last-edited: 2026-07-03

package database

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/falkcorp/audiobook-organizer/internal/util"
)

// AddBookTag adds a user-sourced tag to a book. Server code that
// auto-applies tags should use AddBookTagWithSource so provenance
// is preserved.
func (p *PebbleStore) AddBookTag(bookID, tag string) error {
	return p.AddBookTagWithSource(bookID, tag, "user")
}

// AddBookTagWithSource adds a tag with an explicit source. Typical
// sources: "user" (default), "system" (auto-applied by the server).
// Upserts when the row already exists — later writes overwrite the
// source field so a user-claimed tag can promote to system or vice
// versa without needing a delete-first step.
func (p *PebbleStore) AddBookTagWithSource(bookID, tag, source string) error {
	tag = util.NormalizeString(tag)
	if tag == "" {
		return fmt.Errorf("tag cannot be empty")
	}
	if source == "" {
		source = "user"
	}

	bt := BookTag{
		BookID:    bookID,
		Tag:       tag,
		Source:    source,
		CreatedAt: time.Now(),
	}
	data, err := json.Marshal(bt)
	if err != nil {
		return err
	}

	// Primary key: book_tag:<bookID>:<tag>
	bookTagKey := []byte(fmt.Sprintf("book_tag:%s:%s", bookID, tag))
	if err := p.db.Set(bookTagKey, data, pebble.Sync); err != nil {
		return err
	}

	// Reverse index: tag_idx:<tag>:<bookID>
	tagIdxKey := []byte(fmt.Sprintf("tag_idx:%s:%s", tag, bookID))
	return p.db.Set(tagIdxKey, []byte{}, pebble.Sync)
}

// RemoveBookTag removes a tag from a book regardless of source.
func (p *PebbleStore) RemoveBookTag(bookID, tag string) error {
	tag = util.NormalizeString(tag)
	if tag == "" {
		return fmt.Errorf("tag cannot be empty")
	}

	bookTagKey := []byte(fmt.Sprintf("book_tag:%s:%s", bookID, tag))
	if err := p.db.Delete(bookTagKey, pebble.Sync); err != nil && err != pebble.ErrNotFound {
		return err
	}

	tagIdxKey := []byte(fmt.Sprintf("tag_idx:%s:%s", tag, bookID))
	if err := p.db.Delete(tagIdxKey, pebble.Sync); err != nil && err != pebble.ErrNotFound {
		return err
	}

	return nil
}

// RemoveBookTagsByPrefix removes every tag on a book whose name
// begins with `prefix`, optionally scoped to a specific source.
// Used to clear a namespace before writing a fresh system tag —
// e.g., re-applying metadata from a new source removes any
// existing `metadata:source:*` system tags first so each book has
// exactly one source tag at a time.
//
// If `source` is empty, all sources match.
func (p *PebbleStore) RemoveBookTagsByPrefix(bookID, prefix, source string) error {
	prefix = util.NormalizeString(prefix)
	if prefix == "" {
		return fmt.Errorf("prefix cannot be empty")
	}

	detailed, err := p.GetBookTagsDetailed(bookID)
	if err != nil {
		return err
	}
	for _, bt := range detailed {
		if !strings.HasPrefix(bt.Tag, prefix) {
			continue
		}
		if source != "" && bt.Source != source {
			continue
		}
		if err := p.RemoveBookTag(bookID, bt.Tag); err != nil {
			return err
		}
	}
	return nil
}

// GetBookTags returns all tag strings for a book, sorted alphabetically.
func (p *PebbleStore) GetBookTags(bookID string) ([]string, error) {
	prefix := []byte(fmt.Sprintf("book_tag:%s:", bookID))
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: prefixEnd(prefix),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var tags []string
	for iter.First(); iter.Valid(); iter.Next() {
		var bt BookTag
		if err := json.Unmarshal(iter.Value(), &bt); err != nil {
			continue
		}
		tags = append(tags, bt.Tag)
	}
	sort.Strings(tags)
	return tags, nil
}

// GetBookTagsDetailed returns tags with their source attribution.
// Rows written before migration 47 deserialize with source="" which
// we promote to "user" so downstream filters treat them as user
// tags (the sensible default for legacy data).
func (p *PebbleStore) GetBookTagsDetailed(bookID string) ([]BookTag, error) {
	prefix := []byte(fmt.Sprintf("book_tag:%s:", bookID))
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: prefixEnd(prefix),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var out []BookTag
	for iter.First(); iter.Valid(); iter.Next() {
		var bt BookTag
		if err := json.Unmarshal(iter.Value(), &bt); err != nil {
			continue
		}
		if bt.Source == "" {
			bt.Source = "user"
		}
		out = append(out, bt)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Source != out[j].Source {
			return out[i].Source < out[j].Source
		}
		return out[i].Tag < out[j].Tag
	})
	return out, nil
}

// SetBookTags replaces all USER tags on a book with the given set.
// System tags (dedup:*, metadata:source:*, ...) are preserved so the
// user-facing bulk-replace doesn't clobber server-applied provenance.
func (p *PebbleStore) SetBookTags(bookID string, tags []string) error {
	detailed, err := p.GetBookTagsDetailed(bookID)
	if err != nil {
		return err
	}

	// Normalize incoming tags.
	normalized := make(map[string]bool)
	for _, t := range tags {
		t = util.NormalizeString(t)
		if t != "" {
			normalized[t] = true
		}
	}

	// Existing user tags we may need to drop.
	existingUser := make(map[string]bool)
	for _, bt := range detailed {
		if bt.Source == "user" || bt.Source == "" {
			existingUser[bt.Tag] = true
		}
	}

	// Remove user tags not in new set.
	for t := range existingUser {
		if !normalized[t] {
			if err := p.RemoveBookTag(bookID, t); err != nil {
				return err
			}
		}
	}

	// Add user tags not already present.
	for t := range normalized {
		if !existingUser[t] {
			if err := p.AddBookTagWithSource(bookID, t, "user"); err != nil {
				return err
			}
		}
	}

	return nil
}

// ListAllTags returns all unique tags with their usage counts.
func (p *PebbleStore) ListAllTags() ([]TagWithCount, error) {
	prefix := []byte("tag_idx:")
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: prefixEnd(prefix),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	counts := make(map[string]int)
	for iter.First(); iter.Valid(); iter.Next() {
		// Key format: tag_idx:<tag>:<bookID>
		key := string(iter.Key())
		parts := strings.SplitN(key, ":", 3)
		if len(parts) >= 2 {
			counts[parts[1]]++
		}
	}

	result := make([]TagWithCount, 0, len(counts))
	for tag, count := range counts {
		result = append(result, TagWithCount{Tag: tag, Count: count})
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Tag < result[j].Tag
	})
	return result, nil
}

// GetBooksByTag returns all book IDs that have the given tag.
func (p *PebbleStore) GetBooksByTag(tag string) ([]string, error) {
	tag = util.NormalizeString(tag)
	if tag == "" {
		return nil, fmt.Errorf("tag cannot be empty")
	}

	prefix := []byte(fmt.Sprintf("tag_idx:%s:", tag))
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: prefixEnd(prefix),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var bookIDs []string
	for iter.First(); iter.Valid(); iter.Next() {
		// Key format: tag_idx:<tag>:<bookID>
		key := string(iter.Key())
		parts := strings.SplitN(key, ":", 3)
		if len(parts) == 3 {
			bookIDs = append(bookIDs, parts[2])
		}
	}
	return bookIDs, nil
}

// pebbleAddTag upserts a tag for any entity type. Serializes a
// BookTag with the source field so it survives round-trips.
func (p *PebbleStore) pebbleAddTag(ks pebbleTagKeyspace, entityID, tag, source string) error {
	tag = util.NormalizeString(tag)
	if tag == "" {
		return fmt.Errorf("tag cannot be empty")
	}
	if source == "" {
		source = "user"
	}
	bt := BookTag{
		BookID:    entityID, // reused as the generic entity ID
		Tag:       tag,
		Source:    source,
		CreatedAt: time.Now(),
	}
	data, err := json.Marshal(bt)
	if err != nil {
		return err
	}
	primary := []byte(fmt.Sprintf("%s%s:%s", ks.tagPrefix, entityID, tag))
	if err := p.db.Set(primary, data, pebble.Sync); err != nil {
		return err
	}
	idx := []byte(fmt.Sprintf("%s%s:%s", ks.indexPrefix, tag, entityID))
	return p.db.Set(idx, []byte{}, pebble.Sync)
}

func (p *PebbleStore) pebbleRemoveTag(ks pebbleTagKeyspace, entityID, tag string) error {
	tag = util.NormalizeString(tag)
	if tag == "" {
		return fmt.Errorf("tag cannot be empty")
	}
	primary := []byte(fmt.Sprintf("%s%s:%s", ks.tagPrefix, entityID, tag))
	if err := p.db.Delete(primary, pebble.Sync); err != nil && err != pebble.ErrNotFound {
		return err
	}
	idx := []byte(fmt.Sprintf("%s%s:%s", ks.indexPrefix, tag, entityID))
	if err := p.db.Delete(idx, pebble.Sync); err != nil && err != pebble.ErrNotFound {
		return err
	}
	return nil
}

func (p *PebbleStore) pebbleGetTags(ks pebbleTagKeyspace, entityID string) ([]string, error) {
	prefix := []byte(fmt.Sprintf("%s%s:", ks.tagPrefix, entityID))
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: prefixEnd(prefix),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var tags []string
	for iter.First(); iter.Valid(); iter.Next() {
		var bt BookTag
		if err := json.Unmarshal(iter.Value(), &bt); err != nil {
			continue
		}
		tags = append(tags, bt.Tag)
	}
	sort.Strings(tags)
	return tags, nil
}

func (p *PebbleStore) pebbleGetTagsDetailed(ks pebbleTagKeyspace, entityID string) ([]BookTag, error) {
	prefix := []byte(fmt.Sprintf("%s%s:", ks.tagPrefix, entityID))
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: prefixEnd(prefix),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var out []BookTag
	for iter.First(); iter.Valid(); iter.Next() {
		var bt BookTag
		if err := json.Unmarshal(iter.Value(), &bt); err != nil {
			continue
		}
		if bt.Source == "" {
			bt.Source = "user"
		}
		out = append(out, bt)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Source != out[j].Source {
			return out[i].Source < out[j].Source
		}
		return out[i].Tag < out[j].Tag
	})
	return out, nil
}

func (p *PebbleStore) pebbleRemoveTagsByPrefix(ks pebbleTagKeyspace, entityID, prefix, source string) error {
	prefix = util.NormalizeString(prefix)
	if prefix == "" {
		return fmt.Errorf("prefix cannot be empty")
	}
	detailed, err := p.pebbleGetTagsDetailed(ks, entityID)
	if err != nil {
		return err
	}
	for _, bt := range detailed {
		if !strings.HasPrefix(bt.Tag, prefix) {
			continue
		}
		if source != "" && bt.Source != source {
			continue
		}
		if err := p.pebbleRemoveTag(ks, entityID, bt.Tag); err != nil {
			return err
		}
	}
	return nil
}

func (p *PebbleStore) pebbleSetTags(ks pebbleTagKeyspace, entityID string, tags []string) error {
	detailed, err := p.pebbleGetTagsDetailed(ks, entityID)
	if err != nil {
		return err
	}
	normalized := make(map[string]bool)
	for _, t := range tags {
		t = util.NormalizeString(t)
		if t != "" {
			normalized[t] = true
		}
	}
	existingUser := make(map[string]bool)
	for _, bt := range detailed {
		if bt.Source == "user" || bt.Source == "" {
			existingUser[bt.Tag] = true
		}
	}
	for t := range existingUser {
		if !normalized[t] {
			if err := p.pebbleRemoveTag(ks, entityID, t); err != nil {
				return err
			}
		}
	}
	for t := range normalized {
		if !existingUser[t] {
			if err := p.pebbleAddTag(ks, entityID, t, "user"); err != nil {
				return err
			}
		}
	}
	return nil
}

func (p *PebbleStore) pebbleListAllTags(ks pebbleTagKeyspace) ([]TagWithCount, error) {
	prefix := []byte(ks.indexPrefix)
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: prefixEnd(prefix),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	counts := make(map[string]int)
	for iter.First(); iter.Valid(); iter.Next() {
		// Key format: <indexPrefix><tag>:<entityID>
		key := string(iter.Key())
		rest := strings.TrimPrefix(key, ks.indexPrefix)
		parts := strings.SplitN(rest, ":", 2)
		if len(parts) >= 1 {
			counts[parts[0]]++
		}
	}

	result := make([]TagWithCount, 0, len(counts))
	for tag, count := range counts {
		result = append(result, TagWithCount{Tag: tag, Count: count})
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Tag < result[j].Tag
	})
	return result, nil
}

func (p *PebbleStore) pebbleEntitiesByTag(ks pebbleTagKeyspace, tag string) ([]string, error) {
	tag = util.NormalizeString(tag)
	if tag == "" {
		return nil, fmt.Errorf("tag cannot be empty")
	}
	prefix := []byte(fmt.Sprintf("%s%s:", ks.indexPrefix, tag))
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: prefixEnd(prefix),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var ids []string
	for iter.First(); iter.Valid(); iter.Next() {
		key := string(iter.Key())
		rest := strings.TrimPrefix(key, fmt.Sprintf("%s%s:", ks.indexPrefix, tag))
		if rest != "" {
			ids = append(ids, rest)
		}
	}
	return ids, nil
}

func (p *PebbleStore) AddAuthorTag(authorID int, tag string) error {
	return p.pebbleAddTag(authorTagKeyspace, strconv.Itoa(authorID), tag, "user")
}

func (p *PebbleStore) AddAuthorTagWithSource(authorID int, tag, source string) error {
	return p.pebbleAddTag(authorTagKeyspace, strconv.Itoa(authorID), tag, source)
}

func (p *PebbleStore) RemoveAuthorTag(authorID int, tag string) error {
	return p.pebbleRemoveTag(authorTagKeyspace, strconv.Itoa(authorID), tag)
}

func (p *PebbleStore) RemoveAuthorTagsByPrefix(authorID int, prefix, source string) error {
	return p.pebbleRemoveTagsByPrefix(authorTagKeyspace, strconv.Itoa(authorID), prefix, source)
}

func (p *PebbleStore) GetAuthorTags(authorID int) ([]string, error) {
	return p.pebbleGetTags(authorTagKeyspace, strconv.Itoa(authorID))
}

func (p *PebbleStore) GetAuthorTagsDetailed(authorID int) ([]BookTag, error) {
	return p.pebbleGetTagsDetailed(authorTagKeyspace, strconv.Itoa(authorID))
}

func (p *PebbleStore) SetAuthorTags(authorID int, tags []string) error {
	return p.pebbleSetTags(authorTagKeyspace, strconv.Itoa(authorID), tags)
}

func (p *PebbleStore) ListAllAuthorTags() ([]TagWithCount, error) {
	return p.pebbleListAllTags(authorTagKeyspace)
}

func (p *PebbleStore) GetAuthorsByTag(tag string) ([]int, error) {
	raw, err := p.pebbleEntitiesByTag(authorTagKeyspace, tag)
	if err != nil {
		return nil, err
	}
	ids := make([]int, 0, len(raw))
	for _, s := range raw {
		n, err := strconv.Atoi(s)
		if err != nil {
			continue // skip malformed entries
		}
		ids = append(ids, n)
	}
	return ids, nil
}

func (p *PebbleStore) AddSeriesTag(seriesID int, tag string) error {
	return p.pebbleAddTag(seriesTagKeyspace, strconv.Itoa(seriesID), tag, "user")
}

func (p *PebbleStore) AddSeriesTagWithSource(seriesID int, tag, source string) error {
	return p.pebbleAddTag(seriesTagKeyspace, strconv.Itoa(seriesID), tag, source)
}

func (p *PebbleStore) RemoveSeriesTag(seriesID int, tag string) error {
	return p.pebbleRemoveTag(seriesTagKeyspace, strconv.Itoa(seriesID), tag)
}

func (p *PebbleStore) RemoveSeriesTagsByPrefix(seriesID int, prefix, source string) error {
	return p.pebbleRemoveTagsByPrefix(seriesTagKeyspace, strconv.Itoa(seriesID), prefix, source)
}

func (p *PebbleStore) GetSeriesTags(seriesID int) ([]string, error) {
	return p.pebbleGetTags(seriesTagKeyspace, strconv.Itoa(seriesID))
}

func (p *PebbleStore) GetSeriesTagsDetailed(seriesID int) ([]BookTag, error) {
	return p.pebbleGetTagsDetailed(seriesTagKeyspace, strconv.Itoa(seriesID))
}

func (p *PebbleStore) SetSeriesTags(seriesID int, tags []string) error {
	return p.pebbleSetTags(seriesTagKeyspace, strconv.Itoa(seriesID), tags)
}

func (p *PebbleStore) ListAllSeriesTags() ([]TagWithCount, error) {
	return p.pebbleListAllTags(seriesTagKeyspace)
}

func (p *PebbleStore) GetSeriesByTag(tag string) ([]int, error) {
	raw, err := p.pebbleEntitiesByTag(seriesTagKeyspace, tag)
	if err != nil {
		return nil, err
	}
	ids := make([]int, 0, len(raw))
	for _, s := range raw {
		n, err := strconv.Atoi(s)
		if err != nil {
			continue
		}
		ids = append(ids, n)
	}
	return ids, nil
}
