package analysis

// BloatIndex represents bloat information for an index.
type BloatIndex struct {
	Schema     string
	IndexName  string
	TableName  string
	IndexSize  int64
	BloatSize  int64
	BloatRatio float64
}

// BloatTable represents bloat information for a table.
type BloatTable struct {
	Schema     string
	TableName  string
	TableSize  int64
	BloatSize  int64
	BloatRatio float64
}
