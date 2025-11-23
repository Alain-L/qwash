package maintenance

import (
	"context"
	"fmt"
	"log"

	"qwash/db"
)

// CleanIndex performs a REINDEX CONCURRENTLY on the given index.
func CleanIndex(ctx context.Context, dbConn *db.DB, schema, index string) error {
	qualifiedIndex := fmt.Sprintf("%s.%s", schema, index)

	// Log the reindexing attempt
	log.Printf("[INFO] Initiating REINDEX CONCURRENTLY on index: %s", qualifiedIndex)

	// Execute the REINDEX CONCURRENTLY command
	reindexQuery := fmt.Sprintf("REINDEX INDEX CONCURRENTLY %s", qualifiedIndex)
	_, err := dbConn.Exec(ctx, reindexQuery)
	if err != nil {
		return fmt.Errorf("[ERROR] Failed to reindex %s: %w", qualifiedIndex, err)
	}

	log.Printf("[SUCCESS] Reindexed index: %s", qualifiedIndex)
	return nil
}
