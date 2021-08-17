package cache

type Chunk struct {
	Chunks map[string]int `json:"chunks"`
}

func NewChunk() *Chunk {
	return &Chunk{
		Chunks: make(map[string]int),
	}
}
