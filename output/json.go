package output

import (
	"encoding/json"
	"fmt"
	"qwash/analysis"
)

// BloatReport represents the JSON output structure.
type BloatReport struct {
	Tables  []analysis.BloatTable `json:"tables"`
	Indexes []analysis.BloatIndex `json:"indexes,omitempty"`
}

// PrintBloatJSON exports the bloat analysis results in JSON format.
func PrintBloatJSON(tableBloat []analysis.BloatTable, indexBloat []analysis.BloatIndex) {
	report := BloatReport{
		Tables:  tableBloat,
		Indexes: indexBloat,
	}

	jsonOutput, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		fmt.Println("[ERROR] Failed to generate JSON output:", err)
		return
	}

	fmt.Println(string(jsonOutput))
}
