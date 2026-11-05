package core

// Change is a reviewed, optimistic-concurrency-controlled file replacement.
// BeforeSHA256 is "absent" for a new path; content is never sent by the browser.
type Change struct {
	Path string `json:"path"`
	Content []byte `json:"-"`
	BeforeSHA256 string `json:"beforeSHA256"`
	Mode uint32 `json:"mode"`
	Source string `json:"source"`
}
