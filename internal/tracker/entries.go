package tracker

// Entry is one tracker with its latest position and state: what the API,
// the Loxone feed and the panel all show.
type Entry struct {
	ID           int64
	PublicID     string
	Name         string
	Model        string
	LoxoneNumber int
	Position     Position
	Status       Status
}
