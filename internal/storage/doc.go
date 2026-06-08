// Package storage owns S3-compatible object upload, key generation,
// thumbnail/derivative records, and signed URLs. The real client is wired
// in Phase 3 when assets first need to land on disk.
package storage
