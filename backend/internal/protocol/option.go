package protocol

// Optional preserves the difference between an absent value and an explicitly
// supplied zero value. Its fields are private so callers cannot construct an
// inconsistent value.
type Optional[T any] struct {
	value T
	set   bool
}

// Some returns an explicitly supplied optional value.
func Some[T any](value T) Optional[T] {
	return Optional[T]{value: value, set: true}
}

// None returns an absent optional value.
func None[T any]() Optional[T] {
	return Optional[T]{}
}

// Get returns the value and whether it was explicitly supplied.
func (o Optional[T]) Get() (T, bool) {
	return o.value, o.set
}

// IsSet reports whether the value was explicitly supplied.
func (o Optional[T]) IsSet() bool {
	return o.set
}
