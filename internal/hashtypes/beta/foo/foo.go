// Package foo exists only so the hash tests can build two DISTINCT named
// types that reflect.Type.String() renders identically.
//
// String() shortens a named type to "pkg.Name" and documents that this is not
// unique: this package and its sibling under alpha/ are both named "foo" and
// both declare ID, so struct{ X foo.ID } spells the same for either.
package foo

// ID is a named integer type. Its twin in the alpha package is a different
// type with the same spelling.
type ID int
