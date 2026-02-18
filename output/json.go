package output

import (
	"encoding/json"
	"fmt"
	"qwash/analysis"
)

// BloatReport represents the JSON output structure.
type BloatReport struct {
	Tables  []analysis.BloatTable `json:"tables,omitempty"`
	Indexes []analysis.BloatIndex `json:"indexes,omitempty"`
	Toast   []analysis.ToastBloat `json:"toast,omitempty"`
}

// PrintBloatJSON exports the bloat analysis results in JSON format.
func PrintBloatJSON(tableBloat []analysis.BloatTable, indexBloat []analysis.BloatIndex, toastBloat []analysis.ToastBloat) {
	report := BloatReport{
		Tables:  tableBloat,
		Indexes: indexBloat,
		Toast:   toastBloat,
	}

	jsonOutput, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		fmt.Println("[ERROR] Failed to generate JSON output:", err)
		return
	}

	fmt.Println(string(jsonOutput))
}
