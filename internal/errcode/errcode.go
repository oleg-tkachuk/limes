// Package errcode is how an error of a package limes cannot import names
// its code to limes: the meter's sentinels carry theirs, and limes answers
// them from its own table, so one registry knows every code.
package errcode

// Coded is an error that names its own code.
type Coded interface {
	error
	LimesErrorCode() string
}
