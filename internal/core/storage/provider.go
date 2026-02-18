package storage

import "time"

type FileMetadata struct {
	Size        int64
	ContentType string
	ModTime     time.Time
}
