package sql

import (
	_ "embed"
	"strings"
	"text/template"
)

//go:embed debloat_queries.sql
var debloatQueriesSQL string

// DebloatQueries holds parsed SQL query templates
type DebloatQueries struct {
	SelectHighestPages  *template.Template
	CreateTempTable     *template.Template
	DeleteFromTable     *template.Template
	ReinsertFromTemp    *template.Template
	VacuumTable         *template.Template
	AnalyzeTable        *template.Template
	VerifyHighestCTIDs  *template.Template
}

// NewDebloatQueries parses and returns all debloat query templates
func NewDebloatQueries() (*DebloatQueries, error) {
	queries := &DebloatQueries{}

	// Parse the embedded SQL file and extract individual queries
	sections := parseQuerySections(debloatQueriesSQL)

	var err error

	queries.SelectHighestPages, err = parseTemplate("select_highest_pages", sections["select_highest_pages"])
	if err != nil {
		return nil, err
	}

	queries.CreateTempTable, err = parseTemplate("create_temp_table", sections["create_temp_table"])
	if err != nil {
		return nil, err
	}

	queries.DeleteFromTable, err = parseTemplate("delete_from_table", sections["delete_from_table"])
	if err != nil {
		return nil, err
	}

	queries.ReinsertFromTemp, err = parseTemplate("reinsert_from_temp", sections["reinsert_from_temp"])
	if err != nil {
		return nil, err
	}

	queries.VacuumTable, err = parseTemplate("vacuum_table", sections["vacuum_table"])
	if err != nil {
		return nil, err
	}

	queries.AnalyzeTable, err = parseTemplate("analyze_table", sections["analyze_table"])
	if err != nil {
		return nil, err
	}

	queries.VerifyHighestCTIDs, err = parseTemplate("verify_highest_ctids", sections["verify_highest_ctids"])
	if err != nil {
		return nil, err
	}

	return queries, nil
}

// parseQuerySections splits the SQL file into named sections
func parseQuerySections(content string) map[string]string {
	sections := make(map[string]string)
	lines := strings.Split(content, "\n")

	var currentQuery string
	var currentSQL []string

	for _, line := range lines {
		// Check for query section marker
		if strings.HasPrefix(line, "-- QUERY: ") {
			// Save previous section if exists
			if currentQuery != "" {
				sections[currentQuery] = strings.TrimSpace(strings.Join(currentSQL, "\n"))
			}

			// Start new section
			currentQuery = strings.TrimPrefix(line, "-- QUERY: ")
			currentSQL = []string{}
			continue
		}

		// Skip comment-only lines and empty lines at start
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			continue
		}

		// Collect SQL lines
		if currentQuery != "" {
			currentSQL = append(currentSQL, line)
		}
	}

	// Save last section
	if currentQuery != "" {
		sections[currentQuery] = strings.TrimSpace(strings.Join(currentSQL, "\n"))
	}

	return sections
}

// parseTemplate creates a template from SQL string
func parseTemplate(name, sql string) (*template.Template, error) {
	return template.New(name).Parse(sql)
}
