package flowfixture

// Row and DB stand in for a generated sqlc querier. The N+1 detector never
// type-checks anything, so the shapes only have to be syntactically what a
// repository call looks like.
type Row struct{ ID int }

// DB is the query surface. GetUser is the "query operation" the detector is
// told about (by a graph edge in production, by name in the fallback).
type DB interface {
	GetUser(id int) (Row, error)
	GetUsers(ids []int) ([]Row, error)
}

// LoadUsers issues one query per element of a collection: the N+1 shape.
func LoadUsers(db DB, ids []int) []Row {
	var out []Row
	for _, id := range ids {
		r, err := db.GetUser(id)
		if err != nil {
			continue
		}
		out = append(out, r)
	}
	return out
}

// LoadUsersHoisted issues the same query once, outside the loop. The
// detector must stay silent here; firing on this is what makes an N+1
// warning unusable.
func LoadUsersHoisted(db DB, ids []int) []Row {
	rows, err := db.GetUsers(ids)
	if err != nil {
		return nil
	}
	out := make([]Row, 0, len(rows))
	for _, r := range rows {
		out = append(out, r)
	}
	return out
}

// LoadUsersCached queries only on a cache miss. Structurally this is still a
// query inside a loop, so the detector fires — but at reduced confidence,
// because a guarded query is exactly what a correct cache looks like.
func LoadUsersCached(db DB, ids []int, cache map[int]Row) []Row {
	out := make([]Row, 0, len(ids))
	for _, id := range ids {
		if r, ok := cache[id]; ok {
			out = append(out, r)
			continue
		}
		r, err := db.GetUser(id)
		if err != nil {
			continue
		}
		cache[id] = r
		out = append(out, r)
	}
	return out
}
