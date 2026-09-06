package gen

// WireBuild stands in for codegen output parked in a gen/ directory —
// invisible to the directory rule, reachable only by a configured glob.
func WireBuild() string {
	return "wire"
}
