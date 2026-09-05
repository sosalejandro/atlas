package shipping

// Order collides with billing.Order by short name.
type Order struct {
	weight int
}

// Total returns the shipping cost.
func (o *Order) Total() int {
	return rate(o.weight)
}

// rate collides with nothing, but is package-private.
func rate(weight int) int {
	if weight > 10 {
		return weight * 2
	}
	return weight
}
