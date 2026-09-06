package beta

// Chat collides with alpha.Chat by short name.
type Chat struct {
	loaded bool
}

// MarkLoaded collides with alpha.Chat.MarkLoaded by short name.
func (c *Chat) MarkLoaded() {
	c.loaded = true
}
