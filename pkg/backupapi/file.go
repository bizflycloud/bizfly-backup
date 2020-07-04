package backupapi

// File ...
type File struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	Size        int    `json:"size"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
	ContentType string `json:"content_type"`
	Etag        string `json:"eTag"`
}

// Multipart ...
type Multipart struct {
	UploadID string `json:"upload_id"`
	FileName string `json:"file_name"`
	Parts    []byte `json:"parts"`
}

// Part ...
type Part struct {
	PartNumber int    `json:"part_number"`
	Size       int    `json:"size"`
	Etag       string `json:"etag"`
}
