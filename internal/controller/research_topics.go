package controller

import (
	"encoding/json"
	"errors"
	"fmt"
)

// ResearchTopics enumerates topics in the latest visible index epoch only.
// Project-library links define the search authority.
func (db *Database) ResearchTopics(project string) ([]map[string]any, error) {
	if !validID.MatchString(project) {
		return nil, errors.New("invalid project")
	}
	return db.query(`SELECT topic.value AS topic,COUNT(*) AS passages,
		COUNT(DISTINCT p.source) AS sources
		FROM research_passages p
		JOIN research_sources s ON s.id=p.source AND s.epoch=p.epoch
		JOIN project_libraries l ON l.library=s.library
		JOIN json_each(p.topics) topic
		WHERE l.project=? GROUP BY topic.value
		ORDER BY topic.value LIMIT 500`, project)
}

// ResearchTopicPassages returns bounded, source-linked entries for one
// validated topic without evaluating a user-supplied SQLite expression.
func (db *Database) ResearchTopicPassages(project, topic string, limit int) ([]map[string]any, error) {
	if !validID.MatchString(project) || !researchTopic.MatchString(topic) || limit < 1 || limit > 100 {
		return nil, errors.New("invalid research topic request")
	}
	rows, err := db.query(`SELECT p.id,p.source,s.library,s.title,
		s.origin_kind,s.origin_name,s.origin_revision,s.origin_digest,s.origin_path,
		p.epoch,p.ordinal,p.page,p.section,p.line_start,p.line_end,
		p.body_sha256,p.topics,substr(p.body,1,1200) excerpt
		FROM research_passages p
		JOIN research_sources s ON s.id=p.source AND s.epoch=p.epoch
		JOIN project_libraries l ON l.library=s.library
		JOIN json_each(p.topics) named_topic
		WHERE l.project=? AND named_topic.value=?
		ORDER BY p.created DESC,p.ordinal LIMIT ?`, project, topic, limit)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		row["citation_ref"] = fmt.Sprintf("source:%s@%d:%d",
			asString(row["source"]), asInt(row["epoch"]), asInt(row["ordinal"]))
		var topics []string
		if err := json.Unmarshal([]byte(asString(row["topics"])), &topics); err != nil {
			return nil, err
		}
		row["topics"] = topics
	}
	return rows, nil
}
