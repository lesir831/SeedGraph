package store

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	torrentGroupQueryVersion       = 1
	maxTorrentGroupQueryDepth      = 3
	maxTorrentGroupQueryConditions = 30
	maxTorrentGroupQueryNodes      = 100
	maxTorrentGroupQueryArrayItems = 20
	maxTorrentGroupQueryTextLength = 1024
	maxTorrentGroupQueryBindValues = 300
	maxTorrentGroupCountValue      = int64(1_000_000_000)
	maxTorrentGroupSizeValue       = int64(1<<63 - 1)
)

// TorrentGroupQuery is the safe, versioned filter language accepted by the
// torrent-group list. It intentionally models predicates rather than SQL.
// Every condition is validated against an allowlist and compiled to bound SQL
// parameters by compileTorrentGroupQuery.
type TorrentGroupQuery struct {
	Version int                    `json:"version"`
	Root    *TorrentGroupQueryNode `json:"root"`
}

type TorrentGroupQueryNode struct {
	Type       string                  `json:"type"`
	Combinator string                  `json:"combinator,omitempty"`
	Scope      string                  `json:"scope,omitempty"`
	Negated    *bool                   `json:"negated,omitempty"`
	Children   []TorrentGroupQueryNode `json:"children,omitempty"`
	Field      string                  `json:"field,omitempty"`
	Operator   string                  `json:"operator,omitempty"`
	Value      json.RawMessage         `json:"value,omitempty"`
}

type torrentGroupQueryCompiler struct {
	conditionCount int
	nodeCount      int
	bindValueCount int
	location       *time.Location
}

func compileTorrentGroupQuery(query *TorrentGroupQuery, location *time.Location) (string, []any, error) {
	if query == nil {
		return "", nil, nil
	}
	if query.Version != torrentGroupQueryVersion {
		return "", nil, invalidTorrentGroupQuery("version must be 1")
	}
	if query.Root == nil {
		return "", nil, invalidTorrentGroupQuery("root is required")
	}
	if query.Root.Type != "group" {
		return "", nil, invalidTorrentGroupQuery("root must be a group")
	}
	compiler := torrentGroupQueryCompiler{location: location}
	clause, args, err := compiler.compileNode(*query.Root, 1, false)
	if err != nil {
		return "", nil, err
	}
	if compiler.conditionCount == 0 {
		return "", nil, invalidTorrentGroupQuery("at least one condition is required")
	}
	return clause, args, nil
}

func (c *torrentGroupQueryCompiler) compileNode(node TorrentGroupQueryNode, groupDepth int, instanceScope bool) (string, []any, error) {
	c.nodeCount++
	if c.nodeCount > maxTorrentGroupQueryNodes {
		return "", nil, invalidTorrentGroupQuery("too many query nodes")
	}
	switch node.Type {
	case "group":
		return c.compileGroup(node, groupDepth, instanceScope)
	case "condition":
		return c.compileCondition(node, instanceScope)
	default:
		return "", nil, invalidTorrentGroupQuery("node type must be group or condition")
	}
}

func (c *torrentGroupQueryCompiler) compileGroup(node TorrentGroupQueryNode, depth int, inheritedInstanceScope bool) (string, []any, error) {
	if depth > maxTorrentGroupQueryDepth {
		return "", nil, invalidTorrentGroupQuery("group nesting exceeds maximum depth of 3")
	}
	if node.Field != "" || node.Operator != "" || node.Value != nil {
		return "", nil, invalidTorrentGroupQuery("group nodes may only contain combinator and children")
	}
	if node.Scope != "" && node.Scope != "instance" {
		return "", nil, invalidTorrentGroupQuery("group scope must be instance when provided")
	}
	if node.Combinator != "and" && node.Combinator != "or" {
		return "", nil, invalidTorrentGroupQuery("group combinator must be and or or")
	}
	if len(node.Children) == 0 {
		return "", nil, invalidTorrentGroupQuery("group children cannot be empty")
	}
	if len(node.Children) > maxTorrentGroupQueryConditions {
		return "", nil, invalidTorrentGroupQuery("a group cannot contain more than 30 children")
	}

	startsInstanceScope := node.Scope == "instance" && !inheritedInstanceScope
	instanceScope := inheritedInstanceScope || node.Scope == "instance"
	parts := make([]string, 0, len(node.Children))
	args := make([]any, 0)
	for _, child := range node.Children {
		childDepth := depth
		if child.Type == "group" {
			childDepth++
		}
		part, childArgs, err := c.compileNode(child, childDepth, instanceScope)
		if err != nil {
			return "", nil, err
		}
		parts = append(parts, part)
		args = append(args, childArgs...)
	}
	clause := "(" + strings.Join(parts, " "+strings.ToUpper(node.Combinator)+" ") + ")"
	if startsInstanceScope {
		clause = groupScopedInstanceExists(clause)
	}
	if node.Negated != nil && *node.Negated {
		clause = "NOT (" + clause + ")"
	}
	return clause, args, nil
}

func (c *torrentGroupQueryCompiler) compileCondition(node TorrentGroupQueryNode, instanceScope bool) (string, []any, error) {
	if node.Combinator != "" || node.Scope != "" || node.Negated != nil || node.Children != nil {
		return "", nil, invalidTorrentGroupQuery("condition nodes may only contain field, operator, and value")
	}
	if node.Field == "" || node.Operator == "" {
		return "", nil, invalidTorrentGroupQuery("condition field and operator are required")
	}
	c.conditionCount++
	if c.conditionCount > maxTorrentGroupQueryConditions {
		return "", nil, invalidTorrentGroupQuery("query cannot contain more than 30 conditions")
	}

	var (
		clause string
		args   []any
		err    error
	)
	switch node.Field {
	case "group_name":
		if instanceScope {
			err = invalidTorrentGroupQuery("group_name cannot be used inside instance scope")
			break
		}
		clause, args, err = compileGroupTextCondition("gm.display_name", false, node.Operator, node.Value)
	case "instance_name":
		clause, args, err = compileGroupInstanceTextCondition("qti.name", node.Operator, node.Value, instanceScope)
	case "path":
		clause, args, err = compileGroupInstanceTextCondition("qti.canonical_path", node.Operator, node.Value, instanceScope)
	case "size":
		if instanceScope {
			err = invalidTorrentGroupQuery("size cannot be used inside instance scope")
			break
		}
		clause, args, err = compileGroupNumberCondition("gm.size_bytes", node.Operator, node.Value, maxTorrentGroupSizeValue)
	case "instance_count":
		if instanceScope {
			err = invalidTorrentGroupQuery("instance_count cannot be used inside instance scope")
			break
		}
		clause, args, err = compileGroupNumberCondition("gm.task_count", node.Operator, node.Value, maxTorrentGroupCountValue)
	case "site_count":
		if instanceScope {
			err = invalidTorrentGroupQuery("site_count cannot be used inside instance scope")
			break
		}
		clause, args, err = compileGroupNumberCondition("gm.site_count", node.Operator, node.Value, maxTorrentGroupCountValue)
	case "downloader_count":
		if instanceScope {
			err = invalidTorrentGroupQuery("downloader_count cannot be used inside instance scope")
			break
		}
		clause, args, err = compileGroupNumberCondition("gm.downloader_count", node.Operator, node.Value, maxTorrentGroupCountValue)
	case "data_copy_count":
		if instanceScope {
			err = invalidTorrentGroupQuery("data_copy_count cannot be used inside instance scope")
			break
		}
		clause, args, err = compileGroupNumberCondition("gm.data_copy_count", node.Operator, node.Value, maxTorrentGroupCountValue)
	case "oldest_added_at":
		if instanceScope {
			err = invalidTorrentGroupQuery("oldest_added_at cannot be used inside instance scope")
			break
		}
		clause, args, err = compileGroupTimeCondition("gm.oldest_added_at", node.Operator, node.Value, c.location)
	case "updated_at":
		if instanceScope {
			err = invalidTorrentGroupQuery("updated_at cannot be used inside instance scope")
			break
		}
		clause, args, err = compileGroupTimeCondition("gm.updated_at", node.Operator, node.Value, c.location)
	case "site":
		clause, args, err = compileGroupSiteCondition(node.Operator, node.Value, instanceScope)
	case "downloader":
		clause, args, err = compileGroupInstanceListCondition("qti.downloader_id", "", node.Operator, node.Value, instanceScope)
	case "state":
		clause, args, err = compileGroupInstanceListCondition("COALESCE(qtr.status, 'unknown')", "LEFT JOIN torrent_runtime qtr ON qtr.instance_id = qti.id", node.Operator, node.Value, instanceScope)
	case "locked":
		if instanceScope {
			err = invalidTorrentGroupQuery("locked cannot be used inside instance scope")
			break
		}
		clause, args, err = compileGroupBooleanCondition("gm.locked", node.Operator, node.Value)
	case "grouping_method":
		if instanceScope {
			err = invalidTorrentGroupQuery("grouping_method cannot be used inside instance scope")
			break
		}
		clause, args, err = compileGroupModeCondition(node.Operator, node.Value)
	case "confidence":
		if instanceScope {
			err = invalidTorrentGroupQuery("confidence cannot be used inside instance scope")
			break
		}
		clause, args, err = compileGroupConfidenceCondition(node.Operator, node.Value)
	case "stale":
		if instanceScope {
			err = invalidTorrentGroupQuery("stale cannot be used inside instance scope")
			break
		}
		clause, args, err = compileGroupBooleanCondition("gm.stale", node.Operator, node.Value)
	case "has_unmapped_tracker":
		if instanceScope {
			err = invalidTorrentGroupQuery("has_unmapped_tracker cannot be used inside instance scope")
			break
		}
		clause, args, err = compileGroupUnmappedTrackerCondition(node.Operator, node.Value)
	default:
		err = invalidTorrentGroupQuery("unsupported field %q", node.Field)
	}
	if err != nil {
		return "", nil, err
	}
	c.bindValueCount += len(args)
	if c.bindValueCount > maxTorrentGroupQueryBindValues {
		return "", nil, invalidTorrentGroupQuery("query contains too many values")
	}
	return clause, args, nil
}

func compileGroupTextCondition(expression string, path bool, operator string, raw json.RawMessage) (string, []any, error) {
	if operator == "is_empty" || operator == "is_not_empty" {
		if err := requireNoGroupQueryValue(raw); err != nil {
			return "", nil, err
		}
		empty := "(" + expression + " IS NULL OR TRIM(" + expression + ") = '')"
		if operator == "is_not_empty" {
			return "NOT " + empty, nil, nil
		}
		return empty, nil, nil
	}
	value, err := decodeGroupQueryString(raw)
	if err != nil {
		return "", nil, err
	}
	switch operator {
	case "contains":
		return expression + " LIKE ? ESCAPE '\\'", []any{"%" + escapeLike(value) + "%"}, nil
	case "not_contains":
		return expression + " NOT LIKE ? ESCAPE '\\'", []any{"%" + escapeLike(value) + "%"}, nil
	case "starts_with":
		return expression + " LIKE ? ESCAPE '\\'", []any{escapeLike(value) + "%"}, nil
	case "ends_with":
		return expression + " LIKE ? ESCAPE '\\'", []any{"%" + escapeLike(value)}, nil
	case "eq":
		if path {
			return expression + " = ?", []any{value}, nil
		}
		return expression + " = ? COLLATE NOCASE", []any{value}, nil
	case "ne":
		if path {
			return expression + " <> ?", []any{value}, nil
		}
		return expression + " <> ? COLLATE NOCASE", []any{value}, nil
	default:
		return "", nil, invalidTorrentGroupQuery("operator %q is not supported for text fields", operator)
	}
}

func compileGroupInstanceTextCondition(expression, operator string, raw json.RawMessage, instanceScope bool) (string, []any, error) {
	if operator == "is_empty" || operator == "is_not_empty" {
		if err := requireNoGroupQueryValue(raw); err != nil {
			return "", nil, err
		}
		nonEmpty := expression + " IS NOT NULL AND TRIM(" + expression + ") <> ''"
		if instanceScope {
			if operator == "is_empty" {
				return "NOT (" + nonEmpty + ")", nil, nil
			}
			return "(" + nonEmpty + ")", nil, nil
		}
		exists := groupInstanceExists(nonEmpty, "")
		if operator == "is_empty" {
			return "NOT " + exists, nil, nil
		}
		return exists, nil, nil
	}
	value, err := decodeGroupQueryString(raw)
	if err != nil {
		return "", nil, err
	}
	var predicate string
	negative := false
	switch operator {
	case "contains", "not_contains":
		predicate = expression + " LIKE ? ESCAPE '\\'"
		value = "%" + escapeLike(value) + "%"
		negative = operator == "not_contains"
	case "starts_with":
		predicate = expression + " LIKE ? ESCAPE '\\'"
		value = escapeLike(value) + "%"
	case "ends_with":
		predicate = expression + " LIKE ? ESCAPE '\\'"
		value = "%" + escapeLike(value)
	case "eq", "ne":
		predicate = expression + " = ?"
		negative = operator == "ne"
	default:
		return "", nil, invalidTorrentGroupQuery("operator %q is not supported for instance text fields", operator)
	}
	if instanceScope {
		if negative {
			predicate = "NOT (" + predicate + ")"
		}
		return predicate, []any{value}, nil
	}
	clause := groupInstanceExists(predicate, "")
	if negative {
		clause = "NOT " + clause
	}
	return clause, []any{value}, nil
}

func compileGroupNumberCondition(expression, operator string, raw json.RawMessage, maximum int64) (string, []any, error) {
	if operator == "between" {
		values, err := decodeGroupQueryIntegers(raw, 2, 2)
		if err != nil {
			return "", nil, err
		}
		if values[0] > values[1] {
			return "", nil, invalidTorrentGroupQuery("between lower bound must not exceed upper bound")
		}
		if values[1] > maximum {
			return "", nil, invalidTorrentGroupQuery("numeric value exceeds the supported range")
		}
		return "(" + expression + " >= ? AND " + expression + " <= ?)", []any{values[0], values[1]}, nil
	}
	value, err := decodeGroupQueryInteger(raw)
	if err != nil {
		return "", nil, err
	}
	if value > maximum {
		return "", nil, invalidTorrentGroupQuery("numeric value exceeds the supported range")
	}
	comparison := map[string]string{
		"eq": "=", "ne": "<>", "lt": "<", "lte": "<=", "gt": ">", "gte": ">=",
	}[operator]
	if comparison == "" {
		return "", nil, invalidTorrentGroupQuery("operator %q is not supported for numeric fields", operator)
	}
	return expression + " " + comparison + " ?", []any{value}, nil
}

func compileGroupTimeCondition(expression, operator string, raw json.RawMessage, location *time.Location) (string, []any, error) {
	if operator == "is_empty" || operator == "is_not_empty" {
		if err := requireNoGroupQueryValue(raw); err != nil {
			return "", nil, err
		}
		if operator == "is_empty" {
			return expression + " IS NULL", nil, nil
		}
		return expression + " IS NOT NULL", nil, nil
	}
	if operator == "between" {
		values, err := decodeGroupQueryTimes(raw, 2, 2, location)
		if err != nil {
			return "", nil, err
		}
		start := torrentGroupQueryDayStart(values[0])
		lastDay := torrentGroupQueryDayStart(values[1])
		if start.After(lastDay) {
			return "", nil, invalidTorrentGroupQuery("between lower bound must not exceed upper bound")
		}
		end := lastDay.AddDate(0, 0, 1)
		return "(" + expression + " >= ? AND " + expression + " < ?)", []any{start.Unix(), end.Unix()}, nil
	}
	value, err := decodeGroupQueryTime(raw, location)
	if err != nil {
		return "", nil, err
	}
	start := torrentGroupQueryDayStart(value)
	end := start.AddDate(0, 0, 1)
	switch operator {
	case "before":
		return expression + " < ?", []any{start.Unix()}, nil
	case "on_or_before":
		return expression + " < ?", []any{end.Unix()}, nil
	case "after":
		return expression + " >= ?", []any{end.Unix()}, nil
	case "on_or_after":
		return expression + " >= ?", []any{start.Unix()}, nil
	case "on":
		return "(" + expression + " >= ? AND " + expression + " < ?)", []any{start.Unix(), end.Unix()}, nil
	default:
		return "", nil, invalidTorrentGroupQuery("operator %q is not supported for time fields", operator)
	}
}

func torrentGroupQueryDayStart(value time.Time) time.Time {
	return time.Date(value.Year(), value.Month(), value.Day(), 0, 0, 0, 0, value.Location())
}

func compileGroupSiteCondition(operator string, raw json.RawMessage, instanceScope bool) (string, []any, error) {
	operator = normalizeGroupListOperator(operator)
	if operator != "in" && operator != "contains_all" && operator != "not_in" {
		return "", nil, invalidTorrentGroupQuery("operator %q is not supported for site", operator)
	}
	values, err := decodeGroupQueryStrings(raw, 1, maxTorrentGroupQueryArrayItems)
	if err != nil {
		return "", nil, err
	}
	filters, err := parseGroupSiteFilters(values)
	if err != nil {
		return "", nil, err
	}
	parts := make([]string, 0, len(filters))
	args := make([]any, 0, len(filters))
	for _, filter := range filters {
		if instanceScope {
			parts = append(parts, scopedInstanceSitePredicate(filter, operator == "not_in"))
		} else {
			existsOperator := "EXISTS"
			if operator == "not_in" {
				existsOperator = "NOT EXISTS"
			}
			parts = append(parts, groupSiteExistsPredicate(existsOperator, filter))
		}
		args = append(args, filter.value)
	}
	joiner := " OR "
	if operator == "contains_all" || operator == "not_in" {
		joiner = " AND "
	}
	return "(" + strings.Join(parts, joiner) + ")", args, nil
}

func scopedInstanceSitePredicate(filter groupSiteFilter, negative bool) string {
	condition := "qst.site_id IS NULL AND qst.host_identity = ?"
	if filter.mapped {
		condition = "qst.site_id IS NOT NULL AND qs.name = ?"
	}
	predicate := `EXISTS (
		SELECT 1
		FROM torrent_trackers qst
		LEFT JOIN sites qs ON qs.id = qst.site_id
		WHERE qst.instance_id = qti.id AND ` + condition + `
	)`
	if negative {
		predicate = "NOT " + predicate
	}
	return predicate
}

func compileGroupInstanceListCondition(expression, join, operator string, raw json.RawMessage, instanceScope bool) (string, []any, error) {
	operator = normalizeGroupListOperator(operator)
	if operator != "in" && operator != "not_in" {
		return "", nil, invalidTorrentGroupQuery("operator %q is not supported for list fields", operator)
	}
	values, err := decodeGroupQueryStrings(raw, 1, maxTorrentGroupQueryArrayItems)
	if err != nil {
		return "", nil, err
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(values)), ",")
	predicate := expression + " IN (" + placeholders + ")"
	if instanceScope {
		if operator == "not_in" {
			predicate = "NOT (" + predicate + ")"
		}
		args := make([]any, len(values))
		for index := range values {
			args[index] = values[index]
		}
		return predicate, args, nil
	}
	clause := groupInstanceExists(predicate, join)
	if operator == "not_in" {
		clause = "NOT " + clause
	}
	args := make([]any, len(values))
	for index := range values {
		args[index] = values[index]
	}
	return clause, args, nil
}

func normalizeGroupListOperator(operator string) string {
	switch operator {
	case "contains_any":
		return "in"
	case "none":
		return "not_in"
	default:
		return operator
	}
}

func groupInstanceExists(predicate, join string) string {
	if join != "" {
		join = " " + join
	}
	return `EXISTS (
		SELECT 1
		FROM torrent_instances qti` + join + `
		WHERE qti.content_group_id = gm.id
		  AND qti.deleted_at IS NULL
		  AND ` + predicate + `
	)`
}

func groupScopedInstanceExists(predicate string) string {
	return `EXISTS (
		SELECT 1
		FROM torrent_instances qti
		LEFT JOIN torrent_runtime qtr ON qtr.instance_id = qti.id
		WHERE qti.content_group_id = gm.id
		  AND qti.deleted_at IS NULL
		  AND ` + predicate + `
	)`
}

func compileGroupBooleanCondition(expression, operator string, raw json.RawMessage) (string, []any, error) {
	if operator != "eq" && operator != "ne" {
		return "", nil, invalidTorrentGroupQuery("operator %q is not supported for boolean fields", operator)
	}
	var value bool
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil || string(raw) == "null" {
		return "", nil, invalidTorrentGroupQuery("boolean value is required")
	}
	comparison := "="
	if operator == "ne" {
		comparison = "<>"
	}
	integer := 0
	if value {
		integer = 1
	}
	return expression + " " + comparison + " ?", []any{integer}, nil
}

func compileGroupModeCondition(operator string, raw json.RawMessage) (string, []any, error) {
	if operator != "eq" && operator != "ne" {
		return "", nil, invalidTorrentGroupQuery("operator %q is not supported for grouping_method", operator)
	}
	value, err := decodeGroupQueryString(raw)
	if err != nil {
		return "", nil, err
	}
	if value != "auto" && value != "manual" {
		return "", nil, invalidTorrentGroupQuery("grouping_method must be auto or manual")
	}
	comparison := "="
	if operator == "ne" {
		comparison = "<>"
	}
	return "gm.mode " + comparison + " ?", []any{value}, nil
}

func compileGroupConfidenceCondition(operator string, raw json.RawMessage) (string, []any, error) {
	if operator != "eq" && operator != "ne" {
		return "", nil, invalidTorrentGroupQuery("operator %q is not supported for confidence", operator)
	}
	value, err := decodeGroupQueryString(raw)
	if err != nil {
		return "", nil, err
	}
	if value != "verified" && value != "tentative" && value != "manual" && value != "conflict" {
		return "", nil, invalidTorrentGroupQuery("confidence must be verified, tentative, manual, or conflict")
	}
	comparison := "="
	if operator == "ne" {
		comparison = "<>"
	}
	return "gm.confidence " + comparison + " ?", []any{value}, nil
}

func compileGroupUnmappedTrackerCondition(operator string, raw json.RawMessage) (string, []any, error) {
	if operator != "eq" && operator != "ne" {
		return "", nil, invalidTorrentGroupQuery("operator %q is not supported for has_unmapped_tracker", operator)
	}
	var value bool
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil || string(raw) == "null" {
		return "", nil, invalidTorrentGroupQuery("boolean value is required")
	}
	wantsUnmapped := value
	if operator == "ne" {
		wantsUnmapped = !wantsUnmapped
	}
	exists := `EXISTS (
		SELECT 1
		FROM torrent_instances quti
		JOIN torrent_trackers qutt ON qutt.instance_id = quti.id
		WHERE quti.content_group_id = gm.id
		  AND quti.deleted_at IS NULL
		  AND qutt.site_id IS NULL
	)`
	if !wantsUnmapped {
		exists = "NOT " + exists
	}
	return exists, nil, nil
}

func decodeGroupQueryString(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", invalidTorrentGroupQuery("string value is required")
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", invalidTorrentGroupQuery("value must be a string")
	}
	if strings.TrimSpace(value) == "" {
		return "", invalidTorrentGroupQuery("string value cannot be empty")
	}
	if len(value) > maxTorrentGroupQueryTextLength {
		return "", invalidTorrentGroupQuery("string value is too long")
	}
	return value, nil
}

func decodeGroupQueryStrings(raw json.RawMessage, minimum, maximum int) ([]string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, invalidTorrentGroupQuery("array value is required")
	}
	var values []string
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, invalidTorrentGroupQuery("value must be a string array")
	}
	if len(values) < minimum || len(values) > maximum {
		return nil, invalidTorrentGroupQuery("array value must contain between %d and %d items", minimum, maximum)
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return nil, invalidTorrentGroupQuery("array values cannot be empty")
		}
		if len(value) > maxTorrentGroupQueryTextLength {
			return nil, invalidTorrentGroupQuery("array value is too long")
		}
		result = append(result, value)
	}
	return result, nil
}

func decodeGroupQueryInteger(raw json.RawMessage) (int64, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return 0, invalidTorrentGroupQuery("integer value is required")
	}
	var number json.Number
	if err := json.Unmarshal(raw, &number); err != nil {
		return 0, invalidTorrentGroupQuery("value must be an integer")
	}
	value, err := strconv.ParseInt(number.String(), 10, 64)
	if err != nil || value < 0 {
		return 0, invalidTorrentGroupQuery("value must be a non-negative integer")
	}
	return value, nil
}

func decodeGroupQueryIntegers(raw json.RawMessage, minimum, maximum int) ([]int64, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, invalidTorrentGroupQuery("integer array value is required")
	}
	var numbers []json.Number
	if err := json.Unmarshal(raw, &numbers); err != nil {
		return nil, invalidTorrentGroupQuery("value must be an integer array")
	}
	if len(numbers) < minimum || len(numbers) > maximum {
		return nil, invalidTorrentGroupQuery("array value must contain between %d and %d items", minimum, maximum)
	}
	values := make([]int64, len(numbers))
	for index, number := range numbers {
		value, err := strconv.ParseInt(number.String(), 10, 64)
		if err != nil || value < 0 {
			return nil, invalidTorrentGroupQuery("array values must be non-negative integers")
		}
		values[index] = value
	}
	return values, nil
}

func decodeGroupQueryTime(raw json.RawMessage, location *time.Location) (time.Time, error) {
	value, err := decodeGroupQueryString(raw)
	if err != nil {
		return time.Time{}, err
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, invalidTorrentGroupQuery("time value must use RFC3339")
	}
	if location != nil {
		year, month, day := parsed.Date()
		parsed = time.Date(year, month, day, 0, 0, 0, 0, location)
	}
	return parsed, nil
}

func decodeGroupQueryTimes(raw json.RawMessage, minimum, maximum int, location *time.Location) ([]time.Time, error) {
	values, err := decodeGroupQueryStrings(raw, minimum, maximum)
	if err != nil {
		return nil, err
	}
	result := make([]time.Time, len(values))
	for index, value := range values {
		parsed, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			return nil, invalidTorrentGroupQuery("time values must use RFC3339")
		}
		if location != nil {
			year, month, day := parsed.Date()
			parsed = time.Date(year, month, day, 0, 0, 0, 0, location)
		}
		result[index] = parsed
	}
	return result, nil
}

func requireNoGroupQueryValue(raw json.RawMessage) error {
	if raw != nil {
		return invalidTorrentGroupQuery("operator does not accept a value")
	}
	return nil
}

func invalidTorrentGroupQuery(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrInvalidGroupFilter}, args...)...)
}
