package alpha

// Chat is a duplicated type name — the same identifier exists in the beta
// package. Monorepos with per-bounded-context packages hit this constantly.
type Chat struct {
	loaded bool
}

// MarkLoaded flips the loaded flag.
func (c *Chat) MarkLoaded() {
	c.loaded = true
	normalize(c)
}

// normalize is an unexported plain function: the Go compiler instruments it
// for coverage, so atlas must index it or its statements get charged to a
// neighbouring symbol.
func normalize(c *Chat) {
	if c == nil {
		return
	}
	c.loaded = true
}
