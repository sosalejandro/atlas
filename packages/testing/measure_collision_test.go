package atlastest

import "testing"

// TestMeasureCollisionRate is a measurement harness, not an assertion. It
// prints the share of the 64 default seeds whose generated tree contains at
// least one CROSS-PACKAGE method-id collision, at several pressures. Run:
//
//	go test ./packages/testing/ -run TestMeasureCollisionRate -v
func TestMeasureCollisionRate(t *testing.T) {
	for _, noRoot := range []bool{true, false} {
		for _, pressure := range []int{1, 20, 40, 70, 100} {
			methodHits, anyHits := 0, 0
			for i := range DefaultCases {
				r := New(uint64(i) + 1)
				p := GenGoProject(r, GoProjectOptions{
					CollisionPressure: pressure,
					NoRootCollision:   noRoot,
				})
				methodOwners, anyOwners := map[string]string{}, map[string]string{}
				method, any := false, false
				for _, d := range p.Decls {
					key := d.ShortID()
					if prev, ok := anyOwners[key]; ok && prev != d.Dir {
						any = true
					}
					anyOwners[key] = d.Dir
					if d.Receiver == "" {
						continue
					}
					if prev, ok := methodOwners[key]; ok && prev != d.Dir {
						method = true
					}
					methodOwners[key] = d.Dir
				}
				if method {
					methodHits++
				}
				if any {
					anyHits++
				}
			}
			t.Logf("noRootCollision=%-5v pressure=%3d: method collisions %d/%d, any collisions %d/%d",
				noRoot, pressure, methodHits, DefaultCases, anyHits, DefaultCases)
		}
	}
}
