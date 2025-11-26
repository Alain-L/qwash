package analysis

// BloatIndex represents bloat information for an index.
type BloatIndex struct {
	Schema     string  `json:"schema"`
	IndexName  string  `json:"index_name"`
	TableName  string  `json:"table_name"`
	IndexSize  int64   `json:"index_size"`
	BloatSize  int64   `json:"bloat_size"`
	BloatRatio float64 `json:"bloat_ratio"`
}

// BloatTable represents bloat information for a table.
type BloatTable struct {
	Schema     string  `json:"schema"`
	TableName  string  `json:"table_name"`
	TableSize  int64   `json:"table_size"`
	BloatSize  int64   `json:"bloat_size"`
	BloatRatio float64 `json:"bloat_ratio"`
	// Detailed fields for -t --estimate mode
	Pages      int   `json:"pages"`
	MinPages   int   `json:"min_pages"`
	LiveTuples int64 `json:"live_tuples"`
	DeadTuples int64 `json:"dead_tuples"`
	FillFactor int   `json:"fill_factor"`
}
